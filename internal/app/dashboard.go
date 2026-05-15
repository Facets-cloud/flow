package app

import (
	"flow/internal/dashboard"
	"fmt"
	"os"
)

// cmdDashboard launches the read-only flow dashboard TUI. Renders
// counts, working/awaiting/stale task sections, and a git-log-style
// activity stream. Pressing enter on a selected task runs `flow do`
// behind the scenes, which spawns (or focuses) a terminal tab.
func cmdDashboard(args []string) int {
	fs := flagSet("dashboard")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "error: dashboard takes no positional arguments")
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := dashboard.Run(dbPath, root); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
