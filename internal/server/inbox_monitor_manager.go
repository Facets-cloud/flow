package server

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"

	"flow/internal/monitor"
)

type inboxMonitorManager struct {
	target monitor.WakeTarget

	mu     sync.Mutex
	cancel map[string]*inboxMonitorRun
}

type inboxMonitorRun struct {
	cancel context.CancelFunc
}

func newInboxMonitorManager(target monitor.WakeTarget) *inboxMonitorManager {
	return &inboxMonitorManager{
		target: target,
		cancel: make(map[string]*inboxMonitorRun),
	}
}

func (m *inboxMonitorManager) start(slug string) {
	m.mu.Lock()
	if _, ok := m.cancel[slug]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &inboxMonitorRun{cancel: cancel}
	m.cancel[slug] = run
	m.mu.Unlock()

	// Watch for events arriving from now on, not the whole historical backlog —
	// otherwise restoring monitors on boot would respawn the agent for every
	// old inbox event. No-op when a cursor already exists.
	if err := monitor.SeedInboxMonitorCursorToEnd(slug); err != nil {
		log.Printf("flow inbox monitor %s: seed cursor: %v", slug, err)
	}

	go func() {
		err := monitor.NewInboxMonitor(slug, m.target, monitor.InboxMonitorOptions{}).Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrNotExist) {
			log.Printf("flow inbox monitor %s: %v", slug, err)
		}
		m.mu.Lock()
		if m.cancel[slug] == run {
			delete(m.cancel, slug)
		}
		m.mu.Unlock()
	}()
}

func (m *inboxMonitorManager) stop(slug string) {
	m.mu.Lock()
	run := m.cancel[slug]
	delete(m.cancel, slug)
	m.mu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// running reports whether a monitor goroutine is currently active for slug.
func (m *inboxMonitorManager) running(slug string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.cancel[slug]
	return ok
}

// runningSlugs returns the slugs with an active monitor (snapshot copy).
func (m *inboxMonitorManager) runningSlugs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.cancel))
	for slug := range m.cancel {
		out = append(out, slug)
	}
	return out
}
