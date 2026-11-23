package app

import (
	"os"
	"path/filepath"

	"flow/internal/spawner"
)

// executablePath is os.Executable, a seam for tests.
var executablePath = os.Executable

// tabFlowBinPath is the launching flow binary as invoked, deliberately NOT
// symlink-resolved: a tab can outlive a `brew upgrade` + cleanup, which
// deletes the versioned Cellar path a resolved symlink points at, while
// the /opt/homebrew/bin/flow link keeps working.
var tabFlowBinPath = func() string {
	p, err := executablePath()
	if err != nil || p == "" {
		return "flow"
	}
	return p
}

// pinnedFlowEnv is the env every flow-spawned tab gets: FLOW_BIN (the
// launcher binary) and FLOW_ROOT when set. PATH is not in it — copying the
// launcher's PATH would freeze it into the tab (and flood the no-terminal
// handoff); withFlowOnPath prepends just the flow dir instead.
func pinnedFlowEnv() map[string]string {
	env := map[string]string{"FLOW_BIN": tabFlowBinPath()}
	if root := os.Getenv("FLOW_ROOT"); root != "" {
		env["FLOW_ROOT"] = root
	}
	return env
}

// withFlowOnPath prefixes a tab command with PATH=<flowdir>:"$PATH" so the
// launching flow wins `flow` lookups (hooks, flow done) in the tab. The
// $PATH is expanded by the tab's own shell, so the rest of PATH is whatever
// the tab's login shell set, not a copy of the launcher's. Tab commands
// already assume a POSIX shell (ShellQuote's single-quote escaping). A
// relative bin (no dir to pin) leaves the command unchanged.
func withFlowOnPath(command string) string {
	bin := tabFlowBinPath()
	if !filepath.IsAbs(bin) {
		return command
	}
	return "PATH=" + spawner.ShellQuote(filepath.Dir(bin)) + `:"$PATH" ` + command
}
