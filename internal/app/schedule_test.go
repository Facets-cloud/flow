package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/schedule"
)

// `flow schedule` installs the OS timer for the owner scheduler or a
// playbook cadence. Tests pin HOME/XDG_CONFIG_HOME to temp dirs and mock
// schedule.Runner, so no real launchctl/systemctl/crontab is ever touched.

type schedRunner struct {
	calls  [][]string
	stdins []string
	tab    string // simulated crontab
}

func fakeScheduleRunner(t *testing.T) *schedRunner {
	t.Helper()
	fr := &schedRunner{}
	orig := schedule.Runner
	t.Cleanup(func() { schedule.Runner = orig })
	schedule.Runner = func(stdin string, name string, args ...string) (string, error) {
		argv := append([]string{name}, args...)
		fr.calls = append(fr.calls, argv)
		fr.stdins = append(fr.stdins, stdin)
		key := strings.Join(argv, " ")
		switch {
		case key == "crontab -l":
			if fr.tab == "" {
				return "no crontab for test", &schedule.RunError{Argv: argv, Output: "no crontab for test"}
			}
			return fr.tab, nil
		case key == "crontab -":
			fr.tab = stdin
		case key == "crontab -r":
			fr.tab = ""
		}
		return "", nil
	}
	return fr
}

