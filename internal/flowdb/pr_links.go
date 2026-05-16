package flowdb

import (
	"database/sql"
	"fmt"
)

type TaskPRLink struct {
	TaskSlug  string
	Repo      string
	PRNumber  int
	PRURL     string
	State     string
	MergedAt  sql.NullString
	CreatedAt string
	UpdatedAt string
}

func UpsertTaskPRLink(db *sql.DB, taskSlug, repo string, prNumber int, prURL string) error {
	if taskSlug == "" || repo == "" || prNumber <= 0 || prURL == "" {
		return nil
	}
	now := NowISO()
	_, err := db.Exec(
		`INSERT INTO task_pr_links (task_slug, repo, pr_number, pr_url, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'open', ?, ?)
		 ON CONFLICT(task_slug, repo, pr_number) DO UPDATE SET
			pr_url = excluded.pr_url,
			updated_at = excluded.updated_at`,
		taskSlug, repo, prNumber, prURL, now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert task PR link: %w", err)
	}
	return nil
}

func ListOpenTaskPRLinks(db *sql.DB) ([]TaskPRLink, error) {
	rows, err := db.Query(
		`SELECT task_slug, repo, pr_number, pr_url, state, merged_at, created_at, updated_at
		 FROM task_pr_links
		 WHERE state = 'open'
		 ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list task PR links: %w", err)
	}
	defer rows.Close()
	var out []TaskPRLink
	for rows.Next() {
		link, err := scanTaskPRLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *link)
	}
	return out, rows.Err()
}

func ListTaskPRLinks(db *sql.DB, taskSlug string) ([]TaskPRLink, error) {
	rows, err := db.Query(
		`SELECT task_slug, repo, pr_number, pr_url, state, merged_at, created_at, updated_at
		 FROM task_pr_links
		 WHERE task_slug = ?
		 ORDER BY updated_at DESC`,
		taskSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("list task PR links: %w", err)
	}
	defer rows.Close()
	var out []TaskPRLink
	for rows.Next() {
		link, err := scanTaskPRLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *link)
	}
	return out, rows.Err()
}

func MarkTaskPRMerged(db *sql.DB, taskSlug, repo string, prNumber int, mergedAt string) error {
	if mergedAt == "" {
		mergedAt = NowISO()
	}
	_, err := db.Exec(
		`UPDATE task_pr_links
		 SET state = 'merged', merged_at = ?, updated_at = ?
		 WHERE task_slug = ? AND repo = ? AND pr_number = ?`,
		mergedAt, NowISO(), taskSlug, repo, prNumber,
	)
	if err != nil {
		return fmt.Errorf("mark task PR merged: %w", err)
	}
	return nil
}

func MarkTaskDoneIfSessionBound(db *sql.DB, taskSlug string) (bool, error) {
	now := NowISO()
	res, err := db.Exec(
		`UPDATE tasks
		 SET status = 'done',
		     status_changed_at = CASE WHEN status != 'done' THEN ? ELSE status_changed_at END,
		     updated_at = ?
		 WHERE slug = ? AND archived_at IS NULL AND deleted_at IS NULL AND status != 'done' AND session_id IS NOT NULL`,
		now, now, taskSlug,
	)
	if err != nil {
		return false, fmt.Errorf("mark task done: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func scanTaskPRLink(row interface{ Scan(dest ...any) error }) (*TaskPRLink, error) {
	var link TaskPRLink
	if err := row.Scan(&link.TaskSlug, &link.Repo, &link.PRNumber, &link.PRURL, &link.State, &link.MergedAt, &link.CreatedAt, &link.UpdatedAt); err != nil {
		return nil, err
	}
	return &link, nil
}
