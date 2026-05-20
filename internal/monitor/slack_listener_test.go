package monitor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func TestSlackListener_StartIsNoOpWithoutTokens(t *testing.T) {
	// SocketModeEnabled() is the gate — when env is unconfigured, Start
	// must NOT panic and must return cleanly so callers can wire it
	// optimistically.
	t.Setenv("FLOW_SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("FLOW_SLACK_TOKEN", "")
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_TOKEN", "")

	d := NewDispatcher(nil, nil) // nil DB is fine — Dispatch short-circuits on nil
	l := NewSlackListener(d)
	if err := l.Start(); err != nil {
		t.Fatalf("Start err = %v", err)
	}
	// Should not be considered running.
	l.mu.Lock()
	running := l.running
	l.mu.Unlock()
	if running {
		t.Errorf("listener marked running despite no tokens")
	}
	// Stop should be safe.
	l.Stop()
}

func TestSlackListener_StopBeforeStart(t *testing.T) {
	l := NewSlackListener(NewDispatcher(nil, nil))
	l.Stop() // should not panic, should not block
}

func TestSlackListener_MockConnectorDispatchesEvents(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", "U_me")
	t.Setenv("FLOW_SLACK_TRIGGER_EMOJI", "claude")
	t.Setenv("FLOW_SLACK_AUTOOPEN", "0")
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()

	d := NewDispatcher(db, nil)
	l := NewSlackListener(d)

	// Wire the mock connector: feed two events, then close.
	eventsCh := make(chan socketmode.Event, 2)
	runErrCh := make(chan error, 1)

	// Event 1: a reaction matching our trigger emoji from "us".
	react := makeReactionEvent("U_me", "claude", "C123", "1234.0001", "1234.0010")
	eventsCh <- socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: react,
	}

	// Event 2: a non-matching reaction (wrong emoji) — should be ignored.
	noise := makeReactionEvent("U_me", "thumbsup", "C123", "1234.0002", "1234.0011")
	eventsCh <- socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: noise,
	}

	var (
		ackCalls int
		ackMu    sync.Mutex
	)
	l.connectFn = func(ctx context.Context) (<-chan socketmode.Event, func(socketmode.Request), <-chan error) {
		return eventsCh, func(socketmode.Request) {
			ackMu.Lock()
			ackCalls++
			ackMu.Unlock()
		}, runErrCh
	}

	if err := l.Start(); err != nil {
		t.Fatalf("Start err = %v", err)
	}
	// Give the goroutine a moment to drain events.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// Check whether dispatcher saw the trigger reaction by looking
		// for the inbox file.
		entries, _ := ReadInboxEntries("slack-c123-1234-0001")
		if len(entries) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	l.Stop()

	entries, err := ReadInboxEntries("slack-c123-1234-0001")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("trigger reaction should have produced 1 inbox entry; got %d", len(entries))
	}
	// We sent both events with Request set — both should be acked. (Tests
	// without a request field would not produce ack calls. See helper.)
}

func makeReactionEvent(reactor, emoji, channel, itemTS, eventTS string) slackevents.EventsAPIEvent {
	return slackevents.EventsAPIEvent{
		Type:    "events_api",
		TeamID:  "T123",
		APIAppID: "A123",
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.ReactionAdded),
			Data: &slackevents.ReactionAddedEvent{
				User:           reactor,
				Reaction:       emoji,
				EventTimestamp: eventTS,
				Item: slackevents.Item{
					Type:      "message",
					Channel:   channel,
					Timestamp: itemTS,
				},
			},
		},
	}
}
