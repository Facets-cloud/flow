package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"flow/internal/agenthooks"
	"flow/internal/agents"
	"flow/internal/flowdb"
	"flow/internal/workdirreg"
	"flow/internal/worktree"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const terminalScrollbackBytes = 1024 * 1024 * 1024

var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || strings.Contains(origin, r.Host)
	},
}

type terminalHub struct {
	server   *Server
	mu       sync.Mutex
	sessions map[string]*terminalSession
	// sharedRunningCache backs sharedRunning, which is invoked once per task
	// per SSE tick (every 2s). Each raw call forks `tmux has-session` — with
	// N tasks visible that's N forks per tick. The cache collapses repeats
	// within a 2.5s window; we explicitly invalidate on create/kill so the
	// UI never lies after a state change the user just triggered.
	sharedRunningCache *ttlCache[string, bool]
}

type terminalLaunch struct {
	Slug         string
	SessionID    string
	Provider     string
	WorkDir      string
	Args         []string
	Created      bool
	NeedsCapture bool
	StartedAt    time.Time
}

type terminalSession struct {
	hub        *terminalHub
	slug       string
	sessionID  string
	provider   string
	workDir    string
	sharedName string
	cmd        *exec.Cmd
	tty        *os.File
	done       chan struct{}

	mu           sync.Mutex
	clients      map[*terminalClient]struct{}
	scrollback   []byte
	closed       bool
	exitStatus   string
	lastOutputAt time.Time
}

type terminalClient struct {
	conn      *websocket.Conn
	send      chan terminalWSMessage
	done      chan struct{}
	closeOnce sync.Once
}

type terminalWSMessage struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Message string `json:"message,omitempty"`
}

func newTerminalHub(s *Server) *terminalHub {
	return &terminalHub{
		server:             s,
		sessions:           map[string]*terminalSession{},
		sharedRunningCache: newTTLCache[string, bool](2500 * time.Millisecond),
	}
}

func (s *Server) handleTerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if err := validateSlug(slug); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	cols := intQueryDefault(r, "cols", 120)
	rows := intQueryDefault(r, "rows", 32)
	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	sess, err := s.terminals.attach(slug, cols, rows)
	if err != nil {
		_ = conn.WriteJSON(terminalWSMessage{Type: "error", Message: err.Error()})
		_ = conn.Close()
		return
	}

	client := &terminalClient{conn: conn, send: make(chan terminalWSMessage, 128), done: make(chan struct{})}
	sess.addClient(client, true)

	go client.writeLoop()
	client.readLoop(sess)
	sess.removeClient(client)
}

func intQueryDefault(r *http.Request, key string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return def
	}
	if value > 500 {
		return 500
	}
	return value
}

func (h *terminalHub) attach(slug string, cols, rows int) (*terminalSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sess := h.sessions[slug]; sess != nil && sess.running() {
		_ = sess.resize(cols, rows)
		return sess, nil
	}
	launch, err := h.server.prepareTerminalLaunch(slug)
	if err != nil {
		return nil, err
	}
	sess, err := h.startSessionLocked(launch, cols, rows)
	if err != nil {
		if launch.Created {
			h.server.rollbackPreparedTerminalLaunch(launch)
		}
		return nil, err
	}
	h.sessions[slug] = sess
	return sess, nil
}

func (h *terminalHub) stop(slug string) {
	h.mu.Lock()
	sess := h.sessions[slug]
	delete(h.sessions, slug)
	h.mu.Unlock()
	if sess != nil {
		sess.terminate()
	}
}

func (h *terminalHub) sendInput(slug, data string) error {
	h.mu.Lock()
	sess := h.sessions[slug]
	h.mu.Unlock()
	if sess == nil || !sess.running() {
		return fmt.Errorf("terminal session for %s is not running", slug)
	}
	return sess.write(data)
}

func terminalPasteInput(prompt string) string {
	return "\x1b[200~" + prompt + "\x1b[201~\r"
}

func (h *terminalHub) wakeTask(slug, prompt string) error {
	if err := h.sendInput(slug, terminalPasteInput(prompt)); err == nil {
		return nil
	}
	if _, err := h.attach(slug, 120, 32); err != nil {
		return err
	}
	return h.sendInput(slug, terminalPasteInput(prompt))
}

func (h *terminalHub) scrollbackText(slug string, limit int) (string, bool) {
	h.mu.Lock()
	sess := h.sessions[slug]
	h.mu.Unlock()
	if sess == nil {
		return "", false
	}
	sess.mu.Lock()
	data := append([]byte(nil), sess.scrollback...)
	sess.mu.Unlock()
	if len(data) == 0 {
		return "", true
	}
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
	}
	return string(stripTerminalAltScreenControls(data)), true
}

