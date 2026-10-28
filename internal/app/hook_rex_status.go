package app

import (
	"encoding/json"
	"io"
	"os"

	"flow/internal/harness"
	"flow/internal/rex"
	"flow/internal/spawner"
)

// Rex status hook commands. Each is the exact string settings.json
// records; stable — changing one would orphan existing installations.
const (
	rexStatusWorkingCommand      = "flow hook rex-status working"
	rexStatusDoneCommand         = "flow hook rex-status done"
	rexStatusNotificationCommand = "flow hook rex-status notification"
)

// rexStatusHooks lists the Claude events flow reports to Rex and the
// command each runs:
//
//	UserPromptSubmit, PreToolUse, PostToolUse → working
//	  (PostToolUse also clears "blocked" once a permission is granted)
//	Notification (permission_prompt|elicitation_dialog) → blocked
//	Stop → done
//
// Known gap: Claude fires no hook when a turn is interrupted (Esc) —
// not Stop, and not Notification idle_prompt (that only follows a
// finished turn) — so an interrupted tab shows "working" until the next
// prompt.
var rexStatusHooks = []struct{ event, matcher, command string }{
	{"UserPromptSubmit", "", rexStatusWorkingCommand},
	{"PreToolUse", "", rexStatusWorkingCommand},
	{"PostToolUse", "", rexStatusWorkingCommand},
	{"Notification", "permission_prompt|elicitation_dialog", rexStatusNotificationCommand},
	{"Stop", "", rexStatusDoneCommand},
}

// cmdHookRexStatus implements `flow hook rex-status working|done|notification`.
// It reports Claude's state to the Rex tab it runs in (see
// rex.ReportStatus). It never prints and always exits 0: stdout of a
// UserPromptSubmit hook becomes model context, and a failing status
// marker must never disturb the session.
func cmdHookRexStatus(args []string) int {
	if os.Getenv("REX_SESSION") == "" || len(args) != 1 {
		return 0
	}
	switch args[0] {
	case "working", "done":
		_ = rex.ReportStatus(args[0], "", "")
	case "notification":
		if state, kind, msg, ok := rexNotificationStatus(os.Stdin); ok {
			_ = rex.ReportStatus(state, kind, msg)
		}
	}
	return 0
}

// rexNotificationStatus maps a Notification hook payload to a status.
// Only prompts that wait on the user count; other notification types
// (idle_prompt after a finished turn, auth_success, …) are ignored so
// they don't overwrite "done".
func rexNotificationStatus(r io.Reader) (state, kind, msg string, ok bool) {
	var p struct {
		NotificationType string `json:"notification_type"`
		Message          string `json:"message"`
	}
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return "", "", "", false
	}
	switch p.NotificationType {
	case "permission_prompt":
		return "blocked", "permission", p.Message, true
	case "elicitation_dialog":
		return "blocked", "question", p.Message, true
	}
	return "", "", "", false
}

// installRexStatusHooks registers the Rex status hooks when flow runs
// inside Rex. Outside Rex it does nothing, so non-Rex users never pay
// the extra per-event hook process. Returns how many were newly added.
func installRexStatusHooks(h harness.Harness) (int, error) {
	if spawner.Detect() != spawner.BackendRex {
		return 0, nil
	}
	added := 0
	for _, hk := range rexStatusHooks {
		ok, err := h.InstallEventHook(hk.event, hk.matcher, hk.command)
		if err != nil {
			return added, err
		}
		if ok {
			added++
		}
	}
	return added, nil
}

// uninstallRexStatusHooks removes the Rex status hooks wherever flow
// runs. Returns how many entries were removed.
func uninstallRexStatusHooks(h harness.Harness) (int, error) {
	removed := 0
	for _, hk := range rexStatusHooks {
		ok, err := h.UninstallEventHook(hk.event, hk.command)
		if err != nil {
			return removed, err
		}
		if ok {
			removed++
		}
	}
	return removed, nil
}
