// Package app implements the flow CLI — personal task and Claude session
// manager backed by SQLite.
package app

import (
	"fmt"
	"os"
)

// Run is the entry point for the CLI. Returns an exit code.
func Run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return cmdInit(rest)
	case "add":
		return cmdAdd(rest)
	case "do":
		return cmdDo(rest)
	case "done":
		return cmdDone(rest)
	case "due":
		return cmdDue(rest)
	case "show":
		return cmdShow(rest)
	case "list":
		return cmdList(rest)
	case "edit":
		return cmdEdit(rest)
	case "archive":
		return cmdArchive(rest)
	case "unarchive":
		return cmdUnarchive(rest)
	case "priority":
		return cmdPriority(rest)
	case "waiting":
		return cmdWaiting(rest)
	case "workdir":
		return cmdWorkdir(rest)
	case "skill":
		return cmdSkill(rest)
	case "register-session":
		return cmdRegisterSession(rest)
	case "transcript":
		return cmdTranscript(rest)
	case "hook":
		return cmdHook(rest)
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
  flow register-session  [<slug>] [--force]    (run from inside an execution session to self-report its session_id)
  flow hook session-start                      (SessionStart hook handler — wire via ~/.claude/settings.json)

Read:
  flow show task       [<ref>]
  flow show project    [<ref>]
  flow transcript      [<ref>] [--compact]           (readable transcript from session jsonl)
  flow list tasks    [--status ...] [--project ...] [--priority ...] [--since ...] [--include-archived]
  flow list projects [--status ...] [--include-archived]

Edit / mutate:
  flow edit      <ref>
  flow due       <ref> <date> | --clear                    (set or clear due date; date: YYYY-MM-DD, today, tomorrow, monday, 3d)
  flow priority  <ref> high|medium|low
  flow waiting   <ref> "<who or what>" | --clear
  flow archive   <ref>
  flow unarchive <ref>

Workdirs:
  flow workdir list
  flow workdir add <path> [--name <nickname>]
  flow workdir remove <path>
  flow workdir scan [<root>] [--add]`)
}
