package monitor

import (
	"strings"
	"testing"

	"github.com/slack-go/slack/slackevents"
)

func TestParseEventsAPIEvent_DMMessage(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		TeamID: "T123",
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.Message),
			Data: &slackevents.MessageEvent{
				Type:        "message",
				Channel:     "D123",
				ChannelType: slackevents.ChannelTypeIM,
				User:        "U234",
				Text:        "can you review this?",
				TimeStamp:   "1710000000.000001",
			},
		},
	}
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	got := out[0]
	if got.Kind != "message" {
		t.Errorf("kind = %q, want message", got.Kind)
	}
	if got.ChannelType != "im" {
		t.Errorf("channel_type = %q, want im", got.ChannelType)
	}
	if got.TS != "1710000000.000001" || got.ThreadTS != "1710000000.000001" {
		t.Errorf("ts/thread_ts = %q / %q (thread should default to ts)", got.TS, got.ThreadTS)
	}
	if got.UserID != "U234" || got.Text != "can you review this?" {
		t.Errorf("user/text = %q / %q", got.UserID, got.Text)
	}
}

func TestParseEventsAPIEvent_MpIMMessage(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		TeamID: "T123",
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.Message),
			Data: &slackevents.MessageEvent{
				Type:        "message",
				Channel:     "G123",
				ChannelType: slackevents.ChannelTypeMPIM,
				User:        "U234",
				Text:        "team huddle?",
				TimeStamp:   "1710000004.000001",
			},
		},
	}
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 || out[0].ChannelType != "mpim" {
		t.Fatalf("channel_type = %q, want mpim (full = %+v)", out[0].ChannelType, out[0])
	}
}

func TestParseEventsAPIEvent_ChannelMessageWithThread(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		TeamID: "T123",
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.Message),
			Data: &slackevents.MessageEvent{
				Type:            "message",
				Channel:         "C123",
				ChannelType:     slackevents.ChannelTypeChannel,
				User:            "U234",
				Text:            "reply inside a thread",
				TimeStamp:       "1710000005.000001",
				ThreadTimeStamp: "1710000000.000001",
			},
		},
	}
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].ThreadTS != "1710000000.000001" {
		t.Errorf("thread_ts = %q, want parent ts", out[0].ThreadTS)
	}
	if out[0].ChannelType != "channel" {
		t.Errorf("channel_type = %q, want channel", out[0].ChannelType)
	}
}

func TestParseEventsAPIEvent_MessageEditAndDeleteIgnored(t *testing.T) {
	for _, sub := range []string{"message_changed", "message_deleted"} {
		envelope := slackevents.EventsAPIEvent{
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Type: string(slackevents.Message),
				Data: &slackevents.MessageEvent{
					Type:      "message",
					SubType:   sub,
					Channel:   "C123",
					TimeStamp: "1710000099.000001",
				},
			},
		}
		out := ParseEventsAPIEvent(envelope, nil)
		if len(out) != 0 {
			t.Errorf("subtype %q should be ignored, got %+v", sub, out)
		}
	}
}

func TestParseEventsAPIEvent_AppMention(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		TeamID: "T123",
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.AppMention),
			Data: &slackevents.AppMentionEvent{
				Type:      string(slackevents.AppMention),
				Channel:   "C123",
				User:      "U234",
				Text:      "<@U999> heads up",
				TimeStamp: "1710000001.000001",
			},
		},
	}
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 || out[0].Kind != "app_mention" {
		t.Fatalf("kind = %q (full %+v)", out[0].Kind, out[0])
	}
	if out[0].ThreadTS != out[0].TS {
		t.Errorf("thread_ts should default to ts when no parent")
	}
}

func TestParseEventsAPIEvent_ReactionAdded(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		TeamID: "T123",
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.ReactionAdded),
			Data: &slackevents.ReactionAddedEvent{
				Type:           string(slackevents.ReactionAdded),
				User:           "U234", // reactor
				Reaction:       "flow-claude",
				ItemUser:       "U555", // author of the reacted message
				EventTimestamp: "1710000010.000001",
				Item: slackevents.Item{
					Type:      "message",
					Channel:   "C123",
					Timestamp: "1710000005.000001", // the message the reaction targets
				},
			},
		},
	}
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	got := out[0]
	if got.Kind != "reaction_added" {
		t.Errorf("kind = %q", got.Kind)
	}
	if got.UserID != "U234" {
		t.Errorf("user_id should be the reactor; got %q", got.UserID)
	}
	if got.Reaction != "flow-claude" {
		t.Errorf("reaction = %q", got.Reaction)
	}
	if got.ItemChannel != "C123" || got.ItemTS != "1710000005.000001" || got.ItemAuthor != "U555" {
		t.Errorf("item refs wrong: channel=%q ts=%q author=%q", got.ItemChannel, got.ItemTS, got.ItemAuthor)
	}
	if got.Channel != "C123" {
		t.Errorf("channel should mirror item.channel; got %q", got.Channel)
	}
	if got.ThreadTS != "1710000005.000001" {
		t.Errorf("thread_ts should default to item.ts for partition-by-thread; got %q", got.ThreadTS)
	}
	if !strings.Contains(got.RawJSON, "flow-claude") {
		t.Errorf("RawJSON should include the reaction; got %q", got.RawJSON)
	}
}

func TestParseEventsAPIEvent_ReactionAddedColonStripped(t *testing.T) {
	// Slack typically sends "flow-claude" already, but defensively strip
	// colons so callers don't get tripped up on ":flow-claude:" forms.
	envelope := slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.ReactionAdded),
			Data: &slackevents.ReactionAddedEvent{
				Reaction:       ":eyes:",
				User:           "U1",
				EventTimestamp: "1.1",
				Item: slackevents.Item{
					Type:      "message",
					Channel:   "C1",
					Timestamp: "0.1",
				},
			},
		},
	}
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 || out[0].Reaction != "eyes" {
		t.Fatalf("reaction = %q, want eyes (full = %+v)", out[0].Reaction, out[0])
	}
}

func TestParseEventsAPIEvent_UnknownEventIgnored(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: "channel_left",
			Data: nil,
		},
	}
	if got := ParseEventsAPIEvent(envelope, nil); got != nil {
		t.Fatalf("unknown event should return nil, got %+v", got)
	}
}

func TestParseEventsAPIEvent_EmptyMessageRejected(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.Message),
			Data: &slackevents.MessageEvent{
				Type:      "message",
				Channel:   "", // missing
				TimeStamp: "1710000000.000001",
			},
		},
	}
	if got := ParseEventsAPIEvent(envelope, nil); got != nil {
		t.Fatalf("missing channel should reject, got %+v", got)
	}
}

func TestParseEventsAPIEvent_BotMessagePreserved(t *testing.T) {
	envelope := slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Type: string(slackevents.Message),
			Data: &slackevents.MessageEvent{
				Type:        "message",
				SubType:     "bot_message",
				Channel:     "C123",
				ChannelType: slackevents.ChannelTypeChannel,
				Text:        "from a bot",
				TimeStamp:   "1710000020.000001",
			},
		},
	}
	out := ParseEventsAPIEvent(envelope, nil)
	if len(out) != 1 {
		t.Fatalf("bot_message should be kept; got %+v", out)
	}
}
