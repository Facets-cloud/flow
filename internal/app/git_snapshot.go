package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"flow/internal/flowdb"
)

const gitSnapshotVersion = 1

type taskGitStartSnapshot struct {
	Version     int    `json:"version"`
	CapturedAt  string `json:"captured_at"`
	TaskSlug    string `json:"task_slug"`
	WorkDir     string `json:"work_dir"`
	RepoRoot    string `json:"repo_root"`
	Branch      string `json:"branch"`
	Head        string `json:"head"`
	HeadShort   string `json:"head_short"`
	HeadSubject string `json:"head_subject,omitempty"`
}

type taskGitCloseoutSnapshot struct {
	Version                   int                   `json:"version"`
	CapturedAt                string                `json:"captured_at"`
	TaskSlug                  string                `json:"task_slug"`
	WorkDir                   string                `json:"work_dir"`
	RepoRoot                  string                `json:"repo_root"`
	Branch                    string                `json:"branch"`
	EndHead                   string                `json:"end_head"`
	EndHeadShort              string                `json:"end_head_short"`
	EndHeadSubject            string                `json:"end_head_subject,omitempty"`
	Start                     *taskGitStartSnapshot `json:"start,omitempty"`
	StartError                string                `json:"start_error,omitempty"`
	CommitRange               string                `json:"commit_range,omitempty"`
	CommitsSinceStart         []string              `json:"commits_since_start,omitempty"`
	FilesChangedSinceStart    []string              `json:"files_changed_since_start,omitempty"`
	FilesChangedSinceStartErr string                `json:"files_changed_since_start_error,omitempty"`
	HeadCommitFiles           []string              `json:"head_commit_files,omitempty"`
	WorkingTreeStatus         []string              `json:"working_tree_status,omitempty"`
	MetadataPath              string                `json:"metadata_path"`
	UpdatePath                string                `json:"update_path"`
}

type gitRepositorySnapshot struct {
	WorkDir     string
	RepoRoot    string
	Branch      string
	Head        string
	HeadShort   string
	HeadSubject string
}

var gitOutput = func(workDir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", workDir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), msg, err)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func captureTaskGitStartSnapshot(task *flowdb.Task, overwrite bool) error {
	if task == nil || strings.TrimSpace(task.WorkDir) == "" {
		return nil
	}
	root, err := flowRoot()
	if err != nil {
		return err
	}
	path := taskGitStartSnapshotPath(root, task.Slug)
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	capturedAt := flowdb.NowISO()
	repo, ok, err := collectGitRepositorySnapshot(task.WorkDir)
	if err != nil || !ok {
		return err
	}
	snap := taskGitStartSnapshot{
		Version:     gitSnapshotVersion,
		CapturedAt:  capturedAt,
		TaskSlug:    task.Slug,
		WorkDir:     repo.WorkDir,
		RepoRoot:    repo.RepoRoot,
		Branch:      repo.Branch,
		Head:        repo.Head,
		HeadShort:   repo.HeadShort,
		HeadSubject: repo.HeadSubject,
	}
	return writeJSONFile(path, snap)
}

