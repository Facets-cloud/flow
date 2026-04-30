package app

import (
	"database/sql"
	"flow/internal/flowdb"
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
	db, err := flowdb.OpenDB(dbPath)
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
	case "playbook":
		briefPath = filepath.Join(root, "playbooks", slug, "brief.md")
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

	now := flowdb.NowISO()
	var updateErr error
	switch kind {
	case "task":
		_, updateErr = db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, now, slug)
	case "project":
		_, updateErr = db.Exec(`UPDATE projects SET updated_at = ? WHERE slug = ?`, now, slug)
	case "playbook":
		_, updateErr = db.Exec(`UPDATE playbooks SET updated_at = ? WHERE slug = ?`, now, slug)
	}
	if updateErr != nil {
		fmt.Fprintf(os.Stderr, "error: bump updated_at: %v\n", updateErr)
		return 1
	}
	fmt.Printf("Edited %s %s\n", kind, slug)
	return 0
}

// resolveEditRef resolves a ref that could be a task, project, or playbook.
// Supports task/, project/, and playbook/ prefixes. Includes archived rows.
// On bare refs, tries each kind in order; errors on ambiguity.
func resolveEditRef(db *sql.DB, ref string) (kind, slug string, err error) {
	if strings.HasPrefix(ref, "task/") {
		t, err := ResolveTask(db, strings.TrimPrefix(ref, "task/"), true)
		if err != nil {
			return "", "", err
		}
		return "task", t.Slug, nil
	}
	if strings.HasPrefix(ref, "project/") {
		p, err := ResolveProject(db, strings.TrimPrefix(ref, "project/"), true)
		if err != nil {
			return "", "", err
		}
		return "project", p.Slug, nil
	}
	if strings.HasPrefix(ref, "playbook/") {
		pb, err := ResolvePlaybook(db, strings.TrimPrefix(ref, "playbook/"), true)
		if err != nil {
			return "", "", err
		}
		return "playbook", pb.Slug, nil
	}

	t, tErr := ResolveTask(db, ref, true)
	p, pErr := ResolveProject(db, ref, true)
	pb, pbErr := ResolvePlaybook(db, ref, true)

	matches := []string{}
	if tErr == nil {
		matches = append(matches, "task "+t.Slug)
	}
	if pErr == nil {
		matches = append(matches, "project "+p.Slug)
	}
	if pbErr == nil {
		matches = append(matches, "playbook "+pb.Slug)
	}

	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("no task, project, or playbook matching %q", ref)
	case 1:
		switch {
		case tErr == nil:
			return "task", t.Slug, nil
		case pErr == nil:
			return "project", p.Slug, nil
		default:
			return "playbook", pb.Slug, nil
		}
	default:
		return "", "", fmt.Errorf("ambiguous ref %q matches %s; use a prefix like task/, project/, or playbook/", ref, strings.Join(matches, ", "))
	}
}
