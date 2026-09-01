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
	case "init", "skill", "--version", "-v", "version", "-h", "--help", "help", "__auto-exec", "__owner-tick":
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
	case "__auto-exec":
		// Hidden: the detached supervisor entry point for `flow do --auto`.
		// Not listed in usage; invoked only by autoLauncher.
		return cmdAutoExec(rest)
	case "__owner-tick":
		// Hidden: the detached owner-tick supervisor. Invoked only by
		// ownerTickLauncher (via `flow owner tick-due`).
		return cmdOwnerTick(rest)
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
	case "owner":
		return cmdOwner(rest)
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
	case "stats":
		return cmdStats(rest)
	case "hook":
		return cmdHook(rest)
	case "message", "msg":
		return cmdMessage(rest)
	case "inbox":
		return cmdInbox(rest)
	case "post":
		return cmdPost(rest)
	case "watch":
		return cmdWatch(rest)
	case "-h", "--help", "help":
		printUsage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: unknown subcommand %q\n", cmd)
	printUsage()
	return 2
}

func printUsage() {
	fmt.Println(`flow — personal task and agent-session manager

Setup:
  flow init
  flow skill install [--force]
  flow skill uninstall
  flow skill update

Create:
  flow add project "<name>" --work-dir <path> [--slug <s>] [--priority h|m|l] [--mkdir]
  flow add task    "<name>" [--slug <s>] [--project <slug>] [--work-dir <path>] [--mkdir] [--priority h|m|l] [--due <date>]

Sessions:
  flow do                <ref> [--harness claude|codex] [--fresh] [--dangerously-skip-permissions]
  flow do --auto         <ref> [--harness claude|codex] (run headlessly in the background; self-completes via flow done)
  flow done              <ref>
  flow hook session-start                      (SessionStart hook handler — wire via ~/.claude/settings.json)

Read:
  flow show task       [<ref>]
  flow show project    [<ref>]
  flow transcript      [<ref>] [--compact]           (readable transcript from session jsonl)
  flow stats           [--since all|<N>d] [--project <slug>] [--card] [--out <path>]
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
  flow do        <ref> [--harness claude|codex] [--fresh] [--dangerously-skip-permissions] [--force]   (choose harness only for a new task session; --force overrides the live-session guard)
  flow do --here <ref> [--force]                                              (bind THIS harness session to the task; --force overwrites a prior binding)
  flow archive   <ref>
  flow unarchive <ref>

Message bus (directed + broadcast, CLI-only — see flow skill §4.18):
  flow message <assignee>[/<task-slug>] "<body>" [--urgent]
                                     (message a human — pending + escalation schedule until
                                      answered — or the session bound to a task; alias: msg)
  flow inbox [--as <assignee>] [--json]        (what's pending; --as self = the human even in a
                                                bound session; --as = any assignee's queue)
  flow inbox pop [--wait] [--timeout <s>] [--as <assignee>] [--json]
                                     (atomically consume the oldest, one at a time; --wait blocks
                                      until mail arrives — park a Monitor on it; safe with
                                      concurrent consumers)
  flow inbox ack [<id>]              (answer by hand; replying in the sending session acks automatically)
  flow inbox due [--as <assignee>] [--json]   (escalation feed for YOUR notifier script: prints
                                      due messages, advances their backoff; flow ships no UI)
  flow inbox stats                   (wait-time metrics)
  flow post "<one-liner>"            (broadcast to whoever watches this task/project/assignee)
  flow watch <task|project|assignee> [--as <assignee>] | --list | --rm <target>

Workdirs:
  flow workdir list
  flow workdir add <path> [--name <nickname>]
  flow workdir remove <path>
  flow workdir scan [<root>] [--add]

Playbooks:
  flow add playbook   "<name>" --work-dir <path> [--slug <s>] [--project <slug>] [--mkdir]
  flow run playbook   <slug> [--dangerously-skip-permissions]   (spawn a new tab)
  flow run playbook   <slug> --here                              (bind THIS harness session to the new run; no new tab)
  flow run playbook   <slug> --auto                              (run the playbook headlessly in the background)
  flow show playbook  <ref>
  flow list playbooks [--project <slug>] [--include-archived]

Owners (autonomous, self-prompting controllers — see flow skill §4.17):
  flow add owner     "<name>" --work-dir <path> [--every <dur>] [--slug <s>] [--project <slug>] [--mkdir]
  flow owner list                              (alias: flow list owners)
  flow owner show    <slug>                     (alias: flow show owner <slug>)
  flow owner start|pause <slug>                 (start reactivates a paused OR retired owner)
  flow owner tick    <slug> [--auto]            (wake now; interactive by default, --auto = headless)
  flow owner next    <slug> --in <dur> | --at <when>   (self-pace the next wake)
  flow owner retire  <slug> [--delete]`)
}
