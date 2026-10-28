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
//     (prints {"window_id": ..., "block_ids": [...]}), or, when the
//     target session doesn't exist yet,
//     rex new <session> --cwd <cwd> --window <title> --json
//  2. rex send -s <session> -b <block> " <env-prefix><flat-command>\n"
//  3. when the target session isn't the one flow ran in, ask the Mac
//     app to show it (client.perform session.select)
//
// Step 1 opens a new tab running the default shell and returns the
// tab's block id. Step 2 types the command into that block's PTY so
// the shell executes it. The leading space triggers histignorespace on
// shells that have it on, matching the kitty/zellij backends. The
// trailing newline submits the line.
//
// Session routing: SpawnTabIn takes a target session label (flow
// resolves it from the task's project terminal_group).
// An empty label means $REX_SESSION, the session of the terminal
// `flow do` ran in. When that is unset too (FLOW_TERM=rex from outside
// Rex), -s is omitted and Rex picks its default session.
//
// Step 3 needs Remote Control turned on in the Rex app's settings.
// Without it the tab is still created; flow prints a hint instead of
// switching the view.
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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
// `rex window new`, `rex ls`, `rex api call`, etc. On failure the error
// carries rex's stderr so callers can recognise refusals.
var RunnerOutput = func(args []string) ([]byte, error) {
	out, err := exec.Command(binary(), args...).Output()
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
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

// Warn receives non-fatal notices, such as the Remote Control hint.
var Warn io.Writer = os.Stderr

// maxTypedLine is the longest command line typed straight into the new
// tab. Longer ones go through a sourced temp script (see sendCommand).
// macOS caps a line at MAX_CANON (1024 bytes) while the tty is still in
// canonical mode — which it is until the tab's shell finishes its
// startup files and starts its line editor — and silently drops the
// excess, Enter included. 512 leaves room for the env prefix and
// multibyte text.
const maxTypedLine = 512

// NewScriptPath returns a fresh per-spawn temp script path. Separate
// from WriteScript because the script deletes itself by path, so the
// path must be known before the body is built. Tests override both.
var NewScriptPath = func() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate rex script id: %w", err)
	}
	return filepath.Join(os.TempDir(), "flow-rex-"+hex.EncodeToString(buf[:])+".sh"), nil
}

// WriteScript writes a spawn script, readable only by the user.
var WriteScript = func(path, body string) error {
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write rex spawn script: %w", err)
	}
	return nil
}

// removeScript deletes a spawn script on the error path. The happy path
// leaves cleanup to the script's own first line. Tests override it.
var removeScript = func(path string) error { return os.Remove(path) }

