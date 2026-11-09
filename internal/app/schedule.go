package app

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/schedule"
)

// `flow schedule` installs the OS-native timer for the two things flow
// needs a clock for, instead of a hand-written plist/unit/crontab:
//
//	flow schedule install owners [--every 60s]           ─► <flow> owner tick-due
//	flow schedule install playbook <slug> --at HH:MM     ─► <flow> run playbook <slug> --auto
//	                                     | --every <dur>
//	flow schedule list
//	flow schedule remove owners | playbook <slug>
//
//	macOS  ─► launchd   ~/Library/LaunchAgents/cloud.facets.flow.<name>.plist
//	Linux  ─► systemd   ~/.config/systemd/user/flow-<name>.{service,timer}   (user session reachable)
//	       └► cron      one tagged crontab line                              (otherwise, or --backend cron)
//
// It prints every file (with content) it wrote and every command it ran.
// Files flow writes carry a marker; a hand-written file is never replaced
// or removed without --force. Logs go to $FLOW_ROOT/schedules/<name>.log.

const scheduleUsage = `usage: flow schedule <install|list|remove> ...
  flow schedule install owners [--every <dur>]                 run the owner scheduler (flow owner tick-due) on an interval (default 60s)
  flow schedule install playbook <slug> --at HH:MM | --every <dur>
                                                               run a playbook headlessly (flow run playbook <slug> --auto) daily at HH:MM or every <dur>
  flow schedule list                                           installed schedules (flow-written and hand-written)
  flow schedule remove owners | playbook <slug>                stop and delete a schedule
  common flags: --backend launchd|systemd|cron   (default: launchd on macOS; systemd --user on Linux, cron when no user session)
                --dry-run                       print the files and commands without touching anything
                --force                         replace/remove a file flow schedule did not write`

// ownerScheduleName is the schedule name (and launchd label suffix) of
// the owner scheduler — the same label the skill used to hand-write.
const ownerScheduleName = "owner-scheduler"

// scheduleFlags is the common flag set; positionals and flags may be
// interleaved (`install --every 5m owners` and `install owners --every 5m`).
type scheduleFlags struct {
	every, at, backend string
	force, dryRun      bool
	pos                []string
}

func parseScheduleArgs(args []string) (*scheduleFlags, error) {
	f := &scheduleFlags{}
	fs := flagSet("schedule")
	fs.StringVar(&f.every, "every", "", "interval, e.g. 60s, 5m, 2h")
	fs.StringVar(&f.at, "at", "", "time of day HH:MM (24h) for a daily schedule")
	fs.StringVar(&f.backend, "backend", "", "launchd | systemd | cron (default: by OS)")
	fs.BoolVar(&f.force, "force", false, "replace or remove a file flow schedule did not write")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print what would be written and run; touch nothing")
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			f.pos = append(f.pos, rest[0])
			rest = rest[1:]
		}
	}
	return f, nil
}

// cmdSchedule implements `flow schedule ...`.
func cmdSchedule(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, scheduleUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Println(scheduleUsage)
		return 0
	}
	f, err := parseScheduleArgs(args)
	if err != nil {
		return 2
	}
	if len(f.pos) == 0 {
		fmt.Fprintln(os.Stderr, scheduleUsage)
		return 2
	}
	verb, pos := f.pos[0], f.pos[1:]
	switch verb {
	case "install":
		return scheduleInstall(f, pos)
	case "list", "ls":
		return scheduleList(f, pos)
	case "remove", "rm":
		return scheduleRemove(f, pos)
	}
	fmt.Fprintf(os.Stderr, "error: unknown schedule subcommand %q\n%s\n", verb, scheduleUsage)
	return 2
}

// scheduleTarget resolves "owners" | "playbook <slug>" to a schedule name
// and the flow argv to run. Exit code 2 on a usage error.
func scheduleTarget(pos []string) (name string, flowArgs []string, rc int) {
	if len(pos) == 0 {
		fmt.Fprintf(os.Stderr, "error: what to schedule? owners | playbook <slug>\n%s\n", scheduleUsage)
		return "", nil, 2
	}
	switch pos[0] {
	case "owners", "owner":
		if len(pos) != 1 {
			fmt.Fprintf(os.Stderr, "error: `owners` takes no slug\n%s\n", scheduleUsage)
			return "", nil, 2
		}
		return ownerScheduleName, []string{"owner", "tick-due"}, 0
	case "playbook":
		if len(pos) != 2 || pos[1] == "" {
			fmt.Fprintf(os.Stderr, "error: `playbook` needs a slug\n%s\n", scheduleUsage)
			return "", nil, 2
		}
		return pos[1], []string{"run", "playbook", pos[1], "--auto"}, 0
	}
	fmt.Fprintf(os.Stderr, "error: unknown schedule target %q (owners | playbook <slug>)\n%s\n", pos[0], scheduleUsage)
	return "", nil, 2
}

func pickScheduleBackend(override string) (schedule.Backend, error) {
	return schedule.Pick(runtime.GOOS, override)
}

