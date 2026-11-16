package app

import (
	"os"
	"path/filepath"
	"strings"
)

// pinnedFlowEnv is the env every flow-spawned tab gets. The tab's login
// shell may put a different flow first on PATH (e.g. a harness-bundled
// build in ~/.local/bin), which would then run the session's hooks and its
// `flow done`. So the launcher's binary is pinned: its directory goes first
// on PATH and FLOW_BIN names it. $FLOW_ROOT is forwarded when set so the
// tab reads the same flow.db / kb / briefs as the launcher.
func pinnedFlowEnv() map[string]string {
	bin := flowBinPath()
	env := map[string]string{"FLOW_BIN": bin}
	if root := os.Getenv("FLOW_ROOT"); root != "" {
		env["FLOW_ROOT"] = root
	}
	if !filepath.IsAbs(bin) {
		return env
	}
	dir := filepath.Dir(bin)
	parts := []string{dir}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != "" && p != dir {
			parts = append(parts, p)
		}
	}
	env["PATH"] = strings.Join(parts, string(os.PathListSeparator))
	return env
}
