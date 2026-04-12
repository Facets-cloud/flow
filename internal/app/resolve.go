package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"strings"
)

// ---------- exact-match resolvers ----------
//
// All ref resolution is exact match on slug (case-insensitive).

// ResolveTask resolves a ref to exactly one task by slug.
// includeArchived controls whether archived rows are eligible.
func ResolveTask(db *sql.DB, ref string, includeArchived bool) (*flowdb.Task, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty task ref")
	}

	t, err := flowdb.GetTask(db, ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no task matching %q", ref)
		}
		return nil, err
	}
	if !includeArchived && t.ArchivedAt.Valid {
		return nil, fmt.Errorf("task %q is archived", ref)
	}
	return t, nil
}

// ResolveProject resolves a ref to exactly one project by slug.
func ResolveProject(db *sql.DB, ref string, includeArchived bool) (*flowdb.Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty project ref")
	}

	p, err := flowdb.GetProject(db, ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no project matching %q", ref)
		}
		return nil, err
	}
	if !includeArchived && p.ArchivedAt.Valid {
		return nil, fmt.Errorf("project %q is archived", ref)
	}
	return p, nil
}

// ResolveTaskOrProject resolves a ref that could be either a task or project.
// Supports optional task/ or project/ prefix. Without prefix, tries both.
// Errors if the ref matches in both tables.
func ResolveTaskOrProject(db *sql.DB, ref string, includeArchived bool) (kind string, slug string, err error) {
	if strings.HasPrefix(ref, "task/") {
		t, err := ResolveTask(db, strings.TrimPrefix(ref, "task/"), includeArchived)
		if err != nil {
			return "", "", err
		}
		return "task", t.Slug, nil
	}
	if strings.HasPrefix(ref, "project/") {
		p, err := ResolveProject(db, strings.TrimPrefix(ref, "project/"), includeArchived)
		if err != nil {
			return "", "", err
		}
		return "project", p.Slug, nil
	}

	t, tErr := ResolveTask(db, ref, includeArchived)
	p, pErr := ResolveProject(db, ref, includeArchived)

	switch {
	case tErr == nil && pErr == nil:
		return "", "", fmt.Errorf(
			"ambiguous ref %q: matches task %q and project %q; use 'task/%s' or 'project/%s'",
			ref, t.Slug, p.Slug, ref, ref)
	case tErr == nil:
		return "task", t.Slug, nil
	case pErr == nil:
		return "project", p.Slug, nil
	default:
		return "", "", fmt.Errorf("no task or project matching %q", ref)
	}
}
