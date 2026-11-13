package app

import (
	"fmt"
	"os"
	"path/filepath"

	"flow/internal/harness"
	"flow/internal/trust"
)

// applyWorkspaceTrust pre-accepts the harness's workspace-trust gate for
// workDir — but ONLY when workDir is a folder flow created (under
// $FLOW_ROOT/tasks and not a git repo; see trust.FlowOwnedRoot). User
// repos are never trusted by flow.
//
// Two mechanisms, one per harness shape:
//   - harnesses implementing harness.WorkspaceTruster (claude) persist
//     trust for the resolved $FLOW_ROOT/tasks — one entry covering every
//     flow workspace;
//   - opts.TrustDir is set to the resolved workDir so per-launch
//     harnesses (codex: `-c projects."<dir>".trust_level="trusted"`) can
//     trust it for that launch.
//
// Failure is a warning, never fatal: the spawn proceeds and, at worst,
// hits the harness's own trust message.
func applyWorkspaceTrust(h harness.Harness, workDir string, opts *harness.LaunchOpts) {
	root, err := flowRoot()
	if err != nil {
		return
	}
	if _, ok := trust.FlowOwnedRoot(root, workDir); !ok {
		return
	}
	if opts != nil {
		if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
			opts.TrustDir = resolved
		}
	}
	if wt, ok := h.(harness.WorkspaceTruster); ok {
		if _, err := wt.TrustWorkspace(root, workDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: could not pre-trust flow workspace: %v\n", h.Name(), err)
		}
	}
}

// trustFlowTasksDir is the `flow init` half: pre-trust $FLOW_ROOT/tasks
// for every target harness that persists trust, so even the first spawn
// in a fresh workspace finds it trusted.
func trustFlowTasksDir(targets []harness.Harness, root string) {
	tasks := filepath.Join(root, "tasks")
	if resolved, ok := trust.FlowOwnedRoot(root, tasks); ok {
		tasks = resolved
	}
	for _, h := range targets {
		wt, ok := h.(harness.WorkspaceTruster)
		if !ok {
			continue
		}
		changed, err := wt.TrustWorkspace(root, tasks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: could not pre-trust %s: %v\n", h.Name(), tasks, err)
			continue
		}
		if changed {
			fmt.Printf("%s: trusted flow-created workspaces under %s\n", h.Name(), tasks)
		}
	}
}
