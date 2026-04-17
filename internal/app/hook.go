package app

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
// Two modes:
//   - $FLOW_TASK set (spawned by `flow do`): emit the full task-context
//     reload instructions. On a fresh spawn this is redundant with the
//     bootstrap prompt but harmless; on a resume it's the only way to
//     force the agent to re-read potentially-updated briefs and updates.
//   - $FLOW_TASK unset (ad-hoc session, e.g. bare `flowde`): emit a
//     short hint that the flow skill is installed and should be used
//     when the request touches task / project / session management.
//     Without this, Claude Code may not auto-invoke the skill on the
//     user's first message, so the wrapper's "skill is current"
//     guarantee would have no observable effect for ad-hoc sessions.
func cmdHookSessionStart(args []string) int {
	fs := flagSet("hook session-start")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	slug := os.Getenv("FLOW_TASK")
	if slug == "" {
		return emitAmbientSkillHint()
	}

	instructions := fmt.Sprintf(
		"You are running inside a flow execution session for task %q. "+
			"Before doing anything else in this turn, re-load your task context — "+
			"the brief and update files may have been edited since your previous "+
			"session. Do these in order: "+
			"(1) invoke the `flow` skill via the Skill tool. That skill is your "+
			"operating manual for this session: it defines the bootstrap contract, "+
			"the workflows for starting/saving/logging/archiving work, KB scoop "+
			"discipline, and the scope-creep detection that keeps unrelated work "+
			"from landing in the wrong task. Do this FIRST, and do it even if a "+
			"later step in this list fails — the skill does not depend on "+
			"register-session succeeding. "+
			"(2) run `flow register-session` to ensure your session_id is captured "+
			"(idempotent, no-op on resume); "+
			"(3) run `flow show task` and use your Read tool on the file at the "+
			"`brief:` path AND every file listed under `updates:`; "+
			"(4) if a project is listed on the task, run `flow show project <that-slug>` "+
			"and Read its brief and updates too; "+
			"(5) Read `CLAUDE.md` in your work_dir and any nested CLAUDE.md under "+
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

	return emitSessionStartContext(instructions)
}

// emitAmbientSkillHint is the FLOW_TASK-unset branch of the SessionStart
// hook. Used for ad-hoc `flowde` / `claude` sessions where there is no
// specific task to load. It nudges Claude to invoke the flow skill when
// the user's request touches flow-managed concerns, and — critically —
// to offer to create a new flow task or switch to an existing one when
// the user starts substantive work without a task attached. Otherwise
// the transcript of this session is homeless: no brief, no updates, no
// resumability tomorrow.
func emitAmbientSkillHint() int {
	hint := "The `flow` skill is installed in this Claude Code environment " +
		"(see ~/.claude/skills/flow/SKILL.md). It manages the user's personal " +
		"tasks, per-task Claude sessions, and a knowledge base of durable " +
		"facts about the user, their org, products, processes, and business. " +
		"Invoke it via the Skill tool whenever the user's request touches " +
		"tasks, projects, sessions, progress notes, priorities, or any `flow` " +
		"CLI usage — including natural-language phrasings like 'what should " +
		"I work on', 'start my day', 'add a task', 'resume X', 'save a note', " +
		"or 'mark done'. " +
		"IMPORTANT: this session is not bound to any flow task (FLOW_TASK is " +
		"unset). If the user starts substantive work — anything beyond a " +
		"one-shot question, like building a feature, debugging an issue, or " +
		"making edits across multiple turns — pause before diving in and " +
		"invoke the flow skill. Run `flow list tasks --status in-progress` " +
		"and `flow list tasks --status backlog` to see candidates, then use " +
		"AskUserQuestion to offer three choices: (a) create a new flow task " +
		"for this work (runs the §5.2 intake interview), (b) switch to an " +
		"existing task (list the matches as options, spawn `flow do <slug>` " +
		"on selection), or (c) proceed ad-hoc without a task (user accepts " +
		"that this session won't be resumable and won't accumulate context). " +
		"If the request is unrelated to flow and is clearly a one-off, ignore " +
		"this hint."
	return emitSessionStartContext(hint)
}

// emitSessionStartContext marshals the SessionStart hookSpecificOutput
// shape to stdout. Shared by both the per-task and ambient paths.
func emitSessionStartContext(ctx string) int {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": ctx,
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error: encode hook json: %v\n", err)
		return 1
	}
	return 0
}
