// Package app — playbook run command and helpers.
package app

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"flow/internal/flowdb"
)

// cmdRun handles `flow run <subcommand>`. Currently only `run playbook <slug>` is supported.
func cmdRun(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: run requires a subcommand (playbook)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "playbook":
		return cmdRunPlaybook(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown run subcommand %q\n", sub)
		return 2
	}
}

func cmdRunPlaybook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: run playbook requires a slug")
		return 2
	}
	slug := args[0]
	fs := flagSet("run playbook")
	dangerSkip := fs.Bool("dangerously-skip-permissions", false, "pass --dangerously-skip-permissions through to claude")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	pb, err := ResolvePlaybook(db, slug, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	pbBriefPath := filepath.Join(root, "playbooks", pb.Slug, "brief.md")
	pbBriefBytes, err := os.ReadFile(pbBriefPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read playbook brief %s: %v\n", pbBriefPath, err)
		return 1
	}

	runSlug, err := generateRunSlug(db, pb.Slug, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Insert the run-task row.
	now := flowdb.NowISO()
	_, err = db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, kind, playbook_slug, priority, work_dir, status_changed_at, created_at, updated_at)
		 VALUES (?, ?, ?, 'backlog', 'playbook_run', ?, 'medium', ?, ?, ?, ?)`,
		runSlug,
		fmt.Sprintf("%s run %s", pb.Slug, runSlug),
		pb.ProjectSlug,
		pb.Slug,
		pb.WorkDir,
		now, now, now,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: insert run task: %v\n", err)
		return 1
	}

	// Materialize tasks/<run-slug>/ and snapshot brief.md.
	runDir := filepath.Join(root, "tasks", runSlug)
	if err := os.MkdirAll(filepath.Join(runDir, "updates"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", runDir, err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(runDir, "brief.md"), pbBriefBytes, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: write run brief.md: %v\n", err)
		return 1
	}

	// Close our DB handle so cmdDo can re-open it (cmdDo opens its own).
	db.Close()

	// Delegate to cmdDo to spawn the session.
	doArgs := []string{runSlug}
	if *dangerSkip {
		doArgs = append(doArgs, "--dangerously-skip-permissions")
	}
	return cmdDo(doArgs)
}

// generateRunSlug computes the unique slug for a new playbook run.
//
// Cascade:
//
//  1. <pb>--YYYY-MM-DD-HH-MM             (default; minute precision)
//  2. <pb>--YYYY-MM-DD-HH-MM-SS          (on minute collision)
//  3. <pb>--YYYY-MM-DD-HH-MM-SS-N        (on second collision; N from 2)
//
// Existence is determined by SELECT slug FROM tasks WHERE slug = ?.
// Inputs use UTC to make slugs unambiguous across timezone changes.
func generateRunSlug(db *sql.DB, playbookSlug string, t time.Time) (string, error) {
	t = t.UTC()
	minute := fmt.Sprintf("%s--%04d-%02d-%02d-%02d-%02d",
		playbookSlug, t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
	if !runSlugExists(db, minute) {
		return minute, nil
	}
	second := fmt.Sprintf("%s-%02d", minute, t.Second())
	if !runSlugExists(db, second) {
		return second, nil
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", second, n)
		if !runSlugExists(db, candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("could not generate unique run slug after 1000 attempts")
}

// runSlugExists returns true iff a tasks row with the given slug exists.
// Checks all tasks (any kind) since slug is the primary key.
func runSlugExists(db *sql.DB, slug string) bool {
	var got string
	err := db.QueryRow(`SELECT slug FROM tasks WHERE slug = ?`, slug).Scan(&got)
	return err == nil
}
