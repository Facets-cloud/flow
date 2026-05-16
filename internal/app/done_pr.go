package app

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"flow/internal/flowdb"
	"flow/internal/worktree"
)

// gitCmdRunner is the subprocess shim for `git -C <cwd> <args...>`. Tests
// swap this to drive done's PR path without a real git on the box.
var gitCmdRunner = func(cwd string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", cwd}, args...)
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

// ghCmdRunner is the subprocess shim for `gh` invocations executed in
// the worktree. Tests swap this to assert flag composition and inject
// fake URLs without a real gh CLI present.
var ghCmdRunner = func(cwd string, args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("gh %s: %s: %w", strings.Join(args, " "), msg, err)
		}
		return "", fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// raiseDonePRForTask pushes the worktree branch to origin and opens a
// PR against the repo's default branch. Runs after status is already
// flipped to done; failures only warn and never roll back the close-out.
//
// Best-effort skips, all returning ("", nil) so done can ignore them:
//   - task has no recorded worktree
//   - worktree dir is missing on disk (already pruned)
//   - the worktree HEAD is on the base branch (no PR to open)
//   - the branch has zero commits ahead of base
//   - `gh pr create` produced no parseable URL
//
// Errors are returned only for unambiguous failures (git/gh exits, push
// rejection). On success, the PR URL is persisted via UpsertTaskPRLink
// and returned.
func raiseDonePRForTask(db *sql.DB, task *flowdb.Task) (string, error) {
	if task == nil {
		return "", nil
	}
	if !task.WorktreePath.Valid || strings.TrimSpace(task.WorktreePath.String) == "" {
		return "", nil
	}
	wt := task.WorktreePath.String
	if _, err := os.Stat(wt); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat worktree %s: %w", wt, err)
	}

	branch, err := gitCmdRunner(wt, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("read worktree branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", nil
	}

	base := worktree.BaseBranch(wt)
	if base == "" || base == branch {
		return "", nil
	}

	aheadStr, err := gitCmdRunner(wt, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return "", fmt.Errorf("count commits ahead of %s: %w", base, err)
	}
	ahead, _ := strconv.Atoi(strings.TrimSpace(aheadStr))
	if ahead <= 0 {
		return "", nil
	}

	if _, err := gitCmdRunner(wt, "push", "-u", "origin", branch); err != nil {
		return "", fmt.Errorf("push %s: %w", branch, err)
	}

	args := []string{"pr", "create", "--base", base, "--head", branch, "--title", task.Name}
	bodyPath := ""
	if root, ferr := flowRoot(); ferr == nil {
		candidate := filepath.Join(root, "tasks", task.Slug, "brief.md")
		if _, serr := os.Stat(candidate); serr == nil {
			bodyPath = candidate
			args = append(args, "--body-file", bodyPath)
		}
	}

	out, err := ghCmdRunner(wt, args...)
	if err != nil {
		return "", err
	}
	prURL := firstHTTPSURL(out)
	if prURL == "" {
		return "", nil
	}
	if repo, num := parsePRURL(prURL); num > 0 {
		if err := flowdb.UpsertTaskPRLink(db, task.Slug, repo, num, prURL); err != nil {
			return prURL, fmt.Errorf("persist PR link: %w", err)
		}
	}
	return prURL, nil
}

// firstHTTPSURL scans output line by line and returns the first whole
// token that starts with "https://". `gh pr create` prints the URL on
// its own line; this is a small allowance for surrounding chatter that
// some `gh` versions emit.
func firstHTTPSURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		for _, tok := range strings.Fields(line) {
			if strings.HasPrefix(tok, "https://") {
				return strings.TrimRight(tok, ".,;")
			}
		}
	}
	return ""
}

// prURLRE matches GitHub PR URLs of the form
// https://github.com/<owner>/<repo>/pull/<number>. Captures
// "<owner>/<repo>" and the number.
var prURLRE = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/pull/(\d+)`)

// parsePRURL extracts the "owner/repo" identifier and PR number from a
// GitHub PR URL. Returns ("", 0) if the URL isn't a recognized GitHub
// PR URL — non-GitHub remotes get persisted without a number.
func parsePRURL(url string) (repo string, number int) {
	m := prURLRE.FindStringSubmatch(url)
	if m == nil {
		return "", 0
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], n
}
