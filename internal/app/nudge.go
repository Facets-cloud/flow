package app

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"flow/internal/flowdb"
)

// The star nudge: when `flow done` leaves the flow root with
// starNudgeThreshold or more closed tasks, flow writes ONE FYI into the
// local human's inbox asking for a GitHub star, then drops a marker file
// so it never asks again. It is mail, not stdout: flow's users talk to an
// agent, and the inbox is the one place the human actually reads. The
// agent relays it like any other user mail; nothing in the skill tells an
// agent to repeat it.
//
// Many existing installs already have far more than 25 closed tasks, so
// the trigger is `count >= 25 AND marker absent`, not `count == 25`.

// starNudgeThreshold is the number of closed (status=done, not archived)
// tasks at or above which the nudge fires once.
const starNudgeThreshold = 25

// starNudgeSender is the from_assignee on the one-time star FYI. The bus
// has no task behind it (from_task_slug empty), so `flow inbox` renders the
// sender as plain "flow" — the program itself, not a session or connector.
const starNudgeSender = "flow"

// starNudgeMarker is the flow-root-relative file whose presence means the
// star FYI was already sent. Checked first, so the hot path on every
// `flow done` after the nudge is a single stat.
const starNudgeMarker = "state/nudge-star-25"

// starNudgeBody is the verbatim text of the FYI.
const starNudgeBody = "flow has closed 25 tasks for you. If it has earned it, a star helps the next person find it: https://github.com/Facets-cloud/flow\nThis is the only time flow will ask."

// quietEnvVar suppresses the nudge entirely (no mail, no marker).
const quietEnvVar = "FLOW_QUIET"

// maybeStarNudge runs on the `flow done` success path. It never returns an
// error: a nudge that cannot be written is logged to stderr and dropped,
// because the status flip is the contract and the nudge is best-effort.
func maybeStarNudge(db *sql.DB) {
	if q := os.Getenv(quietEnvVar); q != "" && q != "0" {
		return
	}
	root, err := flowRoot()
	if err != nil {
		return
	}
	marker := filepath.Join(root, starNudgeMarker)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status='done' AND archived_at IS NULL`).Scan(&n); err != nil {
		fmt.Fprintf(os.Stderr, "warning: star nudge: count done tasks: %v\n", err)
		return
	}
	if n < starNudgeThreshold {
		return
	}
	// The marker is the once-guard, claimed BEFORE the insert with
	// O_EXCL: of two concurrent `flow done` calls exactly one creates it
	// and sends. If the marker cannot be written the nudge is skipped
	// (and never retried from this call) rather than risk sending it on
	// every subsequent done.
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: star nudge skipped: %v\n", err)
		return
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			fmt.Fprintf(os.Stderr, "warning: star nudge skipped: %v\n", err)
		}
		return
	}
	fmt.Fprintf(f, "%s\n", flowdb.NowISO())
	f.Close()

	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: starNudgeSender, ToAssignee: flowdb.BusUser,
		Body: starNudgeBody, FYI: true,
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		fmt.Fprintf(os.Stderr, "warning: star nudge not sent: %v\n", err)
		return
	}
}
