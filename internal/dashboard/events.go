package dashboard

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// buildActivity assembles the most-recent N activity events. Sources:
//   - mtime of every ~/.flow/tasks/<slug>/updates/*.md   → "note added"
//   - mtime of every ~/.flow/projects/<slug>/updates/*.md → "note added"
//   - tasks with status_changed_at set and status='in-progress' → "started"
//   - tasks with status_changed_at set and status='done'        → "done"
//   - projects with created_at within the activity window      → "project +"
//
// Events are sorted desc by When and capped to `cap`.
func buildActivity(db *sql.DB, root string, asOf time.Time, cap int) ([]Event, error) {
	var events []Event

	taskNotes, err := scanUpdateMtimes(filepath.Join(root, "tasks"))
	if err != nil {
		return nil, err
	}
	for _, n := range taskNotes {
		events = append(events, Event{
			When:       n.When,
			Kind:       "note added",
			TargetSlug: n.OwnerSlug,
		})
	}

	projNotes, err := scanUpdateMtimes(filepath.Join(root, "projects"))
	if err != nil {
		return nil, err
	}
	for _, n := range projNotes {
		events = append(events, Event{
			When:       n.When,
			Kind:       "note added",
			TargetSlug: n.OwnerSlug,
		})
	}

	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{Kind: "regular", IncludeArchived: true})
	if err != nil {
		return nil, fmt.Errorf("list tasks for events: %w", err)
	}
	for _, t := range tasks {
		if t.StatusChangedAt.Valid {
			when, err := time.Parse(time.RFC3339, t.StatusChangedAt.String)
			if err == nil {
				kind := ""
				switch t.Status {
				case "in-progress":
					kind = "started"
				case "done":
					kind = "done"
				}
				if kind != "" {
					events = append(events, Event{
						When:       when,
						Kind:       kind,
						TargetSlug: t.Slug,
						TargetProj: nullStr(t.ProjectSlug),
					})
				}
			}
		}
	}

	projects, err := flowdb.ListProjects(db, flowdb.ProjectFilter{IncludeArchived: true})
	if err != nil {
		return nil, fmt.Errorf("list projects for events: %w", err)
	}
	for _, p := range projects {
		when, err := time.Parse(time.RFC3339, p.CreatedAt)
		if err != nil {
			continue
		}
		events = append(events, Event{
			When:       when,
			Kind:       "project +",
			TargetSlug: p.Slug,
		})
	}

	// Fill in TargetProj on note events for tasks (cheaper to do once now
	// than per-file).
	projOfTask := make(map[string]string, len(tasks))
	for _, t := range tasks {
		projOfTask[t.Slug] = nullStr(t.ProjectSlug)
	}
	for i := range events {
		if events[i].TargetProj == "" && events[i].Kind == "note added" {
			if p, ok := projOfTask[events[i].TargetSlug]; ok {
				events[i].TargetProj = p
			}
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].When.After(events[j].When)
	})

	if len(events) > cap {
		events = events[:cap]
	}

	for i := range events {
		events[i].RelTime = relTimeDur(asOf.Sub(events[i].When))
		events[i].Hash = hashEvent(events[i])
	}
	return events, nil
}

// noteFile is one update file with its parsed owner slug and mtime.
type noteFile struct {
	Path      string
	OwnerSlug string
	When      time.Time
}

// scanUpdateMtimes returns every *.md inside <base>/<slug>/updates/ with
// its mtime and the owning slug derived from the directory name.
func scanUpdateMtimes(base string) ([]noteFile, error) {
	pattern := filepath.Join(base, "*", "updates", "*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}
	out := make([]noteFile, 0, len(matches))
	for _, p := range matches {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		// .../<slug>/updates/<file>.md — owner is two dirs up.
		owner := filepath.Base(filepath.Dir(filepath.Dir(p)))
		out = append(out, noteFile{
			Path:      p,
			OwnerSlug: owner,
			When:      info.ModTime(),
		})
	}
	return out, nil
}

// loadPlaybookHeadlines returns, for each playbook slug, the headline
// of its newest update file under <root>/playbooks/<slug>/updates/.
// Playbooks without any update files map to "".
func loadPlaybookHeadlines(root string, pbs []*flowdb.Playbook) (map[string]string, error) {
	out := make(map[string]string, len(pbs))
	for _, pb := range pbs {
		dir := filepath.Join(root, "playbooks", pb.Slug, "updates")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var newest os.DirEntry
		var newestTime time.Time
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if newest == nil || info.ModTime().After(newestTime) {
				newest = e
				newestTime = info.ModTime()
			}
		}
		if newest == nil {
			continue
		}
		headline, err := firstHeadline(filepath.Join(dir, newest.Name()))
		if err == nil && headline != "" {
			out[pb.Slug] = headline
		}
	}
	return out, nil
}

// loadHeadlines returns, for each task slug, the headline of its newest
// update file (first non-empty, non-heading line). Tasks without any
// update files map to "".
func loadHeadlines(root string, tasks []*flowdb.Task) (map[string]string, error) {
	out := make(map[string]string, len(tasks))
	for _, t := range tasks {
		dir := filepath.Join(root, "tasks", t.Slug, "updates")
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Missing updates dir is normal for new tasks.
			continue
		}
		var newest os.DirEntry
		var newestTime time.Time
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if newest == nil || info.ModTime().After(newestTime) {
				newest = e
				newestTime = info.ModTime()
			}
		}
		if newest == nil {
			continue
		}
		headline, err := firstHeadline(filepath.Join(dir, newest.Name()))
		if err == nil && headline != "" {
			out[t.Slug] = headline
		}
	}
	return out, nil
}

// firstHeadline returns the first non-empty, non-`#`-heading line of a
// markdown file. Strips trailing whitespace.
func firstHeadline(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		return line, scanner.Err()
	}
	return "", scanner.Err()
}

// hashEvent produces a 4-char cosmetic ID. Stable across runs given the
// same event tuple, but purely for visual interest — not used for
// equality, joining, or de-duplication.
func hashEvent(e Event) string {
	h := sha256.Sum256([]byte(e.When.Format(time.RFC3339Nano) + "|" + e.Kind + "|" + e.TargetSlug))
	return hex.EncodeToString(h[:2])
}

// relTime renders an RFC3339 timestamp as a short relative string like
// "2h ago" or "3d ago". Returns "" if t is unparseable.
func relTime(asOf time.Time, rfc3339 string) string {
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	return relTimeDur(asOf.Sub(parsed)) + " ago"
}

// relTimeDur formats a positive duration as "Nm", "Nh", or "Nd". Negative
// durations (clock skew / future events) clamp to "now".
func relTimeDur(d time.Duration) string {
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
