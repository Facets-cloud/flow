package monitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// SlackListener owns the Slack Socket Mode WebSocket connection. When
// running, it receives events from Slack, parses them via the package
// event parser, and forwards them to a Dispatcher for routing into
// flow tasks.
//
// Lifecycle:
//
//	l := monitor.NewSlackListener(dispatcher)
//	if err := l.Start(); err != nil { ... }   // returns immediately, runs in background
//	defer l.Stop()                            // graceful shutdown
//
// Start is idempotent — calling it again after a successful start is a
// no-op. Stop is safe to call before Start (no-op) or twice.
//
// Configuration is env-driven (FLOW_SLACK_APP_TOKEN, FLOW_SLACK_USER_TOKEN
// / SLACK_BOT_TOKEN, etc.) so that production wiring just calls
// NewSlackListener + Start without juggling secrets.
type SlackListener struct {
	dispatcher *Dispatcher

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}

	// Hooks for tests. Production paths use the real slack-go client.
	connectFn func(ctx context.Context) (eventsCh <-chan socketmode.Event, ack func(req socketmode.Request), runErr <-chan error)
	logFn     func(string, ...any)
	changeFn  func(kind string)
}

// NewSlackListener constructs a listener bound to the given dispatcher.
// Returns nil when no dispatcher is provided — the listener exists only
// to route events into it.
func NewSlackListener(d *Dispatcher) *SlackListener {
	if d == nil {
		return nil
	}
	return &SlackListener{
		dispatcher: d,
		logFn: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[slack listener] "+format+"\n", args...)
		},
	}
}

// SetChangeNotifier registers a callback for listener-owned database writes
// that need a UI refresh. The server wires this to publish a ui_change event.
func (l *SlackListener) SetChangeNotifier(fn func(kind string)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.changeFn = fn
}

func (l *SlackListener) notifyChange(kind string) {
	l.mu.Lock()
	fn := l.changeFn
	l.mu.Unlock()
	if fn != nil {
		fn(kind)
	}
}

// SocketModeEnabled returns true when env config indicates we should
// attempt to start a Socket Mode connection. We require both an app
// token (xapp-) for the WebSocket and a bot/user token (xoxb-/xoxp-)
// for any Web API calls — neither side works alone.
//
// Explicit FLOW_SLACK_SOCKET_MODE=0 disables even when tokens are
// present, to support "leave the wiring in place but don't connect."
func SocketModeEnabled() bool {
	if !envBoolDefault("FLOW_SLACK_SOCKET_MODE", true) {
		return false
	}
	return SlackAppToken() != "" && SlackBotToken() != ""
}

// SlackAppToken returns the xapp- app-level token Slack requires for
// Socket Mode. Single env precedence — there's no fallback because
// xapp- tokens aren't transferable across apps.
func SlackAppToken() string {
	return firstNonEmpty(
		os.Getenv("FLOW_SLACK_APP_TOKEN"),
		os.Getenv("SLACK_APP_TOKEN"),
	)
}

// Start begins receiving Slack events in a background goroutine. Returns
// nil if already running, or if SocketModeEnabled() is false (caller can
// proceed without a connection). Any connection-setup error surfaces here;
// runtime errors (network blips during operation) are logged but not
// propagated — the underlying socketmode client handles reconnects.
func (l *SlackListener) Start() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		return nil
	}
	go func() {
		updated, err := l.dispatcher.BackfillSlackTaskTitles(context.Background())
		if err != nil {
			l.logFn("backfill slack task titles: %v", err)
			return
		}
		if updated > 0 {
			l.logFn("backfilled %d slack task title(s)", updated)
			l.notifyChange("slack-title-backfill")
		}
	}()
	if !SocketModeEnabled() {
		l.logFn("not starting: SocketModeEnabled() is false (set FLOW_SLACK_APP_TOKEN + SLACK_BOT_TOKEN and FLOW_SLACK_SOCKET_MODE=1)")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.done = make(chan struct{})
	l.running = true

	go func() {
		defer close(l.done)
		l.run(ctx)
	}()
	return nil
}

// Stop signals the listener to shut down and waits up to 5 seconds for
// the goroutine to exit. Safe to call when not started; safe to call
// multiple times.
func (l *SlackListener) Stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.running {
		l.mu.Unlock()
		return
	}
	cancel := l.cancel
	done := l.done
	l.running = false
	l.cancel = nil
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			l.logFn("stop: timeout waiting for listener goroutine to exit")
		}
	}
}

func (l *SlackListener) run(ctx context.Context) {
	// connectFn is tests-only; production goes through the real client.
	if l.connectFn != nil {
		l.runWith(ctx, l.connectFn)
		return
	}
	l.runReal(ctx)
}

