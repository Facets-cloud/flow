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

// F3: human mail is unread until a HUMAN has seen it (seen_at), whatever
// its delivery status — a `delivered` row left by a legacy keep-unread
// reader is still unread. One predicate drives list, count and label, and
// the notice separates asks from FYI broadcasts.
func TestF3HumanMailUnreadUntilSeen(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	ask := sendHumanAsk(t, db, "sid-asker", "pick a region")
	if err := flowdb.AddWatch(db, "user", "asker"); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if rc := cmdBroadcast([]string{"halfway there"}); rc != 0 {
			t.Fatal("broadcast")
		}
	})
	// Simulate a reader that claimed both to delivered (pre-F2 behavior).
	if _, err := db.Exec(`UPDATE bus_messages SET status='delivered', delivered_at=?`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}

	listUser := func() []busMsgJSON {
		out := captureStdout(t, func() { _ = cmdInbox([]string{"--as", "user", "--json"}) })
		var rows []busMsgJSON
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("parse %v\n%s", err, out)
		}
		return rows
	}
	rows := listUser()
	if len(rows) != 2 {
		t.Fatalf("delivered-but-unseen human mail hidden: %d rows", len(rows))
	}
	for _, r := range rows {
		if r.Mail != "unread" {
			t.Errorf("row %s labelled %q, want unread", r.ID, r.Mail)
		}
	}
	notice := humanPendingNotice(db)
	if !strings.Contains(notice, "1 ask(s) await you · 1 FYI") {
		t.Errorf("notice must separate asks from FYI: %q", notice)
	}

	// The human reads the ask from their own terminal → seen → not unread.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"read", ask}); rc != 0 {
			t.Fatal("read")
		}
	})
	var seen string
	_ = db.QueryRow(`SELECT COALESCE(seen_at,'') FROM bus_messages WHERE id=?`, ask).Scan(&seen)
	if seen == "" {
		t.Error("human read did not stamp seen_at")
	}
	if rows = listUser(); len(rows) != 1 || rows[0].Kind != "broadcast" {
		t.Errorf("after reading the ask, unread = %+v", rows)
	}
	if n := humanPendingNotice(db); !strings.Contains(n, "0 ask(s) await you · 1 FYI") {
		t.Errorf("notice after read: %q", n)
	}
}

// F3 legacy: when seen_at is first added, every pre-existing human row is
// stamped seen (the user chose to leave old mail behind).
func TestF3MigrationStampsLegacyHumanMailSeen(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	t.Setenv("FLOW_AUTO_RUN", "")
	sendHumanAsk(t, db, "sid-asker", "old ask")
	if _, err := db.Exec(`ALTER TABLE bus_messages DROP COLUMN seen_at`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db2 := openFlowDB(t) // re-open runs the migration
	var n int
	_ = db2.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE seen_at IS NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("legacy human rows not stamped seen: %d unseen", n)
	}
	if notice := humanPendingNotice(db2); notice != "" {
		t.Errorf("legacy mail still announced: %q", notice)
	}
}

// hookRun runs one hook with a JSON stdin payload and returns its
// additionalContext. It clears the hook-scoped session id afterwards so
// later non-hook calls in the test see only the env var.
func hookRun(t *testing.T, payload string, fn func(args []string) int) string {
	t.Helper()
	var out string
	withHookStdin(t, payload, func() {
		out = captureStdout(t, func() { _ = fn(nil) })
	})
	hookSessionID, hookPrompt, hookPromptKnown = "", "", false
	return busHookContext(t, out)
}

func seenAt(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	_ = db.QueryRow(`SELECT COALESCE(seen_at,'') FROM bus_messages WHERE id=?`, id).Scan(&s)
	return s
}

func upsPayload(sid, prompt string) string {
	b, _ := json.Marshal(map[string]string{"session_id": sid, "prompt": prompt})
	return string(b)
}

