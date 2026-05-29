package server

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"flow/internal/flowdb"
	"flow/internal/monitor"
)

const (
	// monitorReconcileInterval is how often the persistent-monitor reconciler
	// re-derives which tasks need a background monitor and converges the
	// running set. It also recreates any monitor goroutine that has died.
	monitorReconcileInterval = 30 * time.Second
	// respawnDebounceWindow bounds how often a single task may respawn its
	// agent, so a burst of inbox events can't trigger a respawn storm.
	respawnDebounceWindow = 60 * time.Second
)

// taskNeedsMonitor reports whether a task should carry a persistent background
// monitor: it has an external event source — a Slack thread (slack-reply /
// slack-thread:) or a GitHub PR/issue (gh-pr: / gh-issue:) — AND is still
// active (backlog or in-progress). Finished, archived, or deleted tasks never
// need one.
//
// A bare git worktree/branch is deliberately NOT a trigger: a branch with no
// PR has nothing external feeding its inbox. Once a PR is raised it is tagged
// gh-pr: (tracked by the GitHub poller), which is what flips this on — "we
// updated THE PR", not merely "we made a branch". Pure function — no I/O.
func taskNeedsMonitor(t *flowdb.Task, tags []string) bool {
	if t == nil {
		return false
	}
	if t.ArchivedAt.Valid || t.DeletedAt.Valid {
		return false
	}
	if t.Status != "backlog" && t.Status != "in-progress" {
		return false
	}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "slack-reply" ||
			strings.HasPrefix(tag, "slack-thread:") ||
			strings.HasPrefix(tag, "gh-pr:") ||
			strings.HasPrefix(tag, "gh-issue:") {
			return true
		}
	}
	return false
}

// respawnGate debounces agent respawns per task so a burst of inbox events
// (or a hot retry loop) can't spawn a pile of sessions.
type respawnGate struct {
	mu     sync.Mutex
	last   map[string]time.Time
	window time.Duration
}

func newRespawnGate(window time.Duration) *respawnGate {
	return &respawnGate{last: map[string]time.Time{}, window: window}
}

// allow reports whether a respawn is permitted for slug now, recording the
// attempt when it is. Recording on attempt (not success) means a failed
// respawn still debounces.
func (g *respawnGate) allow(slug string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if last, ok := g.last[slug]; ok && now.Sub(last) < g.window {
		return false
	}
	g.last[slug] = now
	return true
}

// monitorReconciler keeps the set of running inbox monitors converged with the
// set of tasks that need one. It runs on boot (restoring monitors after a
// restart) and on a timer (recreating any monitor goroutine that died, and
// tearing down monitors for tasks that have finished). It is the convergent
// source of truth; the attach-time start() stays for instant responsiveness.
type monitorReconciler struct {
	srv      *Server
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newMonitorReconciler(srv *Server) *monitorReconciler {
	return &monitorReconciler{srv: srv, interval: monitorReconcileInterval}
}

func (r *monitorReconciler) start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	go r.loop(ctx)
}

func (r *monitorReconciler) stop() {
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.done = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (r *monitorReconciler) loop(ctx context.Context) {
	defer close(r.done)
	r.tick() // immediate tick restores monitors right after boot
	tick := time.NewTicker(r.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.tick()
		}
	}
}

func (r *monitorReconciler) tick() {
	s := r.srv
	if s == nil || s.cfg.DB == nil || s.inboxMonitors == nil {
		return
	}
	tasks, err := flowdb.ListTasks(s.cfg.DB, flowdb.TaskFilter{IncludeArchived: false})
	if err != nil {
		return
	}
	// Active = non-archived tasks (includes "done"); desired = needs a monitor.
	active := make(map[string]bool, len(tasks))
	desired := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		active[t.Slug] = t.Status != "done"
		tags, terr := flowdb.GetTaskTags(s.cfg.DB, t.Slug)
		if terr != nil {
			continue // skip this task this tick; retry next round
		}
		if taskNeedsMonitor(t, tags) {
			desired[t.Slug] = true
		}
	}
	// Start monitors for qualifying tasks that aren't running one.
	for slug := range desired {
		if !s.inboxMonitors.running(slug) {
			s.inboxMonitors.start(slug)
		}
	}
	// Stop monitors whose task has finished (done) or is gone (archived/deleted
	// → absent from the active set). Leave monitors for still-active,
	// non-qualifying tasks alone — those were attach-started and stop with the
	// session.
	for _, slug := range s.inboxMonitors.runningSlugs() {
		if alive, ok := active[slug]; !ok || !alive {
			if !desired[slug] {
				s.inboxMonitors.stop(slug)
			}
		}
	}
}

// deliverInboxEvents routes new actionable inbox events to a task's agent:
//   - flow-managed PTY live  → inject the wake prompt (existing behavior)
//   - native session live    → leave events in the inbox (no PTY, no duplicate)
//   - agent dead             → respawn it (debounced, active-only)
//
// Returning nil lets the monitor advance its cursor. An error is reserved for
// transient inject failures on the live path so it retries; the respawn/native
// paths return nil to avoid hot loops (events remain in inbox.jsonl regardless).
func (s *Server) deliverInboxEvents(slug string, entries []monitor.InboxEntry) error {
	if s.terminals != nil && s.terminals.running(slug) {
		return s.terminals.wakeTask(slug, formatInboxWakePrompt(slug, entries))
	}
	task, err := flowdb.GetTask(s.cfg.DB, slug)
	if err != nil {
		return nil // task gone — nothing to deliver to
	}
	if s.taskAgentProcessLive(task) {
		// A native (user-owned terminal) session is alive: flow has no PTY to
		// inject into and must not spawn a duplicate. The events stay in the
		// inbox UI for the user.
		return nil
	}
	if task.Status != "backlog" && task.Status != "in-progress" {
		return nil // finished — don't recreate
	}
	provider := task.SessionProvider
	if provider == "" {
		provider = "claude"
	}
	if err := s.ensureProviderAvailable(provider); err != nil {
		log.Printf("flow monitor: cannot respawn %s: %v", slug, err)
		return nil
	}
	if !s.respawn.allow(slug) {
		return nil // debounced — a recent respawn is handling the backlog
	}
	log.Printf("flow monitor: respawning %s (%s) for %d new inbox event(s)", slug, provider, len(entries))
	resp, _ := s.openBrowserTerminalBridge(slug, "")
	if !resp.OK {
		log.Printf("flow monitor: respawn %s failed: %s", slug, resp.Message)
	}
	return nil
}

// taskAgentProcessLive reports whether the task's stored session is alive in
// the OS process table (provider-agnostic, via cachedLiveAgentSessions).
func (s *Server) taskAgentProcessLive(t *flowdb.Task) bool {
	if t == nil || !t.SessionID.Valid || strings.TrimSpace(t.SessionID.String) == "" {
		return false
	}
	live, err := s.cachedLiveAgentSessions()
	if err != nil {
		return false
	}
	return live[strings.ToLower(strings.TrimSpace(t.SessionID.String))]
}