func (fr *schedRunner) ran(prefix string) bool {
	for _, c := range fr.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

// setupScheduleEnv: a flow root, a fake HOME (launchd agents dir), a fake
// XDG_CONFIG_HOME (systemd units), a pinned flow binary path.
func setupScheduleEnv(t *testing.T) (root, home string) {
	t.Helper()
	root = setupFlowRoot(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", "/opt/test/bin:/usr/bin:/bin")
	orig := flowBinPath
	t.Cleanup(func() { flowBinPath = orig })
	flowBinPath = func() string { return "/opt/test/bin/flow" }
	return root, home
}

func scheduleRC(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var rc int
	out := captureStdout(t, func() { rc = cmdSchedule(args) })
	return rc, out
}

func insertPlaybook(t *testing.T, root, slug string) {
	t.Helper()
	db, err := flowdb.OpenDB(filepath.Join(root, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := flowdb.NowISO()
	if _, err := db.Exec(`INSERT INTO playbooks (slug, name, work_dir, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		slug, slug, "/tmp/wd", now, now); err != nil {
		t.Fatalf("insert playbook: %v", err)
	}
}

func TestScheduleInstallOwnersLaunchd(t *testing.T) {
	root, home := setupScheduleEnv(t)
	fr := fakeScheduleRunner(t)
	rc, out := scheduleRC(t, "install", "owners", "--backend", "launchd")
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	p := filepath.Join(home, "Library", "LaunchAgents", "cloud.facets.flow.owner-scheduler.plist")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	for _, want := range []string{
		schedule.Marker, "<string>/opt/test/bin/flow</string>", "<string>owner</string>", "<string>tick-due</string>",
		"<integer>60</integer>", "<string>/opt/test/bin:/usr/bin:/bin</string>", "<key>HOME</key>", "<string>" + home + "</string>",
		"<key>FLOW_ROOT</key>", "<string>" + root + "</string>",
		filepath.Join(root, "schedules", "owner-scheduler.log"),
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q:\n%s", want, b)
		}
	}
	if !fr.ran("launchctl load -w " + p) {
		t.Errorf("launchctl calls = %v", fr.calls)
	}
	// It prints exactly what it wrote and ran, plus where logs go.
	for _, want := range []string{"backend: launchd", "wrote " + p, "<key>Label</key>", "ran: launchctl load -w " + p, "logs: " + filepath.Join(root, "schedules", "owner-scheduler.log")} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "schedules")); err != nil {
		t.Errorf("log dir not created: %v", err)
	}
}

func TestScheduleInstallOwnersEveryFlag(t *testing.T) {
	_, home := setupScheduleEnv(t)
	fakeScheduleRunner(t)
	if rc, out := scheduleRC(t, "install", "owners", "--every", "5m", "--backend", "launchd"); rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	b, _ := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "cloud.facets.flow.owner-scheduler.plist"))
	if !strings.Contains(string(b), "<integer>300</integer>") {
		t.Errorf("--every 5m not honored:\n%s", b)
	}
	// Flags may come before the target too.
	if rc, out := scheduleRC(t, "install", "--every", "2m", "owners", "--backend", "launchd"); rc != 0 {
		t.Fatalf("flags-first rc=%d out=%s", rc, out)
	}
	b, _ = os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "cloud.facets.flow.owner-scheduler.plist"))
	if !strings.Contains(string(b), "<integer>120</integer>") {
		t.Errorf("flags-first --every 2m not honored:\n%s", b)
	}
	if rc, out := scheduleRC(t, "install", "owners", "--every", "bogus", "--backend", "launchd"); rc != 2 {
		t.Errorf("bad --every rc=%d out=%s", rc, out)
	}
	if rc, out := scheduleRC(t, "install", "owners", "--at", "09:00", "--backend", "launchd"); rc != 2 || !strings.Contains(out, "--every") {
		t.Errorf("owners takes no --at: rc=%d out=%s", rc, out)
	}
}

func TestScheduleInstallPlaybook(t *testing.T) {
	root, home := setupScheduleEnv(t)
	fr := fakeScheduleRunner(t)
	// Unknown playbook → refused before anything is written.
	rc, out := scheduleRC(t, "install", "playbook", "nope", "--at", "09:00", "--backend", "launchd")
	if rc != 1 || !strings.Contains(out, "no playbook") {
		t.Errorf("unknown playbook rc=%d out=%s", rc, out)
	}
	if len(fr.calls) != 0 {
		t.Errorf("nothing may run for an unknown playbook: %v", fr.calls)
	}
	insertPlaybook(t, root, "daily-triage")
	// Needs a cadence.
	if rc, out := scheduleRC(t, "install", "playbook", "daily-triage", "--backend", "launchd"); rc != 2 || !strings.Contains(out, "--at") {
		t.Errorf("missing cadence rc=%d out=%s", rc, out)
	}
	if rc, out := scheduleRC(t, "install", "playbook", "daily-triage", "--at", "9am", "--backend", "launchd"); rc != 2 {
		t.Errorf("bad --at rc=%d out=%s", rc, out)
	}
	if rc, out := scheduleRC(t, "install", "playbook", "daily-triage", "--at", "09:00", "--every", "1h", "--backend", "launchd"); rc != 2 {
		t.Errorf("both cadences rc=%d out=%s", rc, out)
	}
	rc, out = scheduleRC(t, "install", "playbook", "daily-triage", "--at", "09:00", "--backend", "launchd")
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	p := filepath.Join(home, "Library", "LaunchAgents", "cloud.facets.flow.daily-triage.plist")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>run</string>", "<string>playbook</string>", "<string>daily-triage</string>", "<string>--auto</string>",
		"<key>Hour</key>", "<integer>9</integer>", "<key>Minute</key>", "<integer>0</integer>"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "dangerously-skip-permissions") {
		t.Errorf("scheduled playbooks run --auto (headless), not a tab:\n%s", b)
	}
	// Interval cadence works for playbooks too.
	if rc, out := scheduleRC(t, "install", "playbook", "daily-triage", "--every", "6h", "--backend", "launchd"); rc != 0 {
		t.Fatalf("every rc=%d out=%s", rc, out)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "<integer>21600</integer>") {
		t.Errorf("--every 6h not honored:\n%s", b)
	}
}

func TestScheduleRefusesHandWrittenAndForces(t *testing.T) {
	_, home := setupScheduleEnv(t)
	fr := fakeScheduleRunner(t)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "cloud.facets.flow.owner-scheduler.plist")
	hand := "<plist><dict><key>Label</key><string>mine</string></dict></plist>\n"
	if err := os.WriteFile(p, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, out := scheduleRC(t, "install", "owners", "--backend", "launchd")
	if rc != 1 || !strings.Contains(out, "--force") {
		t.Errorf("rc=%d out=%s", rc, out)
	}
	if b, _ := os.ReadFile(p); string(b) != hand {
		t.Errorf("hand-written plist changed")
	}
	if len(fr.calls) != 0 {
		t.Errorf("nothing may run: %v", fr.calls)
	}
	if rc, out := scheduleRC(t, "install", "owners", "--backend", "launchd", "--force"); rc != 0 {
		t.Fatalf("force rc=%d out=%s", rc, out)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), schedule.Marker) {
		t.Errorf("force did not replace the plist")
	}
}

func TestScheduleDryRun(t *testing.T) {
	_, home := setupScheduleEnv(t)
	fr := fakeScheduleRunner(t)
	rc, out := scheduleRC(t, "install", "owners", "--backend", "systemd", "--dry-run")
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	if len(fr.calls) != 0 {
		t.Errorf("dry run ran: %v", fr.calls)
	}
	if ents, _ := os.ReadDir(filepath.Join(home, ".config", "systemd", "user")); len(ents) != 0 {
		t.Errorf("dry run wrote units: %v", ents)
	}
	for _, want := range []string{"dry run", "would write", "flow-owner-scheduler.timer", "OnUnitActiveSec=1m0s", "would run: systemctl --user enable --now flow-owner-scheduler.timer"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestScheduleSystemdAndCronBackends(t *testing.T) {
	root, home := setupScheduleEnv(t)
	fr := fakeScheduleRunner(t)
	insertPlaybook(t, root, "nightly")
	if rc, out := scheduleRC(t, "install", "playbook", "nightly", "--at", "23:15", "--backend", "systemd"); rc != 0 {
		t.Fatalf("systemd rc=%d out=%s", rc, out)
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	tmr, err := os.ReadFile(filepath.Join(unitDir, "flow-nightly.timer"))
	if err != nil || !strings.Contains(string(tmr), "OnCalendar=*-*-* 23:15:00") {
		t.Errorf("timer = %s, %v", tmr, err)
	}
	svc, _ := os.ReadFile(filepath.Join(unitDir, "flow-nightly.service"))
	if !strings.Contains(string(svc), "ExecStart=/opt/test/bin/flow run playbook nightly --auto") {
		t.Errorf("service = %s", svc)
	}
	if !fr.ran("systemctl --user enable --now flow-nightly.timer") {
		t.Errorf("systemctl calls = %v", fr.calls)
	}

	if rc, out := scheduleRC(t, "install", "owners", "--backend", "cron"); rc != 0 {
		t.Fatalf("cron rc=%d out=%s", rc, out)
	}
	for _, want := range []string{"* * * * * ", "PATH='/opt/test/bin:/usr/bin:/bin'", "FLOW_ROOT='" + root + "'", "'/opt/test/bin/flow' owner tick-due", "# flow-schedule:owner-scheduler"} {
		if !strings.Contains(fr.tab, want) {
			t.Errorf("crontab lacks %q: %q", want, fr.tab)
		}
	}
	// cron cannot do 90s.
	if rc, out := scheduleRC(t, "install", "owners", "--every", "90s", "--backend", "cron"); rc != 1 || !strings.Contains(out, "whole minutes") {
		t.Errorf("cron 90s rc=%d out=%s", rc, out)
	}

	rc, out := scheduleRC(t, "list", "--backend", "systemd")
	if rc != 0 || !strings.Contains(out, "nightly") || !strings.Contains(out, "daily 23:15") || !strings.Contains(out, "flow run playbook nightly --auto") {
		t.Errorf("systemd list rc=%d out=%s", rc, out)
	}
	rc, out = scheduleRC(t, "list", "--backend", "cron")
	if rc != 0 || !strings.Contains(out, "owner-scheduler") || !strings.Contains(out, "every 1m0s") {
		t.Errorf("cron list rc=%d out=%s", rc, out)
	}

	if rc, out := scheduleRC(t, "remove", "playbook", "nightly", "--backend", "systemd"); rc != 0 {
		t.Fatalf("remove rc=%d out=%s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(unitDir, "flow-nightly.timer")); !os.IsNotExist(err) {
		t.Errorf("timer still present")
	}
	if rc, out := scheduleRC(t, "remove", "owners", "--backend", "cron"); rc != 0 || strings.Contains(fr.tab, "flow-schedule:") {
		t.Errorf("cron remove rc=%d out=%s tab=%q", rc, out, fr.tab)
	}
	if rc, out := scheduleRC(t, "remove", "owners", "--backend", "cron"); rc != 1 {
		t.Errorf("second remove rc=%d out=%s", rc, out)
	}
}

func TestScheduleListEmptyAndUsage(t *testing.T) {
	setupScheduleEnv(t)
	fakeScheduleRunner(t)
	rc, out := scheduleRC(t, "list", "--backend", "launchd")
	if rc != 0 || !strings.Contains(out, "no schedules") {
		t.Errorf("empty list rc=%d out=%s", rc, out)
	}
	for _, args := range [][]string{nil, {"bogus"}, {"install"}, {"install", "bogus"}, {"install", "playbook"}, {"remove"}, {"remove", "playbook"}} {
		if rc, _ := scheduleRC(t, args...); rc != 2 {
			t.Errorf("%v rc=%d, want usage error 2", args, rc)
		}
	}
	if rc, out := scheduleRC(t, "install", "owners", "--backend", "bogus"); rc != 1 || !strings.Contains(out, "unknown backend") {
		t.Errorf("bad backend rc=%d out=%s", rc, out)
	}
	if rc, out := scheduleRC(t, "--help"); rc != 0 || !strings.Contains(out, "flow schedule install owners") {
		t.Errorf("help rc=%d out=%s", rc, out)
	}
}

func TestUsageAndDocsMentionSchedule(t *testing.T) {
	out := captureStdout(t, func() { printUsage() })
	for _, want := range []string{"flow schedule install owners", "flow schedule install playbook", "flow schedule list", "flow schedule remove"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	owners := string(mustReadSkill(t, "references/owners.md"))
	for _, want := range []string{"flow schedule install owners", "systemd", "cron", "flow schedule list"} {
		if !strings.Contains(owners, want) {
			t.Errorf("owners.md lacks %q", want)
		}
	}
	if strings.Contains(owners, "write\n`~/Library/LaunchAgents/cloud.facets.flow.owner-scheduler.plist`") {
		t.Errorf("owners.md still tells the skill to hand-write the plist")
	}
	pb := string(mustReadSkill(t, "references/playbooks.md"))
	if !strings.Contains(pb, "flow schedule install playbook") {
		t.Errorf("playbooks.md lacks the schedule recipe")
	}
	conn := string(mustReadSkill(t, "references/connectors.md"))
	for _, want := range []string{"flow connector add linux", "notify-send", "zenity", "kdialog", "no display"} {
		if !strings.Contains(conn, want) {
			t.Errorf("connectors.md lacks %q", want)
		}
	}
}

func mustReadSkill(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("skill", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}
