package schedule

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CronTag marks the crontab lines flow owns: "# flow-schedule:<name>".
const CronTag = "# flow-schedule:"

// Cron keeps one tagged line per schedule in the user's crontab. It is
// the Linux fallback when no systemd user session is reachable (a plain
// SSH box, a container) and never touches untagged lines.
//
// The line itself is deliberately tiny:
//
//	*/5 * * * * /bin/sh '<LogDir>/<name>.sh' # flow-schedule:<name>
//
// and the wrapper script carries the environment, the command and the log
// redirects. Two cron quirks force that split: Debian's cron rejects the
// whole crontab when a line is long (a real PATH easily is), and '%' in a
// command field is a newline to cron, so a path like /home/u/100%/.flow
// would silently truncate the command. The wrapper is plain sh, where
// neither applies; only the wrapper's own path appears on the line (with
// '%' escaped as "\%").
type Cron struct{}

func (c *Cron) Name() string { return "cron" }

// readCrontab returns the current crontab; "no crontab for <user>" is
// an empty one, any other failure is an error.
func readCrontab() (string, error) {
	out, err := Runner("", "crontab", "-l")
	if err != nil {
		if strings.Contains(strings.ToLower(out), "no crontab") {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

// cronExpr renders the five-field cadence. cron only speaks whole
// minutes, so an interval must be a minute count under an hour or a
// whole-hour count under a day.
func cronExpr(s Spec) (string, error) {
	if s.At != nil {
		return fmt.Sprintf("%d %d * * *", s.At.Minute, s.At.Hour), nil
	}
	if s.Every%time.Minute != 0 {
		return "", fmt.Errorf("cron cannot fire every %v: use whole minutes (e.g. 1m, 5m) or whole hours (e.g. 2h)", s.Every)
	}
	mins := int(s.Every / time.Minute)
	switch {
	case mins == 1:
		return "* * * * *", nil
	case mins < 60:
		return fmt.Sprintf("*/%d * * * *", mins), nil
	case mins%60 == 0 && mins/60 == 24:
		return "0 0 * * *", nil
	case mins%60 == 0 && mins/60 < 24:
		return fmt.Sprintf("0 */%d * * *", mins/60), nil
	}
	return "", fmt.Errorf("cron cannot fire every %v: use minutes under an hour, whole hours under a day, or --at HH:MM", s.Every)
}

// wrapperPath is the sh script the crontab line runs: <LogDir>/<name>.sh.
func wrapperPath(s Spec) string { return filepath.Join(s.LogDir, s.Name+".sh") }

// cronEsc escapes '%' for a crontab command field (cron turns an unescaped
// one into a newline before the shell ever sees the line).
func cronEsc(s string) string { return strings.ReplaceAll(s, "%", `\%`) }

func cronUnesc(s string) string { return strings.ReplaceAll(s, `\%`, "%") }

// renderWrapper is the wrapper script: exports, then exec with the log
// redirects. Everything is single-quoted for sh; no cron rules apply here.
func renderWrapper(s Spec) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# %s: flow schedule remove %s\n", Marker, s.Name)
	fmt.Fprintf(&b, "# %s (%s)\n", s.Command(), s.When())
	for _, k := range s.sortedEnvKeys() {
		fmt.Fprintf(&b, "export %s=%s\n", k, shQuote(s.Env[k]))
	}
	parts := []string{"exec", shQuote(s.FlowBin)}
	for _, a := range s.Args {
		if a == "" || strings.ContainsAny(a, " \t\"'\\$;&|<>`%*?[]#~") {
			a = shQuote(a)
		}
		parts = append(parts, a)
	}
	fmt.Fprintf(&b, "%s >>%s 2>>%s\n", strings.Join(parts, " "), shQuote(s.LogPath()), shQuote(s.ErrPath()))
	return b.String()
}

// renderCronLine is the one tagged line for s: cadence, the wrapper, the tag.
func renderCronLine(s Spec) (string, error) {
	expr, err := cronExpr(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s /bin/sh %s %s%s", expr, cronEsc(shQuote(wrapperPath(s))), CronTag, s.Name), nil
}

func cronTagged(line, name string) bool {
	return strings.HasSuffix(strings.TrimRight(line, " \t"), CronTag+name)
}

// withoutTagged drops the lines tagged name; dropped reports them.
func withoutTagged(tab, name string) (kept []string, dropped []string) {
	for _, l := range strings.Split(strings.TrimRight(tab, "\n"), "\n") {
		if cronTagged(l, name) {
			dropped = append(dropped, l)
			continue
		}
		kept = append(kept, l)
	}
	if len(kept) == 1 && kept[0] == "" {
		kept = nil
	}
	return kept, dropped
}

func (c *Cron) Install(s Spec, o Options) (*Report, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	line, err := renderCronLine(s)
	if err != nil {
		return nil, err
	}
	wp := wrapperPath(s)
	if err := guard(wp, o, "replace"); err != nil {
		return nil, err
	}
	tab, err := readCrontab()
	if err != nil {
		return nil, fmt.Errorf("read crontab: %w", err)
	}
	kept, _ := withoutTagged(tab, s.Name)
	newTab := strings.Join(append(kept, line), "\n") + "\n"
	pl := newPlan(c.Name(), o)
	if err := pl.writeMode(wp, renderWrapper(s), 0o755); err != nil {
		return pl.rep, err
	}
	pl.rep.Writes = append(pl.rep.Writes, Write{Path: "crontab", Content: line})
	if _, err := pl.run(newTab, false, "crontab", "-"); err != nil {
		return pl.rep, err
	}
	return pl.rep, nil
}

func (c *Cron) Remove(name string, o Options) (*Report, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	tab, err := readCrontab()
	if err != nil {
		return nil, fmt.Errorf("read crontab: %w", err)
	}
	kept, dropped := withoutTagged(tab, name)
	if len(dropped) == 0 {
		return nil, fmt.Errorf("no cron schedule %q (no crontab line tagged %s%s)", name, CronTag, name)
	}
	pl := newPlan(c.Name(), o)
	pl.rep.Removed = append(pl.rep.Removed, dropped...)
	newTab := ""
	if len(kept) > 0 {
		newTab = strings.Join(kept, "\n") + "\n"
	}
	if newTab == "" {
		if _, err := pl.run("", false, "crontab", "-r"); err != nil {
			return pl.rep, err
		}
	} else if _, err := pl.run(newTab, false, "crontab", "-"); err != nil {
		return pl.rep, err
	}
	// The line is gone; now its wrapper (only one flow wrote).
	for _, l := range dropped {
		wp := wrapperFromLine(l)
		if wp == "" {
			continue
		}
		if exists, marked := fileHasMarker(wp); exists && (marked || o.Force) {
			if err := pl.remove(wp); err != nil {
				return pl.rep, err
			}
		}
	}
	return pl.rep, nil
}

var cronLineRe = regexp.MustCompile(`^(\S+ \S+ \S+ \S+ \S+) (.*?)(?: >>\S+ 2>>\S+)? ` + regexp.QuoteMeta(CronTag) + `(\S+)\s*$`)
var cronEveryMinRe = regexp.MustCompile(`^\*/(\d+) \* \* \* \*$`)
var cronEveryHourRe = regexp.MustCompile(`^0 \*/(\d+) \* \* \*$`)
var cronDailyRe = regexp.MustCompile(`^(\d+) (\d+) \* \* \*$`)

// wrapperFromLine returns the wrapper path a tagged line runs
// (`/bin/sh '<path>'`), or "" for any other shape.
func wrapperFromLine(line string) string {
	m := cronLineRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	toks := splitShell(cronUnesc(m[2]))
	if len(toks) == 2 && toks[0] == "/bin/sh" && filepath.IsAbs(toks[1]) {
		return toks[1]
	}
	return ""
}

// wrapperCommand reads the command back out of a wrapper's exec line (the
// redirects dropped), or "" when the file is not a flow wrapper.
func wrapperCommand(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), Marker) {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "exec ") {
			continue
		}
		var args []string
		for _, tok := range splitShell(strings.TrimPrefix(l, "exec ")) {
			if strings.HasPrefix(tok, ">>") || strings.HasPrefix(tok, "2>>") {
				continue
			}
			args = append(args, tok)
		}
		return strings.Join(args, " ")
	}
	return ""
}

