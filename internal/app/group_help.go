package app

import "fmt"

// groupUsage is what `flow <group> --help` prints. Each subcommand's own
// --help lists its flags.
var groupUsage = map[string]string{
	"add": `usage: flow add <project|task|playbook|owner> "<name>" [flags]
  flow add project  "<name>" --work-dir <path> [--slug <s>] [--priority h|m|l] [--mkdir]
  flow add task     "<name>" [--slug <s>] [--project <slug>] [--work-dir <path>] [--mkdir] [--priority h|m|l] [--due <date>]
  flow add playbook "<name>" --work-dir <path> [--slug <s>] [--project <slug>] [--mkdir]
  flow add owner    "<name>" --work-dir <path> [--every <dur>] [--slug <s>] [--project <slug>] [--mkdir]
  (a name that starts with a dash goes after "--": flow add task -- "-5 degrees fix")`,
	"run": `usage: flow run playbook <slug> [flags]
  flow run playbook <slug> [--dangerously-skip-permissions]   (spawn a new tab)
  flow run playbook <slug> --here                             (bind THIS harness session to the new run)
  flow run playbook <slug> --auto                             (run headlessly in the background)`,
	"show": `usage: flow show <task|project|playbook|owner> [<ref>]
  flow show task     [<ref>]   (no ref: the task bound to this session)
  flow show project  [<ref>]   (no ref: the bound task's project)
  flow show playbook <ref>
  flow show owner    <slug>`,
	"list": `usage: flow list <tasks|projects|playbooks|runs|tags|owners> [flags]
  flow list tasks     [--status ...] [--project ...] [--priority ...] [--tag <t>] [--since ...] [--include-archived]
  flow list projects  [--status ...] [--include-archived]
  flow list playbooks [--project <slug>] [--include-archived]
  flow list runs      [<playbook-slug>]
  flow list tags
  flow list owners`,
	"update": `usage: flow update <task|project> <ref> [flags]
  flow update task    <ref> [--status <s>] [--priority h|m|l] [--work-dir <path>] [--tag <t> ...] ...
  flow update project <ref> [--priority h|m|l] [--terminal-group <name>] [--clear-terminal-group]
  (flow update task --help lists every flag)`,
	"owner": `usage: flow owner <list|show|start|pause|tick|next|retire> [<slug>] [flags]
  flow owner list
  flow owner show    <slug>
  flow owner start|pause <slug>
  flow owner tick    <slug> [--auto]
  flow owner next    <slug> --in <dur> | --at <when>
  flow owner retire  <slug> [--delete]`,
	"workdir": `usage: flow workdir <list|add|remove|scan> [args]
  flow workdir list
  flow workdir add <path> [--name <nickname>]
  flow workdir remove <path>
  flow workdir scan [<root>] [--add]`,
	"skill": `usage: flow skill <install|uninstall|update> [flags]
  flow skill install [--force] [--harness claude|codex]
  flow skill uninstall [--harness claude|codex]
  flow skill update [--harness claude|codex]`,
	"hook": `usage: flow hook <session-start|user-prompt-submit|pre-tool-use|stop>
  flow hook session-start        (SessionStart hook handler)
  flow hook user-prompt-submit   (UserPromptSubmit hook handler)
  flow hook pre-tool-use         (PreToolUse hook handler)
  flow hook stop                 (Stop hook handler)
  (wired into the harness settings by flow skill install; not run by hand)`,
}

// groupHelp handles `flow <group> -h|--help`: it prints the group's usage
// and reports true, so the dispatcher returns 0 instead of answering
// "unknown subcommand".
func groupHelp(group string, args []string) bool {
	if len(args) == 0 || !isHelpArg(args[0]) {
		return false
	}
	fmt.Println(groupUsage[group])
	return true
}
