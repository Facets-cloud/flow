// Package notify posts macOS user notifications, preferring a
// clickable banner when the host can deliver one.
//
// Two delivery paths, in preference order:
//
//  1. terminal-notifier, when it's on $PATH. It is a signed .app
//     bundle with its own bundle identifier, which is what lets it
//     register a click action via -execute. flow uses that to run
//     `flow focus <session-id>`, so clicking a banner jumps to the tab
//     that raised it.
//
//  2. osascript `display notification`, as a fallback. Always
//     available, but the banner is NOT clickable in any useful sense:
//     the notification is owned by whichever app osascript is running
//     under, so a click activates that app rather than running our
//     command. macOS provides no way to attach a custom action to an
//     osascript notification — that requires a signed app registering
//     UNNotificationAction categories.
//
// The fallback is deliberately still worth posting: knowing WHICH task
// is asking is most of the value, even when the click does nothing.
//
// Delivery is best-effort throughout. Every caller is on a hook path
// where failing loud would disrupt the user's session, so errors are
// returned for tests and logging but callers are expected to ignore
// them.
package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// LookPath resolves a binary on $PATH. Overridable so tests can force
// the terminal-notifier-present and terminal-notifier-absent branches
// without mutating the environment.
var LookPath = exec.LookPath

// Runner executes a command. Overridable for tests. Returns combined
// output for error context only — no caller reads it on success.
var Runner = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Request describes one notification.
type Request struct {
	// Title is the bold first line. Typically "flow: <task-slug>".
	Title string
	// Subtitle is an optional second line, shown smaller.
	Subtitle string
	// Message is the body — the question text or status detail.
	Message string
	// Execute is a shell command run when the banner is clicked.
	// Ignored by the osascript fallback, which cannot honor it.
	Execute string
	// Group coalesces banners: posting with a group ID replaces any
	// earlier banner carrying the same ID. Keyed on session id by
	// callers so one chatty session can't bury the others under a
	// stack of its own notifications.
	Group string
	// Sound is an optional sound name ("default" for the standard
	// notification sound). Empty means silent.
	Sound string
	// IgnoreDoNotDisturb delivers the notification even while a Focus
	// mode is active. Set for "a session is blocked waiting on you"
	// banners: the whole point is that the task stalls until noticed, so
	// suppressing it defeats the feature. Not set for informational
	// banners like auto-run completion.
	//
	// This does NOT control banner-vs-alert persistence — macOS keeps
	// that under the user's control in System Settings → Notifications,
	// and no CLI flag can override it. See PersistenceHint.
	IgnoreDoNotDisturb bool
}

// PersistenceHint explains how to make banners persist on screen rather
// than auto-dismissing after a few seconds. Exposed as a string (rather
// than being applied automatically) because macOS deliberately reserves
// this choice for the user: an app declares a default, but System
// Settings → Notifications is authoritative and no command-line flag can
// override it.
//
// The notification is owned by terminal-notifier — the signed bundle
// that posts it — so it's terminal-notifier's entry that has to change,
// not flow's.
const PersistenceHint = `To keep flow banners on screen until dismissed (like Calendar alerts):
  System Settings → Notifications → terminal-notifier → Alert style: Alerts

macOS reserves this setting for you; flow cannot set it programmatically.`

// Available reports whether a clickable banner can be delivered — i.e.
// whether terminal-notifier is installed. Callers use this to decide
// whether to bother computing a click command, and `flow init` uses it
// to decide whether to offer the dependency install.
func Available() bool {
	_, err := LookPath("terminal-notifier")
	return err == nil
}

// Notify posts the notification, using terminal-notifier when
// available and falling back to osascript otherwise. A blank Message
// is a no-op: macOS silently drops a notification with no body, and
// posting one would burn a subprocess for nothing.
func Notify(req Request) error {
	if strings.TrimSpace(req.Message) == "" {
		return nil
	}
	if Available() {
		return notifyViaTerminalNotifier(req)
	}
	return notifyViaOsascript(req)
}

// notifyViaTerminalNotifier builds the terminal-notifier argv. Values
// are passed as separate argv entries, NOT interpolated into a shell
// string, so no quoting or escaping of user-controlled text is needed
// — except for Execute, which terminal-notifier hands to a shell by
// definition and which callers must therefore build safely.
func notifyViaTerminalNotifier(req Request) error {
	args := []string{"-message", req.Message}
	if req.Title != "" {
		args = append(args, "-title", req.Title)
	}
	if req.Subtitle != "" {
		args = append(args, "-subtitle", req.Subtitle)
	}
	if req.Group != "" {
		args = append(args, "-group", req.Group)
	}
	if req.Sound != "" {
		args = append(args, "-sound", req.Sound)
	}
	if req.IgnoreDoNotDisturb {
		args = append(args, "-ignoreDnD")
	}
	if req.Execute != "" {
		args = append(args, "-execute", req.Execute)
	}
	return Runner("terminal-notifier", args...)
}

// notifyViaOsascript posts via `display notification`. Execute is
// dropped — see the package doc for why it cannot be honored here.
func notifyViaOsascript(req Request) error {
	script := fmt.Sprintf("display notification %s", quoteAppleScript(req.Message))
	if req.Title != "" {
		script += fmt.Sprintf(" with title %s", quoteAppleScript(req.Title))
	}
	if req.Subtitle != "" {
		script += fmt.Sprintf(" subtitle %s", quoteAppleScript(req.Subtitle))
	}
	if req.Sound != "" {
		script += fmt.Sprintf(" sound name %s", quoteAppleScript(req.Sound))
	}
	return Runner("osascript", "-e", script)
}

// quoteAppleScript returns an AppleScript expression evaluating to s.
//
// Notification text is user-controlled (it's Claude's question, which
// can contain quotes, backslashes, and newlines), and AppleScript
// double-quoted literals support neither embedded newlines nor \n
// escapes. Newlines are therefore emitted as separate quoted strings
// joined with `& linefeed &`, matching the approach in
// internal/iterm's quoteAppleScriptString.
func quoteAppleScript(s string) string {
	lines := strings.Split(s, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.ReplaceAll(line, `\`, `\\`)
		line = strings.ReplaceAll(line, `"`, `\"`)
		parts = append(parts, `"`+line+`"`)
	}
	return strings.Join(parts, " & linefeed & ")
}
