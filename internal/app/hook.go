package app

import (
	"encoding/json"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"strings"
	"time"
)

// cmdHook dispatches `flow hook <subcommand>`. Two subcommands:
//
//   - session-start: wired as a Claude Code SessionStart hook so that
//     every session start (fresh spawn AND resume) re-injects the
//     "load your task context" instruction. Without it, resumed
//     sessions never re-read briefs and updates that may have been
//     edited since the previous session.
//
//   - user-prompt-submit: wired as a Claude Code UserPromptSubmit hook.
//     In bound sessions it injects a tiny per-prompt anchor that re-runs
//     the skill's drift (§4.11) and close-out (§4.7) checks; unbound
//     sessions are a no-op.
//
//   - pre-tool-use: wired as a Claude Code PreToolUse hook. In bound
//     sessions it delta-gates the inbox and surfaces a just-arrived
//     directed message before the tool runs (so a mid-turn "stop" lands
//     in time); inform-only, announced once per message. Everything else
//     (unbound, nothing new) is a no-op.
func cmdHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: hook requires a subcommand (session-start|user-prompt-submit|pre-tool-use|stop)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "session-start":
		return cmdHookSessionStart(rest)
	case "user-prompt-submit":
		return cmdHookUserPromptSubmit(rest)
	case "pre-tool-use":
		return cmdHookPreToolUse(rest)
	case "post-tool-use":
		// Retired: per-tool-call delivery was removed in favor of the
		// `flow inbox pop --wait` listener discipline. Kept as a silent
		// no-op so stale settings.json entries never error.
		return 0
	case "stop":
		return cmdHookStop(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown hook subcommand %q\n", sub)
		return 2
	}
}

// hookSessionID is the session id from the running hook's stdin payload
// ("" outside a hook). currentSessionID prefers it over the env var.
var hookSessionID string

// hookPrompt is the prompt text from a UserPromptSubmit payload;
// hookPromptKnown is true only when a payload was actually decoded.
// isHumanPrompt reads them.
var (
	hookPrompt      string
	hookPromptKnown bool
)

// hookPayload is the subset of the JSON that Claude Code and Codex write to
// a hook's stdin that flow reads.
type hookPayload struct {
	SessionID      string `json:"session_id"`
	Source         string `json:"source"`
	StopHookActive bool   `json:"stop_hook_active"`
	Prompt         string `json:"prompt"`
}

// readHookPayload decodes the hook's stdin payload and records its
// session_id for currentSessionID. Codex exports $CODEX_THREAD_ID only to
// tool shells, never to hook processes, so without this every Codex hook
// saw an unbound session. ok=false when stdin is a terminal or holds no
// JSON object; hookSessionID is then "" and the env var applies.
func readHookPayload() (p hookPayload, ok bool) {
	hookSessionID, hookPrompt, hookPromptKnown = "", "", false
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return p, false
	}
	if err := json.NewDecoder(os.Stdin).Decode(&p); err != nil {
		return p, false
	}
	hookSessionID = p.SessionID
	hookPrompt, hookPromptKnown = p.Prompt, true
	return p, true
}

