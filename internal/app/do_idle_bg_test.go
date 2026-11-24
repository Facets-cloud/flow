package app

import (
	"testing"

	"flow/internal/flowdb"
)

// A --with instruction is the user's, so it must reach an idle background
// session as a prompt, not as a peer message. prx pushes its notices as chat
// from another session, so for it the --with text takes the stop + same-id
// resume path (deliverToIdleBg returns false and never calls Wake). Mail
// notices are not instructions and still use the push.
func TestDeliverToIdleBgKeepsWithTextOffChatOnlyPush(t *testing.T) {
	db := wakeSetup(t)
	task, err := flowdb.GetTask(db, "w")
	if err != nil {
		t.Fatal(err)
	}

	chat := &fakeWaker{live: true, chatOnly: true}
	stubWaker(t, chat)
	if deliverToIdleBg(db, task, "sid-w", "run the audit", 0) {
		t.Fatal("--with text went over a chat-only push: it would reach the agent as a peer message")
	}
	if len(chat.pushes) != 0 {
		t.Fatalf("Wake called for --with on a chat-only push: %+v", chat.pushes)
	}
	if !deliverToIdleBg(db, task, "sid-w", "", 2) {
		t.Fatal("unread-mail notice should still use the push on a chat-only waker")
	}
	if len(chat.pushes) != 1 {
		t.Fatalf("mail notice pushes = %d, want 1", len(chat.pushes))
	}

	instr := &fakeWaker{live: true}
	stubWaker(t, instr)
	if !deliverToIdleBg(db, task, "sid-w", "run the audit", 0) {
		t.Fatal("a waker that delivers instructions should carry --with")
	}
	if len(instr.pushes) != 1 {
		t.Fatalf("--with pushes = %d, want 1", len(instr.pushes))
	}
}