func (h *terminalHub) lastOutputAt(slug string) (time.Time, bool) {
	h.mu.Lock()
	sess := h.sessions[slug]
	h.mu.Unlock()
	if sess == nil {
		return time.Time{}, false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.lastOutputAt, !sess.lastOutputAt.IsZero()
}

func (h *terminalHub) sharedSessionName(slug string) (string, bool) {
	h.mu.Lock()
	sess := h.sessions[slug]
	h.mu.Unlock()
	if sess == nil || !sess.running() {
		return "", false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.sharedName, sess.sharedName != ""
}

func (h *terminalHub) sharedRunning(slug string) bool {
	if v, ok := h.sharedRunningCache.get(slug); ok {
		return v
	}
	var running bool
	if name, ok := h.sharedSessionName(slug); ok {
		running = sharedTerminalHasSession(name)
	} else {
		running = sharedTerminalHasSession(sharedTerminalSessionName(slug))
	}
	h.sharedRunningCache.set(slug, running)
	return running
}

func (h *terminalHub) startSessionLocked(launch terminalLaunch, cols, rows int) (*terminalSession, error) {
	provider := launch.Provider
	if provider == "" {
		provider = agents.ProviderClaude
	}
	if err := h.server.ensureProviderAvailable(provider); err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	sharedName := ""
	initialScrollback := []byte(nil)
	if sharedTerminalAvailable() {
		name, created, err := h.server.ensureSharedTerminalSession(launch, cols, rows)
		if err != nil {
			return nil, err
		}
		sharedName = name
		if !created {
			history, historyErr := sharedTerminalCaptureHistory(name)
			if historyErr != nil {
				fmt.Fprintf(os.Stderr, "warning: capture shared terminal history: %v\n", historyErr)
			} else {
				initialScrollback = history
			}
		}
		// `-f` is server-startup only — only the first tmux invocation
		// that actually starts the tmux server reads the config; later
		// invocations against the same server ignore it. Passing it on
		// attach is a defensive belt-and-braces in case attach races
		// ahead of new-session in some path. ensureTmuxConfig errors
		// are non-fatal: tmux without our config still works, just
		// without the mouse-scroll default.
		attachArgs := []string{"attach-session", "-t", name}
		if cfgPath, cfgErr := ensureTmuxConfig(h.server.cfg.FlowRoot); cfgErr == nil && cfgPath != "" {
			attachArgs = append([]string{"-f", cfgPath}, attachArgs...)
		}
		cmd = exec.Command("tmux", attachArgs...)
	} else {
		bin := provider
		if provider == agents.ProviderClaude {
			bin = "claude"
		}
		cmd = exec.Command(bin, launch.Args...)
	}
	cmd.Dir = launch.WorkDir
	cmd.Env = append(terminalEnvWithHook(h.server.cfg.FlowRoot, h.server.cfg.CommandPath, h.server.cfg.HookURL),
		"FLOW_TASK="+launch.Slug,
		"FLOW_SESSION_PROVIDER="+provider,
	)

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("start %s terminal: %w", provider, err)
	}
	sess := &terminalSession{
		hub:        h,
		slug:       launch.Slug,
		sessionID:  launch.SessionID,
		provider:   provider,
		workDir:    launch.WorkDir,
		sharedName: sharedName,
		cmd:        cmd,
		tty:        f,
		done:       make(chan struct{}),
		clients:    map[*terminalClient]struct{}{},
		scrollback: initialScrollback,
	}
	go sess.readPTY()
	go sess.wait()
	if launch.NeedsCapture {
		go sess.captureCodexSession(launch.StartedAt)
	}
	// A new session just started; force the next sharedRunning check to
	// re-query tmux so the UI flips to "running" within one tick.
	h.sharedRunningCache.invalidate(launch.Slug)
	return sess, nil
}

func terminalEnv(flowRoot, commandPath string) []string {
	env := os.Environ()
	env = setEnvValue(env, "TERM", "xterm-256color")
	env = setEnvValue(env, "COLORTERM", "truecolor")
	env = setEnvValue(env, "FORCE_COLOR", "1")
	env = setEnvValue(env, "TERM_PROGRAM", "flow-ui")
	env = setEnvValue(env, "CLAUDE_CODE_NO_FLICKER", "0")
	env = setEnvValue(env, "CLAUDE_CODE_DISABLE_MOUSE", "1")
	env = appendEnvDefault(env, "LANG", "en_US.UTF-8")
	env = appendEnvDefault(env, "LC_CTYPE", "en_US.UTF-8")
	// Mark this PTY as flow-spawned for the agent-event hook (see
	// internal/app/hook.go injectHookMetadata). Lets the server tell
	// apart flow-managed sessions from ambient agents in the same repo.
	env = setEnvValue(env, "FLOW_HOOK_OWNED", "1")
	if root := strings.TrimSpace(flowRoot); root != "" {
		env = setEnvValue(env, "FLOW_ROOT", root)
	} else if root := os.Getenv("FLOW_ROOT"); root != "" {
		env = setEnvValue(env, "FLOW_ROOT", root)
	}
	return prependCommandDirToPath(env, commandPath)
}

func terminalEnvWithHook(flowRoot, commandPath, hookURL string) []string {
	env := terminalEnv(flowRoot, commandPath)
	if hookURL = strings.TrimSpace(hookURL); hookURL != "" {
		env = setEnvValue(env, "FLOW_HOOK_URL", hookURL)
	}
	return env
}

func terminalEnvMap(flowRoot, commandPath, hookURL, slug, provider string) map[string]string {
	env := terminalEnvWithHook(flowRoot, commandPath, hookURL)
	out := map[string]string{
		"TERM":                      "xterm-256color",
		"COLORTERM":                 "truecolor",
		"FORCE_COLOR":               "1",
		"TERM_PROGRAM":              "flow-ui",
		"CLAUDE_CODE_NO_FLICKER":    "0",
		"CLAUDE_CODE_DISABLE_MOUSE": "1",
		"LANG":                      "en_US.UTF-8",
		"LC_CTYPE":                  "en_US.UTF-8",
		"FLOW_HOOK_OWNED":           "1",
		"FLOW_TASK":                 slug,
		"FLOW_SESSION_PROVIDER":     provider,
	}
	for _, key := range []string{"PATH", "FLOW_ROOT", "FLOW_HOOK_URL"} {
		if value := envValueLocal(env, key); value != "" {
			out[key] = value
		}
	}
	return out
}

func setEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func appendEnvDefault(env []string, key, value string) []string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

func prependCommandDirToPath(env []string, commandPath string) []string {
	commandPath = strings.TrimSpace(commandPath)
	if commandPath == "" {
		return env
	}
	dir := filepath.Dir(commandPath)
	if dir == "." || dir == "" {
		return env
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	current := envValueLocal(env, "PATH")
	if current == "" {
		return setEnvValue(env, "PATH", dir)
	}
	for _, part := range filepath.SplitList(current) {
		if part == dir {
			return env
		}
	}
	return setEnvValue(env, "PATH", dir+string(os.PathListSeparator)+current)
}

func envValueLocal(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

var (
	sharedTerminalLookPath = exec.LookPath
	sharedTerminalCommand  = func(args ...string) ([]byte, error) {
		return exec.Command("tmux", args...).CombinedOutput()
	}

	// sharedTerminalAvailable resolves once per process and is the result of
	// an exec.LookPath("tmux") — which walks every directory in $PATH doing
	// stat() syscalls. Before caching, this ran on every per-task UI refresh
	// (~15 tasks × every 2s). Tests that swap sharedTerminalLookPath must
	// call resetSharedTerminalAvailable() to invalidate.
	sharedTerminalAvailableOnce sync.Once
	sharedTerminalAvailableVal  bool
)

func sharedTerminalAvailable() bool {
	sharedTerminalAvailableOnce.Do(func() {
		_, err := sharedTerminalLookPath("tmux")
		sharedTerminalAvailableVal = err == nil
	})
	return sharedTerminalAvailableVal
}

// resetSharedTerminalAvailable forces the next sharedTerminalAvailable call to
// re-run LookPath. Tests that swap sharedTerminalLookPath rely on this.
func resetSharedTerminalAvailable() {
	sharedTerminalAvailableOnce = sync.Once{}
	sharedTerminalAvailableVal = false
}

func sharedTerminalSessionName(slug string) string {
	var b strings.Builder
	b.WriteString("flow-")
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if len(name) > 80 {
		return name[:80]
	}
	return name
}

func sharedTerminalHasSession(name string) bool {
	if strings.TrimSpace(name) == "" || !sharedTerminalAvailable() {
		return false
	}
	_, err := sharedTerminalCommand("has-session", "-t", name)
	return err == nil
}

func sharedTerminalCaptureHistory(name string) ([]byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("tmux session name not set")
	}
	out, err := sharedTerminalCommand("capture-pane", "-p", "-e", "-S", "-", "-E", "-1", "-t", name)
	if err != nil {
		return nil, fmt.Errorf("capture tmux history for %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return normalizeCapturedPaneForTerminal(out), nil
}

func normalizeCapturedPaneForTerminal(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	if !bytes.HasSuffix(data, []byte("\r\n")) {
		data = append(data, '\r', '\n')
	}
	return data
}

func sharedTerminalKillSession(name string) error {
	if strings.TrimSpace(name) == "" || !sharedTerminalAvailable() {
		return nil
	}
	_, err := sharedTerminalCommand("kill-session", "-t", name)
	return err
}

func (s *Server) ensureSharedTerminalSession(launch terminalLaunch, cols, rows int) (string, bool, error) {
	if !sharedTerminalAvailable() {
		return "", false, errors.New("read-write native/browser terminal sharing requires tmux on PATH")
	}
	name := sharedTerminalSessionName(launch.Slug)
	if sharedTerminalHasSession(name) {
		if err := ensureSharedTerminalScrollOptions(name); err != nil {
			return "", false, err
		}
		return name, false, nil
	}
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 32
	}
	if cols > 500 {
		cols = 500
	}
	if rows > 500 {
		rows = 500
	}
	provider := launch.Provider
	if provider == "" {
		provider = agents.ProviderClaude
	}
	command := agentShellCommand(provider, launch.Args)
	env := terminalEnvMap(s.cfg.FlowRoot, s.cfg.CommandPath, s.cfg.HookURL, launch.Slug, provider)
	// Prepend `-f <flowRoot>/tmux.conf` so the tmux server we're about
	// to start picks up flow's defaults (mouse scroll + larger
	// scrollback). The user's ~/.tmux.conf is sourced from inside our
	// config so personal preferences still win. ensureTmuxConfig writes
	// the file on first call per process; errors degrade gracefully —
	// the session still starts, just without the mouse-scroll default.
	args := []string{}
	if cfgPath, cfgErr := ensureTmuxConfig(s.cfg.FlowRoot); cfgErr == nil && cfgPath != "" {
		args = append(args, "-f", cfgPath)
	}
	args = append(args,
		"set-option",
		"-g",
		"mouse",
		"on",
		";",
		// Make tmux's own copy commands forward selections to the outer
		// terminal via OSC 52. Without this, dragging-to-select in the
		// browser terminal puts text into tmux's internal paste buffer
		// but never reaches the page's clipboard.
		"set-option",
		"-g",
		"set-clipboard",
		"on",
		";",
		// Bind mouse-drag-end to copy-and-cancel so a single drag is the
		// whole copy gesture (instead of leaving the user stranded in
		// copy-mode). Bind both copy-mode and copy-mode-vi so this works
		// regardless of the user's mode-keys preference.
		"bind-key",
		"-T",
		"copy-mode",
		"MouseDragEnd1Pane",
		"send-keys",
		"-X",
		"copy-pipe-and-cancel",
		";",
		"bind-key",
		"-T",
		"copy-mode-vi",
		"MouseDragEnd1Pane",
		"send-keys",
		"-X",
		"copy-pipe-and-cancel",
		";",
		"set-window-option",
		"-g",
		"history-limit",
		sharedTerminalHistoryLimit,
		";",
		"new-session",
		"-d",
		"-s", name,
		"-c", launch.WorkDir,
		"-x", strconv.Itoa(cols),
		"-y", strconv.Itoa(rows),
		shellCommandLine(command, env),
	)
	out, err := sharedTerminalCommand(args...)
	if err != nil {
		if sharedTerminalHasSession(name) {
			if optErr := ensureSharedTerminalScrollOptions(name); optErr != nil {
				return "", false, optErr
			}
			return name, false, nil
		}
		return "", false, fmt.Errorf("start shared terminal session %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	if err := ensureSharedTerminalScrollOptions(name); err != nil {
		_ = sharedTerminalKillSession(name)
		return "", false, err
	}
	return name, true, nil
}

var terminalAltScreenRE = regexp.MustCompile(`\x1b\[\?([0-9;]*)([hl])`)
var terminalGeneratedInputRE = regexp.MustCompile(`\x1b\[(?:\?[0-9;]*|>[0-9;]*)c`)

func stripTerminalAltScreenControls(data []byte) []byte {
	return terminalAltScreenRE.ReplaceAllFunc(data, func(seq []byte) []byte {
		match := terminalAltScreenRE.FindSubmatch(seq)
		if len(match) < 2 {
			return seq
		}
		for _, mode := range strings.Split(string(match[1]), ";") {
			switch mode {
			case "47", "1047", "1048", "1049":
				return nil
			}
		}
		return seq
	})
}

func stripTerminalGeneratedInput(data string) string {
	return terminalGeneratedInputRE.ReplaceAllString(data, "")
}

func (s *Server) prepareTerminalLaunch(slug string) (terminalLaunch, error) {
	tx, err := s.cfg.DB.Begin()
	if err != nil {
		return terminalLaunch{}, err
	}
	defer tx.Rollback()

	task, err := flowdb.ScanTask(tx.QueryRow("SELECT "+flowdb.TaskCols+" FROM tasks WHERE slug = ?", slug))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return terminalLaunch{}, fmt.Errorf("task not found: %s", slug)
		}
		return terminalLaunch{}, err
	}
	if task.Slug == overviewTaskSlug {
		flowRoot := strings.TrimSpace(s.cfg.FlowRoot)
		if flowRoot == "" {
			return terminalLaunch{}, errors.New("flow root is not configured")
		}
		absRoot, err := filepath.Abs(flowRoot)
		if err != nil {
			return terminalLaunch{}, err
		}
		if err := os.MkdirAll(filepath.Join(absRoot, "tasks", overviewTaskSlug, "updates"), 0o755); err != nil {
			return terminalLaunch{}, err
		}
		now := flowdb.NowISO()
		if _, err := tx.Exec(
			`UPDATE tasks SET
				project_slug = NULL,
				status = 'backlog',
				kind = 'regular',
				playbook_slug = NULL,
				work_dir = ?,
				waiting_on = NULL,
				session_provider = 'claude',
				session_id = NULL,
				session_started = NULL,
				session_last_resumed = NULL,
				status_changed_at = ?,
				updated_at = ?
			 WHERE slug = ?`,
			absRoot, now, now, task.Slug,
		); err != nil {
			return terminalLaunch{}, err
		}
		task.ProjectSlug = sql.NullString{}
		task.Status = "backlog"
		task.Kind = "regular"
		task.PlaybookSlug = sql.NullString{}
		task.WorkDir = absRoot
		task.WaitingOn = sql.NullString{}
		task.SessionID = sql.NullString{}
		task.SessionStarted = sql.NullString{}
		task.SessionLastResumed = sql.NullString{}
	}
	if task.Status == "done" {
		return terminalLaunch{}, fmt.Errorf("task %s is done; move it back to in-progress before reopening", task.Slug)
	}
	if strings.TrimSpace(task.WorkDir) == "" {
		return terminalLaunch{}, fmt.Errorf("task %s has no work_dir", task.Slug)
	}
	if err := flowdb.EnsureTaskStartable(s.cfg.DB, task); err != nil {
		return terminalLaunch{}, err
	}

	now := flowdb.NowISO()
	sessionID := strings.TrimSpace(task.SessionID.String)
	provider := task.SessionProvider
	if provider == "" {
		provider = agents.ProviderClaude
	}
	created := false
	if sessionID == "" {
		created = true
		if provider == agents.ProviderCodex {
			if _, err := tx.Exec(
				`UPDATE tasks SET
					status = 'in-progress',
					status_changed_at = ?,
					session_provider = 'codex',
					session_id = NULL,
					session_started = ?,
					updated_at = ?
				 WHERE slug = ?`,
				now, now, now, task.Slug,
			); err != nil {
				return terminalLaunch{}, err
			}
		} else {
			sessionID = uuid.NewString()
			if _, err := tx.Exec(
				`UPDATE tasks SET
					status = 'in-progress',
					status_changed_at = ?,
					session_provider = 'claude',
					session_id = ?,
					session_started = ?,
					updated_at = ?
				 WHERE slug = ?`,
				now, sessionID, now, now, task.Slug,
			); err != nil {
				return terminalLaunch{}, err
			}
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE tasks SET
				status = 'in-progress',
				session_last_resumed = ?,
				updated_at = ?
			 WHERE slug = ?`,
			now, now, task.Slug,
		); err != nil {
			return terminalLaunch{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return terminalLaunch{}, err
	}

	originalWorkDir := task.WorkDir
	if task.Slug != overviewTaskSlug {
		wt, wtErr := worktree.Ensure(originalWorkDir, provider, task.Slug)
		if wtErr != nil {
			if created {
				s.rollbackPreparedTerminalLaunch(terminalLaunch{
					Slug:      task.Slug,
					SessionID: sessionID,
					Provider:  provider,
				})
			}
			return terminalLaunch{}, fmt.Errorf("worktree setup failed for %s: %w", task.Slug, wtErr)
		}
		if wt.IsRepo {
			task.WorkDir = wt.WorktreePath
			task.WorktreePath = sql.NullString{String: wt.WorktreePath, Valid: true}
			if _, err := s.cfg.DB.Exec(
				`UPDATE tasks SET worktree_path = ?, updated_at = ? WHERE slug = ?`,
				wt.WorktreePath, flowdb.NowISO(), task.Slug,
			); err != nil {
				if created {
					s.rollbackPreparedTerminalLaunch(terminalLaunch{
						Slug:      task.Slug,
						SessionID: sessionID,
						Provider:  provider,
					})
				}
				return terminalLaunch{}, fmt.Errorf("persist worktree_path: %w", err)
			}
		}
	}

	if err := workdirreg.Touch(s.cfg.DB, originalWorkDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: bump workdir last_used_at: %v\n", err)
	}
	if _, err := agenthooks.InstallLocalWithOptions(task.WorkDir, agenthooks.InstallOptions{
		CommandPath: s.cfg.CommandPath,
		HookURL:     s.cfg.HookURL,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: install local agent hooks: %v\n", err)
	}

	if created {
		prompt := buildBrowserTerminalBootstrapPrompt(s.cfg.DB, task)
		if task.Slug == overviewTaskSlug {
			prompt = overviewInitialPrompt(s.cfg.FlowRoot, task)
		}
		args := agentTerminalArgs(provider, true, sessionID, task.WorkDir, s.cfg.FlowRoot, prompt, task.PermissionMode)
		return terminalLaunch{
			Slug:         task.Slug,
			SessionID:    sessionID,
			Provider:     provider,
			WorkDir:      task.WorkDir,
			Args:         args,
			Created:      created,
			NeedsCapture: provider == agents.ProviderCodex,
			StartedAt:    time.Now().Add(-2 * time.Second),
		}, nil
	}
	args := agentTerminalArgs(provider, false, sessionID, task.WorkDir, s.cfg.FlowRoot, "", task.PermissionMode)
	return terminalLaunch{
		Slug:      task.Slug,
		SessionID: sessionID,
		Provider:  provider,
		WorkDir:   task.WorkDir,
		Args:      args,
		Created:   created,
	}, nil
}

func (s *terminalSession) captureCodexSession(started time.Time) {
	if started.IsZero() {
		started = time.Now().Add(-2 * time.Second)
	}
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case <-s.done:
			return
		case <-deadline:
			return
		case <-ticker.C:
			captured, err := agents.CaptureCodexSessionForTaskSince(s.hub.server.cfg.DB, s.slug, s.workDir, started)
			if err != nil || captured == "" {
				continue
			}
			s.mu.Lock()
			s.sessionID = captured
			clients := make([]*terminalClient, 0, len(s.clients))
			for client := range s.clients {
				clients = append(clients, client)
			}
			s.mu.Unlock()
			for _, client := range clients {
				client.queue(terminalWSMessage{Type: "status", Message: "connected to codex session " + captured})
			}
			return
		}
	}
}

func agentTerminalArgs(provider string, fresh bool, sessionID, workDir, flowRootPath, prompt, permissionMode string) []string {
	if provider == agents.ProviderCodex {
		args := []string{"--no-alt-screen", "-C", workDir}
		args = appendCodexWritableRoot(args, workDir, flowRootPath)
		args = append(args, codexPermissionArgs(permissionMode)...)
		if fresh {
			return append(args, prompt)
		}
		resume := []string{"resume", "--include-non-interactive", "--no-alt-screen", "-C", workDir}
		resume = appendCodexWritableRoot(resume, workDir, flowRootPath)
		resume = append(resume, codexPermissionArgs(permissionMode)...)
		return append(resume, sessionID)
	}
	if fresh {
		args := []string{"--session-id", sessionID}
		args = append(args, claudePermissionArgs(permissionMode)...)
		return append(args, prompt)
	}
	args := []string{"--resume", sessionID}
	return append(args, claudePermissionArgs(permissionMode)...)
}

func appendCodexWritableRoot(args []string, workDir, flowRootPath string) []string {
	flowRootPath = strings.TrimSpace(flowRootPath)
	if flowRootPath == "" {
		return args
	}
	cleanWorkDir := strings.TrimSpace(workDir)
	if cleanWorkDir != "" {
		if abs, err := filepath.Abs(cleanWorkDir); err == nil {
			cleanWorkDir = abs
		}
	}
	if abs, err := filepath.Abs(flowRootPath); err == nil {
		flowRootPath = abs
	}
	if cleanWorkDir == flowRootPath {
		return args
	}
	return append(args, "--add-dir", flowRootPath)
}

func claudePermissionArgs(mode string) []string {
	switch strings.TrimSpace(mode) {
	case "auto":
		return []string{"--permission-mode", "auto"}
	case "bypass":
		return []string{"--dangerously-skip-permissions"}
	default:
		// `default` is the moderate baseline: auto-accept file edits but
		// still prompt for execution. `auto` and `bypass` cover the more
		// permissive options.
		return []string{"--permission-mode", "acceptEdits"}
	}
}

func codexPermissionArgs(mode string) []string {
	switch strings.TrimSpace(mode) {
	case "auto":
		return []string{"--ask-for-approval", "never", "--sandbox", "workspace-write"}
	case "bypass":
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	default:
		return []string{"--ask-for-approval", "on-request", "--sandbox", "workspace-write"}
	}
}

func overviewInitialPrompt(root string, task *flowdb.Task) string {
	body, err := os.ReadFile(filepath.Join(root, "tasks", task.Slug, "brief.md"))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(body))
	const marker = "Latest user request:"
	if idx := strings.LastIndex(text, marker); idx >= 0 {
		return strings.TrimSpace(text[idx+len(marker):])
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "#") {
		text = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
	return text
}

func (s *Server) rollbackPreparedTerminalLaunch(launch terminalLaunch) {
	if launch.Slug == "" {
		return
	}
	if launch.Provider == agents.ProviderCodex && launch.SessionID == "" {
		if _, err := s.cfg.DB.Exec(
			`UPDATE tasks SET
				session_id = NULL,
				session_started = NULL,
				status = 'backlog',
				status_changed_at = NULL,
				updated_at = ?
			 WHERE slug = ? AND session_provider = 'codex' AND session_id IS NULL`,
			flowdb.NowISO(), launch.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "warning: rollback browser codex terminal session: %v\n", err)
		}
		return
	}
	if launch.SessionID == "" {
		return
	}
	if _, err := s.cfg.DB.Exec(
		`UPDATE tasks SET
			session_id = NULL,
			session_started = NULL,
			status = 'backlog',
			status_changed_at = NULL,
			updated_at = ?
		 WHERE slug = ? AND session_id = ?`,
		flowdb.NowISO(), launch.Slug, launch.SessionID,
	); err != nil {
		fmt.Fprintf(os.Stderr, "warning: rollback browser terminal session: %v\n", err)
	}
}

func buildBrowserTerminalBootstrapPrompt(db *sql.DB, task *flowdb.Task) string {
	if task.Kind != "playbook_run" {
		return fmt.Sprintf(
			"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
				"1. Load the flow operating manual. If a Skill tool is available, invoke the flow skill via the Skill tool. Otherwise read ~/.codex/skills/flow/SKILL.md or ~/.claude/skills/flow/SKILL.md, whichever exists. This governs workflows, bootstrap contract, KB discipline, and scope-creep detection.\n"+
				"2. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files listed under other: are sidecar references; load on demand when relevant, not eagerly.\n"+
				"3. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief AND every file under updates:. Files under other: are on-demand references.\n"+
				"4. Read AGENTS.md and/or CLAUDE.md in your work_dir and any nested convention files under subdirectories you will modify. These override any assumption from the brief.\n"+
				"5. Only then begin work. If any brief section is blank or unclear, ASK; do not infer.",
			task.Slug,
		)
	}
	playbookSlug := ""
	if task.PlaybookSlug.Valid {
		playbookSlug = task.PlaybookSlug.String
	}
	isFirstRun := false
	if playbookSlug != "" {
		var runCount int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM tasks WHERE playbook_slug = ? AND kind = 'playbook_run' AND archived_at IS NULL AND deleted_at IS NULL`,
			playbookSlug,
		).Scan(&runCount); err == nil {
			isFirstRun = runCount <= 1
		}
	}
	prompt := fmt.Sprintf(
		"You are running playbook %s as run %s. Do ALL of the following in order before executing anything:\n"+
			"1. Load the flow operating manual. If a Skill tool is available, invoke the flow skill via the Skill tool. Otherwise read ~/.codex/skills/flow/SKILL.md or ~/.claude/skills/flow/SKILL.md, whichever exists.\n"+
			"2. Run: flow show playbook %s. This shows the playbook definition and recent runs as context only, not your instructions.\n"+
			"3. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. The brief is your authoritative snapshot for this run.\n"+
			"4. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief and every file under updates:.\n"+
			"5. Read AGENTS.md and/or CLAUDE.md in your work_dir.\n"+
			"6. Only then begin executing your brief.",
		playbookSlug, task.Slug, playbookSlug,
	)
	if isFirstRun {
		prompt += "\n\nThis is the first run of this playbook. Be proactive about asking whether scripts, decision rules, and edge cases discovered during the run should be captured back into the live playbook for future runs."
	}
	return prompt
}

func (s *terminalSession) running() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *terminalSession) addClient(client *terminalClient, replay bool) {
	s.mu.Lock()
	s.clients[client] = struct{}{}
	scrollback := append([]byte(nil), s.scrollback...)
	status := s.exitStatus
	closed := s.closed
	provider := s.provider
	sessionID := s.sessionID
	s.mu.Unlock()

	if provider == "" {
		provider = agents.ProviderClaude
	}
	message := "connected to " + provider
	if sessionID != "" {
		message += " session " + sessionID
	} else {
		message += " session pending capture"
	}
	client.queue(terminalWSMessage{Type: "status", Message: message})
	if replay && len(scrollback) > 0 {
		scrollback = stripTerminalAltScreenControls(scrollback)
		if len(scrollback) > 0 {
			client.queue(terminalWSMessage{Type: "output", Data: string(scrollback)})
		}
	}
	if closed {
		client.queue(terminalWSMessage{Type: "status", Message: status})
	}
}

func (s *terminalSession) removeClient(client *terminalClient) {
	s.mu.Lock()
	delete(s.clients, client)
	s.mu.Unlock()
	client.close()
}

func (s *terminalSession) readPTY() {
	buf := make([]byte, 8192)
	pending := []byte{}
	for {
		n, err := s.tty.Read(buf)
		if n > 0 {
			data := append(pending, buf[:n]...)
			ready, rest := completeUTF8Prefix(data)
			pending = rest
			if len(ready) > 0 {
				ready = stripTerminalAltScreenControls(ready)
				if len(ready) > 0 {
					s.appendScrollback(ready)
					s.broadcast(terminalWSMessage{Type: "output", Data: string(ready)})
				}
			}
		}
		if err != nil {
			if len(pending) > 0 {
				ready := bytes.ToValidUTF8(pending, []byte("\uFFFD"))
				ready = stripTerminalAltScreenControls(ready)
				if len(ready) > 0 {
					s.appendScrollback(ready)
					s.broadcast(terminalWSMessage{Type: "output", Data: string(ready)})
				}
			}
			return
		}
	}
}

func completeUTF8Prefix(data []byte) ([]byte, []byte) {
	if utf8.Valid(data) {
		return data, nil
	}
	for tailLen := 1; tailLen <= 3 && tailLen <= len(data); tailLen++ {
		head := data[:len(data)-tailLen]
		tail := data[len(data)-tailLen:]
		if utf8.Valid(head) && !utf8.FullRune(tail) {
			return head, append([]byte(nil), tail...)
		}
	}
	return bytes.ToValidUTF8(data, []byte("\uFFFD")), nil
}

func (s *terminalSession) wait() {
	err := s.cmd.Wait()
	provider := s.provider
	if provider == "" {
		provider = agents.ProviderClaude
	}
	status := provider + " terminal exited"
	if err != nil {
		status = provider + " terminal exited: " + err.Error()
	}
	_ = s.tty.Close()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.exitStatus = status
		close(s.done)
	}
	clients := make([]*terminalClient, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	for _, client := range clients {
		client.queue(terminalWSMessage{Type: "status", Message: status})
	}
	s.hub.mu.Lock()
	if s.hub.sessions[s.slug] == s {
		delete(s.hub.sessions, s.slug)
	}
	s.hub.mu.Unlock()
	s.hub.sharedRunningCache.invalidate(s.slug)
}

func (s *terminalSession) terminate() {
	if s.sharedName != "" {
		_ = sharedTerminalKillSession(s.sharedName)
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}
	if s.tty != nil {
		_ = s.tty.Close()
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.exitStatus = "terminal stopped"
		if s.done != nil {
			close(s.done)
		}
	}
	s.mu.Unlock()
	if s.hub != nil && s.hub.sharedRunningCache != nil {
		s.hub.sharedRunningCache.invalidate(s.slug)
	}
}

func (s *terminalSession) appendScrollback(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrollback = append(s.scrollback, data...)
	s.lastOutputAt = time.Now()
	if len(s.scrollback) > terminalScrollbackBytes {
		s.scrollback = append([]byte(nil), s.scrollback[len(s.scrollback)-terminalScrollbackBytes:]...)
	}
}

func (s *terminalSession) broadcast(msg terminalWSMessage) {
	s.mu.Lock()
	clients := make([]*terminalClient, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	for _, client := range clients {
		client.queue(msg)
	}
}

func (s *terminalSession) write(data string) error {
	if data == "" {
		return nil
	}
	_, err := s.tty.Write([]byte(data))
	return err
}

func (s *terminalSession) resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	if cols > 500 {
		cols = 500
	}
	if rows > 500 {
		rows = 500
	}
	return pty.Setsize(s.tty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (c *terminalClient) readLoop(sess *terminalSession) {
	defer c.conn.Close()
	c.conn.SetReadLimit(64 * 1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	for {
		var msg terminalWSMessage
		if err := c.conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case "input":
			if input := stripTerminalGeneratedInput(msg.Data); input != "" {
				_ = sess.write(input)
			}
		case "resize":
			_ = sess.resize(msg.Cols, msg.Rows)
		}
	}
}

func (c *terminalClient) writeLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *terminalClient) queue(msg terminalWSMessage) {
	select {
	case c.send <- msg:
	case <-c.done:
	default:
	}
}

func (c *terminalClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}