func binary() string {
	if dir := os.Getenv("REX_BIN_DIR"); dir != "" {
		p := filepath.Join(dir, "rex")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "rex"
}

type session struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

func listSessions() ([]session, error) {
	out, err := RunnerOutput([]string{"ls", "--json"})
	if err != nil {
		return nil, fmt.Errorf("rex ls: %w", err)
	}
	var listed struct {
		Sessions []session `json:"sessions"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, fmt.Errorf("parse rex ls JSON: %w", err)
	}
	return listed.Sessions, nil
}

// findSession returns the session whose label matches (case-insensitive,
// like Rex's own target matching), or nil.
func findSession(sessions []session, label string) *session {
	for i := range sessions {
		if strings.EqualFold(sessions[i].Label, label) {
			return &sessions[i]
		}
	}
	return nil
}

// SpawnTab opens a new Rex tab in the current session. See SpawnTabIn.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	return SpawnTabIn("", title, cwd, command, envVars)
}

// SpawnTabIn opens a new Rex tab in the session labelled target
// (creating the session if needed), titles it, sets its cwd, types
// `command` into the new tab's PTY, and brings the session into view
// when it isn't the one flow ran in. An empty target means the current
// session.
//
// envVars are attached as an inline shell prefix to `command` only —
// they are present in the command's environment but do NOT persist in
// the tab's shell after the command exits.
func SpawnTabIn(target, title, cwd, command string, envVars map[string]string) error {
	current := os.Getenv("REX_SESSION")
	sessionID := current
	var windowID, blockID string

	if target != "" {
		sessions, err := listSessions()
		if err != nil {
			return err
		}
		if s := findSession(sessions, target); s != nil {
			sessionID = s.ID
		} else {
			out, err := RunnerOutput([]string{"new", target, "--cwd", cwd, "--window", title, "--json"})
			if err != nil {
				return fmt.Errorf("rex new %s: %w", target, err)
			}
			var created struct {
				SessionID      string `json:"session_id"`
				InitialWindows []struct {
					WindowID string   `json:"window_id"`
					BlockIDs []string `json:"block_ids"`
				} `json:"initial_windows"`
			}
			if err := json.Unmarshal(out, &created); err != nil {
				return fmt.Errorf("parse rex new JSON: %w", err)
			}
			if len(created.InitialWindows) == 0 || len(created.InitialWindows[0].BlockIDs) == 0 {
				return fmt.Errorf("rex new returned no block id")
			}
			sessionID = created.SessionID
			windowID = created.InitialWindows[0].WindowID
			blockID = created.InitialWindows[0].BlockIDs[0]
		}
	}

	if blockID == "" {
		args := []string{"window", "new", title}
		if sessionID != "" {
			args = append(args, "-s", sessionID)
		}
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
		windowID = created.WindowID
		blockID = created.BlockIDs[0]
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
	if err := sendCommand(sessionID, blockID, envPrefix+command); err != nil {
		return err
	}

	if sessionID != "" && sessionID != current {
		showSession(sessionID, windowID, target)
	}
	return nil
}

// sendCommand types command into the block's PTY. Short commands are
// typed as-is: " <command>\n" (leading space for histignorespace,
// embedded newlines flattened, trailing newline submits). Commands over
// maxTypedLine are written to a temp script and the tab is sent only
// " . '<script>'\n": sourced, so the command runs in the tab's own
// interactive shell exactly as if typed. The script's first line
// deletes it; if the send itself fails, it is removed here.
func sendCommand(sessionID, blockID, command string) error {
	line := " " + strings.ReplaceAll(command, "\n", " ") + "\n"
	script := ""
	if len(line) > maxTypedLine {
		var err error
		if script, err = NewScriptPath(); err != nil {
			return err
		}
		body := "rm -f -- " + ShellQuote(script) + "\n" + command + "\n"
		if err := WriteScript(script, body); err != nil {
			return err
		}
		line = " . " + ShellQuote(script) + "\n"
	}
	send := []string{"send"}
	if sessionID != "" {
		send = append(send, "-s", sessionID)
	}
	send = append(send, "-b", blockID, line)
	if err := Runner(send); err != nil {
		if script != "" {
			_ = removeScript(script)
		}
		return err
	}
	return nil
}

// showSession asks every connected macOS Rex app to switch its view to
// the given session and window. Best effort: the tab already exists, so
// a refusal (Remote Control off) or a missing app only prints a hint
// naming the session (label) the tab is in.
func showSession(sessionID, windowID, label string) {
	out, err := RunnerOutput([]string{"client", "ls", "--json"})
	if err != nil {
		return
	}
	var listed struct {
		Clients []struct {
			ClientID string `json:"client_id"`
			Info     *struct {
				Kind     string `json:"kind"`
				Platform string `json:"platform"`
			} `json:"info"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return
	}
	for _, c := range listed.Clients {
		if c.Info == nil || c.Info.Kind != "app" || c.Info.Platform != "macos" {
			continue
		}
		req, _ := json.Marshal(map[string]any{
			"client_id": c.ClientID,
			"action":    "session.select",
			"args":      map[string]string{"session_id": sessionID, "window_id": windowID},
		})
		if _, err := RunnerOutput([]string{"api", "call", "client.perform", string(req)}); err != nil {
			if strings.Contains(err.Error(), "Remote Control") {
				fmt.Fprintf(Warn, "flow: the task's tab is in Rex session %q — switch to it, or turn on Remote Control in Rex Settings → Rex Server so flow can switch for you\n", label)
				return
			}
			fmt.Fprintf(Warn, "flow: couldn't switch Rex to the new tab: %v\n", err)
		}
	}
}

// FocusSession tries to focus the Rex tab whose foreground process is
// `binary` carrying the given session UUID. Returns (true, nil) on
// focus, (false, nil) if no block in any session matches, and
// (false, err) only on a backend failure.
//
// Mechanism:
//
//  1. rex ls --json, current session first
//  2. per session, rex api call session.list_blocks
//  3. for each placed block, rex api call com.superlogical.terminal.process
//     → foreground {pid, argv0}
//  4. skip blocks whose argv0/name doesn't contain `binary`; for the
//     rest read the full argv via ps and apply the same UUID regex as
//     the kitty/iterm/zellij backends
//  5. rex focus -s <session> <window_id> on the first hit, then bring
//     that session into view if it isn't the current one
func FocusSession(sessionID, binaryName string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	current := os.Getenv("REX_SESSION")
	sessions, err := listSessions()
	if err != nil {
		return false, err
	}
	// Current session first: it's the likeliest home of the tab and
	// avoids switching the user's view when it's already there.
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].ID == current && sessions[j].ID != current
	})
	needle := strings.ToLower(sessionID)
	for _, s := range sessions {
		windowID, err := findHarnessWindow(s.ID, needle, binaryName)
		if err != nil {
			return false, err
		}
		if windowID == "" {
			continue
		}
		if err := Runner([]string{"focus", "-s", s.ID, windowID}); err != nil {
			return false, fmt.Errorf("rex focus: %w", err)
		}
		if s.ID != current {
			showSession(s.ID, windowID, s.Label)
		}
		return true, nil
	}
	return false, nil
}

// findHarnessWindow returns the window id of the first block in the
// given Rex session running `binary` with the session UUID needle, or ""
// when none matches.
func findHarnessWindow(rexSession, needle, binaryName string) (string, error) {
	out, err := RunnerOutput([]string{"api", "call", "session.list_blocks",
		fmt.Sprintf(`{"session_id":%q}`, rexSession)})
	if err != nil {
		return "", fmt.Errorf("rex session.list_blocks: %w", err)
	}
	var listed struct {
		Blocks []struct {
			BlockID   string `json:"block_id"`
			WindowID  string `json:"window_id"`
			Placement string `json:"placement"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return "", fmt.Errorf("parse rex list_blocks JSON: %w", err)
	}
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
		return b.WindowID, nil
	}
	return "", nil
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
