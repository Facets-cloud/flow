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
)

//go:embed all:static
var staticFS embed.FS

func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	s.terminals = newTerminalHub(s)
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
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/", s.handleTaskRoute)
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
