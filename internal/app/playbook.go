package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
)

const playbookBriefStub = `# %s

## What
*Fill in: one sentence describing what each run does.*

## Why
*Fill in: why this playbook exists.*

## Where
work_dir: %s

## Each run does
- *Fill in: steps that every invocation performs.*

## Out of scope
- *Fill in non-goals.*

## Signals to watch for
- *Fill in: signals that should change behavior or escalate.*

---
*Run with ` + "`flow run playbook %s`" + `. Each run gets its own session
and a snapshot of this brief at run time. Editing this file does not
retroactively change past runs.*
`

// addPlaybook implements `flow add playbook "<name>" [flags]`.
func addPlaybook(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "error: add playbook requires a name")
		return 2
	}
	name := args[0]
	fs := flagSet("add playbook")
	slugFlag := fs.String("slug", "", "short user-chosen slug (default: auto-generated from name)")
	project := fs.String("project", "", "parent project slug (optional)")
	workDir := fs.String("work-dir", "", "absolute path to the playbook's work directory (required)")
	mkdir := fs.Bool("mkdir", false, "create --work-dir if it does not exist")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if *workDir == "" {
		fmt.Fprintln(os.Stderr, "error: --work-dir is required for playbooks")
		return 2
	}
	abs, err := resolveWorkDir(*workDir, *mkdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	// Resolve project if supplied.
	var projectSlug sql.NullString
	if *project != "" {
		p, err := flowdb.GetProject(db, *project)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fmt.Fprintf(os.Stderr, "error: project %q not found\n", *project)
				return 1
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		projectSlug = sql.NullString{String: p.Slug, Valid: true}
	}

	var slug string
	if *slugFlag != "" {
		slug = *slugFlag
	} else {
		baseSlug, err := Slugify(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		slug, err = uniqueSlug(db, "playbooks", baseSlug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	pb := &flowdb.Playbook{
		Slug:        slug,
		Name:        name,
		ProjectSlug: projectSlug,
		WorkDir:     abs,
	}
	if err := flowdb.UpsertPlaybook(db, pb); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	pbDir := filepath.Join(root, "playbooks", slug)
	if err := os.MkdirAll(filepath.Join(pbDir, "updates"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	briefPath := filepath.Join(pbDir, "brief.md")
	if _, err := os.Stat(briefPath); os.IsNotExist(err) {
		stub := fmt.Sprintf(playbookBriefStub, name, abs, slug)
		if err := os.WriteFile(briefPath, []byte(stub), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	if err := flowdb.UpsertWorkdir(db, abs, "", "", ""); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("Created playbook %q at %s\n", slug, pbDir)
	fmt.Printf("Brief: %s\n", briefPath)
	if projectSlug.Valid {
		fmt.Printf("Project: %s\n", projectSlug.String)
	}
	fmt.Printf("Next: flow run playbook %s\n", slug)
	return 0
}
