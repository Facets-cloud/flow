package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"flow/internal/flowdb"
)

// slackOpenTarget reports where new slack-reply tasks should open.
// Values: "ui" (default — browser terminal in flow UI), "iterm" (legacy
// path that shells to `flow do`). Set via FLOW_SLACK_OPEN_TARGET.
func slackOpenTarget() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("FLOW_SLACK_OPEN_TARGET")))
	switch v {
	case "iterm", "terminal":
		return "iterm"
	default:
		return "ui"
	}
}

// TaskOpener is implemented by anyone who can open a freshly-created
// flow task in a way the user can monitor. The server implements this
// by attaching the task to a browser-terminal PTY (so the UI can stream
// the Claude session live); CLI contexts can fall back to shelling out
// to `flow do` for an iTerm tab.
//
// When Dispatcher's Opener is nil, no auto-open happens — the task is
// still created and tagged, but the user has to open it themselves.
type TaskOpener interface {
	OpenInUI(slug string) error
}

// Dispatcher routes parsed InboundEvents into flow tasks. It is the
// integration layer between the side-effect-free DecideReaction and the
// actual filesystem / database / process effects (flow spawn, inbox
// append, opening the new task for the user).
//
// All side-effect operations live behind package-level function vars or
// the Opener interface so tests can swap in pure-Go fakes.
type Dispatcher struct {
	DB     *sql.DB
	Opener TaskOpener
}

// NewDispatcher constructs a dispatcher bound to db. opener may be nil
// (in which case dispatched tasks won't auto-open and the user must
// open them manually via the UI or `flow do`).
func NewDispatcher(db *sql.DB, opener TaskOpener) *Dispatcher {
	return &Dispatcher{DB: db, Opener: opener}
}

// Dispatch processes one InboundEvent. Returns nil for uninteresting
// events (non-trigger reactions, messages in untracked threads, etc.) —
// the listener doesn't distinguish "not for us" from "successfully
// processed."
func (d *Dispatcher) Dispatch(ctx context.Context, ev InboundEvent) error {
	if d == nil || d.DB == nil {
		return nil
	}
	switch ev.Kind {
	case "reaction_added":
		return d.dispatchReaction(ctx, ev)
	case "message", "app_mention":
		return d.dispatchMessage(ctx, ev)
	}
	return nil
}

func (d *Dispatcher) dispatchReaction(ctx context.Context, ev InboundEvent) error {
	decision := DecideReaction(ev, TriggerEmojis(), SelfUserIDs())
	if !decision.Trigger {
		return nil
	}
	slug, found, err := d.findTaskByThreadKey(decision.ThreadKey)
	if err != nil {
		return fmt.Errorf("monitor: lookup task by thread key: %w", err)
	}
	if !found {
		slug, err = d.createSlackTask(ctx, decision)
		if err != nil {
			return fmt.Errorf("monitor: create slack task: %w", err)
		}
	} else {
		_ = d.refreshSlackTaskTitleIfLegacy(ctx, slug, decision)
	}
	if err := AppendInboxEvent(slug, ev); err != nil {
		return fmt.Errorf("monitor: append inbox: %w", err)
	}
	if !found && slackAutoOpenEnabled() {
		// Default path: hand off to the server's browser-terminal so the
		// PTY shows up in the flow UI. Fall back to iTerm only when no
		// Opener is wired (CLI contexts) or explicit env requests it.
		if d.Opener != nil && slackOpenTarget() != "iterm" {
			if err := d.Opener.OpenInUI(slug); err != nil {
				fmt.Fprintf(os.Stderr, "monitor: open in UI: %v\n", err)
			}
		} else {
			// Best-effort iTerm fallback; iTerm not being available
			// shouldn't fail dispatch.
			_ = openSlackReplyTask(slug)
		}
	}
	return nil
}

func (d *Dispatcher) dispatchMessage(ctx context.Context, ev InboundEvent) error {
	key := ThreadKey(ev.Channel, ev.ThreadTS)
	if key == "" {
		return nil
	}
	slug, found, err := d.findTaskByThreadKey(key)
	if err != nil {
		return fmt.Errorf("monitor: lookup task by thread key: %w", err)
	}
	if !found {
		// Untracked thread — we haven't been asked to handle this conversation.
		return nil
	}
	return AppendInboxEvent(slug, ev)
}

