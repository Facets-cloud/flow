package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// sparse makes a file of the given size without writing its bytes.
func sparse(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// F3: flow message <to> --attach <file> (repeatable) copies each file into
// $FLOW_ROOT/attachments/<id>/<basename> (collisions deduped) and stores
// {name,size,sha256} on the row; views show absolute paths.
func TestMessageAttachLocal(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "other", "sid-other")
	src := t.TempDir()
	a := writeFile(t, src, "notes.txt", "hello")
	b := writeFile(t, filepath.Join(src, "sub"), "notes.txt", "world!")
	c := writeFile(t, src, "plan.md", "# plan")

	if rc := msgRC(t, "user/other", "see files", "--attach", a, "--attach", b, "--attach="+c); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	m := rowByBody(t, db, "see files")
	want := []flowdb.Attachment{
		{Name: "notes.txt", Size: 5, SHA256: sha("hello")},
		{Name: "notes-2.txt", Size: 6, SHA256: sha("world!")},
		{Name: "plan.md", Size: 6, SHA256: sha("# plan")},
	}
	if len(m.Attachments) != len(want) {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	for i, w := range want {
		if m.Attachments[i] != w {
			t.Errorf("attachment %d = %+v, want %+v", i, m.Attachments[i], w)
		}
		p := filepath.Join(root, "attachments", m.ID, w.Name)
		got, err := os.ReadFile(p)
		if err != nil || sha(string(got)) != w.SHA256 {
			t.Errorf("copy %s: %v", p, err)
		}
		if fi, _ := os.Stat(p); fi != nil && fi.Mode()&0o111 != 0 {
			t.Errorf("copy %s is executable: %v", p, fi.Mode())
		}
	}

	// The recipient session sees absolute paths, in text and JSON.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-other")
	listing := captureStdout(t, func() { cmdInbox(nil) })
	if !strings.Contains(listing, filepath.Join(root, "attachments", m.ID, "notes-2.txt")) {
		t.Errorf("inbox listing lacks attachment path:\n%s", listing)
	}
	out := captureStdout(t, func() { cmdInbox([]string{"pop", "--json"}) })
	var popped struct {
		ID          string `json:"id"`
		Attachments []struct {
			Name, SHA256, Path string
			Size               int64
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(out), &popped); err != nil {
		t.Fatalf("pop --json %q: %v", out, err)
	}
	if popped.ID != m.ID || len(popped.Attachments) != 3 ||
		popped.Attachments[2].Path != filepath.Join(root, "attachments", m.ID, "plan.md") ||
		popped.Attachments[2].SHA256 != sha("# plan") || popped.Attachments[2].Size != 6 {
		t.Errorf("pop --json attachments = %+v", popped)
	}
	// A message without attachments: no attachments key in JSON, no dir.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if rc := msgRC(t, "user/other", "plain"); rc != 0 {
		t.Fatal(rc)
	}
	p := rowByBody(t, db, "plain")
	if _, err := os.Stat(filepath.Join(root, "attachments", p.ID)); !os.IsNotExist(err) {
		t.Errorf("plain message made an attachments dir: %v", err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-other")
	if out := captureStdout(t, func() { cmdInbox([]string{"pop", "--json"}) }); strings.Contains(out, "attachments") {
		t.Errorf("plain pop --json has attachments: %s", out)
	}
}

// F3 caps: 25 MiB per file, 100 MiB per message, 20 files; only regular
// files (no dirs, no symlinks). A refused send stores nothing.
func TestMessageAttachRejections(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "other", "sid-other")
	src := t.TempDir()
	ok := writeFile(t, src, "ok.txt", "x")
	link := filepath.Join(src, "link.txt")
	if err := os.Symlink(ok, link); err != nil {
		t.Fatal(err)
	}
	big := sparse(t, src, "big.bin", 25<<20+1)
	var fives []string
	for i := 0; i < 5; i++ {
		fives = append(fives, "--attach", sparse(t, src, "f"+string(rune('a'+i))+".bin", 21<<20))
	}
	var many []string
	for i := 0; i < 21; i++ {
		many = append(many, "--attach", ok)
	}
	for name, extra := range map[string][]string{
		"dir":         {"--attach", src},
		"symlink":     {"--attach", link},
		"missing":     {"--attach", filepath.Join(src, "nope")},
		"file > 25":   {"--attach", big},
		"total > 100": fives,
		"21 files":    many,
		"no value":    {"--attach"},
	} {
		args := append([]string{"user/other", "refused " + name}, extra...)
		if rc := msgRC(t, args...); rc != 2 {
			t.Errorf("%s: rc=%d, want 2", name, rc)
		}
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("refused sends stored %d rows", n)
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "attachments")); len(ents) != 0 {
		t.Errorf("refused sends left %d attachment dirs", len(ents))
	}
	// Exactly at the caps is fine: 20 files.
	if rc := msgRC(t, append([]string{"user/other", "twenty"}, many[2:]...)...); rc != 0 {
		t.Errorf("20 files rc=%d", rc)
	}
}

// F3 peer send: the contract-2 `run send` payload carries
// "attachments":[{name,size,sha256,path}] ([] when none).
func TestPeerSendPayloadAttachments(t *testing.T) {
	marks := peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	root, _ := flowRoot()
	f := writeFile(t, t.TempDir(), "spec.pdf", "%PDF")

	if rc := msgRC(t, "bob", "no files"); rc != 0 {
		t.Fatal(rc)
	}
	dispatch(t, rowByBody(t, db, "no files").ID)
	if p := readMark(t, marks, 0); p["attachments"] == nil {
		t.Errorf("payload without files lacks attachments: %v", p)
	} else if as, ok := p["attachments"].([]any); !ok || len(as) != 0 {
		t.Errorf("payload attachments = %v, want []", p["attachments"])
	}

	if rc := msgRC(t, "bob", "with file", "--attach", f); rc != 0 {
		t.Fatal(rc)
	}
	m := rowByBody(t, db, "with file")
	dispatch(t, m.ID)
	as, _ := readMark(t, marks, 1)["attachments"].([]any)
	if len(as) != 1 {
		t.Fatalf("payload attachments = %v", as)
	}
	a := as[0].(map[string]any)
	if a["name"] != "spec.pdf" || a["size"] != float64(4) || a["sha256"] != sha("%PDF") ||
		a["path"] != filepath.Join(root, "attachments", m.ID, "spec.pdf") {
		t.Errorf("payload attachment = %v", a)
	}
}

// F3 inbound: flow peer deliver --attach <path> (repeatable) copies into
// this hub's attachments dir for the new row, same caps.
func TestPeerDeliverAttach(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	root, _ := flowRoot()
	src := t.TempDir()
	f := writeFile(t, src, "log.txt", "boom")

	if rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "a-1", "--attach", f, "see log"); rc != 0 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	m, _ := flowdb.InboundByRef(db, "bob", "a-1")
	if len(m.Attachments) != 1 || m.Attachments[0] != (flowdb.Attachment{Name: "log.txt", Size: 4, SHA256: sha("boom")}) {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	if b, err := os.ReadFile(filepath.Join(root, "attachments", m.ID, "log.txt")); err != nil || string(b) != "boom" {
		t.Errorf("copy: %q %v", b, err)
	}
	n := countRows(t, db)
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "a-2", "--attach", src, "dir"); rc != 2 {
		t.Errorf("dir attach rc=%d, want 2", rc)
	}
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "a-3", "--attach", sparse(t, src, "big", 25<<20+1), "big"); rc != 2 {
		t.Errorf("big attach rc=%d, want 2", rc)
	}
	if countRows(t, db) != n {
		t.Errorf("refused deliver stored rows")
	}
	// The human's view shows the path.
	if out := captureStdout(t, func() { cmdInbox([]string{"--as", "user"}) }); !strings.Contains(out, filepath.Join(root, "attachments", m.ID, "log.txt")) {
		t.Errorf("human inbox lacks attachment path:\n%s", out)
	}
}
