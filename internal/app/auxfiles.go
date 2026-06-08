package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// enumerateAuxFiles returns absolute paths to top-level *.md files in dir,
// excluding brief.md. Subdirectories (notably updates/) are not descended.
// Returns ([], nil) for a missing or empty directory — callers can render
// "(none)" in that case.
//
// The result is sorted lexicographically so output is deterministic.
func enumerateAuxFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "brief.md" {
			continue
		}
		if filepath.Ext(name) != ".md" {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out, nil
}
