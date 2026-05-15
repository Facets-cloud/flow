// Package app implements the flow CLI — personal task and Claude session
// manager backed by SQLite.
package app

import (
	"fmt"
	"os"
)

// Version holds the binary version string, set by main.go from a
// `-ldflags -X main.version=<tag>` build. Defaults to "dev" if main
// never assigns it (e.g. tests linking the package directly).
var Version = "dev"

// Run is the entry point for the CLI. Returns an exit code.
func Run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 0
	}
	cmd, rest := args[0], args[1:]

	// Auto-upgrade the skill + SessionStart hook if the binary version
	// has changed since the last install. Skipped for `init`, `skill`,
	// and `--version` — those manage the skill themselves or need to
	// run before any install state exists. See maybeAutoUpgradeSkill.
	switch cmd {
	case "init", "skill", "--version", "-v", "version", "-h", "--help", "help":
		// no auto-upgrade
	default:
		maybeAutoUpgradeSkill()
	}

	switch cmd {
	case "--version", "-v", "version":
		fmt.Println(Version)
		return 0
	case "init":
		return cmdInit(rest)
	case "add":
		return cmdAdd(rest)
	case "do":
		return cmdDo(rest)
	case "run":
		return cmdRun(rest)
	case "done":
		return cmdDone(rest)
	case "show":
		return cmdShow(rest)
	case "list":
		return cmdList(rest)
	case "edit":
		return cmdEdit(rest)
	case "update":
		return cmdUpdate(rest)
	case "archive":
		return cmdArchive(rest)
	case "unarchive":
		return cmdUnarchive(rest)
	case "workdir":
		return cmdWorkdir(rest)
	case "skill":
		return cmdSkill(rest)
	case "transcript":
		return cmdTranscript(rest)
	case "hook":
		return cmdHook(rest)
	case "dashboard":
		return cmdDashboard(rest)
	case "-h", "--help", "help":
		printUsage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: unknown subcommand %q\n", cmd)
	printUsage()
	return 2
}

func printUsage() {
	fmt.Println(`flow — personal task and Claude session manager

Setup:
  flow init
  flow skill install [--force]
  flow skill uninstall
  flow skill update

Create:
  flow add project "<name>" --work-dir <path> [--slug <s>] [--priority h|m|l] [--mkdir]
  flow add task    "<name>" [--slug <s>] [--project <slug>] [--work-dir <path>] [--mkdir] [--priority h|m|l] [--due <date>]

Sessions:
  flow do                <ref> [--fresh] [--dangerously-skip-permissions]
  flow done              <ref>
  flow hook session-start                      (SessionStart hook handler — wire via ~/.claude/settings.json)

Read:
  flow show task       [<ref>]
  flow show project    [<ref>]
  flow dashboard                                          (read-only TUI: counts, working/awaiting/stale, activity feed)
  flow transcript      [<ref>] [--compact]           (readable transcript from session jsonl)
  flow list tasks    [--status ...] [--project ...] [--priority ...] [--tag <t>] [--since ...] [--include-archived]
  flow list projects [--status ...] [--include-archived]
  flow list tags                                            (every tag in use, with per-tag task counts)

Edit / mutate:
  flow edit        <ref>
  flow update task    <ref> [--work-dir <path>] [--mkdir]
                            [--status <s>] [--priority h|m|l]
                            [--assignee <name>] [--clear-assignee]
                            [--due-date <date>] [--clear-due]
                            [--waiting "<who or what>"] [--clear-waiting]
                            [--tag <t> ...] [--remove-tag <t> ...] [--clear-tags]
  flow update project <ref> [--priority h|m|l]
  flow do        <ref> [--fresh] [--dangerously-skip-permissions] [--force]   (spawn a new tab; --force overrides the live-session guard)
  flow do --here <ref> [--force]                                              (bind THIS Claude session to the task; --force overwrites a prior binding)
  flow archive   <ref>
  flow unarchive <ref>

Workdirs:
  flow workdir list
  flow workdir add <path> [--name <nickname>]
  flow workdir remove <path>
  flow workdir scan [<root>] [--add]

Playbooks:
  flow add playbook   "<name>" --work-dir <path> [--slug <s>] [--project <slug>] [--mkdir]
  flow run playbook   <slug> [--dangerously-skip-permissions]   (spawn a new tab)
  flow run playbook   <slug> --here                              (bind THIS Claude session to the new run; no new tab)
  flow show playbook  <ref>
  flow list playbooks [--project <slug>] [--include-archived]`)
}
