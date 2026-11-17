package app

import (
	"bytes"
	"database/sql"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/match"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is the read-only index layer behind `flow recall`: it scans
// flow.db (strictly read-only), stat-fingerprints each task, and hydrates
// briefs / update notes / cleaned session prompts into match.TaskData. The
// recall command (recall.go) and the on-disk cache (matchcache.go) build on
// these; the match package (internal/match) does the ranking. Nothing here
// ever writes flow.db.

// recallDir is the recall data directory, $FLOW_ROOT/match/ — it holds the
// index cache and the per-user calibration. (The directory keeps its historic
// "match" name so existing caches are not orphaned.)
func recallDir() (string, error) {
	root, err := flowRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "match"), nil
}

func matchFileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return 1
}

// ---------- read-only loader ----------

type matchRow struct {
	data      match.TaskData
	sessionID string
	harness   string
	workDir   string
	taskDir   string
	tpath     string // transcript path ("" if none)
}

// fingerprintRows resolves each row's transcript path and computes its
// stat-based fingerprint, in parallel (disk-bound stat calls).
func fingerprintRows(rows []matchRow, updatedAts []string) {
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				r := &rows[i]
				if r.sessionID != "" {
					if h, err := harnessByName(r.harness); err == nil {
						if tp, ok := h.(harness.TranscriptPather); ok {
							if p, err := tp.TranscriptPath(r.workDir, r.sessionID); err == nil {
								r.tpath = p
							}
						}
					}
				}
				r.data.Fingerprint = fingerprint(*r, updatedAts[i])
			}
		}()
	}
	for i := range rows {
		idx <- i
	}
	close(idx)
	wg.Wait()
}

// openMatchDB opens flow.db strictly read-only: no schema DDL, no
// migrations (unlike flowdb.OpenDB), and SQLite itself refuses writes.
func openMatchDB() (*sql.DB, error) {
	p, err := flowDBPath()
	if err != nil {
		return nil, err
	}
	return flowdb.OpenReadOnly(p)
}

// matchScan lists every non-deleted task with a cheap stat-based
// fingerprint (no file contents read yet).
func matchScan() ([]matchRow, error) {
	db, err := openMatchDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	root, err := flowRoot()
	if err != nil {
		return nil, err
	}
	// Some flow builds add a soft-delete column; honor it when present.
	where := ""
	if hasColumn(db, "tasks", "deleted_at") {
		where = "WHERE deleted_at IS NULL"
	}
	parentCol := "''"
	if hasColumn(db, "tasks", "parent_slug") {
		parentCol = "COALESCE(parent_slug,'')"
	}
	rs, err := db.Query(`SELECT slug, name, status, COALESCE(session_id,''), COALESCE(harness,''), work_dir,
		created_at, updated_at, COALESCE(status_changed_at,''), COALESCE(session_started,''), archived_at IS NOT NULL,
		COALESCE(project_slug,''), ` + parentCol + `, COALESCE(kind,'regular'), COALESCE(playbook_slug,'')
		FROM tasks ` + where + ` ORDER BY created_at, slug`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rs.Close()
	var rows []matchRow
	var updatedAts []string
	for rs.Next() {
		var r matchRow
		var updatedAt, changedAt string
		d := &r.data
		if err := rs.Scan(&d.Slug, &d.Name, &d.Status, &r.sessionID, &r.harness, &r.workDir,
			&d.CreatedAt, &updatedAt, &changedAt, &d.SessionStarted, &d.Archived,
			&d.Project, &d.Parent, &d.Kind, &d.Playbook); err != nil {
			return nil, err
		}
		d.CreatedAt = utcTS(d.CreatedAt)
		d.SessionStarted = utcTS(d.SessionStarted)
		if d.Status == "done" {
			d.DoneAt = utcTS(changedAt)
		}
		r.taskDir = filepath.Join(root, "tasks", d.Slug)
		rows = append(rows, r)
		updatedAts = append(updatedAts, updatedAt)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	// Resolve transcript paths and the stat-based fingerprint in parallel:
	// each row stats its brief, every update note and its transcript, so a
	// serial pass over a few hundred tasks is disk-bound and slow.
	fingerprintRows(rows, updatedAts)
	tags, tagAt := taskTagsRO(db)
	for i := range rows {
		rows[i].data.Tags = tags[rows[i].data.Slug]
		rows[i].data.TagAt = tagAt[rows[i].data.Slug]
	}
	return rows, nil
}

// taskTagsRO reads slug -> tags and slug -> tag -> created_at (UTC)
// (empty when the table is absent).
func taskTagsRO(db *sql.DB) (map[string][]string, map[string]map[string]string) {
	out := map[string][]string{}
	at := map[string]map[string]string{}
	rs, err := db.Query(`SELECT task_slug, tag, COALESCE(created_at,'') FROM task_tags ORDER BY task_slug, tag`)
	if err != nil {
		return out, at
	}
	defer rs.Close()
	for rs.Next() {
		var s, t, c string
		if rs.Scan(&s, &t, &c) == nil {
			out[s] = append(out[s], t)
			if c != "" {
				if at[s] == nil {
					at[s] = map[string]string{}
				}
				at[s][t] = utcTS(c)
			}
		}
	}
	return out, at
}

func hasColumn(db *sql.DB, table, col string) bool {
	rs, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false
	}
	defer rs.Close()
	for rs.Next() {
		var n string
		if rs.Scan(&n) == nil && n == col {
			return true
		}
	}
	return false
}

// utcTS normalizes an RFC3339 timestamp to UTC so string comparison is
// chronological across mixed offsets. Unparseable values pass through.
func utcTS(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339)
}

