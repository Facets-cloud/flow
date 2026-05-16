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

type resolveOptions struct {
	IncludeArchived bool
	IncludeDeleted  bool
}

// ResolveTask resolves a ref to exactly one task by slug.
// includeArchived controls whether archived rows are eligible.
func ResolveTask(db *sql.DB, ref string, includeArchived bool) (*flowdb.Task, error) {
	return ResolveTaskWithOptions(db, ref, resolveOptions{IncludeArchived: includeArchived})
}

func ResolveTaskWithOptions(db *sql.DB, ref string, opts resolveOptions) (*flowdb.Task, error) {
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
	if !opts.IncludeArchived && t.ArchivedAt.Valid {
		return nil, fmt.Errorf("task %q is archived", ref)
	}
	if !opts.IncludeDeleted && t.DeletedAt.Valid {
		return nil, fmt.Errorf("task %q is deleted", ref)
	}
	return t, nil
}

// ResolveProject resolves a ref to exactly one project by slug.
func ResolveProject(db *sql.DB, ref string, includeArchived bool) (*flowdb.Project, error) {
	return ResolveProjectWithOptions(db, ref, resolveOptions{IncludeArchived: includeArchived})
}

func ResolveProjectWithOptions(db *sql.DB, ref string, opts resolveOptions) (*flowdb.Project, error) {
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
	if !opts.IncludeArchived && p.ArchivedAt.Valid {
		return nil, fmt.Errorf("project %q is archived", ref)
	}
	if !opts.IncludeDeleted && p.DeletedAt.Valid {
		return nil, fmt.Errorf("project %q is deleted", ref)
	}
	return p, nil
}

// ResolvePlaybook resolves a ref to exactly one playbook by slug.
// includeArchived controls whether archived rows are eligible.
func ResolvePlaybook(db *sql.DB, ref string, includeArchived bool) (*flowdb.Playbook, error) {
	return ResolvePlaybookWithOptions(db, ref, resolveOptions{IncludeArchived: includeArchived})
}

func ResolvePlaybookWithOptions(db *sql.DB, ref string, opts resolveOptions) (*flowdb.Playbook, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty playbook ref")
	}

	pb, err := flowdb.GetPlaybook(db, ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no playbook matching %q", ref)
		}
		return nil, err
	}
	if !opts.IncludeArchived && pb.ArchivedAt.Valid {
		return nil, fmt.Errorf("playbook %q is archived", ref)
	}
	if !opts.IncludeDeleted && pb.DeletedAt.Valid {
		return nil, fmt.Errorf("playbook %q is deleted", ref)
	}
	return pb, nil
}

// ResolveTaskProjectOrPlaybook resolves a ref that could be a task, project,
// or playbook. Supports task/, project/, playbook/ prefixes. On bare refs,
// tries each kind; errors on ambiguity (slug exists in 2+ tables).
func ResolveTaskProjectOrPlaybook(db *sql.DB, ref string, includeArchived bool) (kind, slug string, err error) {
	return ResolveTaskProjectOrPlaybookWithOptions(db, ref, resolveOptions{IncludeArchived: includeArchived})
}

func ResolveTaskProjectOrPlaybookWithOptions(db *sql.DB, ref string, opts resolveOptions) (kind, slug string, err error) {
	if strings.HasPrefix(ref, "task/") {
		t, err := ResolveTaskWithOptions(db, strings.TrimPrefix(ref, "task/"), opts)
		if err != nil {
			return "", "", err
		}
		return "task", t.Slug, nil
	}
	if strings.HasPrefix(ref, "project/") {
		p, err := ResolveProjectWithOptions(db, strings.TrimPrefix(ref, "project/"), opts)
		if err != nil {
			return "", "", err
		}
		return "project", p.Slug, nil
	}
	if strings.HasPrefix(ref, "playbook/") {
		pb, err := ResolvePlaybookWithOptions(db, strings.TrimPrefix(ref, "playbook/"), opts)
		if err != nil {
			return "", "", err
		}
		return "playbook", pb.Slug, nil
	}

	t, tErr := ResolveTaskWithOptions(db, ref, opts)
	p, pErr := ResolveProjectWithOptions(db, ref, opts)
	pb, pbErr := ResolvePlaybookWithOptions(db, ref, opts)

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
