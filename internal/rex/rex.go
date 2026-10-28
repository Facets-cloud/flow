// Package rex provides Rex (https://www.superlogical.com/rex) tab
// spawning via the `rex` CLI. Activated by spawner.Detect() when
// $REX_SESSION is set or $TERM_PROGRAM=rex (Rex sets both in every
// terminal it opens; it does not set a TERM value flow can key on).
//
// Rex vocabulary: a session is a group of tabs kept alive by the Rex
// server, a window is one tab, and a block is one pane in a tab.
//
// Mechanism:
//
//  1. rex window new <title> -s <session> -c <cwd> --json
//     (prints {"window_id": ..., "block_ids": [...]})
//  2. rex send -s <session> -b <block> " <env-prefix><flat-command>\n"
//
// Step 1 opens a new tab in the session flow was invoked from, running
// the default shell, and returns the tab's block id. Step 2 types the
// command into that block's PTY so the shell executes it. The leading
// space triggers histignorespace on shells that have it on, matching
// the kitty/zellij backends. The trailing newline submits the line.
//
// Session scope: the new tab lands in $REX_SESSION, the session of the
// terminal `flow do` ran in. When $REX_SESSION is unset (FLOW_TERM=rex
// from outside Rex), -s is omitted and Rex picks its default session.
//
// The `rex` binary: Rex puts its own bin dir at the front of PATH in
// every terminal and also exports it as $REX_BIN_DIR. We prefer
// $REX_BIN_DIR/rex so a shell startup file that rewrites PATH doesn't
// break spawning.
//
// This file mirrors the contract of internal/kitty and internal/zellij
// — same SpawnTab signature, same Runner mock vars for tests, same
// ShellQuote helper.
package rex

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Runner is the function used to execute rex for side-effect calls
// (send, focus). Tests override this to capture argv without invoking
// the real CLI.
var Runner = func(args []string) error {
	cmd := exec.Command(binary(), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rex failed: %v: %s", err, string(out))
	}
	return nil
}

// RunnerOutput executes rex and returns stdout. Used to read JSON from
// `rex window new`, `rex api call`, etc.
var RunnerOutput = func(args []string) ([]byte, error) {
	return exec.Command(binary(), args...).Output()
}

// ProcessArgs returns the full argv string of a process. Rex's process
// API deliberately returns only argv0 (other arguments can hold
// secrets), so FocusSession reads the rest from ps. Tests override this.
var ProcessArgs = func(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func binary() string {
	if dir := os.Getenv("REX_BIN_DIR"); dir != "" {
		p := filepath.Join(dir, "rex")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "rex"
}

// sessionArgs returns ["-s", $REX_SESSION] when set, else nil.
func sessionArgs() []string {
	if s := os.Getenv("REX_SESSION"); s != "" {
		return []string{"-s", s}
	}
	return nil
}

// SpawnTab opens a new Rex tab in the current session, titles it, sets
// its cwd, and types `command` into the new tab's PTY.
//
// envVars are attached as an inline shell prefix to `command` only —
// they are present in the command's environment but do NOT persist in
// the tab's shell after the command exits.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	args := append([]string{"window", "new", title}, sessionArgs()...)
	args = append(args, "-c", cwd, "--json")
	out, err := RunnerOutput(args)
	if err != nil {
		return fmt.Errorf("rex window new: %w (is the Rex server running? try `rex ls`)", err)
	}
	var created struct {
		WindowID string   `json:"window_id"`
		BlockIDs []string `json:"block_ids"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		return fmt.Errorf("parse rex window new JSON: %w", err)
	}
	if len(created.BlockIDs) == 0 {
		return fmt.Errorf("rex window new returned no block id")
	}

	envPrefix := ""
	if len(envVars) > 0 {
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(envVars))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, ShellQuote(envVars[k])))
		}
		envPrefix = strings.Join(parts, " ") + " "
	}
	flat := strings.ReplaceAll(command, "\n", " ")
	line := " " + envPrefix + flat + "\n"
	send := append([]string{"send"}, sessionArgs()...)
	send = append(send, "-b", created.BlockIDs[0], line)
	return Runner(send)
}

// FocusSession tries to focus the Rex tab whose foreground process is
// `binary` carrying the given session UUID. Returns (true, nil) on
// focus, (false, nil) if no block in the current session matches, and
// (false, err) only on a backend failure.
//
// Mechanism:
//
//  1. rex api call session.list_blocks {"session_id": ...}
//  2. for each placed block, rex api call com.superlogical.terminal.process
//     → foreground {pid, argv0}
//  3. skip blocks whose argv0/name doesn't contain `binary`; for the
//     rest read the full argv via ps and apply the same UUID regex as
//     the kitty/iterm/zellij backends
//  4. rex focus -s <session> <window_id> on the first hit
//
// Scope: the current Rex session only ($REX_SESSION), like zellij.
// Tabs living in other Rex sessions are not found; flow then spawns a
// fresh tab as it would for any backend that reports no match.
func FocusSession(sessionID, binaryName string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	rexSession := os.Getenv("REX_SESSION")
	if rexSession == "" {
		return false, nil
	}
	out, err := RunnerOutput([]string{"api", "call", "session.list_blocks",
		fmt.Sprintf(`{"session_id":%q}`, rexSession)})
	if err != nil {
		return false, fmt.Errorf("rex session.list_blocks: %w", err)
	}
	var listed struct {
		Blocks []struct {
			BlockID   string `json:"block_id"`
			WindowID  string `json:"window_id"`
			Placement string `json:"placement"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return false, fmt.Errorf("parse rex list_blocks JSON: %w", err)
	}
	needle := strings.ToLower(sessionID)
	for _, b := range listed.Blocks {
		if b.Placement != "placed" || b.WindowID == "" {
			continue
		}
		pout, err := RunnerOutput([]string{"api", "call", "com.superlogical.terminal.process",
			fmt.Sprintf(`{"session_id":%q,"block_id":%q}`, rexSession, b.BlockID)})
		if err != nil {
			// A block that isn't a terminal (or just exited) has no
			// process API; skip it rather than failing the scan.
			continue
		}
		var proc struct {
			Foreground *struct {
				PID   int    `json:"pid"`
				Name  string `json:"name"`
				Argv0 string `json:"argv0"`
			} `json:"foreground"`
		}
		if err := json.Unmarshal(pout, &proc); err != nil || proc.Foreground == nil {
			continue
		}
		fg := proc.Foreground
		if !strings.Contains(fg.Argv0, binaryName) && !strings.Contains(fg.Name, binaryName) {
			continue
		}
		argv, err := ProcessArgs(fg.PID)
		if err != nil {
			continue
		}
		m := sessionUUIDRowRe.FindStringSubmatch(argv)
		if len(m) < 2 || strings.ToLower(m[1]) != needle {
			continue
		}
		if err := Runner([]string{"focus", "-s", rexSession, b.WindowID}); err != nil {
			return false, fmt.Errorf("rex focus: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// sessionUUIDRowRe matches a harness invocation carrying a session
// UUID in argv. Same shape as the kitty, iterm and zellij regex so
// focus behaviour stays consistent across backends.
var sessionUUIDRowRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// ShellQuote wraps s in single quotes with proper escaping. Same
// implementation as kitty.ShellQuote / iterm.ShellQuote.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
