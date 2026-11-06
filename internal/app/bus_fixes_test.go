package app

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// Bus fixes F1–F10 (human mail hygiene). One block per behavior.

func readSkillFile(t *testing.T, p string) string {
	t.Helper()
	data, err := fs.ReadFile(skillFiles(), p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(data)
}

// skillSection returns the text of a "### <heading>" section of SKILL.md
// up to the next "### " heading.
func skillSection(t *testing.T, heading string) string {
	t.Helper()
	core := readSkillFile(t, "SKILL.md")
	i := strings.Index(core, "### "+heading)
	if i < 0 {
		t.Fatalf("SKILL.md has no section %q", heading)
	}
	rest := core[i+4:]
	if j := strings.Index(rest, "\n### "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// F1: only BOUND sessions arm an inbox Monitor. An unbound (dispatch)
// session runs as the human's identity, so a Monitor there would sit on
// the human's own queue.
func TestF1SkillMonitorIsBoundOnly(t *testing.T) {
	for where, text := range map[string]string{
		"SKILL.md §4.18":          skillSection(t, "4.18"),
		"references/messaging.md": readSkillFile(t, "references/messaging.md"),
	} {
		text = strings.Join(strings.Fields(text), " ")
		if !strings.Contains(text, "BOUND session") {
			t.Errorf("%s must say the inbox Monitor is armed only in a BOUND session", where)
		}
		if !strings.Contains(text, "unbound") {
			t.Errorf("%s must tell unbound sessions not to arm a Monitor", where)
		}
	}
}

// busRow reads one row's raw status/acked_by for assertions.
func busRow(t *testing.T, db *sql.DB, id string) (status, ackedBy string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, COALESCE(acked_by,'') FROM bus_messages WHERE id=?`, id).
		Scan(&status, &ackedBy); err != nil {
		t.Fatalf("row %s: %v", id, err)
	}
	return
}

// sendHumanAsk sends `flow message user <body>` from the bound session sid
// and returns the new row id.
func sendHumanAsk(t *testing.T, db *sql.DB, sid, body string) string {
	t.Helper()
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", body}); rc != 0 {
			t.Fatalf("message user rc=%d", rc)
		}
	})
	var id string
	if err := db.QueryRow(`SELECT id FROM bus_messages WHERE body=?`, body).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// F2: an agent's `pop` — any mode — never claims, acks, or returns human
// mail (rows to user with no task). Listing still shows it.
func TestF2PopNeverConsumesHumanMail(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	ask := sendHumanAsk(t, db, "sid-asker", "approve the release?")
	// A broadcast to the human (user watches asker).
	if err := flowdb.AddWatch(db, "user", "asker"); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if rc := cmdBroadcast([]string{"imports done"}); rc != 0 {
			t.Fatal("broadcast")
		}
	})
	var fyi string
	_ = db.QueryRow(`SELECT id FROM bus_messages WHERE kind='broadcast'`).Scan(&fyi)

	// An UNBOUND agent session (dispatch) pops as user in every mode.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-dispatch")
	for _, args := range [][]string{
		{"pop"},
		{"pop", "--json"},
		{"pop", "--keep-unread"},
		{"pop", "--as", "user"},
		{"pop", "--as", "self"},
		{"pop", "--keep-unread", "--as", "user"},
		{"pop", "--wait", "--timeout", "0", "--json"},
	} {
		out := captureStdout(t, func() {
			if rc := cmdInbox(args); rc != 1 {
				t.Errorf("%v rc=%d, want 1 (nothing popped)", args, rc)
			}
		})
		if strings.Contains(out, "approve the release") || strings.Contains(out, "imports done") {
			t.Errorf("%v returned human mail:\n%s", args, out)
		}
	}
	for _, id := range []string{ask, fyi} {
		if st, by := busRow(t, db, id); st != "pending" || by != "" {
			t.Errorf("row %s changed by pop: status=%s acked_by=%s", id, st, by)
		}
	}
	// An agent `read <id>` doesn't consume it either.
	captureStdout(t, func() { _ = cmdInbox([]string{"read", ask}) })
	if st, _ := busRow(t, db, ask); st != "pending" {
		t.Errorf("agent read <id> consumed human mail: %s", st)
	}
	// The human's listing still shows both.
	out := captureStdout(t, func() { _ = cmdInbox([]string{"--as", "user", "--json"}) })
	var rows []busMsgJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("inbox --as user = %v %v\n%s", rows, err, out)
	}
}

// F2: the skill no longer tells agents `pop --as user` answers the human.
func TestF2SkillDropsPopAsUser(t *testing.T) {
	corpus := skillCorpus(t)
	if strings.Contains(corpus, "pop --as user") || strings.Contains(corpus, "`--as user` drains") {
		t.Error("skill still advertises popping the human queue as an agent action")
	}
}
