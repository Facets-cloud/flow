// Package schedule installs the OS-native timer that fires a flow command
// on a cadence: launchd on macOS, a systemd --user timer on Linux, or a
// crontab line where no systemd user session is reachable. Two targets
// use it — the owner scheduler (`flow owner tick-due`) and a playbook run
// on a cadence (`flow run playbook <slug> --auto`).
//
//	flow schedule install …
//	  └─ Pick(GOOS) ─► Launchd | Systemd | Cron
//	        └─ Install(spec) ─► write file(s) ─► launchctl / systemctl / crontab
//	                              └─ Report: every path+content written, every command run
//
// Files flow writes carry Marker, so install/remove never replace a
// hand-written plist or unit without Options.Force. Tests pin Runner.
package schedule

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Marker tags every file (or crontab line) flow schedule writes.
const Marker = "written by flow schedule"

// Clock is a time of day for a daily schedule.
type Clock struct {
	Hour, Minute int
}

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

var clockRe = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// ParseClock parses "HH:MM" (24h).
func ParseClock(s string) (Clock, error) {
	m := clockRe.FindStringSubmatch(s)
	if m == nil {
		return Clock{}, fmt.Errorf("time %q must be HH:MM (24-hour), e.g. 09:00", s)
	}
	var c Clock
	fmt.Sscanf(m[1], "%d", &c.Hour)
	fmt.Sscanf(m[2], "%d", &c.Minute)
	return c, nil
}

// Spec is one schedule: run `FlowBin Args...` every Every, or daily At.
type Spec struct {
	Name    string            // schedule name: "owner-scheduler" or a playbook slug
	Args    []string          // flow argv after the binary, e.g. ["owner","tick-due"]
	Every   time.Duration     // interval schedule (>= 1s); exclusive with At
	At      *Clock            // daily calendar schedule; exclusive with Every
	FlowBin string            // absolute path of the flow binary to run
	Env     map[string]string // environment for the job (PATH, HOME, FLOW_ROOT …)
	LogDir  string            // where <Name>.log / <Name>.err.log go
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Validate checks the spec is complete and self-consistent.
func (s Spec) Validate() error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("schedule name %q must be lowercase letters, digits, '.', '_' or '-'", s.Name)
	}
	if len(s.Args) == 0 {
		return errors.New("schedule has no command")
	}
	if (s.Every > 0) == (s.At != nil) {
		return errors.New("a schedule needs exactly one of an interval (--every) or a time of day (--at)")
	}
	if s.Every > 0 && s.Every < time.Second {
		return fmt.Errorf("interval %v is shorter than 1s", s.Every)
	}
	if s.At != nil && (s.At.Hour < 0 || s.At.Hour > 23 || s.At.Minute < 0 || s.At.Minute > 59) {
		return fmt.Errorf("time of day %02d:%02d is out of range", s.At.Hour, s.At.Minute)
	}
	if !filepath.IsAbs(s.FlowBin) {
		return fmt.Errorf("flow binary %q must be an absolute path", s.FlowBin)
	}
	if s.LogDir == "" {
		return errors.New("schedule has no log directory")
	}
	return nil
}

// When renders the cadence for humans: "every 1m0s" or "daily 09:00".
func (s Spec) When() string {
	if s.At != nil {
		return "daily " + s.At.String()
	}
	return "every " + s.Every.String()
}

// Command is the job's command line for display.
func (s Spec) Command() string {
	return strings.Join(append([]string{s.FlowBin}, s.Args...), " ")
}

// LogPath / ErrPath are the job's stdout / stderr files.
func (s Spec) LogPath() string { return filepath.Join(s.LogDir, s.Name+".log") }
func (s Spec) ErrPath() string { return filepath.Join(s.LogDir, s.Name+".err.log") }