// cmdHookSessionStart emits a Claude Code SessionStart hook response.
// Wired via ~/.claude/settings.json with a matcher of
// "startup|resume|compact|fork" so it fires for fresh spawns, `claude
// --resume`, `claude --bg --resume` (a `--fork-session` under a new id —
// the wake endpoint must be recorded under that id), AND after
// compaction (the compacted context has lost the task reload + listener
// discipline; this output restores it).
//
// Two modes, branching on whether this session is bound to a flow
// task. The binding is discovered via reverse-lookup of the hook
// payload's session_id (falling back to the harness's session-id env
// var) against tasks.session_id:
//   - Bound (a task carries this session_id): emit the full
//     task-context reload instructions. On a fresh spawn this is
//     redundant with the bootstrap prompt but harmless; on a resume
//     it's the only way to force the agent to re-read potentially-
//     updated briefs and updates.
//   - Unbound (no task carries it, or env var missing): emit the
//     ambient skill hint so Claude knows the flow skill is available.
func cmdHookSessionStart(args []string) int {
	fs := flagSet("hook session-start")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	payload, _ := readHookPayload()
	// Claude socket push: only this hook process sees the session's
	// socket token, so record it on every start/resume/compaction.
	recordClaudeEndpoint(currentSessionID())
	// A resumed session that last ran on an older flow gets one pointer at
	// the upgrade notes (read before the record below overwrites it).
	resumePtr := resumeUpgradePointer(payload.Source)
	// The session (re)loads the current skill now: record its version
	// silently so the prompt/tool hooks don't announce an upgrade.
	recordSessionVersion()

	bound := lookupBoundTask()
	if bound != nil && !isAutoRunSession(bound) {
		markSessionState(bound.Slug, flowdb.SessionIdle)
	}
	if bound == nil {
		// Unbound sessions are where the human usually sits — surface
		// their pending-mail count here too (inform-only).
		return emitAmbientSkillHint(resumePtr)
	}
	slug := bound.Slug
	pageCtx := busSessionStartContext() + resumePtr
	// Only a human-driven driver session gets the driver block (and the
	// intake-turn stop while Done when is open). An auto child never sees
	// it, so delegation cannot recurse.
	// The first start of a spawned driver (never resumed) is its intake turn.
	pageCtx += roleBlockForSlug(bound, payload.Source == "startup" && !bound.SessionLastResumed.Valid)
	// Every bound session, tab or headless child, is told the PR trailer
	// (an instruction, not an enforcement; FLOW_PR_TRAILER=off drops it).
	pageCtx += prTrailerBlock()

	instructions := fmt.Sprintf(
		"You are running inside a flow execution session for task %q. "+
			"Before doing anything else in this turn, re-load your task context — "+
			"the brief and update files may have been edited since your previous "+
			"session. Do these in order: "+
			"(1) load the `flow` skill — "+flowSkillHow()+". That skill is your "+
			"operating manual for this session: it defines the bootstrap contract, "+
			"the workflows for starting/saving/logging/archiving work, KB scoop "+
			"discipline, and the scope-creep detection that keeps unrelated work "+
			"from landing in the wrong task. "+
			"(2) run `flow show task` and read the file at the "+
			"`brief:` path AND every file listed under `updates:`; "+
			"(3) if a project is listed on the task, run `flow show project <that-slug>` "+
			"and read its brief and updates too; "+
			"(4) read "+repoInstructionFiles+" in your work_dir and any nested ones under "+
			"subdirectories you plan to modify. "+
			"Only then proceed with the user's request. "+
			"If any brief section is blank or unclear, ASK — do not infer. "+
			"The `kb:` section of `flow show task` lists the knowledge-base files "+
			"(durable facts about the user, org, products, processes, business). "+
			"DO NOT read these eagerly on every turn — lazy-load only when the current "+
			"task requires that context (e.g. a brief that uses domain-specific terminology "+
			"you don't recognize, a question about who someone is, a request for org context). "+
			"Throughout the session, if the user shares a durable fact about themselves, "+
			"the org, products, processes, or business, append it to the matching kb "+
			"file on the fly — no permission needed — per the flow skill's §4.10.",
		slug,
	)

	return emitSessionStartContext(instructions + appendStaleVersionHint() + pageCtx)
}

// roleBlockForSlug is roleBlockFor for the hook (opens its own DB).
func roleBlockForSlug(t *flowdb.Task, first bool) string {
	path, err := flowDBPath()
	if err != nil {
		return ""
	}
	db, err := flowdb.OpenDB(path)
	if err != nil {
		return ""
	}
	defer db.Close()
	return roleBlockFor(db, t, first)
}

// appendStaleVersionHint returns a short suffix to add to SessionStart
// hint payloads when the local flow binary is older than the latest
// GitHub release. Returns "" when the check fails, the binary is not
// older, the running binary is a dev build (no version embedded), or
// the automatic upgrade takes care of it (selfUpgradeHandles). The
// check is best-effort and silent on any error — session-start latency
// must not be impacted by a flaky network.
func appendStaleVersionHint() string {
	if Version == "" || Version == "dev" {
		return ""
	}
	latest := LatestRelease()
	if latest == "" || !isNewerRelease(latest, Version) || selfUpgradeHandles(latest) {
		return ""
	}
	return fmt.Sprintf(
		" flow-version-stale: %s — the running flow binary is %s but a newer release is available. "+
			"When natural, offer the user an upgrade (`flow upgrade`) per skill §4.15 (Upgrade flow itself). "+
			"Do not interrupt active work to push this; surface it at a pause.",
		latest, Version,
	)
}