func (c *Cron) List() ([]Entry, error) {
	tab, err := readCrontab()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, l := range strings.Split(tab, "\n") {
		m := cronLineRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		e := Entry{Name: m[3], Backend: c.Name(), Source: "flow", Status: "installed", Path: "crontab", When: m[1]}
		switch expr := m[1]; {
		case expr == "* * * * *":
			e.When = "every 1m0s"
		case cronEveryMinRe.MatchString(expr):
			var n int
			fmt.Sscanf(cronEveryMinRe.FindStringSubmatch(expr)[1], "%d", &n)
			e.When = "every " + (time.Duration(n) * time.Minute).String()
		case cronEveryHourRe.MatchString(expr):
			var n int
			fmt.Sscanf(cronEveryHourRe.FindStringSubmatch(expr)[1], "%d", &n)
			e.When = "every " + (time.Duration(n) * time.Hour).String()
		case cronDailyRe.MatchString(expr):
			dm := cronDailyRe.FindStringSubmatch(expr)
			var mi, h int
			fmt.Sscanf(dm[1], "%d", &mi)
			fmt.Sscanf(dm[2], "%d", &h)
			e.When = "daily " + Clock{Hour: h, Minute: mi}.String()
		}
		if wp := wrapperFromLine(l); wp != "" {
			e.Path = wp
			if cmd := wrapperCommand(wp); cmd != "" {
				e.Command = cmd
				out = append(out, e)
				continue
			}
		}
		// Not a wrapper line (or the wrapper is gone): drop the VAR='…'
		// prefixes, unquote the rest.
		var args []string
		for _, tok := range splitShell(cronUnesc(m[2])) {
			if len(args) == 0 && strings.Contains(tok, "=") && !strings.HasPrefix(tok, "/") {
				continue
			}
			args = append(args, tok)
		}
		e.Command = strings.Join(args, " ")
		out = append(out, e)
	}
	return out, nil
}

// splitShell splits a line on spaces honoring single quotes (the only
// quoting renderWrapper emits).
func splitShell(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	has := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inQ:
			inQ = true
			has = true
		case inQ && ch == '\'':
			// '\'' sequence → literal quote
			if strings.HasPrefix(s[i:], `'\''`) {
				cur.WriteByte('\'')
				i += 3
				continue
			}
			inQ = false
		case !inQ && ch == ' ':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(ch)
			has = true
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
