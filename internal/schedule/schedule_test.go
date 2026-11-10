package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner records every command and answers from a script keyed by the
// joined argv prefix. It never touches launchctl/systemctl/crontab.
type fakeRunner struct {
	calls   [][]string
	stdins  []string
	answers map[string]string // argv prefix → stdout
	fail    map[string]string // argv prefix → error text
}

func (f *fakeRunner) install(t *testing.T) {
	t.Helper()
	orig := Runner
	t.Cleanup(func() { Runner = orig })
	Runner = func(stdin string, name string, args ...string) (string, error) {
		argv := append([]string{name}, args...)
		f.calls = append(f.calls, argv)
		f.stdins = append(f.stdins, stdin)
		key := strings.Join(argv, " ")
		for p, msg := range f.fail {
			if strings.HasPrefix(key, p) {
				return msg, &RunError{Argv: argv, Output: msg}
			}
		}
		for p, out := range f.answers {
			if strings.HasPrefix(key, p) {
				return out, nil
			}
		}
		return "", nil
	}
}

func (f *fakeRunner) ran(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func ownersSpec(logDir string) Spec {
	return Spec{
		Name:    "owner-scheduler",
		Args:    []string{"owner", "tick-due"},
		Every:   60 * time.Second,
		FlowBin: "/opt/bin/flow",
		Env:     map[string]string{"PATH": "/opt/bin:/usr/bin:/bin", "HOME": "/home/u", "FLOW_ROOT": "/home/u/.flow"},
		LogDir:  logDir,
	}
}

func playbookSpec(logDir string) Spec {
	return Spec{
		Name:    "daily-triage",
		Args:    []string{"run", "playbook", "daily-triage", "--auto"},
		At:      &Clock{Hour: 9, Minute: 30},
		FlowBin: "/opt/bin/flow",
		Env:     map[string]string{"PATH": "/opt/bin:/usr/bin:/bin"},
		LogDir:  logDir,
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func wantContains(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("%s lacks %q:\n%s", what, sub, s)
		}
	}
}

// ---------- launchd ----------

func TestLaunchdInstallWritesPlistAndLoads(t *testing.T) {
	fr := &fakeRunner{}
	fr.install(t)
	dir := t.TempDir()
	b := &Launchd{AgentsDir: dir}
	rep, err := b.Install(ownersSpec("/home/u/.flow/schedules"), Options{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	p := filepath.Join(dir, "cloud.facets.flow.owner-scheduler.plist")
	got := mustRead(t, p)
	wantContains(t, "plist", got,
		Marker,
		"<string>cloud.facets.flow.owner-scheduler</string>",
		"<string>/opt/bin/flow</string>",
		"<string>owner</string>", "<string>tick-due</string>",
		"<key>StartInterval</key>", "<integer>60</integer>",
		"<key>RunAtLoad</key>", "<true/>",
		"<key>PATH</key>", "<string>/opt/bin:/usr/bin:/bin</string>",
		"<key>FLOW_ROOT</key>", "<string>/home/u/.flow</string>",
		"/home/u/.flow/schedules/owner-scheduler.log", "/home/u/.flow/schedules/owner-scheduler.err.log",
	)
	if !fr.ran("launchctl load -w " + p) {
		t.Errorf("launchctl load -w not run: %v", fr.calls)
	}
	if fr.ran("launchctl unload") {
		t.Errorf("fresh install must not unload: %v", fr.calls)
	}
	if len(rep.Writes) != 1 || rep.Writes[0].Path != p || rep.Writes[0].Content != got {
		t.Errorf("report writes = %+v", rep.Writes)
	}
	if len(rep.Ran) != 1 {
		t.Errorf("report ran = %v", rep.Ran)
	}
}

func TestLaunchdCalendarSpecAndXMLEscaping(t *testing.T) {
	fr := &fakeRunner{}
	fr.install(t)
	dir := t.TempDir()
	s := playbookSpec("/logs")
	s.Env["PATH"] = "/a&b:/c<d>"
	_, err := (&Launchd{AgentsDir: dir}).Install(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, filepath.Join(dir, "cloud.facets.flow.daily-triage.plist"))
	wantContains(t, "plist", got,
		"<key>StartCalendarInterval</key>", "<key>Hour</key>", "<integer>9</integer>", "<key>Minute</key>", "<integer>30</integer>",
		"<string>--auto</string>", "/a&amp;b:/c&lt;d&gt;",
	)
	if strings.Contains(got, "StartInterval") {
		t.Errorf("calendar spec must not set StartInterval")
	}
	if !strings.Contains(got, "<key>RunAtLoad</key>\n\t<false/>") {
		t.Errorf("a calendar job must not RunAtLoad (it would fire on every login):\n%s", got)
	}
}

func TestLaunchdRefusesHandWrittenWithoutForce(t *testing.T) {
	fr := &fakeRunner{}
	fr.install(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "cloud.facets.flow.owner-scheduler.plist")
	hand := "<?xml version=\"1.0\"?><plist><dict><key>Label</key><string>x</string></dict></plist>\n"
	if err := os.WriteFile(p, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &Launchd{AgentsDir: dir}
	_, err := b.Install(ownersSpec("/l"), Options{})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected a --force refusal, got %v", err)
	}
	if got := mustRead(t, p); got != hand {
		t.Errorf("hand-written plist was modified")
	}
	if len(fr.calls) != 0 {
		t.Errorf("nothing may run on refusal: %v", fr.calls)
	}

	// --force replaces it: unload first, then write and load.
	if _, err := b.Install(ownersSpec("/l"), Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !fr.ran("launchctl unload "+p) || !fr.ran("launchctl load -w "+p) {
		t.Errorf("force must unload then load: %v", fr.calls)
	}
	// A flow-written file is replaced without --force (re-install).
	fr.calls = nil
	if _, err := b.Install(ownersSpec("/l"), Options{}); err != nil {
		t.Fatalf("re-install of a flow-written plist: %v", err)
	}
	if !fr.ran("launchctl unload " + p) {
		t.Errorf("re-install must unload the old job: %v", fr.calls)
	}
}

func TestLaunchdDryRunTouchesNothing(t *testing.T) {
	fr := &fakeRunner{}
	fr.install(t)
	dir := t.TempDir()
	rep, err := (&Launchd{AgentsDir: dir}).Install(ownersSpec("/l"), Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("dry run wrote files: %v", ents)
	}
	if len(fr.calls) != 0 {
		t.Errorf("dry run ran commands: %v", fr.calls)
	}
	if len(rep.Writes) != 1 || len(rep.Ran) != 1 {
		t.Errorf("dry-run report must still describe the plan: %+v", rep)
	}
}

func TestLaunchdRemoveAndList(t *testing.T) {
	fr := &fakeRunner{answers: map[string]string{}, fail: map[string]string{}}
	fr.install(t)
	dir := t.TempDir()
	b := &Launchd{AgentsDir: dir}
	if _, err := b.Install(ownersSpec("/l"), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Install(playbookSpec("/l"), Options{}); err != nil {
		t.Fatal(err)
	}
	// A hand-written sibling with the same prefix shows up as such.
	hand := filepath.Join(dir, "cloud.facets.flow.x-account.plist")
	if err := os.WriteFile(hand, []byte(`<plist><dict><key>Label</key><string>cloud.facets.flow.x-account</string>
<key>ProgramArguments</key><array><string>/u/flow</string><string>run</string><string>playbook</string><string>x-account</string></array>
<key>StartCalendarInterval</key><dict><key>Hour</key><integer>11</integer><key>Minute</key><integer>30</integer></dict></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unrelated plists are ignored.
	if err := os.WriteFile(filepath.Join(dir, "com.other.thing.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.fail["launchctl list cloud.facets.flow.x-account"] = "Could not find service"

	ents, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 3 {
		t.Fatalf("entries = %+v", ents)
	}
	byName := map[string]Entry{}
	for _, e := range ents {
		byName[e.Name] = e
	}
	o := byName["owner-scheduler"]
	if o.Source != "flow" || o.Status != "loaded" || o.When != "every 1m0s" || o.Command != "/opt/bin/flow owner tick-due" {
		t.Errorf("owner entry = %+v", o)
	}
	x := byName["x-account"]
	if x.Source != "hand-written" || x.Status != "not loaded" || x.When != "daily 11:30" || x.Command != "/u/flow run playbook x-account" {
		t.Errorf("hand-written entry = %+v", x)
	}
	if byName["daily-triage"].When != "daily 09:30" {
		t.Errorf("playbook entry = %+v", byName["daily-triage"])
	}

	fr.calls = nil
	rep, err := b.Remove("owner-scheduler", Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "cloud.facets.flow.owner-scheduler.plist")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("plist still there")
	}
	if !fr.ran("launchctl unload "+p) || len(rep.Removed) != 1 {
		t.Errorf("remove must unload: %v %+v", fr.calls, rep)
	}
	if _, err := b.Remove("owner-scheduler", Options{}); err == nil {
		t.Errorf("removing a missing schedule must error")
	}
	// A hand-written plist is never removed without --force either.
	if _, err := b.Remove("x-account", Options{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("hand-written remove: %v", err)
	}
	if _, err := os.Stat(hand); err != nil {
		t.Errorf("hand-written plist was deleted")
	}
}

// ---------- systemd --user ----------

func TestSystemdInstallWritesUnitsAndEnables(t *testing.T) {
	fr := &fakeRunner{}
	fr.install(t)
	dir := filepath.Join(t.TempDir(), "systemd", "user") // created on demand
	b := &Systemd{UnitDir: dir}
	rep, err := b.Install(ownersSpec("/home/u/.flow/schedules"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc := mustRead(t, filepath.Join(dir, "flow-owner-scheduler.service"))
	tmr := mustRead(t, filepath.Join(dir, "flow-owner-scheduler.timer"))
	wantContains(t, "service", svc,
		"# "+Marker, "[Service]", "Type=oneshot",
		"ExecStart=/opt/bin/flow owner tick-due",
		`Environment="PATH=/opt/bin:/usr/bin:/bin"`, `Environment="FLOW_ROOT=/home/u/.flow"`,
		"StandardOutput=append:/home/u/.flow/schedules/owner-scheduler.log",
		"StandardError=append:/home/u/.flow/schedules/owner-scheduler.err.log",
	)
	wantContains(t, "timer", tmr,
		"# "+Marker, "[Timer]", "OnBootSec=1m", "OnUnitActiveSec=1m", "AccuracySec=5s",
		"Unit=flow-owner-scheduler.service", "[Install]", "WantedBy=timers.target",
	)
	if strings.Contains(tmr, "OnCalendar") || strings.Contains(tmr, "Persistent") {
		t.Errorf("interval timer must not carry calendar keys:\n%s", tmr)
	}
	if !fr.ran("systemctl --user daemon-reload") || !fr.ran("systemctl --user enable --now flow-owner-scheduler.timer") {
		t.Errorf("systemctl calls = %v", fr.calls)
	}
	if len(rep.Writes) != 2 {
		t.Errorf("writes = %+v", rep.Writes)
	}
}

func TestSystemdCalendarTimerAndQuoting(t *testing.T) {
	fr := &fakeRunner{}
	fr.install(t)
	dir := t.TempDir()
	s := playbookSpec("/l")
	s.Args = []string{"run", "playbook", "daily-triage", "--auto", "--with", `say "hi" there`}
	if _, err := (&Systemd{UnitDir: dir}).Install(s, Options{}); err != nil {
		t.Fatal(err)
	}
	tmr := mustRead(t, filepath.Join(dir, "flow-daily-triage.timer"))
	wantContains(t, "timer", tmr, "OnCalendar=*-*-* 09:30:00", "Persistent=true")
	if strings.Contains(tmr, "OnUnitActiveSec") {
		t.Errorf("calendar timer must not carry OnUnitActiveSec")
	}
	svc := mustRead(t, filepath.Join(dir, "flow-daily-triage.service"))
	wantContains(t, "service", svc, `ExecStart=/opt/bin/flow run playbook daily-triage --auto --with "say \"hi\" there"`)
}

func TestSystemdRemoveListAndGuard(t *testing.T) {
	fr := &fakeRunner{answers: map[string]string{
		"systemctl --user is-active flow-owner-scheduler.timer": "active\n",
		"systemctl --user is-active flow-daily-triage.timer":    "inactive\n",
	}, fail: map[string]string{}}
	fr.install(t)
	dir := t.TempDir()
	b := &Systemd{UnitDir: dir}
	for _, s := range []Spec{ownersSpec("/l"), playbookSpec("/l")} {
		if _, err := b.Install(s, Options{}); err != nil {
			t.Fatal(err)
		}
	}
	// Hand-written timer with the flow- prefix: listed, protected.
	hand := filepath.Join(dir, "flow-mine.timer")
	if err := os.WriteFile(hand, []byte("[Timer]\nOnCalendar=daily\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ents, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entry{}
	for _, e := range ents {
		byName[e.Name] = e
	}
	if len(ents) != 3 {
		t.Fatalf("entries = %+v", ents)
	}
	if e := byName["owner-scheduler"]; e.Status != "active" || e.When != "every 1m0s" || e.Command != "/opt/bin/flow owner tick-due" || e.Source != "flow" {
		t.Errorf("owner entry = %+v", e)
	}
	if e := byName["daily-triage"]; e.Status != "inactive" || e.When != "daily 09:30" {
		t.Errorf("playbook entry = %+v", e)
	}
	if e := byName["mine"]; e.Source != "hand-written" {
		t.Errorf("hand-written entry = %+v", e)
	}
	if _, err := b.Install(Spec{Name: "mine", Args: []string{"owner", "tick-due"}, Every: time.Minute, FlowBin: "/f", LogDir: "/l"}, Options{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("hand-written guard: %v", err)
	}

	fr.calls = nil
	rep, err := b.Remove("owner-scheduler", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"flow-owner-scheduler.service", "flow-owner-scheduler.timer"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s still there", f)
		}
	}
	if !fr.ran("systemctl --user disable --now flow-owner-scheduler.timer") || !fr.ran("systemctl --user daemon-reload") {
		t.Errorf("remove calls = %v", fr.calls)
	}
	if len(rep.Removed) != 2 {
		t.Errorf("removed = %v", rep.Removed)
	}
}

// ---------- cron ----------

func TestCronInstallReplacesOnlyItsOwnLine(t *testing.T) {
	existing := "MAILTO=me\n0 7 * * * /usr/bin/backup # mine\n*/1 * * * * /old/flow owner tick-due # flow-schedule:owner-scheduler\n"
	fr := &fakeRunner{answers: map[string]string{"crontab -l": existing}}
	fr.install(t)
	b := &Cron{}
	s := ownersSpec("/home/u/.flow/schedules")
	s.LogDir = filepath.Join(t.TempDir(), "schedules")
	rep, err := b.Install(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !fr.ran("crontab -") || len(fr.stdins) < 2 {
		t.Fatalf("crontab - not fed: %v", fr.calls)
	}
	newTab := fr.stdins[len(fr.stdins)-1]
	wantContains(t, "crontab", newTab, "MAILTO=me\n", "0 7 * * * /usr/bin/backup # mine\n")
	if strings.Contains(newTab, "/old/flow") {
		t.Errorf("old flow line not replaced:\n%s", newTab)
	}
	if n := strings.Count(newTab, "# flow-schedule:owner-scheduler"); n != 1 {
		t.Errorf("tagged lines = %d:\n%s", n, newTab)
	}
	line := rep.Writes[1].Content
	wantContains(t, "line", line, "* * * * * /bin/sh '"+s.LogDir+"/owner-scheduler.sh' # flow-schedule:owner-scheduler")
	wrapper := rep.Writes[0]
	if wrapper.Path != s.LogDir+"/owner-scheduler.sh" {
		t.Errorf("wrapper path = %q", wrapper.Path)
	}
	wantContains(t, "wrapper", wrapper.Content,
		"export PATH='/opt/bin:/usr/bin:/bin'\n", "export HOME='/home/u'\n", "export FLOW_ROOT='/home/u/.flow'\n",
		"exec '/opt/bin/flow' owner tick-due >>'"+s.LogDir+"/owner-scheduler.log' 2>>'"+s.LogDir+"/owner-scheduler.err.log'\n")
	if !strings.HasSuffix(newTab, "\n") {
		t.Errorf("crontab must end with a newline")
	}
}

func TestCronExpressions(t *testing.T) {
	cases := []struct {
		every time.Duration
		at    *Clock
		want  string
		err   bool
	}{
		{every: time.Minute, want: "* * * * *"},
		{every: 5 * time.Minute, want: "*/5 * * * *"},
		{every: 2 * time.Hour, want: "0 */2 * * *"},
		{every: 24 * time.Hour, want: "0 0 * * *"},
		{at: &Clock{Hour: 9, Minute: 5}, want: "5 9 * * *"},
		{every: 90 * time.Second, err: true},
		{every: 90 * time.Minute, err: true},
		{every: 36 * time.Hour, err: true},
	}
	for _, c := range cases {
		got, err := cronExpr(Spec{Every: c.every, At: c.at})
		if c.err {
			if err == nil {
				t.Errorf("%v/%v: expected error, got %q", c.every, c.at, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%v/%v = %q, %v; want %q", c.every, c.at, got, err, c.want)
		}
	}
}

func TestCronNoCrontabYetListAndRemove(t *testing.T) {
	fr := &fakeRunner{answers: map[string]string{}, fail: map[string]string{"crontab -l": "no crontab for u"}}
	fr.install(t)
	b := &Cron{}
	logDir := t.TempDir()
	if _, err := b.Install(playbookSpec(logDir), Options{}); err != nil {
		t.Fatalf("install on empty crontab: %v", err)
	}
	tab := fr.stdins[len(fr.stdins)-1]
	wantContains(t, "crontab", tab, "30 9 * * * /bin/sh '"+logDir+"/daily-triage.sh' # flow-schedule:daily-triage")
	wantContains(t, "wrapper", mustRead(t, filepath.Join(logDir, "daily-triage.sh")), "exec '/opt/bin/flow' run playbook daily-triage --auto >>")
	// A real `crontab -l` failure (not "no crontab") is an error.
	fr.fail["crontab -l"] = "crontab: permission denied"
	if _, err := b.Install(playbookSpec(logDir), Options{}); err == nil {
		t.Errorf("a failing crontab -l must not be treated as empty")
	}
	delete(fr.fail, "crontab -l")
	fr.answers["crontab -l"] = tab + "1 2 * * * /x # flow-schedule:other\n"
	ents, err := b.List()
	if err != nil || len(ents) != 2 {
		t.Fatalf("list = %+v, %v", ents, err)
	}
	if ents[0].Name != "daily-triage" || ents[0].When != "daily 09:30" || ents[0].Status != "installed" || ents[0].Source != "flow" || ents[0].Command != "/opt/bin/flow run playbook daily-triage --auto" {
		t.Errorf("entry = %+v", ents[0])
	}
	rep, err := b.Remove("daily-triage", Options{})
	if err != nil {
		t.Fatal(err)
	}
	after := fr.stdins[len(fr.stdins)-1]
	if strings.Contains(after, "flow-schedule:daily-triage") || !strings.Contains(after, "flow-schedule:other") {
		t.Errorf("remove rewrote wrong lines:\n%s", after)
	}
	// The line and its wrapper.
	if len(rep.Removed) != 2 || rep.Removed[1] != filepath.Join(logDir, "daily-triage.sh") {
		t.Errorf("removed = %v", rep.Removed)
	}
	if _, err := os.Stat(filepath.Join(logDir, "daily-triage.sh")); !os.IsNotExist(err) {
		t.Errorf("wrapper still there")
	}
	fr.answers["crontab -l"] = after
	if _, err := b.Remove("daily-triage", Options{}); err == nil {
		t.Errorf("removing a missing line must error")
	}
}

// ---------- picking a backend ----------

func TestPickBackend(t *testing.T) {
	orig := SystemdUserAvailable
	t.Cleanup(func() { SystemdUserAvailable = orig })
	SystemdUserAvailable = func() bool { return true }
	if b, err := Pick("linux", ""); err != nil || b.Name() != "systemd" {
		t.Errorf("linux+systemd = %v, %v", b, err)
	}
	SystemdUserAvailable = func() bool { return false }
	if b, err := Pick("linux", ""); err != nil || b.Name() != "cron" {
		t.Errorf("linux-systemd = %v, %v", b, err)
	}
	if b, err := Pick("darwin", ""); err != nil || b.Name() != "launchd" {
		t.Errorf("darwin = %v, %v", b, err)
	}
	if b, err := Pick("darwin", "cron"); err != nil || b.Name() != "cron" {
		t.Errorf("override = %v, %v", b, err)
	}
	if _, err := Pick("windows", ""); err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Errorf("windows must be a clear error: %v", err)
	}
	if _, err := Pick("linux", "bogus"); err == nil {
		t.Errorf("unknown backend must error")
	}
}

func TestSpecValidate(t *testing.T) {
	ok := ownersSpec("/l")
	if err := ok.Validate(); err != nil {
		t.Errorf("valid spec: %v", err)
	}
	bad := []Spec{
		{Name: "", Args: []string{"x"}, Every: time.Minute, FlowBin: "/f", LogDir: "/l"},
		{Name: "Bad Name", Args: []string{"x"}, Every: time.Minute, FlowBin: "/f", LogDir: "/l"},
		{Name: "a", Args: nil, Every: time.Minute, FlowBin: "/f", LogDir: "/l"},
		{Name: "a", Args: []string{"x"}, FlowBin: "/f", LogDir: "/l"},                                   // neither Every nor At
		{Name: "a", Args: []string{"x"}, Every: time.Minute, At: &Clock{}, FlowBin: "/f", LogDir: "/l"}, // both
		{Name: "a", Args: []string{"x"}, Every: 500 * time.Millisecond, FlowBin: "/f", LogDir: "/l"},    // < 1s
		{Name: "a", Args: []string{"x"}, At: &Clock{Hour: 24}, FlowBin: "/f", LogDir: "/l"},             // bad clock
		{Name: "a", Args: []string{"x"}, Every: time.Minute, FlowBin: "relative/flow", LogDir: "/l"},    // relative bin
		{Name: "a", Args: []string{"x"}, Every: time.Minute, FlowBin: "/f", LogDir: ""},                 // no log dir
	}
	for i, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("case %d: expected error for %+v", i, s)
		}
	}
}

func TestParseClock(t *testing.T) {
	if c, err := ParseClock("09:05"); err != nil || c.Hour != 9 || c.Minute != 5 {
		t.Errorf("09:05 = %+v, %v", c, err)
	}
	if c, err := ParseClock("23:59"); err != nil || c.Hour != 23 || c.Minute != 59 {
		t.Errorf("23:59 = %+v, %v", c, err)
	}
	for _, bad := range []string{"", "9", "24:00", "12:60", "ab:cd", "9:5pm", "09:05:00"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
}

// ---------- review fixes (#130): detached children, names, %, long PATH ----------

// The job (`flow owner tick-due`, `flow run playbook --auto`) forks a
// detached child (setsid) and exits. systemd's default KillMode=control-group
// then kills the child with the cgroup, so every fire "succeeds" and nothing
// runs. KillMode=process lets the child outlive the oneshot.
func TestSystemdServiceLetsDetachedChildrenLive(t *testing.T) {
	svc := renderService(ownersSpec("/l"))
	wantContains(t, "service", svc, "Type=oneshot\n", "KillMode=process\n")
	if strings.Index(svc, "[Service]") > strings.Index(svc, "KillMode=process") {
		t.Errorf("KillMode must sit in [Service]:\n%s", svc)
	}
}

// launchd kills what is left in the job's process group unless told not to.
// flow's children are in their own session (setsid) already; the key makes
// the intent explicit and survives a launcher that stops calling setsid.
func TestLaunchdPlistAbandonsProcessGroup(t *testing.T) {
	pl := renderPlist(ownersSpec("/l"))
	wantContains(t, "plist", pl, "<key>AbandonProcessGroup</key>\n\t<true/>\n")
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"owner-scheduler", "daily-triage", "a", "x.y_z-9"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "x/../victim", "../victim", "/abs", "Caps", "has space", "a\nb", strings.Repeat("a", 65), ".hidden", "-dash"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// `flow schedule remove playbook 'x/../victim' --force` must not resolve to
// another file: Remove validates the name before touching anything.
func TestRemoveRejectsPathTraversalOnEveryBackend(t *testing.T) {
	fr := &fakeRunner{answers: map[string]string{"crontab -l": "1 2 * * * /x # flow-schedule:x/../victim\n"}}
	fr.install(t)
	dir := t.TempDir()
	victims := []string{filepath.Join(dir, "victim.plist"), filepath.Join(dir, "victim.timer"), filepath.Join(dir, "victim.service")}
	for _, v := range victims {
		if err := os.WriteFile(v, []byte("hand-written\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []Backend{&Launchd{AgentsDir: dir}, &Systemd{UnitDir: dir}, &Cron{}} {
		for _, name := range []string{"x/../victim", "../victim", ""} {
			rep, err := b.Remove(name, Options{Force: true})
			if err == nil || !strings.Contains(err.Error(), "schedule name") {
				t.Errorf("%s Remove(%q) = %+v, %v; want a name error", b.Name(), name, rep, err)
			}
		}
	}
	for _, v := range victims {
		if _, err := os.Stat(v); err != nil {
			t.Errorf("%s was deleted", filepath.Base(v))
		}
	}
	if len(fr.calls) != 0 {
		t.Errorf("nothing may run for a bad name: %v", fr.calls)
	}
}

// `%` is a specifier in systemd unit files; a literal needs `%%`.
func TestSystemdEscapesPercent(t *testing.T) {
	fr := &fakeRunner{}
	fr.install(t)
	dir := t.TempDir()
	s := Spec{
		Name:    "daily-triage",
		Args:    []string{"run", "playbook", "daily-triage", "--auto", "--with", "100% sure"},
		At:      &Clock{Hour: 9, Minute: 30},
		FlowBin: "/opt/100%/flow",
		Env:     map[string]string{"PATH": "/opt/100%/bin:/usr/bin", "FLOW_ROOT": "/home/u/100%/.flow"},
		LogDir:  "/home/u/100%/.flow/schedules",
	}
	b := &Systemd{UnitDir: dir}
	if _, err := b.Install(s, Options{}); err != nil {
		t.Fatal(err)
	}
	svc := mustRead(t, filepath.Join(dir, "flow-daily-triage.service"))
	wantContains(t, "service", svc,
		`Environment="PATH=/opt/100%%/bin:/usr/bin"`,
		`Environment="FLOW_ROOT=/home/u/100%%/.flow"`,
		`ExecStart=/opt/100%%/flow run playbook daily-triage --auto --with "100%% sure"`,
		"StandardOutput=append:/home/u/100%%/.flow/schedules/daily-triage.log",
		"StandardError=append:/home/u/100%%/.flow/schedules/daily-triage.err.log",
	)
	if n := strings.Count(strings.ReplaceAll(svc, "%%", ""), "%"); n != 0 {
		t.Errorf("%d unescaped %% left:\n%s", n, svc)
	}
	ents, err := b.List()
	if err != nil || len(ents) != 1 {
		t.Fatalf("list = %+v, %v", ents, err)
	}
	if ents[0].Command != "/opt/100%/flow run playbook daily-triage --auto --with 100% sure" {
		t.Errorf("list must show the literal command: %q", ents[0].Command)
	}
}

// cron: the crontab line only points at a short wrapper script, so a long
// PATH never breaks Debian cron's line limit, and `%` (a newline to cron)
// is escaped on the line and never reaches the command through cron.
func TestCronLineIsShortAndPointsAtWrapper(t *testing.T) {
	fr := &fakeRunner{answers: map[string]string{}, fail: map[string]string{"crontab -l": "no crontab for u"}}
	fr.install(t)
	logDir := filepath.Join(t.TempDir(), "100%", "schedules")
	s := ownersSpec(logDir)
	s.Env["PATH"] = strings.Repeat("/some/very/long/dir:", 80) + "/opt/bin"
	s.FlowBin = "/opt/100%/flow"
	s.Args = []string{"owner", "tick-due", "--with", "100% sure"}
	rep, err := (&Cron{}).Install(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(logDir, "owner-scheduler.sh")
	if len(rep.Writes) != 2 || rep.Writes[0].Path != script || rep.Writes[1].Path != "crontab" {
		t.Fatalf("writes = %+v", rep.Writes)
	}
	line := rep.Writes[1].Content
	if len(line) > 300 {
		t.Errorf("crontab line is %d bytes; it must stay short regardless of PATH:\n%s", len(line), line)
	}
	wantContains(t, "line", line, "* * * * * /bin/sh '"+strings.ReplaceAll(script, "%", `\%`)+"' # flow-schedule:owner-scheduler")
	if strings.Contains(line, "PATH=") || strings.Contains(line, "tick-due") {
		t.Errorf("env and command belong in the wrapper, not the line:\n%s", line)
	}
	if n := strings.Count(strings.ReplaceAll(line, `\%`, ""), "%"); n != 0 {
		t.Errorf("%d unescaped %% on the crontab line:\n%s", n, line)
	}
	body := mustRead(t, script)
	wantContains(t, "wrapper", body,
		"#!/bin/sh\n", "# "+Marker,
		"export PATH='"+s.Env["PATH"]+"'\n", "export HOME='/home/u'\n", "export FLOW_ROOT='/home/u/.flow'\n",
		"exec '/opt/100%/flow' owner tick-due --with '100% sure' >>'"+filepath.Join(logDir, "owner-scheduler.log")+"' 2>>'"+filepath.Join(logDir, "owner-scheduler.err.log")+"'\n",
	)
	if strings.Contains(body, `\%`) {
		t.Errorf("the wrapper is plain sh; %% needs no escaping there:\n%s", body)
	}
	if fi, err := os.Stat(script); err != nil || fi.Mode()&0o100 == 0 {
		t.Errorf("wrapper must be executable: %v %v", fi, err)
	}
	tab := fr.stdins[len(fr.stdins)-1]
	if strings.Count(tab, "\n") != 1 || !strings.HasPrefix(tab, line) {
		t.Errorf("crontab = %q", tab)
	}

	// List shows the real command (read from the wrapper), not /bin/sh.
	delete(fr.fail, "crontab -l")
	fr.answers["crontab -l"] = tab
	ents, err := (&Cron{}).List()
	if err != nil || len(ents) != 1 {
		t.Fatalf("list = %+v, %v", ents, err)
	}
	if e := ents[0]; e.Command != "/opt/100%/flow owner tick-due --with 100% sure" || e.When != "every 1m0s" || e.Source != "flow" || e.Path != script {
		t.Errorf("entry = %+v", e)
	}

	// Remove drops the line and the wrapper.
	rep, err = (&Cron{}).Remove("owner-scheduler", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Errorf("wrapper still there")
	}
	if len(rep.Removed) != 2 {
		t.Errorf("removed = %v", rep.Removed)
	}
	if !fr.ran("crontab -r") {
		t.Errorf("empty crontab must be removed: %v", fr.calls)
	}
}

// A hand-written wrapper at the script path is protected like any file.
func TestCronGuardsHandWrittenWrapper(t *testing.T) {
	fr := &fakeRunner{fail: map[string]string{"crontab -l": "no crontab for u"}}
	fr.install(t)
	logDir := t.TempDir()
	script := filepath.Join(logDir, "owner-scheduler.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Cron{}).Install(ownersSpec(logDir), Options{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("guard: %v", err)
	}
	if got := mustRead(t, script); !strings.Contains(got, "echo mine") {
		t.Errorf("hand-written wrapper replaced")
	}
	if _, err := (&Cron{}).Install(ownersSpec(logDir), Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, script); !strings.Contains(got, Marker) {
		t.Errorf("--force must replace it")
	}
}
