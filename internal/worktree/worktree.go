// Package worktree manages per-task git worktrees so that multiple flow
// tasks running against the same repo do not stomp on each other's working
// tree.
//
// The shape: for each task, flow creates (or reuses) a git worktree at
// <repo_root>/.<agent>/worktrees/<task-slug> on branch flow/<task-slug>
// branched from the repo's default branch. The agent session is then
// spawned with the worktree as its cwd, so its edits land in a private
// checkout that other sessions don't see.
//
// Worktree creation is a no-op when the task's work_dir is not a git
// repo (e.g. ~/.flow/tasks/<slug>/workspace/ for floating tasks). In
// that case Ensure returns IsRepo=false and the caller should fall back
// to the raw work_dir.
package worktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AgentClaude / AgentCodex are the two valid agent values. The worktree
// is placed under .<agent>/worktrees/ so Claude and Codex can hold
// separate checkouts of the same branch if the user works on the task
// in both providers.
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// BranchPrefix is prepended to the task slug to form the worktree
// branch name. Namespacing under "flow/" makes flow-managed branches
// easy to identify in `git branch -a` and prevents collision with
// branches the user creates by hand.
const BranchPrefix = "flow/"

// Result is what Ensure returns when it has prepared (or reused) a
// worktree for a task. WorktreePath is the absolute path the caller
// should use as cwd for spawning the agent.
type Result struct {
	IsRepo       bool   // false if work_dir is not inside a git repo
	RepoRoot     string // toplevel of the host repo
	WorktreePath string // absolute path; same as work_dir when IsRepo=false
	Branch       string // e.g. "flow/<slug>" when IsRepo=true
	BaseBranch   string // detected default branch we branched from
	Created      bool   // true when this call created the worktree
}

// gitOutput shells out to `git -C <workDir> <args...>` and returns the
// trimmed stdout. Errors carry the git stderr message. Overridable for
// tests via the package var below.
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

// SetGitOutput swaps the git subprocess shim, returning the prior one.
// Tests use this to inject a fake. Production code never calls it.
func SetGitOutput(fn func(workDir string, args ...string) (string, error)) func(string, ...string) (string, error) {
	prev := gitOutput
	gitOutput = fn
	return prev
}

// RepoRoot returns the absolute path of the repo containing workDir,
// or "" if workDir is not inside a working tree.
func RepoRoot(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		return ""
	}
	inside, err := gitOutput(workDir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return ""
	}
	root, err := gitOutput(workDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return root
}

