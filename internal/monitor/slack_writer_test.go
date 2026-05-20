package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureRequest is a small recorder that turns an httptest server into a
// transcript of (path, body) tuples the test can assert against without
// fishing through goroutine-shared state. Tests that need multiple distinct
// responses route on path inside the handler closure.
type captureRequest struct {
	Path string
	Body string
}

func enabledWriter(t *testing.T, handler http.HandlerFunc) (*SlackWriter, *[]captureRequest, func()) {
	t.Helper()
	var captured []captureRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = append(captured, captureRequest{Path: r.URL.Path, Body: string(body)})
		handler(w, r)
	}))
	writer := &SlackWriter{
		Token:   "xoxp-test",
		BaseURL: srv.URL,
		Enabled: true,
	}
	return writer, &captured, srv.Close
}

func TestSlackWriterDisabledIsNoOp(t *testing.T) {
	// A disabled writer must NOT make an HTTP call regardless of args. We
	// point BaseURL at a server that panics if hit, so any leak is loud.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected HTTP call on disabled writer: %s", r.URL.Path)
	}))
	defer srv.Close()
	w := &SlackWriter{Token: "xoxp-test", BaseURL: srv.URL, Enabled: false}
	if err := w.PostMessage(context.Background(), "C1", "1.1", "hello"); err != nil {
		t.Errorf("disabled PostMessage err = %v, want nil", err)
	}
	if err := w.AddReaction(context.Background(), "C1", "1.1", "eyes"); err != nil {
		t.Errorf("disabled AddReaction err = %v, want nil", err)
	}
	if err := w.PostEphemeral(context.Background(), "C1", "U1", "1.1", "private"); err != nil {
		t.Errorf("disabled PostEphemeral err = %v, want nil", err)
	}
}

func TestSlackWriterPostMessageToDMSucceeds(t *testing.T) {
	w, captured, cleanup := enabledWriter(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"ok": true})
	})
	defer cleanup()

	if err := w.PostMessage(context.Background(), "D123", "", "ping"); err != nil {
		t.Fatalf("PostMessage err = %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.Path != "/chat.postMessage" {
		t.Errorf("path = %q", got.Path)
	}
	if !strings.Contains(got.Body, `"channel":"D123"`) || !strings.Contains(got.Body, `"text":"ping"`) {
		t.Errorf("body = %s", got.Body)
	}
	// No thread_ts for a top-level DM is fine.
	if strings.Contains(got.Body, `"thread_ts"`) {
		t.Errorf("DM top-level should omit thread_ts; body = %s", got.Body)
	}
}

func TestSlackWriterRefusesBroadcastToChannel(t *testing.T) {
	// Channel ID prefix C → public channel. Without thread_ts the safety
	// guard must short-circuit before any HTTP call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("safety guard failed: HTTP call made to %s", r.URL.Path)
	}))
	defer srv.Close()
	w := &SlackWriter{Token: "xoxp-test", BaseURL: srv.URL, Enabled: true}
	err := w.PostMessage(context.Background(), "C42", "", "broadcast attempt")
	if !errors.Is(err, ErrChannelBroadcast) {
		t.Fatalf("err = %v, want ErrChannelBroadcast", err)
	}
}

func TestSlackWriterPostsToChannelWithThreadTS(t *testing.T) {
	w, captured, cleanup := enabledWriter(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"ok": true})
	})
	defer cleanup()

	if err := w.PostMessage(context.Background(), "C42", "1234.0001", "threaded reply"); err != nil {
		t.Fatalf("PostMessage err = %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	got := (*captured)[0].Body
	if !strings.Contains(got, `"thread_ts":"1234.0001"`) {
		t.Errorf("thread_ts not in body: %s", got)
	}
}

func TestSlackWriterPostEphemeralTargetsUserInThread(t *testing.T) {
	w, captured, cleanup := enabledWriter(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"ok": true})
	})
	defer cleanup()

	if err := w.PostEphemeral(context.Background(), "C42", "U123", "1234.0001", "private notice"); err != nil {
		t.Fatalf("PostEphemeral err = %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.Path != "/chat.postEphemeral" {
		t.Fatalf("path = %q, want /chat.postEphemeral", got.Path)
	}
	for _, want := range []string{`"channel":"C42"`, `"user":"U123"`, `"thread_ts":"1234.0001"`, `"text":"private notice"`} {
		if !strings.Contains(got.Body, want) {
			t.Fatalf("body missing %s: %s", want, got.Body)
		}
	}
}

func TestSlackWriterMissingTokenReturnsErrNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("HTTP call should not be made when token is empty")
	}))
	defer srv.Close()
	w := &SlackWriter{Token: "", BaseURL: srv.URL, Enabled: true}
	err := w.PostMessage(context.Background(), "D1", "", "hi")
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestNewSlackWriterPrefersExplicitWriteToken(t *testing.T) {
	t.Setenv("FLOW_SLACK_WRITES_ENABLED", "1")
	t.Setenv("FLOW_SLACK_WRITE_TOKEN", "xoxp-write")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-bot")
	t.Setenv("FLOW_SLACK_TOKEN", "xoxp-legacy")

	w := NewSlackWriter()
	if w.Token != "xoxp-write" {
		t.Fatalf("writer token = %q, want explicit write token", w.Token)
	}
	if !w.Enabled {
		t.Fatal("writer should be enabled")
	}
}

func TestSlackWriterRateLimitedReturnsRetryError(t *testing.T) {
	w, _, cleanup := enabledWriter(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Retry-After", "30")
		rw.WriteHeader(http.StatusTooManyRequests)
	})
	defer cleanup()
	err := w.PostMessage(context.Background(), "D1", "", "hi")
	if err == nil || !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "30") {
		t.Fatalf("err = %v, want rate-limit message naming Retry-After", err)
	}
}

func TestSlackWriterAddReactionStripsColons(t *testing.T) {
	// Slack's reactions.add rejects ":eyes:" — wants "eyes". The writer
	// normalizes both forms so callers don't have to.
	w, captured, cleanup := enabledWriter(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"ok": true})
	})
	defer cleanup()
	if err := w.AddReaction(context.Background(), "C1", "1.1", ":eyes:"); err != nil {
		t.Fatalf("AddReaction err = %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d", len(*captured))
	}
	body := (*captured)[0].Body
	if !strings.Contains(body, `"name":"eyes"`) {
		t.Errorf("name not normalized: %s", body)
	}
	if !strings.Contains(body, `"timestamp":"1.1"`) {
		t.Errorf("timestamp = %s", body)
	}
}

func TestSlackWriterAddReactionTreatsAlreadyReactedAsSuccess(t *testing.T) {
	// reactions.add returns ok=false + error=already_reacted when the
	// reaction is already there. We don't want every caller to special-case
	// that as a non-error.
	w, _, cleanup := enabledWriter(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"ok": false, "error": "already_reacted"})
	})
	defer cleanup()
	if err := w.AddReaction(context.Background(), "C1", "1.1", "eyes"); err != nil {
		t.Errorf("AddReaction err = %v, want nil for already_reacted", err)
	}
}

func TestSlackWriterPostMessageOkFalseSurfacesError(t *testing.T) {
	// Any other ok=false should surface — caller needs to see invalid_channel,
	// not_in_channel, missing_scope, etc.
	w, _, cleanup := enabledWriter(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"ok": false, "error": "not_in_channel"})
	})
	defer cleanup()
	err := w.PostMessage(context.Background(), "D1", "", "hi")
	if err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("err = %v, want not_in_channel", err)
	}
}
