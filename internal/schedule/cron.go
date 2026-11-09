package schedule

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CronTag marks the crontab lines flow owns: "# flow-schedule:<name>".
const CronTag = "# flow-schedule:"

// Cron keeps one tagged line per schedule in the user's crontab. It is
// the Linux fallback when no systemd user session is reachable (a plain
// SSH box, a container) and never touches untagged lines.
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

// renderCronLine is the one tagged line for s.
func renderCronLine(s Spec) (string, error) {
	expr, err := cronExpr(s)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, k := range s.sortedEnvKeys() {
		if k == "HOME" {
			continue // cron sets HOME from the passwd entry
		}
		parts = append(parts, k+"="+shQuote(s.Env[k]))
	}
	parts = append(parts, shQuote(s.FlowBin))
	for _, a := range s.Args {
		if a == "" || strings.ContainsAny(a, " \t\"'\\$;&|<>`%") {
			a = shQuote(a)
		}
		parts = append(parts, a)
	}
	return fmt.Sprintf("%s %s >>%s 2>>%s %s%s", expr, strings.Join(parts, " "),
		shQuote(s.LogPath()), shQuote(s.ErrPath()), CronTag, s.Name), nil
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
	tab, err := readCrontab()
	if err != nil {
		return nil, fmt.Errorf("read crontab: %w", err)
	}
	kept, _ := withoutTagged(tab, s.Name)
	newTab := strings.Join(append(kept, line), "\n") + "\n"
	pl := newPlan(c.Name(), o)
	pl.rep.Writes = append(pl.rep.Writes, Write{Path: "crontab", Content: line})
	if _, err := pl.run(newTab, false, "crontab", "-"); err != nil {
		return pl.rep, err
	}
	return pl.rep, nil
}

func (c *Cron) Remove(name string, o Options) (*Report, error) {
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
		return pl.rep, nil
	}
	if _, err := pl.run(newTab, false, "crontab", "-"); err != nil {
		return pl.rep, err
	}
	return pl.rep, nil
}

var cronLineRe = regexp.MustCompile(`^(\S+ \S+ \S+ \S+ \S+) (.*?)(?: >>\S+ 2>>\S+)? ` + regexp.QuoteMeta(CronTag) + `(\S+)\s*$`)
var cronEveryMinRe = regexp.MustCompile(`^\*/(\d+) \* \* \* \*$`)
var cronEveryHourRe = regexp.MustCompile(`^0 \*/(\d+) \* \* \*$`)
var cronDailyRe = regexp.MustCompile(`^(\d+) (\d+) \* \* \*$`)

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
		// Command: drop the VAR='…' prefixes, unquote the rest.
		var args []string
		for _, tok := range splitShell(m[2]) {
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
// quoting renderCronLine emits).
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
