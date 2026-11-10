package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Folder trust.
//
// Claude Code gates every interactive session — including `claude --bg`
// background agents — behind a "Do you trust the files in this folder?"
// dialog the first time it runs in a directory. The answer is stored in
// the global config (~/.claude.json, or $CLAUDE_CONFIG_DIR/.claude.json)
// as projects[<abs path>].hasTrustDialogAccepted. A background session
// started in a never-trusted folder has nobody to accept the dialog, so
// it sits there forever and does no work — silently.
//
// Claude Code has no CLI flag to pre-accept trust for an interactive or
// --bg session (`-p` mode skips the dialog, but --bg sessions do not), so
// flow writes the same key Claude writes when a human clicks "Yes",
// scoped to exactly the task's work_dir.

// trustKey is the per-project field Claude Code sets when the folder
// trust dialog is accepted.
const trustKey = "hasTrustDialogAccepted"

// claudeConfigPath mirrors Claude Code's own resolution of its global
// config file: $CLAUDE_CONFIG_DIR/.claude.json when set, else
// ~/.claude.json.
func claudeConfigPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("read home dir: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

// EnsureTrusted marks dir as trusted for Claude Code so a session
// launched there without a human does not stall on the folder-trust
// dialog.
//
//   - Exactly dir is trusted — never a parent. Refuses relative paths,
//     "/", $HOME and any ancestor of $HOME, since trust there would cover
//     everything beneath.
//   - No-op (file untouched) if dir or an ancestor is already trusted.
//   - No-op if the config file does not exist: Claude Code has never run
//     here, and fabricating its config would skip its first-run setup.
//   - Errors (without modifying anything) if the file is not valid JSON
//     or `projects` is not an object — never clobber the user's config.
//   - Writes atomically: temp file in the same dir + rename, preserving
//     every other key and the file mode. The file is re-read immediately
//     before the write to keep the window for racing a concurrent writer
//     (Claude Code rewrites this file often) as small as possible.
func EnsureTrusted(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return fmt.Errorf("refusing to trust non-absolute path %q", dir)
	}
	dirs := []string{filepath.Clean(dir)}
	// Claude keys projects by its process cwd, which the OS reports with
	// symlinks resolved (macOS: /var → /private/var). Trust that spelling
	// too when it differs.
	if resolved, err := filepath.EvalSymlinks(dirs[0]); err == nil && resolved != dirs[0] {
		dirs = append(dirs, resolved)
	}
	for _, d := range dirs {
		if err := checkTrustable(d); err != nil {
			return err
		}
	}

	cfg, err := claudeConfigPath()
	if err != nil {
		return err
	}

	// Bounded retries: if the file changed between our read and our
	// rename (a concurrent writer), redo the read-modify-write on the
	// fresh content.
	for attempt := 0; attempt < 3; attempt++ {
		done, err := ensureTrustedOnce(cfg, dirs)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return fmt.Errorf("%s kept changing concurrently while marking %s trusted", cfg, dir)
}

// checkTrustable refuses paths whose trust would cover far more than one
// task's folder: "/", $HOME, and any ancestor of $HOME.
func checkTrustable(dir string) error {
	if dir == string(filepath.Separator) {
		return fmt.Errorf("refusing to trust %q: too broad", dir)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	homes := []string{filepath.Clean(home)}
	if r, err := filepath.EvalSymlinks(homes[0]); err == nil && r != homes[0] {
		homes = append(homes, r)
	}
	for _, h := range homes {
		if dir == h || isAncestor(dir, h) {
			return fmt.Errorf("refusing to trust %q: it is your home directory or one of its parents", dir)
		}
	}
	return nil
}

// ensureTrustedOnce performs one read-modify-write. It returns done=true
// when every dir is (now) trusted on disk, or the file is absent.
func ensureTrustedOnce(cfg string, dirs []string) (bool, error) {
	raw, err := os.ReadFile(cfg)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", cfg, err)
	}
	top, projects, err := decodeClaudeConfig(cfg, raw)
	if err != nil {
		return false, err
	}
	changed := false
	for _, dir := range dirs {
		if isTrusted(projects, dir) {
			continue
		}
		entry := map[string]json.RawMessage{}
		if prev, ok := projects[dir]; ok && string(bytes.TrimSpace(prev)) != "null" {
			if err := json.Unmarshal(prev, &entry); err != nil {
				return false, fmt.Errorf("%s: projects[%q] is not an object (left untouched): %w", cfg, dir, err)
			}
			if entry == nil {
				entry = map[string]json.RawMessage{}
			}
		}
		entry[trustKey] = json.RawMessage("true")
		entryRaw, err := json.Marshal(entry)
		if err != nil {
			return false, err
		}
		projects[dir] = entryRaw
		changed = true
	}
	if !changed {
		return true, nil
	}
	projRaw, err := json.Marshal(projects)
	if err != nil {
		return false, err
	}
	top["projects"] = projRaw
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return false, err
	}

	mode := os.FileMode(0o600)
	if st, err := os.Stat(cfg); err == nil {
		mode = st.Mode().Perm()
	}

	// Re-read right before replacing: if another writer landed since our
	// read, start over on its content rather than overwrite it.
	if again, err := os.ReadFile(cfg); err != nil || !bytes.Equal(again, raw) {
		return false, nil
	}
	if err := atomicWrite(cfg, out, mode); err != nil {
		return false, err
	}

	// Verify: a writer racing the rename may have replaced our file.
	after, err := os.ReadFile(cfg)
	if err != nil {
		return false, nil
	}
	_, p, err := decodeClaudeConfig(cfg, after)
	if err != nil {
		return false, nil
	}
	for _, dir := range dirs {
		if !isTrusted(p, dir) {
			return false, nil
		}
	}
	return true, nil
}