// BaseBranch picks the branch flow worktrees branch off of. Order:
//  1. origin/HEAD symbolic-ref (e.g. origin/main, origin/master)
//  2. local "main"
//  3. local "master"
//  4. whatever HEAD currently points at
//
// Returns "" if none can be resolved.
func BaseBranch(repoRoot string) string {
	if out, err := gitOutput(repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		out = strings.TrimSpace(out)
		if strings.HasPrefix(out, "origin/") {
			return strings.TrimPrefix(out, "origin/")
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, err := gitOutput(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return name
		}
	}
	if head, err := gitOutput(repoRoot, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		head = strings.TrimSpace(head)
		if head != "" && head != "HEAD" {
			return head
		}
	}
	return ""
}

// BranchName returns the flow-namespaced branch for a task slug.
func BranchName(slug string) string {
	return BranchPrefix + slug
}

// WorktreeRelDir returns the path of the worktrees directory for an
// agent, relative to the repo root. The .<agent>/ prefix matches what
// each agent already gitignores by convention.
func WorktreeRelDir(agent string) string {
	return filepath.Join("."+agent, "worktrees")
}

// WorktreePathFor returns the absolute worktree path for (repoRoot,
// agent, slug). No filesystem checks.
func WorktreePathFor(repoRoot, agent, slug string) string {
	return filepath.Join(repoRoot, WorktreeRelDir(agent), slug)
}

// Ensure prepares a worktree for the task. If workDir is not inside a
// git repo, it returns IsRepo=false and the caller should use workDir
// directly. Otherwise it computes the worktree path, creates the
// worktree and branch if missing, ensures the worktrees dir is
// gitignored, and returns the result.
//
// Reuse rules:
//   - If the worktree path already exists and git knows about it
//     (registered in `git worktree list`), reuse it.
//   - If the path exists but git does not know about it, that is a
//     user-supplied directory we must not touch — return an error so
//     the user can clean up.
//   - If the branch flow/<slug> already exists but no worktree is
//     attached, attach a fresh worktree at the path (no -b).
//   - If neither exists, create branch+worktree in one shot.
func Ensure(workDir, agent, slug string) (*Result, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("worktree: empty task slug")
	}
	switch agent {
	case AgentClaude, AgentCodex:
	default:
		return nil, fmt.Errorf("worktree: unknown agent %q", agent)
	}

	root := RepoRoot(workDir)
	if root == "" {
		return &Result{IsRepo: false, WorktreePath: workDir}, nil
	}

	wtPath := WorktreePathFor(root, agent, slug)
	branch := BranchName(slug)

	registered, err := worktreeRegistered(root, wtPath)
	if err != nil {
		return nil, err
	}

	res := &Result{
		IsRepo:       true,
		RepoRoot:     root,
		WorktreePath: wtPath,
		Branch:       branch,
		BaseBranch:   BaseBranch(root),
	}

	if registered {
		if _, err := os.Stat(wtPath); err != nil {
			return nil, fmt.Errorf("worktree path %s is registered with git but missing on disk; run `git -C %s worktree prune` and retry: %w", wtPath, root, err)
		}
		if err := ensureWorktreesIgnored(root, agent); err != nil {
			return nil, err
		}
		return res, nil
	}

	if _, err := os.Stat(wtPath); err == nil {
		return nil, fmt.Errorf("worktree path %s exists on disk but is not a registered git worktree; please remove it or rename so flow can create a clean worktree", wtPath)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat worktree path %s: %w", wtPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir worktrees dir: %w", err)
	}

	branchExists := false
	if _, err := gitOutput(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		branchExists = true
	}

	if branchExists {
		if _, err := gitOutput(root, "worktree", "add", wtPath, branch); err != nil {
			return nil, fmt.Errorf("attach worktree to existing branch %s: %w", branch, err)
		}
	} else {
		args := []string{"worktree", "add", "-b", branch, wtPath}
		if res.BaseBranch != "" {
			args = append(args, res.BaseBranch)
		}
		if _, err := gitOutput(root, args...); err != nil {
			return nil, fmt.Errorf("create worktree %s on branch %s: %w", wtPath, branch, err)
		}
	}
	res.Created = true

	if err := ensureWorktreesIgnored(root, agent); err != nil {
		// best-effort; do not undo the worktree just for a gitignore hiccup
		_ = err
	}
	return res, nil
}

// worktreeRegistered reports whether wtPath appears in `git worktree
// list --porcelain` output. The porcelain form prints one `worktree
// <abs-path>` line per registered worktree, so we just scan for it.
func worktreeRegistered(repoRoot, wtPath string) (bool, error) {
	out, err := gitOutput(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git worktree list: %w", err)
	}
	wantAbs, err := filepath.Abs(wtPath)
	if err != nil {
		wantAbs = wtPath
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		listed := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if listedAbs, err := filepath.Abs(listed); err == nil {
			listed = listedAbs
		}
		if listed == wantAbs {
			return true, nil
		}
	}
	return false, nil
}

// ensureWorktreesIgnored makes sure the host repo's .gitignore covers
// .<agent>/worktrees/ so the registered worktrees do not show up as
// untracked files in `git status` on the main checkout. Best-effort: a
// missing or unwritable .gitignore is reported as an error, but the
// caller is expected to ignore it.
func ensureWorktreesIgnored(repoRoot, agent string) error {
	rel := WorktreeRelDir(agent) + "/"
	pattern := "/" + rel // anchor to repo root
	path := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if trim == pattern || trim == rel || trim == strings.TrimSuffix(pattern, "/") || trim == strings.TrimSuffix(rel, "/") {
			return nil
		}
	}
	var buf bytes.Buffer
	buf.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		buf.WriteByte('\n')
	}
	if len(data) > 0 {
		buf.WriteByte('\n')
	}
	fmt.Fprintln(&buf, "# flow-managed agent worktrees")
	fmt.Fprintln(&buf, pattern)
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
