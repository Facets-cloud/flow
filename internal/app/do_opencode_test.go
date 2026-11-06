package app

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness/opencode"
	"flow/internal/iterm"
)

// stubOpencodePS stubs the opencode process-table scan so the live guard
// never shells out to a real `ps`.
func stubOpencodePS(t *testing.T) {
	t.Helper()
	old := opencode.PSRunner
	opencode.PSRunner = func() ([]byte, error) { return []byte(""), nil }
	t.Cleanup(func() { opencode.PSRunner = old })
}

// opencodeLaunchFromAppleScript extracts the `opencode …` command that
// iTerm's AppleScript will type into the spawned tab. The script is
// `… && [env-prefix]opencode …` wrapped in a double-quoted AppleScript
// string; the command itself contains no double quotes.
func opencodeLaunchFromAppleScript(script string) string {
	re := regexp.MustCompile(`(opencode -s ses_[0-9A-Za-z]{26}[^"]*)`)
	m := re.FindStringSubmatch(script)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// opencodeSessionRe is the native opencode session-id shape.
var opencodeSessionRe = regexp.MustCompile(`^ses_[0-9A-Za-z]{26}$`)

// lastOpencodeCreatePayload returns the decoded body of the most recent
// session.create call, or nil.
func lastOpencodeCreatePayload(t *testing.T, calls *[][]string) map[string]any {
	t.Helper()
	var payload map[string]any
	for _, c := range *calls {
		if len(c) >= 3 && c[0] == "session.create" && c[1] == "-d" {
			var p map[string]any
			if err := json.Unmarshal([]byte(c[2]), &p); err != nil {
				t.Fatalf("decode session.create payload: %v", err)
			}
			payload = p
		}
	}
	return payload
}

func countOpencodeCalls(calls *[][]string, op string) int {
	n := 0
	for _, c := range *calls {
		if len(c) > 0 && c[0] == op {
			n++
		}
	}
	return n
}

// TestCmdDoOpencodeBootstrapAndResume pins the opencode spawn path:
// bootstrap pre-allocates a `ses_…` id, prepares the session through the
// API, spawns `opencode -s <id> --prompt …`, and a second `flow do`
// resumes the same id (and prepares again).
func TestCmdDoOpencodeBootstrapAndResume(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "oc-do")
	stubOpencodePS(t)
	calls := stubOpencodeAPI(t, false)
	_, script := stubITerm(t)

	if rc := cmdDo([]string{"oc-do", "--harness", "opencode"}); rc != 0 {
		t.Fatalf("cmdDo --harness opencode rc=%d, want 0", rc)
	}
	bootCmd := opencodeLaunchFromAppleScript(script())
	if !strings.HasPrefix(bootCmd, "opencode -s ses_") {
		t.Fatalf("spawn command = %q, want it to start with `opencode -s ses_`", bootCmd)
	}
	if !strings.Contains(bootCmd, "--prompt") {
		t.Errorf("bootstrap spawn command missing --prompt: %q", bootCmd)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "oc-do")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
	if task.Harness.String != "opencode" {
		t.Errorf("harness = %q, want opencode", task.Harness.String)
	}
	if !opencodeSessionRe.MatchString(task.SessionID.String) {
		t.Fatalf("session_id = %q, want ^ses_[0-9A-Za-z]{26}$", task.SessionID.String)
	}
	sid := task.SessionID.String

	// prepare ran on bootstrap, with the task slug in the metadata.
	payload := lastOpencodeCreatePayload(t, calls)
	if payload == nil {
		t.Fatalf("no session.create on bootstrap; calls=%v", *calls)
	}
	meta, _ := payload["metadata"].(map[string]any)
	flow, _ := meta["flow"].(map[string]any)
	if flow["taskSlug"] != "oc-do" {
		t.Errorf("metadata.flow.taskSlug = %v, want oc-do", flow["taskSlug"])
	}
	if countOpencodeCalls(calls, "session.create") != 1 {
		t.Errorf("session.create calls = %d, want 1", countOpencodeCalls(calls, "session.create"))
	}

	// Resume: same id, prepared again, no --prompt without an injection.
	if rc := cmdDo([]string{"oc-do"}); rc != 0 {
		t.Fatalf("cmdDo resume rc=%d, want 0", rc)
	}
	resumeCmd := opencodeLaunchFromAppleScript(script())
	if !strings.HasPrefix(resumeCmd, "opencode -s "+sid) {
		t.Errorf("resume command = %q, want it to start with `opencode -s %s`", resumeCmd, sid)
	}
	if n := countOpencodeCalls(calls, "session.create"); n != 2 {
		t.Errorf("session.create calls after resume = %d, want 2 (prepare runs on resume too)", n)
	}
}