func (l *SlackListener) runReal(ctx context.Context) {
	api := slack.New(SlackBotToken(), slack.OptionAppLevelToken(SlackAppToken()))
	client := socketmode.New(api)

	// Drive the socketmode client until ctx cancels. socketmode.Client.RunContext
	// handles reconnects internally; this goroutine survives transient network
	// failures without our intervention.
	runErr := make(chan error, 1)
	go func() { runErr <- client.RunContext(ctx) }()

	l.logFn("started (Socket Mode connecting)")
	for {
		select {
		case <-ctx.Done():
			l.logFn("stopping")
			// Wait for client to exit, but don't block forever.
			select {
			case err := <-runErr:
				if err != nil && !errors.Is(err, context.Canceled) {
					l.logFn("client.RunContext exited: %v", err)
				}
			case <-time.After(2 * time.Second):
			}
			return

		case evt, ok := <-client.Events:
			if !ok {
				l.logFn("events channel closed")
				return
			}
			l.handleSocketEvent(ctx, client, evt)

		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				l.logFn("client.RunContext exited unexpectedly: %v", err)
			}
			return
		}
	}
}

func (l *SlackListener) handleSocketEvent(ctx context.Context, client *socketmode.Client, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		l.logFn("connecting...")
	case socketmode.EventTypeConnected:
		l.logFn("connected")
	case socketmode.EventTypeDisconnect:
		// slack-go's payload-on-disconnect carries the close reason as a
		// string in evt.Data when the server told us why; otherwise it
		// reflects the local close cause (timeout, write error, etc.).
		l.logFn("disconnected (reason: %v)", evt.Data)
	case socketmode.EventTypeConnectionError:
		// Auth-level failures land here: missing or invalid xapp- token,
		// app not installed to the workspace, scope missing, admin
		// approval pending. Slack closes the socket right after the
		// handshake and slack-go reports the underlying error in evt.Data.
		l.logFn("connection error: %v  (most common causes: app not installed to workspace, missing connections:write scope, xapp-/xoxp- from different apps, admin approval pending)", evt.Data)
	case socketmode.EventTypeIncomingError:
		l.logFn("incoming error: %v", evt.Data)
	case socketmode.EventTypeErrorBadMessage:
		l.logFn("bad message error: %v", evt.Data)
	case socketmode.EventTypeErrorWriteFailed:
		l.logFn("write failed: %v", evt.Data)
	case socketmode.EventTypeHello:
		l.logFn("hello received")
	case socketmode.EventTypeEventsAPI:
		payload, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			l.logFn("events_api: unexpected payload type %T", evt.Data)
			if evt.Request != nil {
				client.Ack(*evt.Request)
			}
			return
		}
		l.logFn("events_api: type=%s inner=%T", payload.Type, payload.InnerEvent.Data)
		l.handleEventsAPI(ctx, payload)
		// Ack AFTER dispatch — if we crash before this line, Slack will
		// redeliver the event within its retry window.
		if evt.Request != nil {
			client.Ack(*evt.Request)
		}
	default:
		// Surface unhandled types so we can diagnose unexpected behavior;
		// the listener should be loud about anything it isn't routing.
		l.logFn("unhandled event type %q (data type %T)", evt.Type, evt.Data)
	}
}

func (l *SlackListener) handleEventsAPI(ctx context.Context, event slackevents.EventsAPIEvent) {
	mentionUsers := SlackMentionUserIDs()
	events := ParseEventsAPIEvent(event, mentionUsers)
	if len(events) == 0 {
		// Parser dropped the event. Surface what inner type we got so we can
		// see whether a real event is being silently rejected (vs. genuinely
		// not-of-interest like channel_join).
		l.logFn("parser produced no InboundEvent for inner type %T (subtype check or missing fields?)", event.InnerEvent.Data)
		return
	}
	for _, ev := range events {
		// One concise summary per parsed event — channel/ts/thread_ts/reactor/reaction —
		// so we can correlate Slack-side state with what our pipeline saw.
		l.logFn("inbound kind=%s channel=%s ts=%s thread_ts=%s user=%s reaction=%s item_ts=%s",
			ev.Kind, ev.Channel, ev.TS, ev.ThreadTS, ev.UserID, ev.Reaction, ev.ItemTS)
		if err := l.dispatcher.Dispatch(ctx, ev); err != nil {
			l.logFn("dispatch %s: %v", ev.Kind, err)
		}
	}
}

// SlackMentionUserIDs returns the slack user IDs flow treats as "you" for
// personal-mention detection inside message text. Currently the same set
// as SelfUserIDs (reaction consenter == mention target). Kept as a
// separate function for future flexibility — there may be cases where
// the user wants to consent (react) from one ID but be mentioned at
// another (e.g., bot vs user identity in the same workspace).
func SlackMentionUserIDs() []string {
	return SelfUserIDs()
}

// runWith is the test seam — drives the listener loop from a mock
// connection. The mock returns an events channel and an ack function;
// the loop behaves identically to runReal otherwise. Used by tests to
// inject synthetic Slack events without spinning up a fake Socket Mode
// server.
func (l *SlackListener) runWith(ctx context.Context, connect func(ctx context.Context) (<-chan socketmode.Event, func(socketmode.Request), <-chan error)) {
	events, ack, runErr := connect(ctx)
	l.logFn("started (mock connector)")
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			l.handleSocketEventMock(ctx, ack, evt)
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				l.logFn("mock connector exited: %v", err)
			}
			return
		}
	}
}

func (l *SlackListener) handleSocketEventMock(ctx context.Context, ack func(socketmode.Request), evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		payload, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			if evt.Request != nil {
				ack(*evt.Request)
			}
			return
		}
		l.handleEventsAPI(ctx, payload)
		if evt.Request != nil {
			ack(*evt.Request)
		}
	}
}
