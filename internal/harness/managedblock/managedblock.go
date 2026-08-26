// Package managedblock owns a delimited region inside a file flow does
// not otherwise control.
//
// It exists because most coding agents have no skill directory to write
// into, but every one of them reads an ambient instructions file
// (AGENTS.md, GEMINI.md, .cursorrules). Owning a marked region of that
// file lets flow point the agent at its skill and ask it to invoke
// flow's hooks — without touching a byte the user wrote around it.
//
// The shape is already a de-facto convention: tools in the wild write
//
//	<!-- toolname:managed:start -->
//	...generated...
//	<!-- toolname:managed:end -->
//
// into exactly these files. This package makes flow a well-behaved
// citizen of that convention rather than inventing a third one.
//
// Every operation is idempotent and content-preserving: Apply on an
// unchanged block is a no-op that does not rewrite the file, Apply on a
// changed block replaces only the region between markers, and Remove
// takes the markers with it and leaves the rest untouched.
package managedblock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Comment names the comment syntax used for the marker lines. The right
// one depends on the file being edited, not on flow.
type Comment string

const (
	// HTML suits Markdown instruction files (AGENTS.md, GEMINI.md).
	HTML Comment = "html"
	// Hash suits TOML, YAML, INI and shell-style config.
	Hash Comment = "hash"
	// Slash suits JSONC and other C-family config.
	Slash Comment = "slash"
)

// Valid reports whether c is a comment syntax this package can render,
// so a manifest can be rejected at load rather than at install.
func Valid(c Comment) bool {
	switch c {
	case HTML, Hash, Slash:
		return true
	}
	return false
}

// markers renders the start/end sentinels for a marker name.
func markers(c Comment, name string) (start, end string) {
	switch c {
	case Hash:
		return fmt.Sprintf("# %s:start", name), fmt.Sprintf("# %s:end", name)
	case Slash:
		return fmt.Sprintf("// %s:start", name), fmt.Sprintf("// %s:end", name)
	default:
		return fmt.Sprintf("<!-- %s:start -->", name), fmt.Sprintf("<!-- %s:end -->", name)
	}
}

// Block describes one managed region.
type Block struct {
	// Path is the file to edit. Created (with parent dirs) if absent.
	Path string
	// Name identifies the region, e.g. "flow:managed". Two tools with
	// different names coexist in one file without interfering.
	Name string
	// Comment selects the marker syntax.
	Comment Comment
}

// Apply writes body into the managed region, creating the file or
// appending the region when either is missing.
//
// Returns changed=false when the file already contained exactly this
// content — callers use that to stay quiet instead of reporting an
// install that changed nothing.
func (b Block) Apply(body string) (changed bool, err error) {
	start, end := markers(b.Comment, b.Name)
	region := start + "\n" + strings.TrimRight(body, "\n") + "\n" + end

	existing, err := os.ReadFile(b.Path)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(b.Path), 0o755); err != nil {
			return false, err
		}
		return true, os.WriteFile(b.Path, []byte(region+"\n"), 0o644)
	case err != nil:
		return false, err
	}

	before, after, found, splitErr := split(string(existing), start, end)
	if splitErr != nil {
		return false, fmt.Errorf("managed block %q has unmatched markers in %s; repair the file manually: %w", b.Name, b.Path, splitErr)
	}
	if !found {
		// Append, keeping exactly one blank line between the user's
		// content and ours.
		text := strings.TrimRight(string(existing), "\n")
		if text != "" {
			text += "\n\n"
		}
		return true, os.WriteFile(b.Path, []byte(text+region+"\n"), 0o644)
	}

	updated := before + region + after
	if updated == string(existing) {
		return false, nil
	}
	return true, os.WriteFile(b.Path, []byte(updated), 0o644)
}

// Remove deletes the managed region, markers included.
//
// Returns removed=false when there was nothing to remove. A file that
// consists only of flow's region is deleted outright rather than left
// behind empty; a file with other content keeps that content.
func (b Block) Remove() (removed bool, err error) {
	start, end := markers(b.Comment, b.Name)
	existing, err := os.ReadFile(b.Path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	before, after, found, splitErr := split(string(existing), start, end)
	if splitErr != nil {
		return false, fmt.Errorf("managed block %q has unmatched markers in %s; repair the file manually: %w", b.Name, b.Path, splitErr)
	}
	if !found {
		return false, nil
	}
	rest := strings.TrimSpace(before + after)
	if rest == "" {
		return true, os.Remove(b.Path)
	}
	return true, os.WriteFile(b.Path, []byte(rest+"\n"), 0o644)
}

// Present reports whether the managed region currently exists.
func (b Block) Present() bool {
	start, end := markers(b.Comment, b.Name)
	data, err := os.ReadFile(b.Path)
	if err != nil {
		return false
	}
	_, _, found, splitErr := split(string(data), start, end)
	return splitErr == nil && found
}

// split cuts text into the parts before and after one well-formed managed
// region. Any unmatched, duplicated, or out-of-order owned marker is an
// error: flow cannot infer the intended boundary without risking user data.
func split(text, start, end string) (before, after string, found bool, err error) {
	startCount := strings.Count(text, start)
	endCount := strings.Count(text, end)
	if startCount == 0 && endCount == 0 {
		return "", "", false, nil
	}
	if startCount != 1 || endCount != 1 {
		return "", "", false, fmt.Errorf("found %d start marker(s) and %d end marker(s)", startCount, endCount)
	}
	i := strings.Index(text, start)
	j := strings.Index(text, end)
	if j < i {
		return "", "", false, fmt.Errorf("end marker appears before start marker")
	}
	before = text[:i]
	after = text[j+len(end):]
	return before, after, true, nil
}