// F4: human mail a hook surfaced in an INTERACTIVE session is marked seen
// on the NEXT human-typed prompt there — never on the surfacing call
// itself, and never on an injected/automated prompt.
func TestF4SurfacedHumanMailSeenOnNextHumanPrompt(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	mkBusTask(t, db, "desk", "sid-desk")
	ask := sendHumanAsk(t, db, "sid-asker", "ship it tonight?")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	// SessionStart in the human's interactive session surfaces the ask.
	ctx := hookRun(t, `{"session_id":"sid-desk"}`, cmdHookSessionStart)
	if !strings.Contains(ctx, ask) {
		t.Fatalf("SessionStart did not surface the ask by id: %q", ctx)
	}
	if seenAt(t, db, ask) != "" {
		t.Fatal("the surfacing invocation itself marked the mail seen")
	}

	for name, prompt := range map[string]string{
		"--with injection":  "do the thing\n\n[via flow do --with]\nalso this",
		"/loop firing":      "/loop check the deploy",
		"wakeup sentinel":   "<<autonomous-loop-dynamic>>",
		"flow bootstrap":    buildTaskBootstrapPrompt("desk"),
		"auto bootstrap":    buildAutoBootstrapPrompt("desk", "regular", "", ""),
		"owner tick":        buildOwnerTickPrompt("o", "/tmp/o"),
		"task-notification": "<task-notification>agent finished</task-notification>",
	} {
		hookRun(t, upsPayload("sid-desk", prompt), cmdHookUserPromptSubmit)
		if seenAt(t, db, ask) != "" {
			t.Fatalf("%s prompt marked human mail seen", name)
		}
	}
	// A headless auto run never marks seen, even on plain text.
	t.Setenv("FLOW_AUTO_RUN", "desk")
	hookRun(t, upsPayload("sid-desk", "looks good"), cmdHookUserPromptSubmit)
	if seenAt(t, db, ask) != "" {
		t.Fatal("prompt under FLOW_AUTO_RUN marked human mail seen")
	}
	t.Setenv("FLOW_AUTO_RUN", "")
	// A different session's human prompt doesn't see what desk surfaced.
	hookRun(t, upsPayload("sid-other", "hello"), cmdHookUserPromptSubmit)
	if seenAt(t, db, ask) != "" {
		t.Fatal("another session's prompt marked desk's surfaced mail seen")
	}
	// The human typing in desk marks it seen.
	hookRun(t, upsPayload("sid-desk", "yes, ship it"), cmdHookUserPromptSubmit)
	if seenAt(t, db, ask) == "" {
		t.Fatal("next human-typed prompt did not mark surfaced mail seen")
	}
}

// F4: SessionStart inside a headless auto run doesn't record a surface.
func TestF4AutoRunSurfaceNotRecorded(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	mkBusTask(t, db, "kid", "sid-kid")
	ask := sendHumanAsk(t, db, "sid-asker", "need a key")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("FLOW_AUTO_RUN", "kid")
	hookRun(t, `{"session_id":"sid-kid"}`, cmdHookSessionStart)
	t.Setenv("FLOW_AUTO_RUN", "")
	hookRun(t, upsPayload("sid-kid", "ok"), cmdHookUserPromptSubmit)
	if seenAt(t, db, ask) != "" {
		t.Fatal("mail surfaced in an auto run was later marked seen")
	}
}

// F4: the sender-session "reply acks its own outbound ask" path only
// fires on a human-typed prompt.
func TestF4SenderAckOnlyOnHumanPrompt(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	ask := sendHumanAsk(t, db, "sid-asker", "which bucket?")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	ctx := hookRun(t, upsPayload("sid-asker", "go\n\n[via flow do --with]\nresume"), cmdHookUserPromptSubmit)
	if st, _ := busRow(t, db, ask); st != "pending" || strings.Contains(ctx, "answered after") {
		t.Fatalf("injected prompt acked the ask: %s %q", st, ctx)
	}
	t.Setenv("FLOW_AUTO_RUN", "asker")
	hookRun(t, upsPayload("sid-asker", "the blue one"), cmdHookUserPromptSubmit)
	if st, _ := busRow(t, db, ask); st != "pending" {
		t.Fatal("auto-run prompt acked the ask")
	}
	t.Setenv("FLOW_AUTO_RUN", "")
	ctx = hookRun(t, upsPayload("sid-asker", "the blue one"), cmdHookUserPromptSubmit)
	if st, _ := busRow(t, db, ask); st != "acked" || !strings.Contains(ctx, "answered after") {
		t.Fatalf("human prompt did not ack: %s %q", st, ctx)
	}
	if seenAt(t, db, ask) == "" {
		t.Error("ack did not stamp seen_at")
	}
}
