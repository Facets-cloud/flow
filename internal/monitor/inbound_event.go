package monitor

import (
	"encoding/json"
	"strings"

	"github.com/slack-go/slack/slackevents"
)

// InboundEvent is the parsed, normalized form of a Slack Events API event
// flow's reaction-trigger pipeline acts on. It collapses Slack's many event
// types (message.channels, message.im, message.mpim, app_mention,
// reaction_added) into one shape so downstream handlers don't have to
// switch on Slack-specific structs.
//
// Field semantics differ slightly between kinds:
//
//   - For "message" / "app_mention": Channel / TS / ThreadTS / UserID / Text
//     describe the message itself. ItemTS / ItemAuthor / Reaction are "".
//
//   - For "reaction_added": Channel / TS reflect the reaction event itself
//     (ts = the EventTimestamp on the reaction). The targeted message is
//     in ItemChannel / ItemTS / ItemAuthor. UserID is the reactor. Reaction
//     is the emoji shortname with colons stripped. Text is "".
//
// ThreadTS defaults to TS when the source event has no parent thread, so
// downstream "partition by thread" code can hash on ThreadTS uniformly
// without checking for empty.
type InboundEvent struct {
	Kind        string // "message" | "app_mention" | "reaction_added"
	Channel     string
	ChannelType string // "channel" | "im" | "mpim" | "group" | ""
	TS          string
	ThreadTS    string
	UserID      string
	Text        string
	Reaction    string
	ItemChannel string
	ItemTS      string
	ItemAuthor  string
	TeamID      string
	APIAppID    string
	RawJSON     string
}

// ParseEventsAPIEvent normalizes a Slack EventsAPIEvent into zero or more
// InboundEvents. Returns nil for inner events we don't care about (channel
// joins, file shares, etc.). mentionUserIDs is the list of user IDs flow
// treats as "you" for personal-mention detection inside message text —
// pass SlackMentionUserIDs() in production.
func ParseEventsAPIEvent(event slackevents.EventsAPIEvent, mentionUserIDs []string) []InboundEvent {
	switch ev := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if out, ok := parseAppMention(event, ev); ok {
			return []InboundEvent{out}
		}
	case slackevents.AppMentionEvent:
		if out, ok := parseAppMention(event, &ev); ok {
			return []InboundEvent{out}
		}
	case *slackevents.MessageEvent:
		if out, ok := parseMessage(event, ev, mentionUserIDs); ok {
			return []InboundEvent{out}
		}
	case slackevents.MessageEvent:
		if out, ok := parseMessage(event, &ev, mentionUserIDs); ok {
			return []InboundEvent{out}
		}
	case *slackevents.ReactionAddedEvent:
		if out, ok := parseReactionAdded(event, ev); ok {
			return []InboundEvent{out}
		}
	case slackevents.ReactionAddedEvent:
		if out, ok := parseReactionAdded(event, &ev); ok {
			return []InboundEvent{out}
		}
	}
	return nil
}

func parseAppMention(env slackevents.EventsAPIEvent, ev *slackevents.AppMentionEvent) (InboundEvent, bool) {
	if ev == nil {
		return InboundEvent{}, false
	}
	channel := strings.TrimSpace(ev.Channel)
	ts := firstNonEmpty(ev.TimeStamp, ev.EventTimeStamp)
	text := strings.TrimSpace(ev.Text)
	if channel == "" || ts == "" {
		return InboundEvent{}, false
	}
	threadTS := strings.TrimSpace(ev.ThreadTimeStamp)
	if threadTS == "" {
		threadTS = ts
	}
	return InboundEvent{
		Kind:     "app_mention",
		Channel:  channel,
		TS:       ts,
		ThreadTS: threadTS,
		UserID:   strings.TrimSpace(ev.User),
		Text:     text,
		TeamID:   env.TeamID,
		APIAppID: env.APIAppID,
		RawJSON:  rawJSON(env, ev),
	}, true
}