// TestCmdDoOpencodeSkipPermissionsAddsAuto pins the flag translation:
// --dangerously-skip-permissions becomes opencode's --auto.
func TestCmdDoOpencodeSkipPermissionsAddsAuto(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "oc-auto-flag")
	stubOpencodePS(t)
	stubOpencodeAPI(t, false)
	_, script := stubITerm(t)

	if rc := cmdDo([]string{"oc-auto-flag", "--harness", "opencode", "--dangerously-skip-permissions"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d, want 0", rc)
	}
	cmd := opencodeLaunchFromAppleScript(script())
	if !strings.Contains(cmd, " --auto") {
		t.Errorf("spawn command missing ` --auto`: %q", cmd)
	}
}

// TestCmdDoOpencodePrepareFailureRollsBack pins the failure contract: an
// API error on a fresh bootstrap returns 1, rolls the task back to
// backlog with NULL session_id, and spawns no tab.
func TestCmdDoOpencodePrepareFailureRollsBack(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "oc-prepare-fail")
	stubOpencodePS(t)
	stubOpencodeAPI(t, true)
	spawns, _ := stubITerm(t)

	if rc := cmdDo([]string{"oc-prepare-fail", "--harness", "opencode"}); rc != 1 {
		t.Fatalf("cmdDo rc=%d, want 1 when prepare fails", rc)
	}
	if *spawns != 0 {
		t.Errorf("iterm spawn count = %d, want 0", *spawns)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "oc-prepare-fail")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "backlog" {
		t.Errorf("status = %q, want backlog after prepare failure", task.Status)
	}
	if task.SessionID.Valid {
		t.Errorf("session_id = %q, want NULL after prepare failure", task.SessionID.String)
	}
}

// TestCmdDoOpencodeLiveGuard pins that the live-session guard resolves
// opencode's case-sensitive `ses_…` ids (the map is keyed as-is, unlike
// the lowercased UUID maps): a live task refuses to spawn a duplicate tab.
func TestCmdDoOpencodeLiveGuard(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "oc-live")
	const sid = "ses_ABCdefghijklmnopqrstuvwxyz"
	if !opencodeSessionRe.MatchString(sid) {
		t.Fatalf("bad fixture id %q", sid)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, harness='opencode', status='in-progress' WHERE slug='oc-live'`,
		sid, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}

	old := opencode.PSRunner
	opencode.PSRunner = func() ([]byte, error) {
		return []byte("  12345 opencode -s " + sid + "\n"), nil
	}
	t.Cleanup(func() { opencode.PSRunner = old })

	// Focus path: no iTerm rows → focus miss → refuse the spawn.
	oldFocusPS := iterm.PSRunner
	iterm.PSRunner = func() ([]byte, error) { return []byte(""), nil }
	t.Cleanup(func() { iterm.PSRunner = oldFocusPS })
	spawns, _ := stubITerm(t)
	stubOpencodeAPI(t, false)

	if rc := cmdDo([]string{"oc-live"}); rc != 1 {
		t.Errorf("cmdDo rc=%d, want 1 when the opencode session is live and unfocusable", rc)
	}
	if *spawns != 0 {
		t.Errorf("iterm spawn count = %d, want 0 (live guard should block)", *spawns)
	}
}

// TestLiveCountFor covers both map keying styles.
func TestLiveCountFor(t *testing.T) {
	live := map[string]int{
		"ses_ABCdefghijklmnopqrstuvwxyz":       2, // opencode: exact
		"abcdef12-3456-4789-8abc-def012345678": 1, // claude: lowercased
	}
	if got := liveCountFor(live, "ses_ABCdefghijklmnopqrstuvwxyz"); got != 2 {
		t.Errorf("exact case-sensitive id: got %d, want 2", got)
	}
	if got := liveCountFor(live, "ABCDEF12-3456-4789-8ABC-DEF012345678"); got != 1 {
		t.Errorf("uppercase UUID fallback: got %d, want 1", got)
	}
	if got := liveCountFor(live, "ses_missing"); got != 0 {
		t.Errorf("unknown id: got %d, want 0", got)
	}
}

// TestCmdDoOpencodeAutoPreparesWithAutoFlag pins the --auto path: prepare
// runs before the detached supervisor with Auto=true in the metadata.
func TestCmdDoOpencodeAutoPreparesWithAutoFlag(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "oc-auto")
	stubOpencodePS(t)
	calls := stubOpencodeAPI(t, false)
	stubITerm(t)
	stubAutoLauncher(t, 4242)

	if rc := cmdDo([]string{"oc-auto", "--harness", "opencode", "--auto"}); rc != 0 {
		t.Fatalf("cmdDo --auto rc=%d, want 0", rc)
	}
	payload := lastOpencodeCreatePayload(t, calls)
	if payload == nil {
		t.Fatalf("no session.create on --auto; calls=%v", *calls)
	}
	meta, _ := payload["metadata"].(map[string]any)
	flow, _ := meta["flow"].(map[string]any)
	if flow["auto"] != true {
		t.Errorf("metadata.flow.auto = %v, want true", flow["auto"])
	}
	if flow["taskSlug"] != "oc-auto" {
		t.Errorf("metadata.flow.taskSlug = %v, want oc-auto", flow["taskSlug"])
	}
}
