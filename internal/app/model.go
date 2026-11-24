package app

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// modelFlags registers --model and --effort on fs. Both are passed
// through to the harness as given (see harness.LaunchOpts.Model).
func modelFlags(fs *flag.FlagSet) (model, effort *string) {
	model = fs.String("model", "", "model for the harness session, passed through as given (e.g. opus, haiku); stored on the task and reused on resume")
	effort = fs.String("effort", "", "effort level for the harness session, passed through as given (e.g. low, high); stored on the task and reused on resume")
	return model, effort
}

// modelFlagArgs re-emits --model/--effort for a command that delegates to
// cmdDo (flow auto, flow run playbook).
func modelFlagArgs(model, effort string) []string {
	var a []string
	if model != "" {
		a = append(a, "--model", model)
	}
	if effort != "" {
		a = append(a, "--effort", effort)
	}
	return a
}

// rejectModelWithHere is the usage error for --model/--effort on a --here
// bind: the session is already running, so flow can't change its model.
func rejectModelWithHere(model, effort string) bool {
	if model == "" && effort == "" {
		return false
	}
	fmt.Fprintln(os.Stderr, "error: --model/--effort cannot be used with --here (the current session is already running; flow can't change its model)")
	return true
}

// checkModelFlags checks that h can honour the task's model/effort (the
// flag value, else the one stored on the task) and updates task in memory
// to match. It writes nothing: storeModelFlags persists the flag values
// once the launch is known to proceed, so a refused command leaves the row
// unchanged. Returns a non-zero exit code on failure.
func checkModelFlags(task *flowdb.Task, h harness.Harness, model, effort string) int {
	if model == "" {
		model = task.Model.String
	}
	if effort == "" {
		effort = task.Effort.String
	}
	if err := harness.CheckModelOpts(h, model, effort); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	task.Model = sql.NullString{String: model, Valid: model != ""}
	task.Effort = sql.NullString{String: effort, Valid: effort != ""}
	return 0
}

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// storeModelFlags writes the given --model/--effort flag values to the
// task row so later launches without the flag reuse them. Only the columns
// whose flag was given are written; an omitted flag keeps whatever is
// stored, even if another command changed it meanwhile. task.Model and
// task.Effort are set to the merged values the row now holds, so a launch
// built from task matches the row.
func storeModelFlags(db queryer, task *flowdb.Task, model, effort string) error {
	if model == "" && effort == "" {
		return nil
	}
	if err := db.QueryRow(`UPDATE tasks SET model = COALESCE(?, model), effort = COALESCE(?, effort), updated_at = ? WHERE slug = ? RETURNING model, effort`,
		flowdb.NullIfEmpty(model), flowdb.NullIfEmpty(effort), flowdb.NowISO(), task.Slug).
		Scan(&task.Model, &task.Effort); err != nil {
		return fmt.Errorf("store model/effort: %w", err)
	}
	return nil
}

// checkModelForNewTask is the early --model/--effort check for commands
// that create a task and then delegate to cmdDo (flow auto, flow run
// playbook): it resolves the harness the new task's first session will get
// (the named one, else the ambient/default one) and refuses values that
// harness can't honour, before any row is written. Returns an exit code.
func checkModelForNewTask(harnessName, model, effort string) int {
	if model == "" && effort == "" {
		return 0
	}
	h := defaultHarness()
	if harnessName != "" {
		var err error
		if h, err = harnessByName(harnessName); err != nil {
			return 0 // cmdDo reports the bad --harness
		}
	}
	if err := harness.CheckModelOpts(h, model, effort); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	return 0
}

// ownerModelFlags registers --model and --effort for the owner commands.
func ownerModelFlags(fs *flag.FlagSet) (model, effort *string) {
	model = fs.String("model", "", "model every tick of this owner runs on, passed through as given (e.g. opus, haiku); tasks the owner dispatches keep their own")
	effort = fs.String("effort", "", "effort level every tick of this owner runs on, passed through as given (e.g. low, high)")
	return model, effort
}

// checkOwnerModel refuses model/effort values the owner's harness (named by
// hname; "" is the claude default every owner tick resolves to) can't
// honour. Returns an exit code.
func checkOwnerModel(hname, model, effort string) int {
	h, err := harnessByName(hname)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := harness.CheckModelOpts(h, model, effort); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	return 0
}

// ownerModelOpts is the owner's stored model/effort as launch options.
func ownerModelOpts(o *flowdb.Owner) harness.LaunchOpts {
	return harness.LaunchOpts{Model: o.Model.String, Effort: o.Effort.String}
}

// applyTaskModel copies the task's stored model/effort into opts.
func applyTaskModel(task *flowdb.Task, opts *harness.LaunchOpts) {
	opts.Model = task.Model.String
	opts.Effort = task.Effort.String
}
