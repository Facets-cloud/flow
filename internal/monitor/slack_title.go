package monitor

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/slack-go/slack"
)

const slackTitleContextLimit = 96

// SlackConversation is the small, testable subset of conversations.info data
// needed to name Slack-origin tasks.
type SlackConversation struct {
	ID        string
	Name      string
	User      string
	Members   []string
	IsIM      bool
	IsMpIM    bool
	IsChannel bool
	IsGroup   bool
}

// SlackMessage is the small, testable subset of conversations.replies data
// needed to derive thread context.
type SlackMessage struct {
	User string
	Text string
}

// SlackUser is the small, testable subset of users.info data needed for
// human-readable participant names.
type SlackUser struct {
	ID          string
	Name        string
	RealName    string
	DisplayName string
}

// SlackTitleClient hides slack-go behind the exact read calls title generation
// needs. Tests use a fake client; production uses slackTitleAPIClient.
type SlackTitleClient interface {
	ConversationInfo(ctx context.Context, channelID string) (SlackConversation, error)
	ConversationReplies(ctx context.Context, channelID, threadTS string, limit int) ([]SlackMessage, error)
	UsersInConversation(ctx context.Context, channelID string, limit int) ([]string, error)
	UserInfo(ctx context.Context, userID string) (SlackUser, error)
}

type slackTitleAPIClient struct {
	api *slack.Client
}

func newSlackTitleAPIClient() SlackTitleClient {
	token := SlackBotToken()
	if token == "" {
		return nil
	}
	return slackTitleAPIClient{api: slack.New(token)}
}

func (c slackTitleAPIClient) ConversationInfo(ctx context.Context, channelID string) (SlackConversation, error) {
	ch, err := c.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{
		ChannelID:         normalizeSlackChannelID(channelID),
		IncludeNumMembers: true,
	})
	if err != nil {
		return SlackConversation{}, err
	}
	if ch == nil {
		return SlackConversation{}, errors.New("slack conversation info missing")
	}
	return SlackConversation{
		ID:        firstNonEmpty(ch.ID, normalizeSlackChannelID(channelID)),
		Name:      firstNonEmpty(ch.Name, ch.NameNormalized),
		User:      ch.User,
		Members:   ch.Members,
		IsIM:      ch.IsIM,
		IsMpIM:    ch.IsMpIM,
		IsChannel: ch.IsChannel,
		IsGroup:   ch.IsGroup,
	}, nil
}

func (c slackTitleAPIClient) ConversationReplies(ctx context.Context, channelID, threadTS string, limit int) ([]SlackMessage, error) {
	msgs, _, _, err := c.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: normalizeSlackChannelID(channelID),
		Timestamp: strings.TrimSpace(threadTS),
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SlackMessage, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, SlackMessage{
			User: firstNonEmpty(msg.User, msg.Username),
			Text: msg.Text,
		})
	}
	return out, nil
}

func (c slackTitleAPIClient) UsersInConversation(ctx context.Context, channelID string, limit int) ([]string, error) {
	members, _, err := c.api.GetUsersInConversationContext(ctx, &slack.GetUsersInConversationParameters{
		ChannelID: normalizeSlackChannelID(channelID),
		Limit:     limit,
	})
	return members, err
}

func (c slackTitleAPIClient) UserInfo(ctx context.Context, userID string) (SlackUser, error) {
	u, err := c.api.GetUserInfoContext(ctx, strings.TrimSpace(userID))
	if err != nil {
		return SlackUser{}, err
	}
	if u == nil {
		return SlackUser{}, errors.New("slack user info missing")
	}
	return SlackUser{
		ID:          u.ID,
		Name:        u.Name,
		RealName:    firstNonEmpty(u.Profile.RealName, u.Profile.RealNameNormalized, u.RealName),
		DisplayName: firstNonEmpty(u.Profile.DisplayName, u.Profile.DisplayNameNormalized),
	}, nil
}

