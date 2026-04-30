// Package app — playbook run command and helpers.
package app

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

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
