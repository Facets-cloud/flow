package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/ghref"
)

var ghPRViewOutput = func(ctx context.Context, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "url")
	cmd.Dir = dir
	return cmd.Output()
}

func linkTaskToCurrentBranchPR(db *sql.DB, task *flowdb.Task) error {
	if db == nil || task == nil || strings.TrimSpace(task.WorkDir) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := ghPRViewOutput(ctx, task.WorkDir)
	if err != nil {
		return nil
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return err
	}
	tag, ok := ghref.PRTagFromURL(payload.URL)
	if !ok {
		return nil
	}
	return flowdb.AddTaskTag(db, task.Slug, tag)
}