func (d *Dispatcher) findTaskByThreadKey(key string) (slug string, found bool, err error) {
	if strings.TrimSpace(key) == "" {
		return "", false, nil
	}
	tag := flowdb.NormalizeTag(SlackThreadTagPrefix + key)
	tasks, err := flowdb.ListTasks(d.DB, flowdb.TaskFilter{Tag: tag})
	if err != nil {
		return "", false, err
	}
	if len(tasks) == 0 {
		return "", false, nil
	}
	// Prefer non-done tasks (a closed thread might still receive a fresh
	// reaction — but if so, we want to route it to the live one, not a
	// done one). Falls back to the first hit when all are done so we still
	// re-thread rather than silently dropping the event.
	for _, t := range tasks {
		if t != nil && t.Status != "done" {
			return t.Slug, true, nil
		}
	}
	return tasks[0].Slug, true, nil
}

func (d *Dispatcher) createSlackTask(ctx context.Context, decision ReactionDecision) (string, error) {
	slug := SlugForThread(decision.ThreadKey)
	name := slackTaskName(decision)
	if enriched, err := resolveSlackTaskTitle(ctx, decision); err == nil && strings.TrimSpace(enriched) != "" {
		name = strings.TrimSpace(enriched)
	}
	// Snapshot the live project catalog so the brief can ask the operator
	// to pick a project as the agent's first turn. Failures here are
	// soft — without the snapshot, the picker section just lists nothing,
	// which still leaves the agent free to ask "which project?" blind.
	projects, _ := listProjectChoices(d.DB)
	brief := slackTaskBrief(decision, slug, name, projects)
	provider := ProviderForEmoji(decision.Reaction)
	if err := spawnFlowTask(ctx, name, slug, brief, provider); err != nil {
		return "", err
	}
	if err := tagFlowTask(ctx, slug, "slack-reply"); err != nil {
		return slug, err
	}
	if err := tagFlowTask(ctx, slug, SlackThreadTagPrefix+decision.ThreadKey); err != nil {
		return slug, err
	}
	return slug, nil
}

// projectChoice is a small projection of flowdb.Project — just what the
// brief's "pick a project" section needs.
type projectChoice struct {
	Slug      string
	Name      string
	UpdatedAt string
	Priority  string
}

// listProjectChoices reads active (non-archived, non-deleted) projects
// from flowdb. Package-level variable so the test stub can return a
// canned list without touching the DB.
var listProjectChoices = func(db *sql.DB) ([]projectChoice, error) {
	if db == nil {
		return nil, nil
	}
	projects, err := flowdb.ListProjects(db, flowdb.ProjectFilter{IncludeArchived: false})
	if err != nil {
		return nil, err
	}
	out := make([]projectChoice, 0, len(projects))
	for _, p := range projects {
		if p == nil {
			continue
		}
		out = append(out, projectChoice{
			Slug:      p.Slug,
			Name:      p.Name,
			UpdatedAt: p.UpdatedAt,
			Priority:  p.Priority,
		})
	}
	return out, nil
}

// SlackThreadTagPrefix is the prefix for the per-thread linkage tag.
// A task tagged "slack-thread:C123:1234.0001" is the flow representation
// of the Slack conversation rooted at that channel/ts.
const SlackThreadTagPrefix = "slack-thread:"

// SlugForThread derives a deterministic, idempotent flow task slug from
// a thread key. Same thread key → same slug, so a stray duplicate trigger
// (network blip, double-fire) finds the existing task by tag lookup AND
// the spawn would no-op-or-error in a recognizable way.
//
// Slug shape: "slack-<channel-lower>-<ts-dashed>" (no colons, no dots —
// flow's slug grammar is ASCII + dashes). Length is bounded by the
// shape of Slack IDs (10–11 chars for channel + ~17 chars for ts) so
// no truncation needed.
func SlugForThread(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return ""
	}
	out := make([]rune, 0, len(key)+6)
	out = append(out, []rune("slack-")...)
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == ':', r == '.', r == '-', r == '_':
			out = append(out, '-')
		default:
			// drop anything else
		}
	}
	// Collapse runs of '-' that crept in from adjacent separators.
	collapsed := strings.Builder{}
	prevDash := false
	for _, r := range out {
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		collapsed.WriteRune(r)
	}
	return strings.Trim(collapsed.String(), "-")
}

func slackTaskName(decision ReactionDecision) string {
	channel := decision.Channel
	if channel == "" {
		channel = "?"
	}
	return fmt.Sprintf("Slack reply in %s (thread %s)", channel, shortenTS(decision.ThreadTS))
}

func isLegacySlackTaskName(name string) bool {
	name = strings.TrimSpace(name)
	return strings.HasPrefix(name, "Slack reply in ") && strings.Contains(name, " (thread ") && strings.HasSuffix(name, ")")
}

