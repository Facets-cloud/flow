package monitor

import (
	"os"
	"strings"
)

// DefaultTriggerEmoji is the Slack reaction shortname (no colons) that
// signals "Claude should handle this thread." Override at runtime via
// FLOW_SLACK_TRIGGER_EMOJI. The default matches the convention the user
// settled on during design — a custom workspace emoji combining the
// flow + claude brand.
const DefaultTriggerEmoji = "claude"

// TriggerEmoji resolves the configured trigger emoji shortname, with the
// surrounding colons stripped (so ":flow-claude:" and "flow-claude" both
// resolve identically). Empty / whitespace env values fall through to
// DefaultTriggerEmoji.
func TriggerEmoji() string {
	e := strings.Trim(strings.TrimSpace(os.Getenv("FLOW_SLACK_TRIGGER_EMOJI")), ":")
	if e == "" {
		return DefaultTriggerEmoji
	}
	return e
}

// SelfUserIDs returns the Slack user IDs that count as "the user" for
// reaction-consent purposes. Only reactions added by one of these IDs
// are treated as trigger signals — reactions from coworkers are noise.
//
// Configured via FLOW_SLACK_SELF_USER_IDS (comma/space-separated for
// multi-workspace setups) with fallbacks to the single-id env vars from
// the existing Slack code. Returns an empty slice when unset, in which
// case DecideReaction will refuse to trigger — better to require explicit
// configuration than to fan out replies for every workspace member's
// reaction.
func SelfUserIDs() []string {
	raw := firstNonEmpty(
		os.Getenv("FLOW_SLACK_SELF_USER_IDS"),
		os.Getenv("FLOW_SLACK_SELF_USER_ID"),
		os.Getenv("FLOW_SLACK_USER_ID"),
		os.Getenv("SLACK_USER_ID"),
	)
	if raw == "" {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ThreadKey returns the partition key flow uses to find or create a task
// for a Slack thread. We include the channel ID because two different
// channels can technically have messages with the same ts (Slack ts is
// only unique-per-channel), so "thread_ts" alone could collide.
//
// The corresponding flow task tag is "slack-thread:<key>" — see the
// integration layer for the tag-vs-lookup helpers.
func ThreadKey(channel, threadTS string) string {
	channel = strings.TrimSpace(channel)
	threadTS = strings.TrimSpace(threadTS)
	if channel == "" || threadTS == "" {
		return ""
	}
	return channel + ":" + threadTS
}

// ReactionDecision is the output of DecideReaction: a small struct the
// integration layer turns into side effects (flow add task, inbox append,
// reaction.add for status, etc.). Trigger=false means the event is not
// a consenting trigger from "us" — drop it.
type ReactionDecision struct {
	Trigger   bool
	ThreadKey string
	Channel   string
	ThreadTS  string
	ItemTS    string
	Reactor   string
	Reaction  string
	Event     InboundEvent
}

// DecideReaction classifies an InboundEvent. Returns Trigger=false unless
// ALL of these hold:
//
//   - Kind == "reaction_added"
//   - Reaction (case-insensitive) == triggerEmoji
//   - Reactor (UserID) is in selfUserIDs
//   - Channel + ThreadTS are present (so ThreadKey is meaningful)
//
// The integration layer then uses ThreadKey to look up an existing task
// or create a new one, and appends the event to that task's inbox.
//
// Pass an empty selfUserIDs to short-circuit all reactions to non-trigger
// — useful for tests and as a safety net when SelfUserIDs() resolves to
// empty (operator hasn't configured their Slack user id).
func DecideReaction(ev InboundEvent, triggerEmoji string, selfUserIDs []string) ReactionDecision {
	if ev.Kind != "reaction_added" {
		return ReactionDecision{}
	}
	if !strings.EqualFold(strings.TrimSpace(ev.Reaction), strings.TrimSpace(triggerEmoji)) {
		return ReactionDecision{}
	}
	if !containsUserID(selfUserIDs, ev.UserID) {
		return ReactionDecision{}
	}
	key := ThreadKey(ev.Channel, ev.ThreadTS)
	if key == "" {
		return ReactionDecision{}
	}
	return ReactionDecision{
		Trigger:   true,
		ThreadKey: key,
		Channel:   ev.Channel,
		ThreadTS:  ev.ThreadTS,
		ItemTS:    ev.ItemTS,
		Reactor:   ev.UserID,
		Reaction:  ev.Reaction,
		Event:     ev,
	}
}

func containsUserID(haystack []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, h := range haystack {
		if strings.TrimSpace(h) == needle {
			return true
		}
	}
	return false
}
