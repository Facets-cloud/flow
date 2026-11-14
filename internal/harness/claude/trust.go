package claude

import "flow/internal/trust"

// TrustWorkspace implements harness.WorkspaceTruster: when workDir is a
// flow-created folder, mark the resolved $FLOW_ROOT/tasks as trusted in
// Claude's config (~/.claude.json or $CLAUDE_CONFIG_DIR/.claude.json).
// One entry covers every flow workspace. User repos are never touched.
func (c *claude) TrustWorkspace(flowRoot, workDir string) (bool, error) {
	cfg, err := trust.ClaudeConfigPath()
	if err != nil {
		return false, err
	}
	return trust.EnsureClaudeWorkspaceTrusted(cfg, flowRoot, workDir)
}