func shortenTS(ts string) string {
	// Keep the readable suffix; full Slack ts is 17 chars which is noisy
	// in a task name.
	if i := strings.Index(ts, "."); i > 0 && len(ts) > i+5 {
		return ts[:i] + "." + ts[i+1:i+5]
	}
	return ts
}

func slackTaskBrief(decision ReactionDecision, slug, title string, projects []projectChoice) string {
	dir := TaskDir(slug)
	if dir == "" {
		dir = "~/.flow/tasks/" + slug
	}
	channelType := decision.Event.ChannelType
	if channelType == "" {
		channelType = "unknown"
	}
	picker := renderProjectPicker(slug, projects)
	return fmt.Sprintf(`# %s

## First step — pick a project
%s

## What
You were invoked by a :%s: reaction on a Slack message. Read the thread
context, decide whether and how to reply, and post via the Slack MCP
tools threaded to thread_ts=%s.

## Slack context
channel: %s (%s)
thread_ts: %s
item_ts: %s   (the message the reaction targeted)
item_author: %s
reactor: %s

## Inbox (live event stream for this thread)
All Slack events for this thread are streamed to:
  %s/inbox.jsonl

On bootstrap, read inbox.jsonl to catch up on any events that arrived while
this session was closed. While you're working, arm a Monitor on:
  tail -f %s/inbox.jsonl
so new messages and reactions in this thread appear as live chat
notifications. The first line of each inbox entry is the parsed event;
fetch full thread history via the Slack MCP if you need more context
than the event payload carries.

## How to reply
Use the Slack MCP tools (mcp__claude_ai_Slack__slack_send_message) with:
  channel: %s
  thread_ts: %s
Posts go as YOU (User Token), not as a bot, so be careful with tone and
factual claims. Use mcp__claude_ai_Slack__slack_read_thread first to pull
the full thread context if you weren't already given it.

## Done when
The user marks this task done (flow done) — typically after the question
is answered or the conversation moves on. Don't auto-close. Save a
progress note before closing summarizing what you posted and why.

## Tags
slack-reply, slack-thread:%s

---
*Slack-origin task. The Socket Mode listener inside flow ui serve writes
incoming events to inbox.jsonl as they arrive.*
	`,
		nonEmptyOr(title, slackTaskName(decision)),
		picker,
		decision.Reaction,
		decision.ThreadTS,
		decision.Channel, channelType,
		decision.ThreadTS,
		decision.ItemTS,
		nonEmptyOr(decision.Event.ItemAuthor, "?"),
		nonEmptyOr(decision.Reactor, "?"),
		dir, dir,
		decision.Channel,
		decision.ThreadTS,
		decision.ThreadKey,
	)
}

