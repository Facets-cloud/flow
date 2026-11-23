package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"flow/internal/flowdb"
)

// eventActor classifies who is acting on task, for the task event log:
//
//   - auto-run: this process runs inside the task's own autonomous run
//     (FLOW_AUTO_RUN == slug) — the child closing itself;
//   - driver:   the calling session is bound to the task's parent;
//   - session:  any other harness session (bound to another task, to this
//     task interactively, or unbound);
//   - human:    no harness session at all — a person at a shell.
//
// The session id returned is the caller's harness session, "" for human.
// Derived once here so every event site classifies the same way.
func eventActor(db *sql.DB, task *flowdb.Task) (actor, sessionID string) {
	sid := currentSessionID()
	if os.Getenv(autoRunEnvVar) == task.Slug {
		return flowdb.ActorAutoRun, sid
	}
	if sid == "" {
		return flowdb.ActorHuman, ""
	}
	if caller, err := currentSessionTask(db); err == nil && caller != nil &&
		task.ParentSlug.Valid && caller.Slug == task.ParentSlug.String {
		return flowdb.ActorDriver, sid
	}
	return flowdb.ActorSession, sid
}

// recordTaskEvent appends an event attributed to the caller. Best-effort:
// the event log is evidence, not the contract — a failed insert warns on
// stderr and never fails the command that caused it.
func recordTaskEvent(db *sql.DB, task *flowdb.Task, kind string, detail any) {
	actor, sid := eventActor(db, task)
	recordTaskEventAs(db, task.Slug, kind, actor, sid, detail)
}

// recordTaskEventAs appends an event with an explicit actor (the
// supervisor records its own events).
func recordTaskEventAs(db *sql.DB, slug, kind, actor, sid string, detail any) {
	if err := flowdb.InsertTaskEvent(db, slug, kind, actor, sid, detail); err != nil {
		fmt.Fprintf(os.Stderr, "warning: record %s event for %s: %v\n", kind, slug, err)
	}
}

// cmdEvents is `flow events [<ref>] [--since <RFC3339>] [--after-id <n>]
// [--kind k1,k2] [--limit n] [--format table|json]`: the read path for the
// task event log. Consumers read events here, never from flow.db.
func cmdEvents(args []string) int {
	var query string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		query, args = args[0], args[1:]
	}
	fs := flagSet("events")
	since := fs.String("since", "", "only events at or after this RFC3339 time")
	afterID := fs.Int64("after-id", 0, "only events with id greater than this (incremental cursor)")
	kinds := fs.String("kind", "", "comma-separated event kinds to include")
	limit := fs.Int("limit", 0, "maximum events to return (0 = all)")
	format := fs.String("format", "table", "output format: table|json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(os.Stderr, "error: --format must be table or json, got %q\n", *format)
		return 2
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

	f := flowdb.TaskEventFilter{Since: *since, AfterID: *afterID, Limit: *limit}
	if query != "" {
		task, rc := findTask(db, query)
		if rc != 0 {
			return rc
		}
		f.TaskSlug = task.Slug
	}
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			f.Kinds = append(f.Kinds, k)
		}
	}
	events, err := flowdb.ListTaskEvents(db, f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: list events: %v\n", err)
		return 1
	}

	if *format == "json" {
		if events == nil {
			events = []*flowdb.TaskEvent{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(events); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode: %v\n", err)
			return 1
		}
		return 0
	}
	if len(events) == 0 {
		fmt.Println("(no events)")
		return 0
	}
	for _, e := range events {
		line := fmt.Sprintf("%-5d %s  %-24s %-12s %-10s", e.ID, e.At, e.TaskSlug, e.Kind, e.Actor)
		if len(e.Detail) > 0 {
			line += " " + string(e.Detail)
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
	return 0
}

// recordStatusChange records a status transition from → to; a no-op when
// they are equal. Leaving 'done' is a reopen (its own kind, because a
// reopened task is the strongest "that close was premature" signal there
// is); everything else is a plain status event. reason is optional.
func recordStatusChange(db *sql.DB, task *flowdb.Task, from, to, reason string) {
	if from == to || to == "" {
		return
	}
	d := map[string]string{"from": from, "to": to}
	if reason != "" {
		d["reason"] = reason
	}
	kind := flowdb.EventStatus
	if from == "done" {
		kind = flowdb.EventReopen
	}
	recordTaskEvent(db, task, kind, d)
}

// recordFlipToInProgress records from → in-progress after a spawn/bind
// wrote it, re-reading the row first: the background paths keep a
// concurrent `flow done` (bgKeepConcurrentDoneSQL), and recording a flip
// that did not land would contradict the done event already logged.
func recordFlipToInProgress(db *sql.DB, task *flowdb.Task, from string) {
	var now string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE slug=?`, task.Slug).Scan(&now); err != nil {
		return
	}
	if now == "in-progress" {
		recordStatusChange(db, task, from, now, "")
	}
}
