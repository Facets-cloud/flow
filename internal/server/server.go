package server

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"flow/internal/monitor"
)

//go:embed all:static
var staticFS embed.FS

func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	s.terminals = newTerminalHub(s)
	s.events = newEventHub()
	s.reconcile = newLivenessReconciler(s)
	s.transcripts = newTranscriptCache()
	s.caches = newUICaches()
	s.dbWatcher = newDBWatcher(s)
	s.respawn = newRespawnGate(respawnDebounceWindow)
	s.inboxMonitors = newInboxMonitorManager(inboxWakeTarget{server: s})
	s.monitorReconcile = newMonitorReconciler(s)
	// Resolves Slack user/channel IDs to display names for the Inbox UI.
	// Nil when no Slack token is configured; all uses are nil-safe.
	s.nameResolver = monitor.NewSlackNameResolver()
	// Slack Socket Mode listener: only constructed when a DB is available
	// (the dispatcher needs one). Start()/Stop() are no-ops when the env
	// isn't configured for Socket Mode, so wiring is safe to leave in
	// place at all times. The opener attaches new slack-reply tasks to
	// a server-managed PTY so the Claude session streams into the UI
	// instead of an iTerm tab.
	if cfg.DB != nil {
		slackListener := monitor.NewSlackListener(
			monitor.NewDispatcher(cfg.DB, &slackTaskOpener{server: s}),
		)
		slackListener.SetChangeNotifier(func(kind string) {
			s.publishUIChange(kind)
		})
		s.slackListener = slackListener
		s.githubListener = monitor.NewGitHubListener(
			monitor.NewGitHubDispatcher(cfg.DB, &slackTaskOpener{server: s}),
		)
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/ui-data.js", s.handleUIDataJS)
	mux.HandleFunc("/api/ui-data", s.handleUIDataJSON)
	mux.HandleFunc("/api/events", s.handleUIEvents)
	mux.HandleFunc("/api/actions", s.handleAction)
	mux.HandleFunc("/api/hooks/agent", s.handleAgentHook)
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/fs/entries", s.handleFSEntries)
	mux.HandleFunc("/api/fs/mkdir", s.handleFSMkdir)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/", s.handleTaskRoute)
	mux.HandleFunc("/api/inbox", s.handleInbox)
	mux.HandleFunc("/api/inbox/conversation", s.handleInboxConversation)
	mux.HandleFunc("/api/projects", s.handleProjects)
	mux.HandleFunc("/api/projects/", s.handleProjectRoute)
	mux.HandleFunc("/api/playbooks", s.handlePlaybooks)
	mux.HandleFunc("/api/playbooks/", s.handlePlaybookRoute)
	mux.HandleFunc("/api/workdirs", s.handleWorkdirs)
	mux.HandleFunc("/api/tags", s.handleTags)
	mux.HandleFunc("/api/kb", s.handleKB)
	mux.HandleFunc("/api/kb/", s.handleKBFile)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/ws/terminal", s.handleTerminalWebSocket)
	mux.HandleFunc("/ws/events", s.handleEventWebSocket)
	mux.HandleFunc("/ws", s.handleWebSocketPlaceholder)
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

func (s *Server) ListenAndServe(addr string) int {
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// Start the liveness reconciler so dead Claude/Codex sessions get
	// detected even when their Stop/SessionEnd hook never fires. Stops
	// cleanly on shutdown via the deferred s.reconcile.stop().
	if s.reconcile != nil {
		s.reconcile.start()
		defer s.reconcile.stop()
	}
	// Start the persistent-monitor reconciler: restore background monitors for
	// Slack/GitHub/branch-linked tasks on boot, recreate any that die, and
	// tear down monitors for finished tasks.
	if s.monitorReconcile != nil {
		s.monitorReconcile.start()
		defer s.monitorReconcile.stop()
	}
	// Watch SQLite data_version so writes from external processes
	// (notably the flow CLI) trigger an SSE refresh within ~1s without
	// each CLI command needing to notify us out-of-band.
	if s.dbWatcher != nil {
		s.dbWatcher.start()
		defer s.dbWatcher.stopWatching()
	}
	// Start the Slack Socket Mode listener when configured. The listener
	// is responsible for receiving reaction_added + message events and
	// routing them into flow tasks via monitor.Dispatcher. Start() is a
	// no-op when env config is incomplete; Stop() is safe to call
	// unconditionally on shutdown.
	if s.slackListener != nil {
		if err := s.slackListener.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: slack listener start: %v\n", err)
		}
		defer s.slackListener.Stop()
	}
	// Start the GitHub polling listener when explicitly enabled. Like
	// Slack, Start() is a no-op when env config is incomplete.
	if s.githubListener != nil {
		if err := s.githubListener.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: github listener start: %v\n", err)
		}
		defer s.githubListener.Stop()
	}
	// One-shot async backfill of tasks.session_path for pre-existing
	// Codex sessions captured before the column was added. Skipped if
	// the DB is unset (tests, healthchecks). Errors are swallowed
	// inside; we never want a slow ~/.codex to block startup.
	go s.backfillSessionPaths()
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: serve: %v\n", err)
		return 1
	case <-sigCh:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "error: shutdown: %v\n", err)
			return 1
		}
		return 0
	}
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
	if path == "." || path == "" {
		path = "index.html"
	}
	data, err := staticFS.ReadFile("static/" + path)
	if err != nil {
		data, err = staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "static assets unavailable", http.StatusInternalServerError)
			return
		}
		path = "index.html"
	}
	if ctype := mime.TypeByExtension(filepath.Ext(path)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func embeddedStaticExists(name string) bool {
	_, err := fs.Stat(staticFS, "static/"+name)
	return err == nil
}
