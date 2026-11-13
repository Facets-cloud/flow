package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"flow/internal/harness"
)

const locSID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

// writeSessionJSONL writes a transcript under projectDir (a directory name
// under ~/.claude/projects) whose records report cwds as their cwd.
func writeSessionJSONL(t *testing.T, home, projectDir, sid string, cwds ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", projectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session-owner"}` + "\n"
	for _, cwd := range cwds {
		rec, err := json.Marshal(map[string]string{"type": "user", "cwd": cwd})
		if err != nil {
			t.Fatal(err)
		}
		body += string(rec) + "\n"
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Claude keys a transcript on the resolved cwd, so a symlinked work_dir
// (or /tmp on macOS) must validate against the directory it points at.
func TestValidateSessionResolvesSymlinkedWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	writeSessionJSONL(t, home, EncodeCwd(real), locSID, real)

	h := New()
	if err := h.ValidateSession(link, locSID); err != nil {
		t.Errorf("ValidateSession(symlink) = %v, want nil", err)
	}
	if err := h.ValidateSession(real, locSID); err != nil {
		t.Errorf("ValidateSession(real) = %v, want nil", err)
	}
	if err := h.ValidateSession(base, locSID); err == nil {
		t.Error("ValidateSession(unrelated dir) = nil, want error")
	}
}

func TestLocateSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	loc, ok := New().(harness.SessionLocator)
	if !ok {
		t.Fatal("claude harness must implement harness.SessionLocator")
	}

	t.Run("no transcript", func(t *testing.T) {
		if cwd, found := loc.LocateSession("ffffffff-0000-4000-8000-000000000000"); found || cwd != "" {
			t.Errorf("got (%q, %v), want (\"\", false)", cwd, found)
		}
	})

	t.Run("reports the directory the session was started in", func(t *testing.T) {
		started := "/Users/x/some_repo/.cache"
		writeSessionJSONL(t, home, EncodeCwd(started), locSID, started)
		if cwd, found := loc.LocateSession(locSID); !found || cwd != started {
			t.Errorf("got (%q, %v), want (%q, true)", cwd, found, started)
		}
	})

	// A session that later moved (cd, worktree) records other cwds too;
	// only the one its project directory is keyed on counts.
	t.Run("ignores cwds the transcript is not keyed on", func(t *testing.T) {
		const sid = "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		started := "/Users/x/flow"
		writeSessionJSONL(t, home, EncodeCwd(started), sid, "/Users/x/flow/.claude/worktrees/wt", started)
		if cwd, found := loc.LocateSession(sid); !found || cwd != started {
			t.Errorf("got (%q, %v), want (%q, true)", cwd, found, started)
		}
	})

	// The bg/worktree case: the transcript sits under the parent repo's
	// project dir but every record reports the worktree. The session
	// exists; where to start it from is not knowable.
	t.Run("found but directory unknown", func(t *testing.T) {
		const sid = "cccccccc-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		writeSessionJSONL(t, home, "-Users-x-parent", sid, "/Users/x/parent/.claude/worktrees/wt")
		if cwd, found := loc.LocateSession(sid); !found || cwd != "" {
			t.Errorf("got (%q, %v), want (\"\", true)", cwd, found)
		}
	})
}
