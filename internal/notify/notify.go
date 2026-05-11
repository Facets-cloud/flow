// Package notify posts macOS user notifications via osascript's
// `display notification` AppleScript. No external dependencies.
//
// Custom icons are not supported: Apple's UNUserNotification framework
// reads the icon from the calling process's bundle Info.plist, and
// osascript runs under Script Editor's bundle. The notification
// appears in macOS Notification Center under "Script Editor". The
// duration of the banner follows the user's Alert Style setting for
// Script Editor in System Settings → Notifications.
//
// Set FLOW_NOTIFY to "0" / "false" / "off" / "no" (case-insensitive)
// to disable notifications entirely. Unset or any other value leaves
// them on (default).
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner runs a subprocess. Overridable for tests so we don't actually
// invoke osascript.
var Runner = func(name string, args []string) error {
	return exec.Command(name, args...).Run()
}

// MacOS posts a notification with the given title and body. Best-effort
// — the caller should treat errors as advisory and not block on them.
//
// Returns nil (no error, no work done) when FLOW_NOTIFY indicates the
// user has opted out, so callers don't need to gate the call site.
func MacOS(title, body string) error {
	if !Enabled() {
		return nil
	}
	script := fmt.Sprintf(
		`display notification "%s" with title "%s"`,
		escapeAppleScriptString(body),
		escapeAppleScriptString(title),
	)
	return Runner("osascript", []string{"-e", script})
}

// Enabled reports whether notifications are turned on for this process.
// True unless FLOW_NOTIFY is set to a falsy value.
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("FLOW_NOTIFY")))
	switch v {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