// lookupBoundTask returns the task whose session_id matches
// the active harness's session-id environment variable, or nil if no such task exists, the env var
// is unset, or the DB lookup fails. Hook code must never fail loud — a
// hook error blocks the user's session — so all errors are swallowed
// and treated as "unbound".
func lookupBoundTask() *flowdb.Task {
	sid := currentSessionID()
	if sid == "" {
		return nil
	}
	dbPath, err := flowDBPath()
	if err != nil {
		return nil
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()
	t, err := flowdb.TaskBySessionID(db, sid)
	if err != nil {
		return nil
	}
	return t
}

// lookupBoundTaskSlug returns the slug of the bound task, or "" if the
// session is unbound. Thin wrapper over lookupBoundTask.
func lookupBoundTaskSlug() string {
	if t := lookupBoundTask(); t != nil {
		return t.Slug
	}
	return ""
}

// emitAmbientSkillHint is the unbound-session branch of the
// SessionStart hook. The user has installed flow — a personal task/
// session manager and knowledge base — and benefits from having work
// flow through it. The hint frames that value prop and tells Claude
// to load the skill; it deliberately does NOT ask Claude to pre-judge
// whether the user's request is "substantive enough". That gate is
// the skill's job, not the hook's.
func emitAmbientSkillHint(extra string) int {
	// Substitute the real flow root so the hint points at the right
	// paths under FLOW_ROOT (default ~/.flow). Falling back to literal
	// "~/.flow" keeps the hint useful if home-dir lookup fails.
	root := "~/.flow"
	if r, err := flowRoot(); err == nil {
		root = r
	}
	hint := "This session is not bound to a flow task — this hint IS the " +
		"binding answer. Do NOT re-probe binding with `flow show task` (no arg) " +
		"until you've actually bound this session via `flow do --here <slug>`; " +
		"until then it will error and waste a tool call. The user already tracks " +
		"their work and knowledge in flow — a personal task and session manager that " +
		"captures work as briefs, logs progress notes, resumes agent sessions across " +
		"days, and maintains a central knowledge base of durable facts about them, " +
		"their org, and their work. Their tasks, projects, and KB already exist. " +
		"Load the `flow` skill now — " + flowSkillHow() + " — and let it run alongside the " +
		"conversation. Task first: when the user states work they want done in this " +
		"session (a goal, feature, bug, investigation), start a task via flow FIRST, BEFORE doing it and " +
		"before any other process skill (brainstorming, planning, debugging); those run INSIDE the task. " +
		"Follow the skill's §4.2 \"Start a driver\": one interaction asking the goal, \"what goal do you want " +
		"to achieve?\" (skip it when already stated), and which project it belongs to (best existing matches " +
		"or a new project; never auto-create one, never no project), in one AskUserQuestion call if your harness " +
		"has it, else in chat. Route to a live driver only when its goal really matches. flow tags the new task " +
		"`driver` itself; bind this session to it and continue here. The user's work belongs in flow, don't " +
		"silently proceed in an unbound transcript. A one-off question needs no task. " +
		"\n\n" +
		"If the user's message uses unfamiliar terminology — an internal codename, a " +
		"person, a customer, a product line, a tool you don't recognize — consult " +
		"flow's data before guessing or asking. The KB at " + root + "/kb/ holds durable " +
		"facts; the briefs under " + root + "/projects/<slug>/ and " + root + "/tasks/<slug>/ " +
		"hold project and task context. Names and context that are non-obvious from " +
		"this conversation alone are very likely already documented there — and not " +
		"only in active work: when the reference points at past work, also consult " +
		"done and archived tasks/projects (which need explicit `--status done` / " +
		"`--include-archived` flags on the list commands). The skill's §4.10 governs " +
		"how to lazy-load these without reading them eagerly every turn."
	return emitSessionStartContext(hint + appendStaleVersionHint() + extra + busHumanPendingNotice())
}

// emitSessionStartContext is a thin wrapper around emitHookContext for
// the SessionStart event.
func emitSessionStartContext(ctx string) int {
	return emitHookContext("SessionStart", ctx)
}

// emitHookContext marshals a hookSpecificOutput payload for the given
// Claude Code hook event name. Used by both SessionStart and
// UserPromptSubmit hook handlers.
func emitHookContext(event, ctx string) int {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": ctx,
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error: encode hook json: %v\n", err)
		return 1
	}
	return 0
}

