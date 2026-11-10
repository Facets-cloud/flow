package schedule

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SystemdPrefix prefixes every flow unit name: flow-<name>.{service,timer}.
const SystemdPrefix = "flow-"

// Systemd writes a oneshot .service plus a .timer under the user unit dir
// and enables the timer with `systemctl --user enable --now` (Linux).
type Systemd struct {
	UnitDir string
}

func (s *Systemd) Name() string { return "systemd" }

func (s *Systemd) unit(name string) string { return SystemdPrefix + name }
func (s *Systemd) servicePath(name string) string {
	return filepath.Join(s.UnitDir, s.unit(name)+".service")
}
func (s *Systemd) timerPath(name string) string {
	return filepath.Join(s.UnitDir, s.unit(name)+".timer")
}

func (s *Systemd) Install(sp Spec, o Options) (*Report, error) {
	if err := sp.Validate(); err != nil {
		return nil, err
	}
	for _, p := range []string{s.timerPath(sp.Name), s.servicePath(sp.Name)} {
		if err := guard(p, o, "replace"); err != nil {
			return nil, err
		}
	}
	pl := newPlan(s.Name(), o)
	if err := pl.write(s.servicePath(sp.Name), renderService(sp)); err != nil {
		return pl.rep, err
	}
	if err := pl.write(s.timerPath(sp.Name), renderTimer(sp)); err != nil {
		return pl.rep, err
	}
	if _, err := pl.run("", false, "systemctl", "--user", "daemon-reload"); err != nil {
		return pl.rep, err
	}
	// enable --now (re)starts the timer; on a re-install it picks up the
	// rewritten cadence.
	if _, err := pl.run("", false, "systemctl", "--user", "enable", "--now", s.unit(sp.Name)+".timer"); err != nil {
		return pl.rep, err
	}
	return pl.rep, nil
}

func (s *Systemd) Remove(name string, o Options) (*Report, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	tp, svp := s.timerPath(name), s.servicePath(name)
	if _, err := os.Stat(tp); err != nil {
		return nil, fmt.Errorf("no systemd schedule %q (%s)", name, tp)
	}
	if err := guard(tp, o, "remove"); err != nil {
		return nil, err
	}
	pl := newPlan(s.Name(), o)
	if _, err := pl.run("", true, "systemctl", "--user", "disable", "--now", s.unit(name)+".timer"); err != nil {
		return pl.rep, err
	}
	for _, p := range []string{tp, svp} {
		if _, err := os.Stat(p); err == nil {
			if err := pl.remove(p); err != nil {
				return pl.rep, err
			}
		}
	}
	if _, err := pl.run("", true, "systemctl", "--user", "daemon-reload"); err != nil {
		return pl.rep, err
	}
	return pl.rep, nil
}

var (
	unitExecRe     = regexp.MustCompile(`(?m)^ExecStart=(.*)$`)
	unitActiveRe   = regexp.MustCompile(`(?m)^OnUnitActiveSec=(.*)$`)
	unitCalendarRe = regexp.MustCompile(`(?m)^OnCalendar=(.*)$`)
	calendarDayRe  = regexp.MustCompile(`^\*-\*-\* (\d\d):(\d\d):00$`)
)

func (s *Systemd) List() ([]Entry, error) {
	matches, err := filepath.Glob(filepath.Join(s.UnitDir, SystemdPrefix+"*.timer"))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, tp := range matches {
		unit := strings.TrimSuffix(filepath.Base(tp), ".timer")
		name := strings.TrimPrefix(unit, SystemdPrefix)
		traw, err := os.ReadFile(tp)
		if err != nil {
			continue
		}
		e := Entry{Name: name, Backend: s.Name(), Path: tp, Source: "hand-written", When: "?"}
		if strings.Contains(string(traw), Marker) {
			e.Source = "flow"
		}
		if m := unitActiveRe.FindStringSubmatch(string(traw)); m != nil {
			v := strings.TrimSpace(m[1])
			if d, err := time.ParseDuration(v); err == nil {
				e.When = "every " + d.String()
			} else {
				e.When = "every " + v
			}
		} else if m := unitCalendarRe.FindStringSubmatch(string(traw)); m != nil {
			cal := strings.TrimSpace(m[1])
			if cm := calendarDayRe.FindStringSubmatch(cal); cm != nil {
				e.When = "daily " + cm[1] + ":" + cm[2]
			} else {
				e.When = cal
			}
		}
		if sraw, err := os.ReadFile(s.servicePath(name)); err == nil {
			if m := unitExecRe.FindStringSubmatch(string(sraw)); m != nil {
				e.Command = unquoteExec(strings.TrimSpace(m[1]))
			}
		}
		st, _ := Runner("", "systemctl", "--user", "is-active", unit+".timer")
		e.Status = strings.TrimSpace(st)
		if e.Status == "" {
			e.Status = "unknown"
		}
		out = append(out, e)
	}
	return out, nil
}

