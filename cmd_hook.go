package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// cmdHook dispatches `flow hook <subcommand>`. The only subcommand in
// v2 is `session-start`, which is intended to be wired as a Claude Code
// SessionStart hook so that EVERY session start (fresh spawn AND resume)
// re-injects the "load your task context" instruction. Without the hook,
// resumed sessions never re-read briefs and updates that may have been
// edited since the previous session.
func cmdHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: hook requires a subcommand (session-start)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "session-start":
		return cmdHookSessionStart(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown hook subcommand %q\n", sub)
		return 2
	}
}

// cmdHookSessionStart emits a Claude Code SessionStart hook response.
// Wired via ~/.claude/settings.json with a matcher of "startup|resume"
// so it fires for both fresh spawns and `claude --resume`.
//
// If $FLOW_TASK is unset (i.e. the current session was not spawned by
// `flow do`), we emit an empty response — the hook is a no-op for
// non-flow sessions.
//
// Otherwise we emit additionalContext telling the agent to re-run the
// task-loading workflow. On a fresh spawn this is redundant with the
// bootstrap prompt but harmless. On a resume — which is the case the
// hook exists for — it's the only way to force the agent to re-read
// potentially-updated briefs and update files.
func cmdHookSessionStart(args []string) int {
	fs := flagSet("hook session-start")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	slug := os.Getenv("FLOW_TASK")
	if slug == "" {
		// Non-flow session: exit silently with no output at all.
		// Claude Code treats missing stdout the same as `{}` — no
		// additionalContext, no decision, no visible effect — but
		// skipping the JSON encode saves a trivial amount of work
		// and makes it unambiguous to anyone inspecting hook output
		// that the hook is a deliberate no-op for non-flow sessions.
		return 0
	}

	instructions := fmt.Sprintf(
		"You are running inside a flow execution session for task %q. "+
			"Before doing anything else in this turn, re-load your task context — "+
			"the brief and update files may have been edited since your previous "+
			"session. Do these in order: "+
			"(1) run `flow register-session` to ensure your session_id is captured "+
			"(idempotent, no-op on resume); "+
			"(2) run `flow show task` and use your Read tool on the file at the "+
			"`brief:` path AND every file listed under `updates:`; "+
			"(3) if a project is listed on the task, run `flow show project <that-slug>` "+
			"and Read its brief and updates too; "+
			"(4) Read `CLAUDE.md` in your work_dir and any nested CLAUDE.md under "+
			"subdirectories you plan to modify. "+
			"Only then proceed with the user's request. "+
			"If any brief section is blank or unclear, ASK — do not infer. "+
			"The `kb:` section of `flow show task` lists the knowledge-base files "+
			"(durable facts about the user, org, products, processes, business). "+
			"DO NOT read these eagerly on every turn — lazy-load only when the current "+
			"task requires that context (e.g. a brief that uses Facets terminology you "+
			"don't know, a question about who someone is, a request for org context). "+
			"Throughout the session, if the user shares a durable fact about themselves, "+
			"the org, products, processes, or business, append it to the matching kb "+
			"file on the fly — no permission needed — per the flow skill's §4.10.",
		slug,
	)

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": instructions,
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error: encode hook json: %v\n", err)
		return 1
	}
	return 0
}
