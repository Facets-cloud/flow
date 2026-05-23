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
