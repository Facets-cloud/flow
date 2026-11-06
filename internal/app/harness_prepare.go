package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// prepareHarnessSession runs the optional SessionPreparer capability:
// ensure the harness install (skill + plugin) and prepare the session.
//
// Harnesses without the capability (claude, codex) are a no-op, so their
// spawn path is byte-for-byte unchanged. opencode uses it to create the
// session server-side with flow metadata before a tab or auto run
// attaches to it — on bootstrap AND resume.
func prepareHarnessSession(h harness.Harness, task *flowdb.Task, sessionID string, auto bool) error {
	return prepareHarnessSessionFor(h, task.WorkDir, sessionID, harness.SessionMeta{
		TaskSlug: task.Slug,
		FlowRoot: os.Getenv("FLOW_ROOT"),
		FlowBin:  flowBinaryPath(),
		Auto:     auto,
	})
}

// prepareHarnessSessionFor is the task-independent core of
// prepareHarnessSession: it runs the optional SessionPreparer capability
// for any caller that owns a session id but not necessarily a task (e.g.
// an owner's hand-triggered interactive tick). Harnesses without the
// capability are a no-op.
func prepareHarnessSessionFor(h harness.Harness, workDir, sessionID string, meta harness.SessionMeta) error {
	p, ok := h.(harness.SessionPreparer)
	if !ok {
		return nil
	}
	if err := ensureHarnessInstalled(h); err != nil {
		return err
	}
	return p.PrepareSession(workDir, sessionID, meta)
}

// ensureHarnessInstalled keeps the harness's flow install current: the
// skill tree when the version sidecar is missing or stale, then the
// session-start hook (for opencode that is the embedded plugin file).
//
// Dev builds never change their version string, so the sidecar alone
// would freeze the first install forever — "dev" always reinstalls.
func ensureHarnessInstalled(h harness.Harness) error {
	versionPath, err := h.SkillVersionPath()
	if err != nil {
		return fmt.Errorf("locate skill version: %w", err)
	}
	installed := ""
	if b, err := os.ReadFile(versionPath); err == nil {
		installed = strings.TrimSpace(string(b))
	}
	if installed != Version || Version == "dev" {
		if err := h.InstallSkill(skillFiles()); err != nil {
			return fmt.Errorf("install skill: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(versionPath), 0o755); err != nil {
			return fmt.Errorf("create skill version dir: %w", err)
		}
		if err := os.WriteFile(versionPath, []byte(Version+"\n"), 0o644); err != nil {
			return fmt.Errorf("write skill version: %w", err)
		}
	}
	if _, err := h.InstallSessionStartHook(hookCommand); err != nil {
		return fmt.Errorf("install session-start hook: %w", err)
	}
	return nil
}

// flowBinaryPath returns the absolute path of the running flow binary,
// symlinks resolved where possible, so a prepared session can call back
// into the exact binary that spawned it. Best-effort: "" when the path
// can't be determined.
func flowBinaryPath() string {
	p, err := os.Executable()
	if err != nil || p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