// cmdHookUserPromptSubmit implements `flow hook user-prompt-submit`,
// wired as a Claude Code UserPromptSubmit hook that fires on every user
// prompt but does work only in bound sessions.
//
// In a session bound to a flow task it injects a tiny (~45-token)
// anchor that re-runs the skill's lifecycle-transition checks against
// the current prompt — drift (§4.11 → offer a new task) and close-out
// (§4.7 → offer to mark done) — so those passive disciplines don't
// decay over a long session the way a SessionStart-only reminder does.
// Unbound sessions get one line on their FIRST human prompt only
// (firstPromptNudge: "create a task first"); every later prompt is a
// no-op apart from bus/upgrade notices. Repeating the nudge per prompt
// was retired in v0.1.0-alpha.7 for its token cost.
//
// The semantic judgment is Claude's — the hook only supplies which task
// the session is bound to (a free, deterministic DB lookup); whether
// the prompt has drifted or signals completion is Claude's call.
func cmdHookUserPromptSubmit(args []string) int {
	fs := flagSet("hook user-prompt-submit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	readHookPayload()
	t := lookupBoundTask()
	if t != nil && !isAutoRunSession(t) {
		markSessionState(t.Slug, flowdb.SessionBusy)
	}
	pageCtx := busPromptSubmitContext() + upgradeNoticeContext(t)
	if t == nil {
		// Unbound — the once-per-session "create a task first" nudge,
		// page-bus context (an unbound session can still have paged the
		// user) and the upgrade notice are the only payloads.
		if n := firstPromptNudge(nil); n != "" {
			pageCtx = n + pageCtx
		}
		if pageCtx == "" {
			return 0
		}
		return emitHookContext("UserPromptSubmit", strings.TrimSpace(pageCtx))
	}
	role := ""
	if isDriverSlug(t.Slug) {
		role = " [driver]"
	}
	anchor := fmt.Sprintf(
		"flow session → task %q (%s)%s. Per §4.11/§4.7: if this prompt is "+
			"unrelated work, offer a new task; if it signals the task is done, "+
			"offer to close it out. Else proceed silently.",
		t.Name, t.Slug, role,
	)
	return emitHookContext("UserPromptSubmit", anchor+pageCtx)
}

// cmdHookPreToolUse implements `flow hook pre-tool-use`, wired as a
// Claude Code PreToolUse hook that fires before every tool call. It
// surfaces any just-arrived unread inbox item (directed message OR
// broadcast) so the agent reads it BEFORE running a tool the item might
// change or cancel — the mid-turn "stop, don't do X" case the session-
// start / prompt-submit / stop nudges surface too late.
//
// All the gating lives in busPreToolUseContext: unbound sessions and
// tool calls with nothing new in the inbox produce "" (a silent no-op),
// and the bus_surfaced delta-gate (the hook-side record) guarantees the
// hooks announce a given item at most once, so this never spams the same
// pending set across a turn's many tool calls. Wake pushes keep their own
// record (bus_wake_nudged), so a swallowed push can still be announced
// here once. Inform-only — it never consumes.
func cmdHookPreToolUse(args []string) int {
	fs := flagSet("hook pre-tool-use")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	readHookPayload()
	ctx := busPreToolUseContext() + upgradeNoticeContext(nil)
	if ctx == "" {
		return 0
	}
	return emitHookContext("PreToolUse", strings.TrimSpace(ctx))
}

// markSessionState records a hook-observed turn-state change (silent on
// any error — hooks never fail loud).
func markSessionState(slug, state string) {
	db, err := openBusDB()
	if err != nil {
		return
	}
	defer db.Close()
	_ = flowdb.SetSessionState(db, slug, state, time.Now())
}
