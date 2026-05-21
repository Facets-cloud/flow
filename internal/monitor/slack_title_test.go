package monitor

import (
	"context"
	"errors"
	"testing"
)

type fakeSlackTitleClient struct {
	conversations map[string]SlackConversation
	replies       map[string][]SlackMessage
	members       map[string][]string
	users         map[string]SlackUser
	err           error
}

func (f fakeSlackTitleClient) ConversationInfo(_ context.Context, channelID string) (SlackConversation, error) {
	if f.err != nil {
		return SlackConversation{}, f.err
	}
	return f.conversations[channelID], nil
}

func (f fakeSlackTitleClient) ConversationReplies(_ context.Context, channelID, threadTS string, _ int) ([]SlackMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.replies[channelID+":"+threadTS], nil
}

func (f fakeSlackTitleClient) UsersInConversation(_ context.Context, channelID string, _ int) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.members[channelID], nil
}

func (f fakeSlackTitleClient) UserInfo(_ context.Context, userID string) (SlackUser, error) {
	if f.err != nil {
		return SlackUser{}, f.err
	}
	return f.users[userID], nil
}

func TestBuildSlackTaskTitleDMUsesOtherPersonAndThreadContext(t *testing.T) {
	client := fakeSlackTitleClient{
		conversations: map[string]SlackConversation{
			"D123": {ID: "D123", IsIM: true, User: "U_rohit"},
		},
		replies: map[string][]SlackMessage{
			"D123:1779345633.950689": {
				{User: "U_rohit", Text: "Ishan's call about CoinSwitch CSX project kickoff"},
			},
		},
		users: map[string]SlackUser{
			"U_rohit": {ID: "U_rohit", DisplayName: "Rohit", RealName: "Rohit Sharma"},
		},
	}
	decision := ReactionDecision{
		Channel:   "D123",
		ThreadTS:  "1779345633.950689",
		ThreadKey: "D123:1779345633.950689",
	}

	got, err := BuildSlackTaskTitle(context.Background(), client, decision, []string{"U_me"})
	if err != nil {
		t.Fatalf("BuildSlackTaskTitle: %v", err)
	}
	want := "Rohit - Ishan's call about CoinSwitch CSX project kickoff"
	if got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestBuildSlackTaskTitleMPIMUsesParticipantNames(t *testing.T) {
	client := fakeSlackTitleClient{
		conversations: map[string]SlackConversation{
			"G123": {ID: "G123", IsMpIM: true},
		},
		members: map[string][]string{
			"G123": {"U_me", "U_rohit", "U_ishan", "U_priya"},
		},
		replies: map[string][]SlackMessage{
			"G123:1779345633.950689": {
				{User: "U_ishan", Text: "Please review Niyo launch blockers before tomorrow"},
			},
		},
		users: map[string]SlackUser{
			"U_rohit": {ID: "U_rohit", DisplayName: "Rohit"},
			"U_ishan": {ID: "U_ishan", DisplayName: "Ishan"},
			"U_priya": {ID: "U_priya", DisplayName: "Priya"},
		},
	}
	decision := ReactionDecision{
		Channel:   "G123",
		ThreadTS:  "1779345633.950689",
		ThreadKey: "G123:1779345633.950689",
	}

	got, err := BuildSlackTaskTitle(context.Background(), client, decision, []string{"U_me"})
	if err != nil {
		t.Fatalf("BuildSlackTaskTitle: %v", err)
	}
	want := "Rohit, Ishan, Priya - Please review Niyo launch blockers before tomorrow"
	if got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestBuildSlackTaskTitleChannelUsesChannelName(t *testing.T) {
	client := fakeSlackTitleClient{
		conversations: map[string]SlackConversation{
			"C123": {ID: "C123", Name: "platform", IsChannel: true},
		},
		replies: map[string][]SlackMessage{
			"C123:1779268097.778199": {
				{User: "U_rohit", Text: "Exact path matching Kong plugin"},
			},
		},
	}
	decision := ReactionDecision{
		Channel:   "C123",
		ThreadTS:  "1779268097.778199",
		ThreadKey: "C123:1779268097.778199",
	}

	got, err := BuildSlackTaskTitle(context.Background(), client, decision, nil)
	if err != nil {
		t.Fatalf("BuildSlackTaskTitle: %v", err)
	}
	want := "#platform - Exact path matching Kong plugin"
	if got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestBuildSlackTaskTitleReturnsErrorWhenMetadataUnavailable(t *testing.T) {
	decision := ReactionDecision{Channel: "D123", ThreadTS: "1779345633.950689"}
	_, err := BuildSlackTaskTitle(context.Background(), fakeSlackTitleClient{err: errors.New("slack down")}, decision, nil)
	if err == nil {
		t.Fatal("expected Slack metadata error")
	}
}
