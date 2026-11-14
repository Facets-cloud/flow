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
	case "init", "skill", "--version", "-v", "version", "-h", "--help", "help", "__auto-exec", "__owner-tick", selfUpgradeArg:
		// no auto-upgrade
	default:
		maybeAutoUpgradeSkill()
	}

	rc := runCommand(cmd, rest)
	// Upgrade the binary after the command, so the command never waits.
	// See maybeSelfUpgrade.
	if selfUpgradeTrigger(cmd) {
		maybeSelfUpgrade()
	}
	return rc
}

// runCommand runs the subcommand cmd and returns its exit code.
func runCommand(cmd string, rest []string) int {
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
	case "auto":
		return cmdAuto(rest)
	case "__auto-exec":
		// Hidden: the detached supervisor entry point for `flow do --auto`.
		// Not listed in usage; invoked only by autoLauncher.
		return cmdAutoExec(rest)
	case "__owner-tick":
		// Hidden: the detached owner-tick supervisor. Invoked only by
		// ownerTickLauncher (via `flow owner tick-due`).
		return cmdOwnerTick(rest)
	case selfUpgradeArg:
		// Hidden: the detached automatic upgrade. Invoked only by
		// startDetachedSelfUpgrade.
		return cmdSelfUpgrade()
	case "upgrade":
		return cmdUpgrade(rest)
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
	case "connector":
		return cmdConnector(rest)
	case "human":
		return cmdHuman(rest)
	case "peer":
		return cmdPeer(rest)
	case "inbox":
		return cmdInbox(rest)
	case "broadcast", "post":
		return cmdBroadcast(rest)
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
  flow init [--harness claude|codex|prx]
  flow skill install [--force] [--harness claude|codex|prx]
  flow skill uninstall [--harness claude|codex|prx]
  flow skill update [--harness claude|codex|prx]
  (default: every present harness — binary on PATH or config home exists)
  flow upgrade [--check] [--force]             (install the latest release now; flow also upgrades
                                                itself after a command — FLOW_NO_AUTO_UPGRADE=1 stops that)

Create:
  flow add project "<name>" --work-dir <path> [--slug <s>] [--priority h|m|l] [--mkdir]
  flow add task    "<name>" [--slug <s>] [--project <slug>] [--work-dir <path>] [--mkdir] [--priority h|m|l] [--due <date>]
                   (from a task-bound session: records that task as parent, inherits its project)

Sessions:
  flow do                <ref> [--harness claude|codex|prx] [--fresh] [--dangerously-skip-permissions]
  flow do --auto         <ref> [--harness claude|codex|prx] [--tab] (run headlessly in the background; self-completes via flow done;
                          --tab runs it as a visible interactive tab instead — default from FLOW_AUTO_TAB=1)
  flow auto "<name>" --brief <file|-> [--slug <s>] [--priority h|m|l] [--work-dir <path>] [--mkdir] [--harness claude|codex|prx] [--tab]
                         (one-shot: add task + write brief + do --auto; from a driver session the child
                          gets it as parent and is auto-watched; refused inside an auto run)
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
                            [--project <slug>] [--clear-project]
  flow update project <ref> [--priority h|m|l] [--terminal-group <name>] [--clear-terminal-group]
  flow do        <ref> [--harness claude|codex|prx] [--fresh] [--dangerously-skip-permissions] [--force]   (choose harness only for a new task session; --force overrides the live-session guard)
  flow do --here <ref> [--force]                                              (bind THIS harness session to the task; --force overwrites a prior binding)
  flow archive   <ref>
  flow unarchive <ref>

Message bus (directed + broadcast, CLI-only — see flow skill §4.18):
  flow message <assignee>[/<task-slug>] "<body>" [--urgent] [--options "A|B"] [--fyi] [--attach <file>]
                                     (message a human — "user" is the local human — or the
                                      session bound to a task; never your own address; alias: msg.
                                      Flags may come in any order; the body is positional or
                                      --body "<text>". --options = answer choices, --fyi = never
                                      ping; both only for mail to the human. --attach copies a
                                      file in, repeatable: 25 MiB/file, 100 MiB, 20 files.)
  flow broadcast "<one-liner>"       (fan out to whoever watches this task/project/assignee; alias: post)
  flow inbox [--as <assignee>] [--json]           (what's pending; --as user = the human's queue)
  flow inbox pop [--wait] [--timeout <s>] [--as <assignee>] [--json]
                                     (THE consumption API: atomically consume the oldest — answers,
                                      delivers, clears; loop it freely; --wait blocks until mail)
  flow inbox stats                   (wait-time metrics)
  flow watch <task|project|assignee> [--as <assignee>] | --list | --rm <target>

Human connectors (opt-in pings for asks to the human — skill references/connectors.md):
  flow connector add <dir|sample>    (validate, install, store its secret, send a test notification;
                                      <sample> = a built-in name such as macos — no checkout needed)
  flow connector list
  flow connector list --available    (built-in sample connectors)
  flow connector remove <name>       (delete the connector and its secret)
  flow connector test [<name>]
  flow connector priority <name> <n>|--clear   (ping order: lower n first, next tier only on failure)
  flow connector escalate <dur>|off  (unseen after <dur> → ping the next tier; max 24h)
  flow human reply <id> "<text>"     (connectors only, FLOW_CONNECTOR_TOKEN: answer → asking task's inbox)
  flow human seen <id>               (connectors only: mark seen without answering)

Peer hubs (other flow homes, via a role-"peer" connector — skill references/peers.md):
  flow peer add <name> --via <connector> --at <endpoint>
  flow peer list | show <name> | rm <name> | block <name> | unblock <name>
  flow message <peer> "<body>" [--options "A|B"] [--urgent] [--fyi] [--reply-to <id>] [--attach <file>]
                                     (to another hub's human, ≤4000 chars; never <peer>/<task>)
  flow peer deliver --at <endpoint> --ref <their-id> [--reply-to-ref <our-id>] [--to <task>]
                    [--kind ask|fyi] [--options "A|B"] [--attach <file>] "<body>"   (connectors only;
                                      --to = the local task this mail lands in)

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
