package app

import (
	"encoding/json"
	"flow/internal/notify"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// notificationPayload is the JSON Claude Code writes to a Notification
// hook's stdin. Field names per the hooks documentation; only the ones
// flow acts on are modelled, and unknown fields are ignored so a future
// addition to the payload can't break parsing.
type notificationPayload struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	CWD              string `json:"cwd"`
	PermissionMode   string `json:"permission_mode"`
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
}

// notifiableTypes are the notification_type values flow raises a banner
// for. Claude Code emits several others (auth_success,
// elicitation_dialog, elicitation_complete, elicitation_response,
// agent_needs_input, agent_completed) which are deliberately ignored:
// the point of this feature is "a session is BLOCKED waiting on me",
// and a banner for every event would be noise across many open tabs.
//
// The settings.json matcher filters these server-side too, so in
// practice the hook is rarely invoked for anything else. This check is
// the belt to that braces — a hand-edited settings.json with no matcher
// must not turn every event into a banner.
var notifiableTypes = map[string]bool{
	"permission_prompt": true,
	"idle_prompt":       true,
}

// cmdHookNotification implements `flow hook notification`, wired as a
// Claude Code Notification hook. It reads the payload from stdin,
// resolves which flow task the session belongs to, and posts a macOS
// banner naming that task. Clicking the banner runs `flow focus
// <session-id>`, bringing the asking tab to the front.
//
// Exit code is always 0. A Notification hook cannot block or alter the
// session, so there is nothing a non-zero exit would usefully
// communicate — and a hook that errors loudly on every prompt would be
// worse than no hook at all. Failures are silent by design, matching
// the never-fail-loud discipline the other flow hook handlers follow.
func cmdHookNotification(args []string) int {
	fs := flagSet("hook notification")
	if err := fs.Parse(args); err != nil {
		return 0
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return 0
	}

	var p notificationPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return 0
	}
	if !notifiableTypes[p.NotificationType] {
		return 0
	}

	_ = notify.Notify(buildNotification(p))
	return 0
}

// buildNotification maps a payload to a notification request. Split out
// from cmdHookNotification so tests can assert the mapping without
// wiring stdin.
func buildNotification(p notificationPayload) notify.Request {
	title := "flow"
	subtitle := ""

	// Name the task when the session is one flow spawned. An unbound
	// session still gets a banner — a Claude session asking for input is
	// worth surfacing whether or not flow tracks it — it just can't be
	// labelled with a task.
	if t := taskBySessionIDQuiet(p.SessionID); t != nil {
		title = "flow: " + t.Slug
		subtitle = t.Name
	} else if p.CWD != "" {
		subtitle = shortenPath(p.CWD)
	}

	// permission_prompt carries its own descriptive message; idle_prompt
	// often arrives with an empty or generic one, so give it a body that
	// says what actually happened.
	message := strings.TrimSpace(p.Message)
	if message == "" {
		switch p.NotificationType {
		case "idle_prompt":
			message = "Waiting for your input."
		case "permission_prompt":
			message = "Needs permission to continue."
		}
	}

	return notify.Request{
		Title:    title,
		Subtitle: subtitle,
		Message:  message,
		Execute:  focusCommand(p.SessionID),
		// Group by session so a session that asks repeatedly replaces
		// its own banner instead of stacking. Sessions stay independent
		// of each other, which is the whole point across many tabs.
		Group: notificationGroup(p.SessionID),
		Sound: "default",
		// A blocked session stalls until someone notices it, so a Focus
		// mode suppressing the banner defeats the feature entirely.
		// Auto-run banners deliberately don't set this — those are
		// informational and can wait.
		IgnoreDoNotDisturb: true,
	}
}

// focusCommand builds the shell command terminal-notifier runs when the
// banner is clicked. Returns "" when the session id fails validation, in
// which case the banner is posted without a click action.
//
// The flow binary is referenced by ABSOLUTE PATH, not as a bare `flow`.
// terminal-notifier's click handler runs in a minimal environment that
// does not source the user's shell rc files, so its PATH is the system
// default (/usr/bin:/bin:/usr/sbin:/sbin and friends). flow typically
// lives in ~/.local/bin, which is only on PATH because .zshrc puts it
// there — so a bare `flow focus …` silently does nothing when clicked.
// os.Executable() is the running binary, which is by definition the one
// that has the focus subcommand.
//
// SECURITY: terminal-notifier passes -execute to a shell, so this string
// is a shell injection sink. Two inputs reach it, both handled:
//   - The session id comes from a JSON payload on stdin. It is validated
//     against a strict UUID shape rather than quoted, so no character a
//     shell could act on survives.
//   - The binary path comes from os.Executable() and is single-quoted,
//     since a user's checkout path can legitimately contain spaces (this
//     repo lives under "Facets Work") and could contain quotes.
func focusCommand(sessionID string) string {
	if !looksLikeUUID(sessionID) {
		return ""
	}
	return shellQuote(flowBinaryPath()) + " focus " + sessionID
}

// flowBinaryPath returns an absolute path to the running flow binary,
// falling back to a bare "flow" if the lookup fails — better a click
// that might work via PATH than no click action at all.
func flowBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "flow"
	}
	// Resolve symlinks so a click doesn't depend on a link that may be
	// replaced during an upgrade.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// shellQuote wraps s in single quotes with embedded-quote escaping, so a
// path containing spaces or quotes survives the shell terminal-notifier
// runs -execute through.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// notificationGroup returns the -group ID for a session's banners.
// Validated the same way as focusCommand's input for consistency,
// though this value never reaches a shell.
func notificationGroup(sessionID string) string {
	if !looksLikeUUID(sessionID) {
		return "flow"
	}
	return "flow-" + sessionID
}

// shortenPath renders a path with $HOME collapsed to "~" so a banner
// subtitle doesn't waste its limited width on /Users/<name>.
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// notifyAutoRun posts a banner when an autonomous (`flow do --auto`)
// run reaches a terminal state. Autonomous runs have no tab and no
// human watching, so their completion is otherwise invisible until the
// user thinks to check.
//
// status is the finalized auto_run_status: "completed" (the session
// closed itself via `flow done`) or "dead" (it exited without closing —
// usually a crash, and the log is worth reading).
//
// Best-effort and silent: this is called from the auto-run supervisor's
// shutdown path, where a notification failure must not affect the run's
// recorded outcome.
func notifyAutoRun(slug, status, logPath string) {
	var message string
	switch status {
	case "completed":
		message = "Autonomous run finished and closed itself out."
	case "dead":
		message = "Autonomous run exited without completing."
		if logPath != "" {
			message += " Log: " + shortenPath(logPath)
		}
	default:
		return
	}

	_ = notify.Notify(notify.Request{
		Title:    "flow: " + slug,
		Subtitle: "auto run " + status,
		Message:  message,
		// No -execute: an auto run has no tab to focus. Opening the
		// task's log would need a viewer choice we haven't made.
		Group: "flow-auto-" + slug,
		Sound: "default",
	})
}

// notificationHookHelp is printed by `flow hook notification --help`
// style probing; kept as a var so the string is testable.
var notificationHookHelp = fmt.Sprintf(
	"reads a Claude Code Notification payload on stdin and posts a macOS banner for %s events",
	strings.Join([]string{"permission_prompt", "idle_prompt"}, "/"),
)