func parseMessage(env slackevents.EventsAPIEvent, ev *slackevents.MessageEvent, mentionUserIDs []string) (InboundEvent, bool) {
	if ev == nil {
		return InboundEvent{}, false
	}
	// Slack distinguishes edits, deletes, and bot messages via SubType.
	// We accept top-level user messages (SubType=="") and bot messages.
	// Edits and deletes have their own routing; we deliberately ignore
	// them here so a thread doesn't double-count an edit as new traffic.
	if ev.SubType != "" && ev.SubType != "bot_message" {
		return InboundEvent{}, false
	}
	channel := strings.TrimSpace(ev.Channel)
	ts := firstNonEmpty(ev.TimeStamp, ev.EventTimeStamp)
	text := strings.TrimSpace(ev.Text)
	if text == "" && ev.Message != nil {
		text = strings.TrimSpace(ev.Message.Text)
	}
	if channel == "" || ts == "" {
		return InboundEvent{}, false
	}
	threadTS := strings.TrimSpace(ev.ThreadTimeStamp)
	if threadTS == "" {
		threadTS = ts
	}
	channelType := normalizeChannelType(ev)
	_ = mentionUserIDs // currently unused at parse time; reaction-trigger flow filters in the handler
	return InboundEvent{
		Kind:        "message",
		Channel:     channel,
		ChannelType: channelType,
		TS:          ts,
		ThreadTS:    threadTS,
		UserID:      strings.TrimSpace(ev.User),
		Text:        text,
		TeamID:      env.TeamID,
		APIAppID:    env.APIAppID,
		RawJSON:     rawJSON(env, ev),
	}, true
}

func parseReactionAdded(env slackevents.EventsAPIEvent, ev *slackevents.ReactionAddedEvent) (InboundEvent, bool) {
	if ev == nil {
		return InboundEvent{}, false
	}
	itemChannel := strings.TrimSpace(ev.Item.Channel)
	itemTS := strings.TrimSpace(ev.Item.Timestamp)
	emoji := strings.Trim(strings.TrimSpace(ev.Reaction), ":")
	if itemChannel == "" || itemTS == "" || emoji == "" {
		return InboundEvent{}, false
	}
	// For partition-by-thread, ThreadTS defaults to the targeted message's
	// ts. If the reaction targets a reply inside an existing thread, Slack
	// doesn't tell us the parent ts in the reaction_added payload — the
	// handler must look it up via conversations.replies if it wants exact
	// thread alignment. For v1 we approximate: the message being reacted
	// to is treated as the thread anchor.
	return InboundEvent{
		Kind:        "reaction_added",
		Channel:     itemChannel,
		TS:          strings.TrimSpace(ev.EventTimestamp),
		ThreadTS:    itemTS,
		UserID:      strings.TrimSpace(ev.User),
		Reaction:    emoji,
		ItemChannel: itemChannel,
		ItemTS:      itemTS,
		ItemAuthor:  strings.TrimSpace(ev.ItemUser),
		TeamID:      env.TeamID,
		APIAppID:    env.APIAppID,
		RawJSON:     rawJSON(env, ev),
	}, true
}

func normalizeChannelType(ev *slackevents.MessageEvent) string {
	if ev.IsIM() || strings.EqualFold(ev.ChannelType, slackevents.ChannelTypeIM) {
		return "im"
	}
	if ev.IsMpIM() || strings.EqualFold(ev.ChannelType, slackevents.ChannelTypeMPIM) {
		return "mpim"
	}
	if strings.EqualFold(ev.ChannelType, slackevents.ChannelTypeGroup) {
		return "group"
	}
	return "channel"
}

func rawJSON(env slackevents.EventsAPIEvent, inner any) string {
	raw, _ := json.Marshal(map[string]any{
		"team_id":    env.TeamID,
		"api_app_id": env.APIAppID,
		"event":      inner,
	})
	return string(raw)
}
