package app

import (
	"encoding/json"
	"fmt"
	"os"
)

// cmdHook dispatches `flow hook <subcommand>`. Two subcommands:
//
//   - session-start: wired as a Claude Code SessionStart hook so that
//     every session start (fresh spawn AND resume) re-injects the
//     "load your task context" instruction. Without it, resumed
//     sessions never re-read briefs and updates that may have been
//     edited since the previous session.
//
//   - user-prompt-submit: wired as a UserPromptSubmit hook. Fires on
//     every user prompt; in ad-hoc sessions (FLOW_TASK unset) it
//     reminds Claude to invoke the flow skill before responding so the
//     §4.14 substantive-work check actually runs. In bound sessions
//     (FLOW_TASK set) it no-ops; the SessionStart context already
//     covers the bound case.
func cmdHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: hook requires a subcommand (session-start|user-prompt-submit)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "session-start":
		return cmdHookSessionStart(rest)
	case "user-prompt-submit":
		return cmdHookUserPromptSubmit(rest)
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
//   - $FLOW_TASK unset (ad-hoc session, e.g. bare `claude`): emit a
//     short hint that the flow skill is installed and should be used
//     when the request touches task / project / session management.
//     Without this, Claude Code may not auto-invoke the skill on the
//     user's first message.
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
			"from landing in the wrong task. "+
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
			"task requires that context (e.g. a brief that uses domain-specific terminology "+
			"you don't recognize, a question about who someone is, a request for org context). "+
			"Throughout the session, if the user shares a durable fact about themselves, "+
			"the org, products, processes, or business, append it to the matching kb "+
			"file on the fly — no permission needed — per the flow skill's §4.10.",
		slug,
	)

	return emitSessionStartContext(instructions)
}

// emitAmbientSkillHint is the FLOW_TASK-unset branch of the SessionStart
// hook. The user has installed flow — a personal task/session manager
// and knowledge base — and benefits from having work flow through it.
// The hint frames that value prop and tells Claude to load the skill;
// it deliberately does NOT ask Claude to pre-judge whether the user's
// request is "substantive enough". That gate is the skill's job, not
// the hook's, and prior framing leaked Claude's interpretive bias.
func emitAmbientSkillHint() int {
	hint := "This Claude session is not bound to a flow task. The user already tracks " +
		"their work and knowledge in flow — a personal task and session manager that " +
		"captures work as briefs, logs progress notes, resumes Claude sessions across " +
		"days, and maintains a central knowledge base of durable facts about them, " +
		"their org, and their work. Their tasks, projects, and KB already exist. " +
		"Invoke the `flow` skill via the Skill tool now and let it run alongside the " +
		"conversation. When the user starts any work in this session, before doing it, " +
		"confirm with them via AskUserQuestion: is this part of an existing flow task, " +
		"or should we create a new one? The user's work belongs in flow — don't " +
		"silently proceed in an unbound transcript. The skill knows how to list " +
		"candidates, run intake, and `flow do` the chosen task. " +
		"\n\n" +
		"If the user's message uses unfamiliar terminology — an internal codename, a " +
		"person, a customer, a product line, a tool you don't recognize — consult " +
		"flow's data before guessing or asking. The KB at ~/.flow/kb/ holds durable " +
		"facts; the briefs under ~/.flow/projects/<slug>/ and ~/.flow/tasks/<slug>/ " +
		"hold project and task context. Names and context that are non-obvious from " +
		"this conversation alone are very likely already documented there. The skill's " +
		"§4.10 governs how to lazy-load these without reading them eagerly every turn."
	return emitSessionStartContext(hint)
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

// cmdHookUserPromptSubmit implements `flow hook user-prompt-submit`.
// In ad-hoc sessions (FLOW_TASK unset) it emits a directive instructing
// Claude to invoke the flow skill BEFORE responding and to apply
// §4.14. No keyword gate — the directive fires on every prompt because
// users describe substantive work in their own words ("help me build
// X", "fix this bug") and won't say "create a task" themselves.
//
// In bound sessions (FLOW_TASK set) it exits 0 with no output. The
// SessionStart hook already loaded full task context for those, and
// repeating that on every prompt would be noisy and expensive.
func cmdHookUserPromptSubmit(args []string) int {
	fs := flagSet("hook user-prompt-submit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if os.Getenv("FLOW_TASK") != "" {
		// Bound session — no-op.
		return 0
	}
	hint := "The user already tracks their work and knowledge in flow (a personal " +
		"task/session manager with a central knowledge base of durable facts about " +
		"them, their org, and their work) and this Claude session is unbound. Their " +
		"tasks and KB already exist. Before responding: invoke the `flow` skill via " +
		"the Skill tool if you haven't already this session — it is the operating " +
		"manual for capturing work as flow tasks, logging progress, and keeping the " +
		"user's KB current. When the user starts any work in this session, before " +
		"doing it, confirm with them via AskUserQuestion: is this part of an existing " +
		"flow task, or should we create a new one? The user's work belongs in flow — " +
		"don't silently proceed in an unbound transcript. " +
		"If the user's wording is non-standard or uses names/codenames/tools you " +
		"don't recognize, consult flow's KB (~/.flow/kb/), project briefs " +
		"(~/.flow/projects/), and task briefs (~/.flow/tasks/) before guessing or " +
		"asking. The skill's §4.10 governs lazy-loading so you don't read everything " +
		"every turn."
	return emitHookContext("UserPromptSubmit", hint)
}
