package rex

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Program status reporting (Rex's Program Status Protocol, OSC 7501).
//
// Rex shows a spinner / "blocked" / "done" marker on unfocused tabs and
// in the session picker for programs that report their state with
//
//	ESC ] 7501 ; state=<s>[:kind=<k>]:app=<app>[:msg=<base64>] ESC \
//
// Claude Code doesn't report it, so flow does from Claude's hooks. Hooks
// run in their own session without a controlling terminal: /dev/tty is
// unavailable, and Claude's sanctioned `terminalSequence` hook output
// only allowlists OSC 0/1/2/9/99/777. So ReportStatus finds the tab's
// tty from the hook's ancestors (the claude process owns it) and writes
// the sequence to that device directly. The sequence is a single small
// write that Rex consumes; it never reaches Claude's screen.

// StatusApp identifies Claude in status records (Rex groups and picks
// icons by it).
const StatusApp = "claude-code"

// maxStatusMsg is the protocol's decoded msg cap.
const maxStatusMsg = 2048

var validStates = map[string]bool{"idle": true, "working": true, "done": true, "blocked": true, "error": true, "clear": true}
var validKinds = map[string]bool{"": true, "permission": true, "question": true, "auth": true}

// ProcTTY returns a process's parent pid and controlling tty name as ps
// prints it ("ttys036", or "??" / "" for none). Tests override it.
var ProcTTY = func(pid int) (ppid int, tty string, err error) {
	out, err := exec.Command("ps", "-o", "ppid=,tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return 0, "", fmt.Errorf("no such process %d", pid)
	}
	ppid, err = strconv.Atoi(f[0])
	if err != nil {
		return 0, "", err
	}
	if len(f) > 1 {
		tty = f[1]
	}
	return ppid, tty, nil
}

// OpenTTY opens a terminal device for writing. O_NOCTTY matters: hooks
// are session leaders without a controlling terminal, and opening a tty
// without it would make that tty theirs. Tests override it.
var OpenTTY = func(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|syscall.O_NOCTTY, 0)
}

// maxAncestors bounds the walk from the hook to claude (hook → sh →
// claude is the usual chain).
const maxAncestors = 8

// findAncestorTTY walks up from pid to the first process with a real
// controlling tty and returns its device path, or "" if none is found.
func findAncestorTTY(pid int) string {
	for i := 0; i < maxAncestors && pid > 1; i++ {
		ppid, tty, err := ProcTTY(pid)
		if err != nil {
			return ""
		}
		if tty != "" && tty != "??" && tty != "-" {
			return "/dev/" + strings.TrimPrefix(tty, "/dev/")
		}
		pid = ppid
	}
	return ""
}

// StatusSequence builds an OSC 7501 report. msg is sanitised to the
// protocol's rules (no control characters, at most 2048 bytes decoded)
// and base64-encoded.
func StatusSequence(state, kind, msg string) (string, error) {
	if !validStates[state] {
		return "", fmt.Errorf("invalid status state %q", state)
	}
	if !validKinds[kind] {
		return "", fmt.Errorf("invalid status kind %q", kind)
	}
	if state != "blocked" {
		kind = ""
	}
	pairs := []string{"state=" + state}
	if kind != "" {
		pairs = append(pairs, "kind="+kind)
	}
	if state != "clear" {
		pairs = append(pairs, "app="+StatusApp)
	}
	if m := sanitizeStatusMsg(msg); m != "" {
		pairs = append(pairs, "msg="+base64.StdEncoding.EncodeToString([]byte(m)))
	}
	return "\x1b]7501;" + strings.Join(pairs, ":") + "\x1b\\", nil
}

// sanitizeStatusMsg drops control characters (the protocol rejects the
// whole report otherwise), collapses whitespace runs, and truncates to
// maxStatusMsg bytes on a rune boundary.
func sanitizeStatusMsg(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r' || r == ' ':
			space = b.Len() > 0
			continue
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == utf8.RuneError:
			continue
		}
		if space {
			if b.Len()+1 > maxStatusMsg {
				break
			}
			b.WriteByte(' ')
			space = false
		}
		if b.Len()+utf8.RuneLen(r) > maxStatusMsg {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ReportStatus reports Claude's state for the Rex tab the calling hook
// belongs to. It is a no-op outside Rex ($REX_SESSION unset) and when no
// ancestor has a tty. Errors are returned for the caller to ignore —
// a status marker must never get in Claude's way.
func ReportStatus(state, kind, msg string) error {
	if os.Getenv("REX_SESSION") == "" {
		return nil
	}
	seq, err := StatusSequence(state, kind, msg)
	if err != nil {
		return err
	}
	tty := findAncestorTTY(os.Getppid())
	if tty == "" {
		return nil
	}
	w, err := OpenTTY(tty)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.WriteString(w, seq)
	return err
}