// BuildSlackTaskTitle turns Slack metadata into the human-facing task/session
// title. The slug remains the durable identity; this title is display-only.
func BuildSlackTaskTitle(ctx context.Context, client SlackTitleClient, decision ReactionDecision, selfUserIDs []string) (string, error) {
	if client == nil {
		return "", errors.New("slack title client unavailable")
	}
	channelID := normalizeSlackChannelID(decision.Channel)
	threadTS := strings.TrimSpace(decision.ThreadTS)
	if channelID == "" || threadTS == "" {
		return "", errors.New("slack channel and thread_ts are required")
	}
	conversation, err := client.ConversationInfo(ctx, channelID)
	if err != nil {
		return "", err
	}
	prefix := slackTitlePrefix(ctx, client, conversation, channelID, selfUserIDs)
	if prefix == "" {
		prefix = channelID
	}
	contextText := slackThreadContext(ctx, client, channelID, threadTS, decision.Event.Text)
	if contextText == "" {
		return truncateRunes(prefix, slackTitleContextLimit), nil
	}
	return truncateRunes(prefix+" - "+contextText, slackTitleContextLimit), nil
}

func slackTitlePrefix(ctx context.Context, client SlackTitleClient, conversation SlackConversation, channelID string, selfUserIDs []string) string {
	switch {
	case conversation.IsIM || strings.HasPrefix(channelID, "D"):
		userID := strings.TrimSpace(conversation.User)
		if userID == "" {
			for _, member := range conversation.Members {
				if !containsUserID(selfUserIDs, member) {
					userID = member
					break
				}
			}
		}
		return slackUserDisplayName(ctx, client, userID)
	case conversation.IsMpIM:
		members := conversation.Members
		if len(members) == 0 {
			if got, err := client.UsersInConversation(ctx, channelID, 8); err == nil {
				members = got
			}
		}
		names := make([]string, 0, 3)
		remaining := 0
		for _, member := range members {
			if containsUserID(selfUserIDs, member) {
				continue
			}
			name := slackUserDisplayName(ctx, client, member)
			if name == "" {
				name = member
			}
			if len(names) < 3 {
				names = append(names, name)
			} else {
				remaining++
			}
		}
		if len(names) == 0 {
			return firstNonEmpty(conversation.Name, channelID)
		}
		out := strings.Join(names, ", ")
		if remaining > 0 {
			out += " +" + strconv.Itoa(remaining)
		}
		return out
	default:
		name := firstNonEmpty(conversation.Name, channelID)
		if strings.HasPrefix(name, "#") {
			return name
		}
		if strings.HasPrefix(channelID, "C") || strings.HasPrefix(channelID, "G") {
			return "#" + strings.TrimPrefix(name, "#")
		}
		return name
	}
}

func slackThreadContext(ctx context.Context, client SlackTitleClient, channelID, threadTS, fallback string) string {
	msgs, err := client.ConversationReplies(ctx, channelID, threadTS, 1)
	if err == nil {
		for _, msg := range msgs {
			if text := cleanSlackTitleText(msg.Text); text != "" {
				return text
			}
		}
	}
	return cleanSlackTitleText(fallback)
}

func slackUserDisplayName(ctx context.Context, client SlackTitleClient, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	user, err := client.UserInfo(ctx, userID)
	if err != nil {
		return userID
	}
	return firstNonEmpty(user.DisplayName, user.RealName, user.Name, user.ID)
}

var (
	slackMentionRe  = regexp.MustCompile(`<@([A-Z0-9]+)(?:\|[^>]+)?>`)
	slackLinkRe     = regexp.MustCompile(`<([^|>]+)\|([^>]+)>`)
	slackBareLinkRe = regexp.MustCompile(`<([^>]+)>`)
)

func cleanSlackTitleText(text string) string {
	text = slackMentionRe.ReplaceAllString(text, "@$1")
	text = slackLinkRe.ReplaceAllString(text, "$2")
	text = slackBareLinkRe.ReplaceAllString(text, "$1")
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", "*", "", "`", "", "_", "")
	text = replacer.Replace(text)
	return truncateRunes(strings.Join(strings.Fields(text), " "), slackTitleContextLimit)
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return strings.TrimSpace(string(runes[:max-1])) + "..."
}

func normalizeSlackChannelID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

var resolveSlackTaskTitle = func(ctx context.Context, decision ReactionDecision) (string, error) {
	client := newSlackTitleAPIClient()
	if client == nil {
		return "", errors.New("slack token unavailable")
	}
	return BuildSlackTaskTitle(ctx, client, decision, SelfUserIDs())
}