func decodeClaudeConfig(cfg string, raw []byte) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON (left untouched): %w", cfg, err)
	}
	if top == nil {
		return nil, nil, fmt.Errorf("%s is not a JSON object (left untouched)", cfg)
	}
	projects := map[string]json.RawMessage{}
	if p, ok := top["projects"]; ok && string(bytes.TrimSpace(p)) != "null" {
		if err := json.Unmarshal(p, &projects); err != nil {
			return nil, nil, fmt.Errorf("%s: \"projects\" is not an object (left untouched): %w", cfg, err)
		}
		if projects == nil {
			projects = map[string]json.RawMessage{}
		}
	}
	return top, projects, nil
}

// isTrusted reports whether dir or any of its ancestors carries
// hasTrustDialogAccepted=true — Claude Code treats trust of a parent
// folder as covering its children.
func isTrusted(projects map[string]json.RawMessage, dir string) bool {
	for p := dir; ; p = filepath.Dir(p) {
		if raw, ok := projects[p]; ok {
			var e struct {
				Accepted bool `json:"hasTrustDialogAccepted"`
			}
			if json.Unmarshal(raw, &e) == nil && e.Accepted {
				return true
			}
		}
		if parent := filepath.Dir(p); parent == p {
			return false
		}
	}
}

// isAncestor reports whether a is a strict ancestor directory of b.
func isAncestor(a, b string) bool {
	if a == b {
		return false
	}
	if a == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(b, a+string(filepath.Separator))
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".flow-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// PrepareWorkDir implements harness.WorkDirPreparer: before flow launches
// a session nobody is watching in workDir, make sure Claude Code will not
// park it on the folder-trust dialog.
func (c *claude) PrepareWorkDir(workDir string) error {
	if err := EnsureTrusted(workDir); err != nil {
		return fmt.Errorf("could not mark %s as trusted for Claude Code (a background session there would stall on the folder-trust prompt): %w — open `claude` in that folder once and accept the trust prompt, then retry", workDir, err)
	}
	return nil
}
