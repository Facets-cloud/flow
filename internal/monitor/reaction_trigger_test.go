package monitor

import (
	"testing"

	"github.com/slack-go/slack/slackevents"
)

func TestTriggerEmoji_Default(t *testing.T) {
	t.Setenv("FLOW_SLACK_TRIGGER_EMOJI", "")
	if got := TriggerEmoji(); got != DefaultTriggerEmoji {
		t.Errorf("default = %q, want %q", got, DefaultTriggerEmoji)
	}
}

func TestTriggerEmoji_StripsColons(t *testing.T) {
	t.Setenv("FLOW_SLACK_TRIGGER_EMOJI", ":robot_face:")
	if got := TriggerEmoji(); got != "robot_face" {
		t.Errorf("colon-wrapped env = %q, want robot_face", got)
	}
}

func TestSelfUserIDs_MultiValue(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", " U123, U456,U789 ,,, ")
	t.Setenv("FLOW_SLACK_SELF_USER_ID", "")
	t.Setenv("FLOW_SLACK_USER_ID", "")
	t.Setenv("SLACK_USER_ID", "")
	got := SelfUserIDs()
	if len(got) != 3 || got[0] != "U123" || got[1] != "U456" || got[2] != "U789" {
		t.Fatalf("self user ids = %v, want [U123 U456 U789]", got)
	}
}

func TestSelfUserIDs_FallbackOrder(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", "")
	t.Setenv("FLOW_SLACK_SELF_USER_ID", "")
	t.Setenv("FLOW_SLACK_USER_ID", "U_fallback")
	t.Setenv("SLACK_USER_ID", "U_should_not_win")
	got := SelfUserIDs()
	if len(got) != 1 || got[0] != "U_fallback" {
		t.Fatalf("got %v, want [U_fallback] — earlier env should beat later", got)
	}
}

func TestThreadKey_Empty(t *testing.T) {
	if ThreadKey("", "1.1") != "" || ThreadKey("C1", "") != "" || ThreadKey("  ", " ") != "" {
		t.Errorf("ThreadKey should empty for blank inputs")
	}
}

func TestDecideReaction_HappyPath(t *testing.T) {
	ev := mustParseReaction(t, "U_me", "flow-claude", "C123", "1234.0005", "1234.0001")
	got := DecideReaction(ev, "flow-claude", []string{"U_me"})
	if !got.Trigger {
		t.Fatalf("expected Trigger=true, got %+v", got)
	}
	if got.ThreadKey != "C123:1234.0001" {
		t.Errorf("thread_key = %q", got.ThreadKey)
	}
	if got.Reactor != "U_me" || got.Reaction != "flow-claude" {
		t.Errorf("reactor/reaction = %q / %q", got.Reactor, got.Reaction)
	}
}

func TestDecideReaction_NonSelfUserDropped(t *testing.T) {
	// A colleague reacted with our trigger emoji on a message. We must NOT
	// fire — only consent from "us" counts.
	ev := mustParseReaction(t, "U_coworker", "flow-claude", "C123", "1.5", "1.1")
	got := DecideReaction(ev, "flow-claude", []string{"U_me"})
	if got.Trigger {
		t.Fatalf("coworker reaction should not trigger; got %+v", got)
	}
}

func TestDecideReaction_DifferentEmojiDropped(t *testing.T) {
	ev := mustParseReaction(t, "U_me", "thumbsup", "C123", "1.5", "1.1")
	got := DecideReaction(ev, "flow-claude", []string{"U_me"})
	if got.Trigger {
		t.Fatalf("non-trigger emoji should not fire; got %+v", got)
	}
}

func TestDecideReaction_EmojiCaseInsensitive(t *testing.T) {
	ev := mustParseReaction(t, "U_me", "Flow-Claude", "C123", "1.5", "1.1")
	got := DecideReaction(ev, "flow-claude", []string{"U_me"})
	if !got.Trigger {
		t.Fatalf("emoji match should be case-insensitive")
	}
}

func TestDecideReaction_NonReactionEventDropped(t *testing.T) {
	msg := InboundEvent{Kind: "message", Channel: "C1", ThreadTS: "1.1", UserID: "U_me"}
	got := DecideReaction(msg, "flow-claude", []string{"U_me"})
	if got.Trigger {
		t.Fatalf("message event must not trigger reaction decision; got %+v", got)
	}
}

func TestDecideReaction_NoSelfUserIDsRefuses(t *testing.T) {
	// Safety: when SelfUserIDs() returns empty (operator forgot to configure),
	// the handler must refuse to fire on any reaction, not fire on all.
	ev := mustParseReaction(t, "U_me", "flow-claude", "C123", "1.5", "1.1")
	got := DecideReaction(ev, "flow-claude", nil)
	if got.Trigger {
		t.Fatalf("empty self user IDs must refuse to trigger; got %+v", got)
	}
}

// mustParseReaction builds an InboundEvent from a synthetic reaction event
// by routing through the parser, so the test exercises the production code
// path rather than constructing an InboundEvent literal that could drift.
func mustParseReaction(t *testing.T, reactor, emoji, channel, eventTS, itemTS string) InboundEvent {
	t.Helper()
	envelope := slackevents.EventsAPIEvent{
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
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 parsed event, got %d (envelope = %+v)", len(out), envelope)
	}
	return out[0]
}
