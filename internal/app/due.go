package app

import (
	"flow/internal/flowdb"
	"fmt"
	"os"
	"strings"
	"time"
)

// cmdDue sets or clears a task's due date.
//
//	flow due <ref> <date>        — set due date
//	flow due <ref> --clear       — remove due date
func cmdDue(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: due requires a task ref")
		return 2
	}
	query := args[0]
	fs := flagSet("due")
	clear := fs.Bool("clear", false, "remove the due date")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

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

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}

	now := flowdb.NowISO()
	if *clear {
		if _, err := db.Exec(
			`UPDATE tasks SET due_date=NULL, updated_at=? WHERE slug=?`,
			now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: clear due date: %v\n", err)
			return 1
		}
		fmt.Printf("Cleared due date for %s\n", task.Slug)
		return 0
	}

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: due requires a date (YYYY-MM-DD, today, tomorrow, monday, 3d) or --clear")
		return 2
	}

	dateStr := fs.Arg(0)
	d, err := parseDueDate(dateStr, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	formatted := d.Format("2006-01-02")

	if _, err := db.Exec(
		`UPDATE tasks SET due_date=?, updated_at=? WHERE slug=?`,
		formatted, now, task.Slug,
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: set due date: %v\n", err)
		return 1
	}
	fmt.Printf("Set due date for %s to %s\n", task.Slug, formatted)
	return 0
}

// parseDueDate converts a human-friendly date expression to a time.Time.
// Accepts: YYYY-MM-DD, today, tomorrow, weekday names (next occurrence),
// Nd (N days from now). `now` is passed in for testability.
func parseDueDate(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	switch s {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	case "tomorrow":
		y, m, d := now.AddDate(0, 0, 1).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	}

	// Weekday names — next occurrence (today if it matches).
	weekdays := map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday,
		"tuesday": time.Tuesday, "wednesday": time.Wednesday,
		"thursday": time.Thursday, "friday": time.Friday,
		"saturday": time.Saturday,
	}
	if target, ok := weekdays[s]; ok {
		current := now.Weekday()
		delta := int(target) - int(current)
		if delta <= 0 {
			delta += 7
		}
		d := now.AddDate(0, 0, delta)
		y, m, dd := d.Date()
		return time.Date(y, m, dd, 0, 0, 0, 0, now.Location()), nil
	}

	// Pattern "Nd" — N days from now.
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		var n int
		if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil && n >= 0 {
			d := now.AddDate(0, 0, n)
			y, m, dd := d.Date()
			return time.Date(y, m, dd, 0, 0, 0, 0, now.Location()), nil
		}
	}

	// YYYY-MM-DD.
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unrecognized date %q (want YYYY-MM-DD, today, tomorrow, monday..sunday, Nd)", s)
}
