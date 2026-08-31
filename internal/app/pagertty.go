package app

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Terminal attention primitives for the paging bus (iTerm2 proprietary
// escape sequences, written to a session's tty):
//
//   OSC 9  — native macOS notification; clicking it focuses the exact
//            tab whose tty emitted it. iTerm suppresses it while that
//            tab is focused (desired: never notify someone mid-look).
//   OSC 1337 RequestAttention=once|yes — Dock bounce (yes = until focused).
//   OSC 1337 SetBadgeFormat=<b64>      — tab badge.
//
// Hook processes and the Bash tool have no controlling terminal, so the
// tty is found by walking the process tree to the ancestor that has one
// (the claude process). All failures are silent-best-effort: attention
// is never worth breaking a command over.

// writeTTY writes an escape sequence to a tty device. Package-level var
// for test mocking (tests must not scribble on real terminals).
var writeTTY = func(tty, seq string) error {
	f, err := os.OpenFile(tty, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(seq)
	return err
}

// psOutput runs ps for the process-tree walk. Mockable for tests.
var psOutput = func(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "ppid=,tty=", "-p", strconv.Itoa(pid)).Output()
	return string(out), err
}

// findSessionTTY walks up the process tree from this process to the
// first ancestor with a controlling terminal and returns its device
// path ("" if none found).
func findSessionTTY() string {
	pid := os.Getpid()
	for i := 0; i < 10; i++ {
		out, err := psOutput(pid)
		if err != nil {
			return ""
		}
		fields := strings.Fields(out)
		if len(fields) < 2 {
			return ""
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil {
			return ""
		}
		if tty := fields[1]; tty != "??" && tty != "" {
			return "/dev/" + tty
		}
		if ppid <= 1 {
			return ""
		}
		pid = ppid
	}
	return ""
}

// itermSessionUUID returns the UUID part of $ITERM_SESSION_ID ("" when
// not running under iTerm).
func itermSessionUUID() string {
	sid := os.Getenv("ITERM_SESSION_ID")
	if sid == "" {
		return ""
	}
	parts := strings.Split(sid, ":")
	return parts[len(parts)-1]
}

// notifyOnTTY fires the full attention set on a tty: notification,
// Dock bounce, badge. label prefixes the notification body.
func notifyOnTTY(tty, label, body string, urgent bool) {
	if tty == "" {
		return
	}
	mark := "\U0001F4DE"
	attention := "once"
	if urgent {
		mark = "\U0001F6A8"
		attention = "yes"
	}
	_ = writeTTY(tty, fmt.Sprintf("\x1b]9;%s %s: %s\x07", mark, label, body))
	_ = writeTTY(tty, fmt.Sprintf("\x1b]1337;RequestAttention=%s\x07", attention))
	setBadge(tty, mark+" waiting")
}

// setBadge sets (or with "" clears) the iTerm tab badge on a tty.
func setBadge(tty, text string) {
	if tty == "" {
		return
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	_ = writeTTY(tty, fmt.Sprintf("\x1b]1337;SetBadgeFormat=%s\x07", b64))
}