// scheduleEnv is the job's environment: the user's PATH (launchd/cron
// give a minimal one; without it the tick fails to find claude/codex/gh),
// HOME, and FLOW_ROOT when this invocation uses a non-default root.
func scheduleEnv() map[string]string {
	env := map[string]string{}
	if p := os.Getenv("PATH"); p != "" {
		env["PATH"] = p
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		env["HOME"] = h
	}
	if r := os.Getenv("FLOW_ROOT"); r != "" {
		env["FLOW_ROOT"] = r
	}
	return env
}

func scheduleInstall(f *scheduleFlags, pos []string) int {
	name, flowArgs, rc := scheduleTarget(pos)
	if rc != 0 {
		return rc
	}
	spec := schedule.Spec{Name: name, Args: flowArgs, FlowBin: flowBinPath(), Env: scheduleEnv()}
	if f.every != "" {
		d, err := time.ParseDuration(f.every)
		if err != nil || d <= 0 {
			fmt.Fprintf(os.Stderr, "error: --every %q must be a positive duration like 60s, 5m or 2h\n", f.every)
			return 2
		}
		spec.Every = d
	}
	if f.at != "" {
		c, err := schedule.ParseClock(f.at)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --at: %v\n", err)
			return 2
		}
		spec.At = &c
	}
	if name == ownerScheduleName {
		if spec.At != nil {
			fmt.Fprintln(os.Stderr, "error: the owner scheduler runs on an interval (--every), not at a time of day")
			return 2
		}
		if spec.Every == 0 {
			spec.Every = 60 * time.Second
		}
	} else {
		if spec.Every == 0 && spec.At == nil {
			fmt.Fprintf(os.Stderr, "error: a playbook schedule needs --at HH:MM or --every <dur>\n%s\n", scheduleUsage)
			return 2
		}
		if spec.Every != 0 && spec.At != nil {
			fmt.Fprintln(os.Stderr, "error: pass either --at or --every, not both")
			return 2
		}
		if rc := requirePlaybook(name); rc != 0 {
			return rc
		}
	}
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	spec.LogDir = filepath.Join(root, "schedules")
	if !f.dryRun {
		if err := os.MkdirAll(spec.LogDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}
	b, err := pickScheduleBackend(f.backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	rep, err := b.Install(spec, schedule.Options{Force: f.force, DryRun: f.dryRun})
	printScheduleReport(rep, f.dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("logs: %s (stderr: %s)\n", spec.LogPath(), spec.ErrPath())
	if f.dryRun {
		fmt.Printf("dry run: nothing was written or run. Drop --dry-run to install %s (%s).\n", name, spec.When())
	} else {
		fmt.Printf("scheduled %s: %s %s via %s\n", name, spec.Command(), spec.When(), b.Name())
	}
	return 0
}

// requirePlaybook refuses to schedule a slug that is not a playbook.
func requirePlaybook(slug string) int {
	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if _, err := flowdb.GetPlaybook(db, slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: no playbook %q (flow list playbooks)\n", slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func scheduleRemove(f *scheduleFlags, pos []string) int {
	name, _, rc := scheduleTarget(pos)
	if rc != 0 {
		return rc
	}
	b, err := pickScheduleBackend(f.backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	rep, err := b.Remove(name, schedule.Options{Force: f.force, DryRun: f.dryRun})
	printScheduleReport(rep, f.dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if f.dryRun {
		fmt.Printf("dry run: nothing was removed or run.\n")
	} else {
		fmt.Printf("removed schedule %s (%s)\n", name, b.Name())
	}
	return 0
}

func scheduleList(f *scheduleFlags, pos []string) int {
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "usage: flow schedule list [--backend launchd|systemd|cron]\n")
		return 2
	}
	b, err := pickScheduleBackend(f.backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	ents, err := b.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(ents) == 0 {
		fmt.Printf("no schedules (%s). Install one: flow schedule install owners | flow schedule install playbook <slug> --at HH:MM\n", b.Name())
		return 0
	}
	fmt.Printf("%-24s %-8s %-14s %-11s %-12s %s\n", "NAME", "BACKEND", "WHEN", "STATUS", "SOURCE", "COMMAND")
	for _, e := range ents {
		fmt.Printf("%-24s %-8s %-14s %-11s %-12s %s\n", e.Name, e.Backend, e.When, e.Status, e.Source, e.Command)
	}
	for _, e := range ents {
		if e.Path != "" && e.Path != "crontab" {
			fmt.Printf("  %s: %s\n", e.Name, e.Path)
		}
	}
	return 0
}

// printScheduleReport shows exactly what happened: each file with its
// content, each command, and any note.
func printScheduleReport(rep *schedule.Report, dry bool) {
	if rep == nil {
		return
	}
	wrote, ran, removed := "wrote", "ran", "removed"
	if dry {
		wrote, ran, removed = "would write", "would run", "would remove"
		fmt.Println("dry run (nothing is written or run)")
	}
	fmt.Printf("backend: %s\n", rep.Backend)
	for _, w := range rep.Writes {
		fmt.Printf("%s %s:\n", wrote, w.Path)
		for _, line := range strings.Split(strings.TrimRight(w.Content, "\n"), "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
	for _, r := range rep.Removed {
		fmt.Printf("%s %s\n", removed, r)
	}
	for _, argv := range rep.Ran {
		fmt.Printf("%s: %s\n", ran, strings.Join(argv, " "))
	}
	for _, n := range rep.Notes {
		fmt.Printf("note: %s\n", n)
	}
}