func statSig(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d@%d", fi.Size(), fi.ModTime().UnixNano())
}

// fingerprint is a task's stat-based change signature: its scalar fields plus
// the size+mtime of its brief, every update note and (for an active task) its
// transcript. Two calls agree iff nothing the index reads has changed, which
// is what lets the cache re-hydrate only drifted tasks.
func fingerprint(r matchRow, updatedAt string) string {
	h := fnv.New64a()
	d := r.data
	fmt.Fprintf(h, "%s|%s|%s|%v|%s|%s|%s\n", d.Slug, d.Name, d.Status, d.Archived, updatedAt, d.DoneAt, r.sessionID)
	fmt.Fprintf(h, "brief:%s\n", statSig(filepath.Join(r.taskDir, "brief.md")))
	ups, _ := filepath.Glob(filepath.Join(r.taskDir, "updates", "*.md"))
	sort.Strings(ups)
	for _, u := range ups {
		fmt.Fprintf(h, "up:%s:%s\n", filepath.Base(u), statSig(u))
	}
	// A done/archived task's transcript is frozen (its session won't resume
	// in the common case), so skip the stat of what can be a large file —
	// this halves the fingerprint's I/O on a corpus that is mostly done
	// work. An active task's transcript is still stat'd so growth is caught.
	if r.tpath != "" && d.Status != "done" && !d.Archived {
		fmt.Fprintf(h, "tr:%s:%s\n", r.tpath, statSig(r.tpath))
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

var reUpdateDate = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})`)

// hydrate reads the task's brief, update notes and cleaned session
// prompts.
func hydrate(r matchRow) match.TaskData {
	d := r.data
	if b, err := os.ReadFile(filepath.Join(r.taskDir, "brief.md")); err == nil {
		d.Brief = string(b)
	}
	ups, _ := filepath.Glob(filepath.Join(r.taskDir, "updates", "*.md"))
	sort.Strings(ups)
	for _, u := range ups {
		b, err := os.ReadFile(u)
		if err != nil {
			continue
		}
		date := ""
		if m := reUpdateDate.FindStringSubmatch(filepath.Base(u)); m != nil {
			date = m[1]
		}
		d.Updates = append(d.Updates, match.Update{Date: date, Text: string(b)})
	}
	if r.sessionID != "" {
		// Prefer flow's own transcript copy: it survives harness retention
		// AND reflects a changed task's new prompts incrementally. Fall back
		// to the harness renderer when no copy exists yet.
		if prompts, ok := promptsFromFlowCopy(r); ok {
			d.Prompts = prompts
		} else if h, err := harnessByName(r.harness); err == nil {
			var buf bytes.Buffer
			if err := h.RenderTranscript(r.workDir, r.sessionID, true, &buf); err == nil {
				d.Prompts = match.ExtractPrompts(buf.String())
			}
		}
	}
	return d
}

// promptsFromFlowCopy reads cleaned user prompts from the flow transcript
// copy for a row's session, if one exists. This is the recall prompt-
// loading seam over the harness-agnostic store (internal/app/transcriptstore.go).
func promptsFromFlowCopy(r matchRow) ([]string, bool) {
	if r.sessionID == "" {
		return nil, false
	}
	copyPath, _, err := flowCopyPaths(r.data.Slug, harnessNameOrDefault(r.harness), r.sessionID)
	if err != nil {
		return nil, false
	}
	recs, err := readFlowCopyRecords(copyPath)
	if err != nil || len(recs) == 0 {
		return nil, false
	}
	var out []string
	for _, rec := range recs {
		if rec.Role != "user" || rec.Injected {
			continue
		}
		if c, ok := match.CleanPrompt(rec.Text); ok {
			out = append(out, c)
		}
	}
	return out, true
}

func harnessNameOrDefault(name string) string {
	if name == "" {
		return "claude"
	}
	return name
}

// hydrateLight reads the task's brief and update notes but SKIPS the
// transcript (the slow part — seconds for a long live session). Used by
// the incremental cache for changed tasks, whose prompt index is kept
// from the cache (see Model.UpsertKeepingPrompts).
func hydrateLight(r matchRow) match.TaskData {
	d := r.data
	if b, err := os.ReadFile(filepath.Join(r.taskDir, "brief.md")); err == nil {
		d.Brief = string(b)
	}
	ups, _ := filepath.Glob(filepath.Join(r.taskDir, "updates", "*.md"))
	sort.Strings(ups)
	for _, u := range ups {
		b, err := os.ReadFile(u)
		if err != nil {
			continue
		}
		date := ""
		if m := reUpdateDate.FindStringSubmatch(filepath.Base(u)); m != nil {
			date = m[1]
		}
		d.Updates = append(d.Updates, match.Update{Date: date, Text: string(b)})
	}
	return d
}

// hydrateAll hydrates every row on a small worker pool — transcript
// decoding dominates and is embarrassingly parallel.
func hydrateAll(rows []matchRow) []match.TaskData {
	out := make([]match.TaskData, len(rows))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				out[i] = hydrate(rows[i])
			}
		}()
	}
	for i := range rows {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return out
}

// boundTaskSlugRO resolves the task bound to this harness session
// without opening the DB read-write.
func boundTaskSlugRO() (string, error) {
	sid := currentSessionID()
	if sid == "" {
		return "", fmt.Errorf("not running inside a harness session")
	}
	db, err := openMatchDB()
	if err != nil {
		return "", err
	}
	defer db.Close()
	var slug string
	if err := db.QueryRow(`SELECT slug FROM tasks WHERE session_id = ?`, sid).Scan(&slug); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("this session is not bound to a task")
		}
		return "", err
	}
	return slug, nil
}

// excludeTasks drops tasks whose slug matches any glob.
func excludeTasks(tasks []match.TaskData, globs []string) []match.TaskData {
	var out []match.TaskData
	for _, t := range tasks {
		drop := false
		for _, g := range globs {
			if ok, _ := filepath.Match(strings.TrimSpace(g), t.Slug); ok {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, t)
		}
	}
	return out
}

// lookbackExcludes are slug globs always dropped from the recall eval's
// train+test set, on top of any passed via --exclude. It is empty by default:
// the only tasks worth excluding are meta-tasks whose briefs discuss every
// other task (e.g. the ones in which you developed or evaluated recall
// itself), and those slugs are specific to each user's history — pass them
// with `flow recall eval --exclude "<glob>,<glob>"`.
var lookbackExcludes = []string{}

func splitGlobs(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// excludeTasksSplit is excludeTasks that also returns the dropped slugs.
func excludeTasksSplit(tasks []match.TaskData, globs []string) (kept []match.TaskData, dropped []string) {
	kept = excludeTasks(tasks, globs)
	in := map[string]bool{}
	for _, t := range kept {
		in[t.Slug] = true
	}
	for _, t := range tasks {
		if !in[t.Slug] {
			dropped = append(dropped, t.Slug)
		}
	}
	return kept, dropped
}
