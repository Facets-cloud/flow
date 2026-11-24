package app

import (
	"database/sql"
	"encoding/json"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"os"
	"path/filepath"
	"strings"
)

// showTaskJSONSchema versions the `flow show task --json` contract. Bump
// it only on a breaking change (a removed or retyped field); adding a
// field is not breaking.
const showTaskJSONSchema = 1

// taskJSON is the stable `flow show task --json` document. Optional
// values are JSON null, never absent, so consumers can rely on every key.
type taskJSON struct {
	Schema             int                   `json:"schema"`
	Slug               string                `json:"slug"`
	Name               string                `json:"name"`
	Kind               string                `json:"kind"`
	Project            *string               `json:"project"`
	Parent             *string               `json:"parent"`
	Status             string                `json:"status"`
	Priority           string                `json:"priority"`
	WorkDir            string                `json:"work_dir"`
	Archived           bool                  `json:"archived"`
	WaitingOn          *string               `json:"waiting_on"`
	Assignee           *string               `json:"assignee"`
	DueDate            *string               `json:"due_date"`
	Driver             bool                  `json:"driver"`
	Tags               []string              `json:"tags"`
	Harness            *string               `json:"harness"`
	SessionID          *string               `json:"session_id"`
	Live               bool                  `json:"live"`
	SessionStarted     *string               `json:"session_started"`
	SessionLastResumed *string               `json:"session_last_resumed"`
	Model              *string               `json:"model"`
	Effort             *string               `json:"effort"`
	BG                 *bgJSON               `json:"bg"`
	AutoRun            *autoRunJSON          `json:"auto_run"`
	PreviousSessions   []flowdb.PriorSession `json:"previous_sessions"`
	Brief              string                `json:"brief"`
	Updates            []string              `json:"updates"`
	Other              []string              `json:"other"`
	CreatedAt          string                `json:"created_at"`
	UpdatedAt          string                `json:"updated_at"`
}

type bgJSON struct {
	ShortID string `json:"short_id"`
	PID     int    `json:"pid"`
	Status  string `json:"status"`
	State   string `json:"state"`
}

type autoRunJSON struct {
	Status      string  `json:"status"` // running | completed | dead
	PID         *int64  `json:"pid"`
	Started     *string `json:"started"`
	Finished    *string `json:"finished"`
	Log         *string `json:"log"`
	LastMessage *string `json:"last_message"`
}

func nullStr(n sql.NullString) *string {
	if !n.Valid || n.String == "" {
		return nil
	}
	s := n.String
	return &s
}

// printTaskJSON writes the task as one JSON object to stdout.
//
// Unlike the human view, the background-agent registry is consulted on
// every call (not only under $FLOW_TERM=bg): a machine consumer probing
// liveness must see a background session whichever env it runs in.
func printTaskJSON(db *sql.DB, t *flowdb.Task, root string) error {
	doc := taskJSON{
		Schema:             showTaskJSONSchema,
		Slug:               t.Slug,
		Name:               t.Name,
		Kind:               t.Kind,
		Project:            nullStr(t.ProjectSlug),
		Parent:             nullStr(t.ParentSlug),
		Status:             t.Status,
		Priority:           t.Priority,
		WorkDir:            t.WorkDir,
		Archived:           t.ArchivedAt.Valid,
		WaitingOn:          nullStr(t.WaitingOn),
		Assignee:           nullStr(t.Assignee),
		DueDate:            nullStr(t.DueDate),
		Driver:             isDriverTask(db, t.Slug),
		Tags:               []string{},
		SessionID:          nullStr(t.SessionID),
		SessionStarted:     nullStr(t.SessionStarted),
		SessionLastResumed: nullStr(t.SessionLastResumed),
		Model:              nullStr(t.Model),
		Effort:             nullStr(t.Effort),
		Brief:              filepath.Join(root, "tasks", t.Slug, "brief.md"),
		Updates:            listUpdateFiles(filepath.Join(root, "tasks", t.Slug, "updates")),
		CreatedAt:          t.CreatedAt,
		UpdatedAt:          t.UpdatedAt,
	}
	if tags, err := flowdb.GetTaskTags(db, t.Slug); err == nil && tags != nil {
		doc.Tags = tags
	}
	if doc.Updates == nil {
		doc.Updates = []string{}
	}
	doc.Other, _ = enumerateAuxFiles(filepath.Join(root, "tasks", t.Slug))
	if doc.Other == nil {
		doc.Other = []string{}
	}
	prev, err := flowdb.PriorSessions(db, t.Slug)
	if err != nil {
		return err
	}
	doc.PreviousSessions = prev

	// Harness: the stored pin; a bootstrapped legacy row with a NULL pin
	// is claude (harnessByName's back-compat rule). Unbootstrapped tasks
	// with no pin report null: the harness is chosen at `flow do` time.
	if t.Harness.Valid && t.Harness.String != "" {
		doc.Harness = &t.Harness.String
	} else if doc.SessionID != nil {
		c := string(harness.NameClaude)
		doc.Harness = &c
	}
	if doc.SessionID != nil {
		if h, err := harnessForTask(t); err == nil {
			if live, err := h.LiveSessionIDs(); err == nil {
				doc.Live = live[strings.ToLower(t.SessionID.String)] > 0
			}
			if bg, ok := h.(harness.BackgroundLauncher); ok {
				if a := bgAgentInRegistry(bg, t.SessionID.String); a != nil {
					doc.BG = &bgJSON{ShortID: a.ShortID, PID: a.PID, Status: a.Status, State: a.State}
				}
			}
		}
	}

	if t.AutoRunStatus.Valid && t.AutoRunStatus.String != "" {
		reconcileAutoRun(db, t)
		ar := &autoRunJSON{
			Status:   t.AutoRunStatus.String,
			Started:  nullStr(t.AutoRunStarted),
			Finished: nullStr(t.AutoRunFinished),
			Log:      nullStr(t.AutoRunLog),
		}
		if t.AutoRunPID.Valid {
			pid := t.AutoRunPID.Int64
			ar.PID = &pid
		}
		if ar.Log != nil {
			if last := autoRunLastMessagePath(*ar.Log); statOK(last) {
				ar.LastMessage = &last
			}
		}
		doc.AutoRun = ar
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