// renderProjectPicker writes the body of the "First step — pick a project"
// section. The agent reads the Slack thread, ranks the listed projects by
// relevance, asks the operator IN THIS SESSION (not in Slack) for a
// choice, and then runs `flow update task <slug> --project <choice>` to
// record the answer. Keeping this synchronous-with-the-operator avoids
// the dispatcher having to wait on a Slack reply before the task can be
// created.
func renderProjectPicker(slug string, projects []projectChoice) string {
	var b strings.Builder
	b.WriteString("**Before doing anything else**, decide which flow project this task should belong to.\n\n")
	b.WriteString("1. Read the Slack thread context (channel, item author, recent messages in `inbox.jsonl`).\n")
	b.WriteString("2. From the list below, pick the 2–3 projects that look most relevant to that conversation.\n")
	b.WriteString("3. Ask the operator **in this Claude Code session** (not in Slack) which one to use, ")
	b.WriteString("offering an `adhoc` option if none fit.\n")
	b.WriteString("4. Once the operator answers, run exactly one of:\n\n")
	b.WriteString("   ```bash\n")
	b.WriteString("   flow update task " + slug + " --project <chosen-slug>\n")
	b.WriteString("   # or, if they pick adhoc / none:\n")
	b.WriteString("   flow update task " + slug + " --clear-project\n")
	b.WriteString("   ```\n\n")
	b.WriteString("Do NOT skip this step or proceed to the actual Slack reply work until the project is recorded.\n\n")
	if len(projects) == 0 {
		b.WriteString("_No active projects found in flowdb. Ask the operator whether to leave this task as adhoc, ")
		b.WriteString("or to first create a project via `flow add project \"<name>\" --work-dir <path>` and then ")
		b.WriteString("rerun the update command above._\n")
		return b.String()
	}
	b.WriteString("**Available projects** (active, non-archived):\n\n")
	for _, p := range projects {
		line := "- `" + p.Slug + "`"
		if strings.TrimSpace(p.Name) != "" && p.Name != p.Slug {
			line += " — " + p.Name
		}
		if strings.TrimSpace(p.Priority) != "" {
			line += " · priority " + p.Priority
		}
		if strings.TrimSpace(p.UpdatedAt) != "" {
			line += " · last activity " + p.UpdatedAt
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// BackfillSlackTaskTitles refreshes older Slack-origin task names that still
// use the raw "Slack reply in <channel-id> (thread ...)" format. It deliberately
// skips manually renamed tasks.
func (d *Dispatcher) BackfillSlackTaskTitles(ctx context.Context) (int, error) {
	if d == nil || d.DB == nil {
		return 0, nil
	}
	tasks, err := flowdb.ListTasks(d.DB, flowdb.TaskFilter{Tag: "slack-reply"})
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, task := range tasks {
		if task == nil || !isLegacySlackTaskName(task.Name) {
			continue
		}
		tags, err := flowdb.GetTaskTags(d.DB, task.Slug)
		if err != nil {
			return updated, err
		}
		decision, ok := decisionFromSlackThreadTags(tags)
		if !ok {
			continue
		}
		if d.refreshSlackTaskTitleIfLegacy(ctx, task.Slug, decision) {
			updated++
		}
	}
	return updated, nil
}

func (d *Dispatcher) refreshSlackTaskTitleIfLegacy(ctx context.Context, slug string, decision ReactionDecision) bool {
	task, err := flowdb.GetTask(d.DB, slug)
	if err != nil || !isLegacySlackTaskName(task.Name) {
		return false
	}
	title, err := resolveSlackTaskTitle(ctx, decision)
	if err != nil || strings.TrimSpace(title) == "" {
		return false
	}
	res, err := d.DB.Exec(
		`UPDATE tasks SET name = ?, updated_at = ? WHERE slug = ? AND name = ?`,
		strings.TrimSpace(title), flowdb.NowISO(), slug, task.Name,
	)
	if err != nil {
		return false
	}
	rows, err := res.RowsAffected()
	return err == nil && rows > 0
}

func decisionFromSlackThreadTags(tags []string) (ReactionDecision, bool) {
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		key := strings.TrimPrefix(tag, SlackThreadTagPrefix)
		if key == tag {
			continue
		}
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		channel := normalizeSlackChannelID(parts[0])
		threadTS := strings.TrimSpace(parts[1])
		if channel == "" || threadTS == "" {
			continue
		}
		return ReactionDecision{
			Trigger:   true,
			ThreadKey: ThreadKey(channel, threadTS),
			Channel:   channel,
			ThreadTS:  threadTS,
		}, true
	}
	return ReactionDecision{}, false
}

func nonEmptyOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func slackAutoOpenEnabled() bool {
	return envBoolDefault("FLOW_SLACK_AUTOOPEN", true)
}

// spawnFlowTask shells out to `flow spawn` with --no-open. The provider
// arg routes the new task to either Claude or Codex (mapped from the
// Slack trigger emoji). Empty provider lets `flow spawn` apply its own
// default. Package-level variable so tests can swap it.
var spawnFlowTask = func(ctx context.Context, name, slug, brief, provider string) error {
	args := []string{"spawn", name,
		"--slug", slug,
		"--priority", "high",
		"--prompt", brief,
		"--no-open",
	}
	if p := strings.TrimSpace(provider); p != "" {
		args = append(args, "--agent", p)
	}
	cmd := exec.CommandContext(ctx, "flow", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("flow spawn: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tagFlowTask shells out to `flow update task <slug> --tag <tag>`. The CLI
// is the documented surface for tagging; calling it keeps us behind one
// public API instead of poking flowdb.AddTaskTag directly (which would
// bypass any future validation that lives in the CLI layer).
var tagFlowTask = func(ctx context.Context, slug, tag string) error {
	cmd := exec.CommandContext(ctx, "flow", "update", "task", slug, "--tag", tag)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("flow update task --tag %s: %w (output: %s)", tag, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// openSlackReplyTask shells out to `flow do <slug>` and detaches. We
// don't wait for the iTerm tab to spawn — the user sees it open when
// they look at their desktop; we just need the trigger fired.
var openSlackReplyTask = func(slug string) error {
	cmd := exec.Command("flow", "do", slug)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("flow do %s: %w", slug, err)
	}
	go func() { _ = cmd.Wait() }() // reap the child to avoid zombies
	return nil
}