// sortedEnvKeys gives Env a stable rendering order.
func (s Spec) sortedEnvKeys() []string {
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	// insertion sort; the map is tiny
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// Options tune Install / Remove.
type Options struct {
	Force  bool // replace or remove a file flow did not write
	DryRun bool // describe the plan; touch nothing, run nothing
}

// Write is one file flow wrote (or, in a dry run, would write). For cron,
// Path is "crontab" and Content is the one line.
type Write struct {
	Path    string
	Content string
}

// Report says exactly what Install / Remove did.
type Report struct {
	Backend string
	Writes  []Write
	Removed []string   // paths deleted (or, for cron, the lines dropped)
	Ran     [][]string // commands executed, in order
	Notes   []string   // anything the user should know (a tolerated failure, a caveat)
}

// Entry is one installed schedule as `flow schedule list` shows it.
type Entry struct {
	Name    string
	Backend string
	When    string // "every 1m0s" | "daily 09:00" | raw expression
	Command string
	Status  string // loaded | not loaded | active | inactive | installed
	Source  string // "flow" when the file carries Marker, else "hand-written"
	Path    string
}

// Backend is one OS timer facility.
type Backend interface {
	Name() string
	Install(s Spec, o Options) (*Report, error)
	Remove(name string, o Options) (*Report, error)
	List() ([]Entry, error)
}

// RunError is a failed external command with its output.
type RunError struct {
	Argv   []string
	Output string
	Err    error
}

func (e *RunError) Error() string {
	msg := fmt.Sprintf("%s failed", strings.Join(e.Argv, " "))
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if out := strings.TrimSpace(e.Output); out != "" {
		msg += ": " + out
	}
	return msg
}

func (e *RunError) Unwrap() error { return e.Err }

// Runner executes one external command (launchctl, systemctl, crontab)
// with stdin and returns its combined output. Overridable in tests so no
// real timer is ever touched.
var Runner = func(stdin string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), &RunError{Argv: append([]string{name}, args...), Output: string(out), Err: err}
	}
	return string(out), nil
}

// SystemdUserAvailable reports whether a systemd user manager is reachable
// (so `systemctl --user` can enable timers). Overridable in tests.
var SystemdUserAvailable = func() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	_, err := Runner("", "systemctl", "--user", "show", "--property=Version")
	return err == nil
}

// Pick chooses the backend for goos, or the named override
// ("launchd" | "systemd" | "cron").
func Pick(goos, override string) (Backend, error) {
	switch override {
	case "launchd":
		return &Launchd{AgentsDir: DefaultAgentsDir()}, nil
	case "systemd":
		return &Systemd{UnitDir: DefaultUnitDir()}, nil
	case "cron":
		return &Cron{}, nil
	case "":
	default:
		return nil, fmt.Errorf("unknown backend %q (launchd, systemd or cron)", override)
	}
	switch goos {
	case "darwin":
		return &Launchd{AgentsDir: DefaultAgentsDir()}, nil
	case "linux":
		if SystemdUserAvailable() {
			return &Systemd{UnitDir: DefaultUnitDir()}, nil
		}
		return &Cron{}, nil
	case "windows":
		return nil, errors.New("flow schedule does not support Windows yet (macOS launchd and Linux systemd/cron only)")
	}
	return nil, fmt.Errorf("flow schedule does not support %s (macOS launchd and Linux systemd/cron only)", goos)
}

// DefaultAgentsDir is ~/Library/LaunchAgents.
func DefaultAgentsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents")
}

// DefaultUnitDir is $XDG_CONFIG_HOME/systemd/user (default ~/.config/systemd/user).
func DefaultUnitDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

// plan collects a Report while honoring DryRun: in a dry run nothing is
// written or executed, but the report still lists the full plan.
type plan struct {
	rep *Report
	dry bool
}

func newPlan(backend string, o Options) *plan {
	return &plan{rep: &Report{Backend: backend}, dry: o.DryRun}
}

func (p *plan) write(path, content string) error {
	p.rep.Writes = append(p.rep.Writes, Write{Path: path, Content: content})
	if p.dry {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (p *plan) remove(path string) error {
	p.rep.Removed = append(p.rep.Removed, path)
	if p.dry {
		return nil
	}
	return os.Remove(path)
}

// run executes argv (unless dry) and records it. tolerate makes a failure
// a note instead of an error (e.g. unloading a job that was not loaded).
func (p *plan) run(stdin string, tolerate bool, argv ...string) (string, error) {
	p.rep.Ran = append(p.rep.Ran, argv)
	if p.dry {
		return "", nil
	}
	out, err := Runner(stdin, argv[0], argv[1:]...)
	if err != nil && tolerate {
		p.rep.Notes = append(p.rep.Notes, err.Error())
		return out, nil
	}
	return out, err
}

func (p *plan) note(format string, a ...any) {
	p.rep.Notes = append(p.rep.Notes, fmt.Sprintf(format, a...))
}

// fileHasMarker reports whether path exists and whether it carries Marker.
func fileHasMarker(path string) (exists, marked bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	return true, strings.Contains(string(b), Marker)
}

// guard refuses to touch a hand-written file unless forced.
func guard(path string, o Options, verb string) error {
	exists, marked := fileHasMarker(path)
	if exists && !marked && !o.Force {
		return fmt.Errorf("%s exists and was not written by flow schedule; pass --force to %s it", path, verb)
	}
	return nil
}

// shQuote single-quotes s for /bin/sh.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
