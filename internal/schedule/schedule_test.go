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
	if strings.Contains(got, "<true/>") {
		t.Errorf("a calendar job must not RunAtLoad (it would fire on every login)")
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
	line := rep.Writes[0].Content
	wantContains(t, "line", line,
		"* * * * * ", "PATH='/opt/bin:/usr/bin:/bin' ", "FLOW_ROOT='/home/u/.flow' ", "'/opt/bin/flow' owner tick-due",
		">>'/home/u/.flow/schedules/owner-scheduler.log' 2>>'/home/u/.flow/schedules/owner-scheduler.err.log'",
		"# flow-schedule:owner-scheduler")
	if strings.Contains(line, "HOME=") {
		t.Errorf("cron sets HOME itself; do not override it: %s", line)
	}
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
	if _, err := b.Install(playbookSpec("/l"), Options{}); err != nil {
		t.Fatalf("install on empty crontab: %v", err)
	}
	tab := fr.stdins[len(fr.stdins)-1]
	wantContains(t, "crontab", tab, "30 9 * * * ", "'/opt/bin/flow' run playbook daily-triage --auto", "# flow-schedule:daily-triage")
	// A real `crontab -l` failure (not "no crontab") is an error.
	fr.fail["crontab -l"] = "crontab: permission denied"
	if _, err := b.Install(playbookSpec("/l"), Options{}); err == nil {
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
	if len(rep.Removed) != 1 {
		t.Errorf("removed = %v", rep.Removed)
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