func writeTaskGitCloseoutSnapshot(task *flowdb.Task) (string, error) {
	if task == nil || strings.TrimSpace(task.WorkDir) == "" {
		return "", nil
	}
	root, err := flowRoot()
	if err != nil {
		return "", err
	}
	capturedAt := flowdb.NowISO()
	repo, ok, err := collectGitRepositorySnapshot(task.WorkDir)
	if err != nil || !ok {
		return "", err
	}

	updatePath := filepath.Join(root, "tasks", task.Slug, "updates", isoDatePart(capturedAt)+"-git-closeout.md")
	metadataPath := taskGitCloseoutSnapshotPath(root, task.Slug)
	closeout := taskGitCloseoutSnapshot{
		Version:        gitSnapshotVersion,
		CapturedAt:     capturedAt,
		TaskSlug:       task.Slug,
		WorkDir:        repo.WorkDir,
		RepoRoot:       repo.RepoRoot,
		Branch:         repo.Branch,
		EndHead:        repo.Head,
		EndHeadShort:   repo.HeadShort,
		EndHeadSubject: repo.HeadSubject,
		MetadataPath:   metadataPath,
		UpdatePath:     updatePath,
	}

	start, startErr := readTaskGitStartSnapshot(root, task.Slug)
	if startErr != nil {
		closeout.StartError = startErr.Error()
	} else {
		closeout.Start = start
	}

	if closeout.Start != nil && closeout.Start.Head != "" && closeout.EndHead != "" {
		if closeout.Start.RepoRoot == "" || closeout.Start.RepoRoot == closeout.RepoRoot {
			closeout.CommitRange = closeout.Start.Head + ".." + closeout.EndHead
			if out, err := gitOutput(task.WorkDir, "log", "--oneline", "--no-decorate", closeout.CommitRange); err == nil {
				closeout.CommitsSinceStart = splitGitLines(out)
			}
			if out, err := gitOutput(task.WorkDir, "diff", "--name-status", closeout.CommitRange); err == nil {
				closeout.FilesChangedSinceStart = splitGitLines(out)
			} else {
				closeout.FilesChangedSinceStartErr = err.Error()
			}
		} else {
			closeout.FilesChangedSinceStartErr = fmt.Sprintf("start repo root %q differs from close-out repo root %q", closeout.Start.RepoRoot, closeout.RepoRoot)
		}
	}
	if closeout.EndHead != "" {
		if out, err := gitOutput(task.WorkDir, "diff-tree", "--root", "--no-commit-id", "--name-status", "-r", "HEAD"); err == nil {
			closeout.HeadCommitFiles = splitGitLines(out)
		}
	}
	if out, err := gitOutput(task.WorkDir, "status", "--short"); err == nil {
		closeout.WorkingTreeStatus = splitGitLines(out)
	}

	if err := writeJSONFile(metadataPath, closeout); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(updatePath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(updatePath, []byte(formatGitCloseoutMarkdown(closeout)), 0o644); err != nil {
		return "", err
	}
	return updatePath, nil
}

func collectGitRepositorySnapshot(workDir string) (gitRepositorySnapshot, bool, error) {
	if strings.TrimSpace(workDir) == "" {
		return gitRepositorySnapshot{}, false, nil
	}
	inside, err := gitOutput(workDir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return gitRepositorySnapshot{}, false, nil
	}
	root, err := gitOutput(workDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitRepositorySnapshot{}, false, err
	}
	branch, _ := gitOutput(workDir, "branch", "--show-current")
	if branch == "" {
		if abbrev, err := gitOutput(workDir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && abbrev != "" && abbrev != "HEAD" {
			branch = abbrev
		} else {
			branch = "(detached)"
		}
	}
	head, _ := gitOutput(workDir, "rev-parse", "HEAD")
	headShort := ""
	headSubject := ""
	if head != "" {
		headShort, _ = gitOutput(workDir, "rev-parse", "--short=12", "HEAD")
		headSubject, _ = gitOutput(workDir, "log", "-1", "--format=%s")
	}
	return gitRepositorySnapshot{
		WorkDir:     workDir,
		RepoRoot:    root,
		Branch:      branch,
		Head:        head,
		HeadShort:   headShort,
		HeadSubject: headSubject,
	}, true, nil
}

func readTaskGitStartSnapshot(root, slug string) (*taskGitStartSnapshot, error) {
	b, err := os.ReadFile(taskGitStartSnapshotPath(root, slug))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap taskGitStartSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func taskGitStartSnapshotPath(root, slug string) string {
	return filepath.Join(root, "tasks", slug, "metadata", "git-start.json")
}

func taskGitCloseoutSnapshotPath(root, slug string) string {
	return filepath.Join(root, "tasks", slug, "metadata", "git-closeout.json")
}

func splitGitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func isoDatePart(iso string) string {
	if i := strings.IndexByte(iso, 'T'); i > 0 {
		return iso[:i]
	}
	return iso
}

func formatGitCloseoutMarkdown(s taskGitCloseoutSnapshot) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Git close-out snapshot")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Captured at: %s\n", s.CapturedAt)
	fmt.Fprintf(&b, "Task: %s\n", s.TaskSlug)
	fmt.Fprintf(&b, "Work dir: %s\n", s.WorkDir)
	fmt.Fprintf(&b, "Repo root: %s\n", s.RepoRoot)
	fmt.Fprintf(&b, "Branch: %s\n", valueOrNone(s.Branch))
	if s.Start != nil {
		fmt.Fprintf(&b, "Start HEAD: %s%s\n", valueOrNone(s.Start.HeadShort), subjectSuffix(s.Start.HeadSubject))
		fmt.Fprintf(&b, "Start captured at: %s\n", s.Start.CapturedAt)
	} else if s.StartError != "" {
		fmt.Fprintf(&b, "Start HEAD: (unreadable: %s)\n", s.StartError)
	} else {
		fmt.Fprintln(&b, "Start HEAD: (not captured)")
	}
	fmt.Fprintf(&b, "End HEAD: %s%s\n", valueOrNone(s.EndHeadShort), subjectSuffix(s.EndHeadSubject))
	fmt.Fprintf(&b, "Metadata JSON: %s\n", s.MetadataPath)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Commits since task start")
	if s.CommitRange == "" {
		fmt.Fprintln(&b, "(no start/end commit range available)")
	} else if len(s.CommitsSinceStart) == 0 {
		fmt.Fprintf(&b, "(none in %s)\n", s.CommitRange)
	} else {
		writeMarkdownCodeBlock(&b, s.CommitsSinceStart)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Files changed since task start")
	if s.FilesChangedSinceStartErr != "" {
		fmt.Fprintf(&b, "(unavailable: %s)\n", s.FilesChangedSinceStartErr)
	} else if s.CommitRange == "" {
		fmt.Fprintln(&b, "(no start/end commit range available)")
	} else if len(s.FilesChangedSinceStart) == 0 {
		fmt.Fprintf(&b, "(none in %s)\n", s.CommitRange)
	} else {
		writeMarkdownCodeBlock(&b, s.FilesChangedSinceStart)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Files changed in HEAD commit")
	if len(s.HeadCommitFiles) == 0 {
		fmt.Fprintln(&b, "(none)")
	} else {
		writeMarkdownCodeBlock(&b, s.HeadCommitFiles)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Working tree at close-out")
	if len(s.WorkingTreeStatus) == 0 {
		fmt.Fprintln(&b, "(clean)")
	} else {
		writeMarkdownCodeBlock(&b, s.WorkingTreeStatus)
	}
	return b.String()
}

func writeMarkdownCodeBlock(b *strings.Builder, lines []string) {
	fmt.Fprintln(b, "```text")
	for _, line := range lines {
		fmt.Fprintln(b, line)
	}
	fmt.Fprintln(b, "```")
}

func valueOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func subjectSuffix(subject string) string {
	if subject == "" {
		return ""
	}
	return " - " + subject
}