// unitEsc escapes a literal for a unit-file value: '%' introduces a
// specifier (%h, %u …) in ExecStart=, Environment= and the Standard*=
// paths, so a literal percent is "%%". A path like /home/u/100%/.flow
// would otherwise be rewritten (or rejected) by systemd while install
// reports success.
func unitEsc(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// execQuote quotes one ExecStart argument the systemd way (double quotes,
// backslash escapes) when it needs it, and escapes '%' as "%%".
func execQuote(a string) string {
	a = unitEsc(a)
	if a != "" && !strings.ContainsAny(a, " \t\"'\\$;") {
		return a
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(a) + `"`
}

// unquoteExec undoes execQuote for display: drops quotes around arguments
// and turns "%%" back into '%'.
func unquoteExec(line string) string {
	var out []string
	var cur strings.Builder
	inQ, esc, pct := false, false, false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, c := range line {
		switch {
		case esc:
			cur.WriteRune(c)
			esc = false
		case c == '%':
			if pct {
				cur.WriteRune('%')
			}
			pct = !pct
			continue
		case inQ && c == '\\':
			esc = true
		case c == '"':
			inQ = !inQ
		case !inQ && (c == ' ' || c == '\t'):
			flush()
		default:
			cur.WriteRune(c)
		}
		pct = false
	}
	flush()
	return strings.Join(out, " ")
}

// renderService is the oneshot unit the timer activates.
//
// KillMode=process is load-bearing: both jobs (`flow owner tick-due`,
// `flow run playbook --auto`) fork a detached child (setsid) and exit at
// once. setsid leaves the session, not the cgroup, and systemd's default
// KillMode=control-group kills everything left in the cgroup when
// ExecStart exits — so every fire logged "Launched autonomous run",
// status=0/SUCCESS, and the harness never started. With KillMode=process
// systemd only reaps the main process and the child runs to completion
// (systemd notes "Unit process N remains running after unit stopped",
// which is exactly the point). Chosen over `systemd-run --user --scope`
// around the child because it is one declarative line in the unit flow
// already writes (visible in install output and --dry-run), needs no
// systemd-specific branch or D-Bus round-trip in the launchers that macOS,
// cron and plain shells share, and has no fallback path to get wrong.
func renderService(sp Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: flow schedule remove %s\n", Marker, sp.Name)
	fmt.Fprintf(&b, "[Unit]\nDescription=flow schedule %s (%s)\n\n", sp.Name, sp.When())
	b.WriteString("[Service]\nType=oneshot\n")
	b.WriteString("# flow forks a detached child and exits; do not kill the child with the cgroup.\nKillMode=process\n")
	for _, k := range sp.sortedEnvKeys() {
		// systemd wants the whole VAR=value in one quoted token; the value
		// may hold spaces but PATH-style values never hold quotes.
		fmt.Fprintf(&b, "Environment=\"%s=%s\"\n", k, unitEsc(strings.ReplaceAll(sp.Env[k], `"`, `\"`)))
	}
	args := make([]string, 0, 1+len(sp.Args))
	for _, a := range append([]string{sp.FlowBin}, sp.Args...) {
		args = append(args, execQuote(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=append:%s\n", unitEsc(sp.LogPath()), unitEsc(sp.ErrPath()))
	return b.String()
}

// renderTimer is the timer: an interval (OnUnitActiveSec, first run a
// minute after boot) or a daily OnCalendar with Persistent so a run missed
// while the machine was off fires at the next boot.
func renderTimer(sp Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: flow schedule remove %s\n", Marker, sp.Name)
	fmt.Fprintf(&b, "[Unit]\nDescription=flow schedule %s timer (%s)\n\n[Timer]\n", sp.Name, sp.When())
	if sp.At != nil {
		fmt.Fprintf(&b, "OnCalendar=*-*-* %02d:%02d:00\nPersistent=true\n", sp.At.Hour, sp.At.Minute)
	} else {
		fmt.Fprintf(&b, "OnBootSec=1m\nOnUnitActiveSec=%s\nAccuracySec=5s\n", sp.Every.String())
	}
	fmt.Fprintf(&b, "Unit=%s%s.service\n\n[Install]\nWantedBy=timers.target\n", SystemdPrefix, sp.Name)
	return b.String()
}
