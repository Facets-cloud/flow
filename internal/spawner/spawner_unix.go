//go:build !darwin && !windows

package spawner

import (
	"flow/internal/kitty"
	"flow/internal/zellij"
	"fmt"
	"os"
)

// Detect returns the backend that SpawnTab will use on Linux/BSD.
//
// Selection priority:
//
//	Override                                       → Override (test escape hatch)
//	$ZELLIJ set                                    → internal/zellij
//	$KITTY_WINDOW_ID set or $TERM=xterm-kitty      → internal/kitty
//	$FLOW_TERM=zellij|kitty                        → that backend
//	default                                        → internal/zellij (best-effort; SpawnTab returns an
//	                                                   actionable error if zellij isn't available)
//
// Native Linux terminal backends (gnome-terminal, konsole, alacritty,
// etc.) are not yet implemented. Users on Linux who don't use zellij
// or kitty should set $FLOW_TERM once a supported backend ships.
func Detect() Backend {
	if Override != "" {
		return Override
	}
	if os.Getenv("ZELLIJ") != "" {
		return BackendZellij
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" {
		return BackendKitty
	}
	if v := os.Getenv("FLOW_TERM"); v != "" {
		switch Backend(v) {
		case BackendZellij, BackendKitty:
			return Backend(v)
		}
	}
	return BackendZellij
}

// SpawnTab opens a tab in the auto-detected backend.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	switch Detect() {
	case BackendKitty:
		return kitty.SpawnTab(title, cwd, command, envVars)
	case BackendZellij:
		return zellij.SpawnTab(title, cwd, command, envVars)
	default:
		return fmt.Errorf("no supported terminal backend on this platform — set $FLOW_TERM=zellij or $FLOW_TERM=kitty")
	}
}

// FocusSession tries to focus an existing tab running the named harness
// binary with the given session UUID.
func FocusSession(sessionID, binary string) (bool, error) {
	switch Detect() {
	case BackendKitty:
		return kitty.FocusSession(sessionID, binary)
	case BackendZellij:
		return zellij.FocusSession(sessionID, binary)
	default:
		return false, nil
	}
}
