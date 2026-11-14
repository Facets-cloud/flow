package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness"
)

func TestClaudeIsWorkspaceTruster(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "flow")
	ws := filepath.Join(root, "tasks", "t", "workspace")
	os.MkdirAll(ws, 0o755)

	wt, ok := New().(harness.WorkspaceTruster)
	if !ok {
		t.Fatal("claude must implement harness.WorkspaceTruster")
	}
	changed, err := wt.TrustWorkspace(root, ws)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(b), filepath.Join(root, "tasks")) {
		t.Fatalf("tasks root not trusted:\n%s", b)
	}
}
