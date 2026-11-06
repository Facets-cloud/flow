package app

import (
	"os"
	"strings"

	"flow/internal/harness"
)

// flowPromptPrefixes are the openings of every prompt flow itself feeds a
// session (bootstrap, auto run, owner ticks, close-out sweep). A prompt
// that starts with one was not typed by the human.
var flowPromptPrefixes = []string{
	"You are the execution session for flow task",
	"You are running playbook `",
	"You are an AUTONOMOUS, headless execution session",
	"You are the autonomous OWNER",
	"You are running ONE tick of the flow OWNER",
	"You are running an automated close-out sweep",
	// Injected by flow's opencode plugin (inbox delivery / turn nudge).
	"[flow inbox]",
	"[flow]",
}

// isHumanPrompt reports whether a UserPromptSubmit payload is a prompt the
// human actually typed in an interactive session. Only such a prompt may
// mark human mail seen or count as the human answering an ask. False when:
// no payload was decoded; the session is a headless auto run (FLOW_AUTO_RUN);
// the prompt carries flow's --with injection marker; it is a /loop or
// ScheduleWakeup firing or a harness task notification; or it is one of
// flow's own bootstrap/tick/sweep prompts.
func isHumanPrompt(known bool, prompt string, getenv func(string) string) bool {
	if !known || getenv(autoRunEnvVar) != "" {
		return false
	}
	p := strings.TrimSpace(prompt)
	switch {
	case strings.Contains(p, harness.InjectionMarker),
		strings.Contains(p, "<<autonomous-loop"),
		strings.HasPrefix(p, "/loop"),
		strings.HasPrefix(p, "<task-notification>"):
		return false
	}
	for _, pre := range flowPromptPrefixes {
		if strings.HasPrefix(p, pre) {
			return false
		}
	}
	return true
}

// currentPromptIsHuman applies isHumanPrompt to the running hook's payload.
func currentPromptIsHuman() bool {
	return isHumanPrompt(hookPromptKnown, hookPrompt, os.Getenv)
}

// interactiveSession reports whether a hook runs in a session a human
// may be reading (not a headless auto run).
func interactiveSession() bool {
	return os.Getenv(autoRunEnvVar) == ""
}
