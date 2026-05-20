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
	decision := DecideReaction(ev, TriggerEmoji(), SelfUserIDs())
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
	brief := slackTaskBrief(decision, slug)
	if err := spawnFlowTask(ctx, name, slug, brief); err != nil {
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

func shortenTS(ts string) string {
	// Keep the readable suffix; full Slack ts is 17 chars which is noisy
	// in a task name.
	if i := strings.Index(ts, "."); i > 0 && len(ts) > i+5 {
		return ts[:i] + "." + ts[i+1:i+5]
	}
	return ts
}

func slackTaskBrief(decision ReactionDecision, slug string) string {
	dir := TaskDir(slug)
	if dir == "" {
		dir = "~/.flow/tasks/" + slug
	}
	channelType := decision.Event.ChannelType
	if channelType == "" {
		channelType = "unknown"
	}
	return fmt.Sprintf(`# %s

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
		slackTaskName(decision),
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

func nonEmptyOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func slackAutoOpenEnabled() bool {
	return envBoolDefault("FLOW_SLACK_AUTOOPEN", true)
}

// spawnFlowTask shells out to `flow spawn` with --no-open. Package-level
// variable so tests can swap it.
var spawnFlowTask = func(ctx context.Context, name, slug, brief string) error {
	cmd := exec.CommandContext(ctx, "flow", "spawn", name,
		"--slug", slug,
		"--priority", "high",
		"--prompt", brief,
		"--no-open",
	)
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
