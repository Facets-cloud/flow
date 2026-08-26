package harness

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SkillVersionFile is the sidecar flow writes beside an installed skill
// to record which binary version produced it. It is not part of the
// skill tree, so SyncTree preserves it.
const SkillVersionFile = "VERSION"

// SyncTree makes dir match files exactly: every file in the tree is
// written, and any OTHER file already under dir is removed, except the
// names listed in keep.
//
// Removal is the point. An install that only ever writes leaves an
// orphan behind on every rename, so a skill directory accumulates files
// from corpus versions that no longer exist — and an agent browsing
// references/ cannot tell which are current. Flow exists partly to kill
// exactly that kind of drift, so its own installer must not create it.
//
// This is safe because a skill directory is a wholly generated
// artifact: its contents come from the embedded corpus plus the VERSION
// sidecar, and `flow skill install --force` / `flow skill update`
// already overwrite everything in it. Callers that do NOT own the
// directory must not call this.
//
// Both the hand-written and the manifest-driven adapters share this so
// their installed trees cannot diverge — a property the claude
// differential test asserts directly.
func SyncTree(files fs.FS, dir string, keep ...string) error {
	want := map[string]bool{}
	for _, k := range keep {
		want[filepath.Clean(k)] = true
	}

	if err := fs.WalkDir(files, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(files, rel)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", rel, err)
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		want[filepath.Clean(filepath.FromSlash(rel))] = true
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	}); err != nil {
		return err
	}

	return pruneOrphans(dir, want)
}

// pruneOrphans removes files under dir whose path (relative to dir) is
// not in want, then drops directories left empty.
//
// A dir that does not exist has nothing to prune — that is a no-op, not
// an error. It happens whenever the source tree is empty (nothing was
// written, so nothing created the directory either).
func pruneOrphans(dir string, want map[string]bool) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	var emptyCandidates []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			emptyCandidates = append(emptyCandidates, path)
			return nil
		}
		if want[filepath.Clean(rel)] {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Deepest first, so a directory emptied by the pass above is seen
	// as empty when its turn comes.
	for i := len(emptyCandidates) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(emptyCandidates[i])
		if err == nil && len(entries) == 0 {
			_ = os.Remove(emptyCandidates[i])
		}
	}
	return nil
}
