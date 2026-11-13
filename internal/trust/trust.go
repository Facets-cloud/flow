// Package trust pre-accepts agent-CLI "workspace trust" for the folders
// flow itself creates — and only those.
//
// Why: Claude Code gates every interactive/background session on a
// per-folder trust dialog. A headless `claude --bg` (FLOW_TERM=bg, the
// flow-mesh desks) has no TTY to show the dialog and exits with
// "Workspace not trusted". flow-created work dirs
// ($FLOW_ROOT/tasks/<slug>/workspace from `--mkdir` / floating tasks) are
// brand-new folders nobody ever opened, so they always hit this.
//
// Claude stores trust as projects["<dir>"].hasTrustDialogAccepted=true in
// ~/.claude.json (or $CLAUDE_CONFIG_DIR/.claude.json). The key is the git
// repo root when the cwd is inside a repo, else the (symlink-resolved)
// start dir; trust on a non-repo folder covers its subfolders. No flag,
// env var or setting pre-accepts it (verified: --dangerously-skip-
// permissions does not), so flow writes the entry itself.
//
// Policy: flow trusts exactly one folder — the resolved $FLOW_ROOT/tasks —
// which covers every flow-created workspace. It never trusts a folder that
// is (or is inside) a git repo, and never anything outside $FLOW_ROOT/tasks
// — user repos are the user's call.
package trust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ClaudeConfigPath returns the file Claude Code keeps per-project state
// (including trust) in: $CLAUDE_CONFIG_DIR/.claude.json when set, else
// ~/.claude.json.
func ClaudeConfigPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

// resolve returns the absolute, symlink-resolved form of p. Claude keys
// trust by the resolved path (/tmp/x is stored as /private/tmp/x on
// macOS), so an unresolved key would silently not match.
func resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// inGitRepo reports whether dir or any ancestor holds a .git entry
// (directory, or file for worktrees/submodules).
func inGitRepo(dir string) bool {
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// FlowOwnedRoot decides whether workDir is a folder flow created and, if
// so, returns the single folder to trust: the resolved <flowRoot>/tasks.
// ok is false for anything outside <flowRoot>/tasks, for any workDir that
// is (or is inside) a git repo — Claude would key trust by that repo's
// root, which is not flow's to grant — and when either path can't be
// resolved (e.g. doesn't exist yet).
func FlowOwnedRoot(flowRoot, workDir string) (string, bool) {
	if flowRoot == "" || workDir == "" {
		return "", false
	}
	tasks, err := resolve(filepath.Join(flowRoot, "tasks"))
	if err != nil {
		return "", false
	}
	wd, err := resolve(workDir)
	if err != nil {
		return "", false
	}
	if wd != tasks && !strings.HasPrefix(wd, tasks+string(filepath.Separator)) {
		return "", false
	}
	if inGitRepo(wd) {
		return "", false
	}
	return tasks, true
}

// EnsureClaudeWorkspaceTrusted trusts <flowRoot>/tasks in cfgPath iff
// workDir is flow-owned (see FlowOwnedRoot). A non-flow workDir is a
// silent no-op (changed=false, err=nil).
func EnsureClaudeWorkspaceTrusted(cfgPath, flowRoot, workDir string) (bool, error) {
	root, ok := FlowOwnedRoot(flowRoot, workDir)
	if !ok {
		return false, nil
	}
	return EnsureClaudeTrusted(cfgPath, root)
}

// lockPath is a per-config lock file kept out of $HOME (so flow never
// litters next to ~/.claude.json).
func lockPath(cfgPath string) string {
	sum := sha256.Sum256([]byte(cfgPath))
	return filepath.Join(os.TempDir(), "flow-claude-config-"+hex.EncodeToString(sum[:6])+".lock")
}

// EnsureClaudeTrusted sets projects[dir].hasTrustDialogAccepted=true in
// the Claude config at cfgPath. It is careful because Claude rewrites the
// same file from running sessions:
//
//   - an flock serializes concurrent flow processes;
//   - the file is parsed with UseNumber and every other key — top-level
//     and inside the target project entry — is preserved;
//   - a file that fails to parse (or has an unexpected shape) is left
//     byte-for-byte untouched and a clear error names it;
//   - an already-trusted dir is a no-op (no rewrite);
//   - the write is atomic (temp file in the same dir + rename) with the
//     original permissions, then re-read to verify; if a concurrent Claude
//     write clobbered it, it retries.
//
// A symlinked config is written through to its target. Returns
// changed=true iff the file was modified.
func EnsureClaudeTrusted(cfgPath, dir string) (bool, error) {
	lf, err := os.OpenFile(lockPath(cfgPath), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", cfgPath, err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return false, fmt.Errorf("lock %s: %w", cfgPath, err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	target := cfgPath
	if r, err := filepath.EvalSymlinks(cfgPath); err == nil {
		target = r
	}

	const attempts = 3
	changed := false
	for i := 0; i < attempts; i++ {
		cfg, perm, err := load(target)
		if err != nil {
			return changed, fmt.Errorf("claude config %s: %w (left untouched)", cfgPath, err)
		}
		entry, err := projectEntry(cfg, dir)
		if err != nil {
			return changed, fmt.Errorf("claude config %s: %w (left untouched)", cfgPath, err)
		}
		if v, _ := entry["hasTrustDialogAccepted"].(bool); v {
			return changed, nil
		}
		entry["hasTrustDialogAccepted"] = true
		if err := writeAtomic(target, cfg, perm); err != nil {
			return changed, fmt.Errorf("write claude config %s: %w", cfgPath, err)
		}
		changed = true
		// Loop re-reads: verifies the write, and retries if a running
		// Claude session rewrote the file over ours in between.
	}
	cfg, _, err := load(target)
	if err == nil {
		if entry, e := projectEntry(cfg, dir); e == nil {
			if v, _ := entry["hasTrustDialogAccepted"].(bool); v {
				return changed, nil
			}
		}
	}
	return changed, fmt.Errorf("claude config %s: trust for %s did not persist (concurrent writer?)", cfgPath, dir)
}

// load parses the config. A missing file is an empty object with 0600
// perms (what Claude itself uses).
func load(path string) (map[string]any, os.FileMode, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	perm := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, perm, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, 0, fmt.Errorf("not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, 0, fmt.Errorf("not valid JSON: trailing data")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("top level is not a JSON object")
	}
	return m, perm, nil
}

// projectEntry returns (creating if absent) the mutable projects[dir]
// object, refusing unexpected shapes rather than overwriting them.
func projectEntry(cfg map[string]any, dir string) (map[string]any, error) {
	raw, ok := cfg["projects"]
	if !ok || raw == nil {
		raw = map[string]any{}
		cfg["projects"] = raw
	}
	projects, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`"projects" is not a JSON object`)
	}
	e, ok := projects[dir]
	if !ok || e == nil {
		e = map[string]any{}
		projects[dir] = e
	}
	entry, ok := e.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`projects[%q] is not a JSON object`, dir)
	}
	return entry, nil
}

func writeAtomic(path string, cfg map[string]any, perm os.FileMode) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".flow-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
