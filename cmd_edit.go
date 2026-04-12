package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cmdEdit opens a brief.md in $EDITOR for either a task or a project.
// Per spec §5.5. Bumps updated_at after a clean editor exit.
func cmdEdit(args []string) int {
	fs := flagSet("edit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: edit requires a ref")
		return 2
	}
	ref := fs.Arg(0)

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	kind, slug, err := resolveEditRef(db, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	var briefPath string
	switch kind {
	case "task":
		briefPath = filepath.Join(root, "tasks", slug, "brief.md")
	case "project":
		briefPath = filepath.Join(root, "projects", slug, "brief.md")
	}

	// Ensure the parent directory exists so the editor doesn't refuse.
	if err := os.MkdirAll(filepath.Dir(briefPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", filepath.Dir(briefPath), err)
		return 1
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	// Accept EDITOR values that contain flags ("emacs -nw"), splitting on
	// whitespace. This matches the convention of git and other tools.
	parts := strings.Fields(editor)
	parts = append(parts, briefPath)

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: editor exited non-zero: %v\n", err)
		return 1
	}

	now := nowISO()
	var updateErr error
	switch kind {
	case "task":
		_, updateErr = db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, now, slug)
	case "project":
		_, updateErr = db.Exec(`UPDATE projects SET updated_at = ? WHERE slug = ?`, now, slug)
	}
	if updateErr != nil {
		fmt.Fprintf(os.Stderr, "error: bump updated_at: %v\n", updateErr)
		return 1
	}
	fmt.Printf("Edited %s %s\n", kind, slug)
	return 0
}

// resolveEditRef resolves a ref that could be a task or a project.
// Supports task/ and project/ prefixes. Includes archived rows.
func resolveEditRef(db *sql.DB, ref string) (kind, slug string, err error) {
	return ResolveTaskOrProject(db, ref, true)
}
