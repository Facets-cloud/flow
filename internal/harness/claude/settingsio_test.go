package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The strip's rewrite keeps the user's key order, number literals and
// unescaped characters, and only drops flow's command.
func TestUninstallHookPreservesOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	in := `{
  "zeta": 1.50,
  "model": "opus",
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "flow hook stop"}, {"type": "command", "command": "a && b > c"}]}
    ],
    "PreToolUse": [{"matcher": "Bash", "hooks": []}]
  },
  "alpha": {"y": 2, "x": 1},
  "empty": {},
  "list": []
}
`
	_ = os.WriteFile(sp, []byte(in), 0o600)
	removed, err := uninstallHook("Stop", "flow hook stop")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	got, _ := os.ReadFile(sp)
	want := `{
  "zeta": 1.50,
  "model": "opus",
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "a && b > c"
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": []
      }
    ]
  },
  "alpha": {
    "y": 2,
    "x": 1
  },
  "empty": {},
  "list": []
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if fi, _ := os.Stat(sp); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode changed: %v", fi.Mode())
	}
	// Nothing to remove: the file is untouched.
	if removed, _ := uninstallHook("Stop", "flow hook stop"); removed {
		t.Error("second removal must be a no-op")
	}
}

// A symlinked settings.json is written through, and no temp files linger.
func TestWriteFileAtomicSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(target, []byte("{}\n"), 0o640)
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := writeFileAtomic(link, []byte(`{"a":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced by a file")
	}
	if b, _ := os.ReadFile(target); string(b) != `{"a":1}`+"\n" {
		t.Errorf("target = %q", b)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", fi.Mode())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".flow-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestDecodeOrderedRejectsTrailing(t *testing.T) {
	if _, err := decodeOrdered([]byte(`{} {}`)); err == nil {
		t.Error("trailing data must fail")
	}
}
