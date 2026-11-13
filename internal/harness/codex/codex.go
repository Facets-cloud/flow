// Package codex implements harness.Harness for the Codex CLI.
package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"flow/internal/harness"
	"flow/internal/spawner"
)

var (
	// ProbeRunner mints a persisted Codex thread. Tests replace it to avoid
	// invoking the CLI.
	ProbeRunner = runProbe
	PSRunner    = runPS
	// SkipPermissionsRunner runs the headless close-out sweep. Tests
	// replace it to avoid invoking the CLI.
	SkipPermissionsRunner = runSkipPermissions
)

func New() harness.Harness { return &codex{} }

type codex struct{}

func (c *codex) Name() harness.Name      { return harness.NameCodex }
func (c *codex) Binary() string          { return "codex" }
func (c *codex) SessionIDEnvVar() string { return "CODEX_THREAD_ID" }

// Codex thread ids are lowercase UUIDs. Codex currently creates UUIDv7
// threads, while older saved threads may use other RFC-4122 versions.
var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The probe runs in workDir (the task's work_dir): codex exec records its
// cwd in the thread rollout, and the resumed session should agree with it.
func (c *codex) NewSessionID(workDir string) (string, error) {
	out, err := ProbeRunner(workDir)
	if err != nil {
		return "", fmt.Errorf("mint Codex session: %w", err)
	}
	return parseThreadID(out)
}

func (c *codex) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid lowercase Codex UUID: %q", s)
	}
	return nil
}

// Codex stores transcripts by globally unique thread id, rather than cwd.
func (c *codex) ValidateSession(workDir, sessionID string) error { return nil }

// interactivePrefix is the head of every interactive Codex launch and
// resume. Regular sessions run in Codex's workspace-write sandbox with
// on-request approvals; flow's bus and read-only commands get out of it
// through the flow-owned exec-policy rules file (flowRules), not through
// --add-dir (an added CODEX_HOME would let the sandbox rewrite its own
// config). --dangerously-skip-permissions keeps the full bypass.
// Interactive launches still keep Codex's hook-trust prompt (see
// hookTrustBypass).
func interactivePrefix(opts harness.LaunchOpts) string {
	if opts.SkipPermissions {
		return "codex --dangerously-bypass-approvals-and-sandbox"
	}
	return "codex -s workspace-write -a on-request"
}

func (c *codex) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	return interactivePrefix(opts) + " resume " + sessionID + " " + spawner.ShellQuote(prompt)
}

func (c *codex) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	cmd := interactivePrefix(opts) + " resume " + sessionID
	if opts.Inject != "" {
		cmd += " " + spawner.ShellQuote(harness.InjectionMarker+"\n"+opts.Inject)
	}
	return cmd
}

func (c *codex) SkipPermissionsRun(prompt string) error { return SkipPermissionsRunner(prompt) }

// DefersCloseoutInAutoRun: an inline sweep would be killed when the outer
// `codex exec resume` exits (its shell tool backgrounds `flow done` after
// ~10s), so the supervisor sweeps after exit instead (see
// harness.CloseoutDeferrer).
func (c *codex) DefersCloseoutInAutoRun() bool { return true }

// hookTrustBypass lets enabled hooks run without Codex's persisted hook
// trust. Headless runs only (auto runs, the close-out sweep, owner
// ticks): nobody is there to approve a freshly installed hook, so without
// it flow's hooks would silently not fire in the background, unlike
// Claude's. Interactive launches keep the trust prompt — a human approves
// once at the TUI and the trust persists.
const hookTrustBypass = "--dangerously-bypass-hook-trust"

func skipPermissionsArgv(prompt string) []string {
	return []string{"exec", "--dangerously-bypass-approvals-and-sandbox", hookTrustBypass, prompt}
}

func runSkipPermissions(prompt string) error {
	cmd := exec.Command("codex", skipPermissionsArgv(prompt)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// SkipPermissionsRunSession runs a headless, skip-permissions Codex tick
// pinned to sessionID. Codex mints its own thread in NewSessionID, so a
// "fresh pinned" run and a resume are the same shape: `codex exec resume
// <id>`. Output discarded; only the exit code matters.
func (c *codex) SkipPermissionsRunSession(sessionID, prompt string) error {
	return c.skipPermsResume(sessionID, prompt)
}

// SkipPermissionsResume resumes a Codex thread headlessly (same command as
// SkipPermissionsRunSession — codex resume is idempotent on the thread id).
func (c *codex) SkipPermissionsResume(sessionID, prompt string) error {
	return c.skipPermsResume(sessionID, prompt)
}

func (c *codex) skipPermsResume(sessionID, prompt string) error {
	cmd := exec.Command("codex", "exec", "resume",
		"--dangerously-bypass-approvals-and-sandbox", sessionID, prompt)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func (c *codex) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	argv := []string{"codex", "exec", "resume"}
	if opts.SkipPermissions {
		argv = append(argv, "--dangerously-bypass-approvals-and-sandbox", hookTrustBypass)
	}
	if opts.LastMessagePath != "" {
		argv = append(argv, "-o", opts.LastMessagePath)
	}
	return append(argv, sessionID, prompt)
}

func runProbe(workDir string) ([]byte, error) {
	// This probe intentionally persists its thread: the interactive `codex
	// resume` started immediately afterwards must resume this exact id.
	cmd := exec.Command(
		"codex", "exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check",
		"Reply exactly with OK. Do not make any changes.",
	)
	cmd.Dir = workDir // "" keeps the caller's cwd
	return cmd.Output()
}

func parseThreadID(out []byte) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "thread.started" {
			if err := New().ValidateSessionID(event.ThreadID); err != nil {
				return "", fmt.Errorf("Codex probe returned invalid thread id: %w", err)
			}
			return event.ThreadID, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Codex probe output: %w", err)
	}
	return "", fmt.Errorf("Codex probe output did not include a thread.started event")
}

// Flags may sit between `resume` and the id: AutoRunArgv emits
// `codex exec resume --dangerously-bypass-approvals-and-sandbox -o <file> <id> ...`.
var runningSessionRe = regexp.MustCompile(`\bresume(?:\s+--?[A-Za-z][\w-]*(?:\s+[^-\s]\S*)?)*?\s+([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\b`)

func (c *codex) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, err
	}
	live := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(strings.ToLower(line), "codex") {
			continue
		}
		seen := map[string]bool{}
		for _, m := range runningSessionRe.FindAllStringSubmatch(line, -1) {
			id := strings.ToLower(m[1])
			if !seen[id] {
				live[id]++
				seen[id] = true
			}
		}
	}
	return live, nil
}

func runPS() ([]byte, error) { return exec.Command("ps", "-axo", "pid,command").Output() }

func (c *codex) SkillInstallPath() (string, error) {
	home, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "skills", "flow", "SKILL.md"), nil
}

// codexHome is Codex's state root: $CODEX_HOME when set, else ~/.codex.
// Skills, hooks.json and session rollouts all live under it.
func codexHome() (string, error) {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func (c *codex) SkillVersionPath() (string, error) {
	p, err := c.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "VERSION"), nil
}

// flowRules is Codex exec-policy that lets a workspace-write session run
// flow's message-bus and read-only commands outside the sandbox (the
// sandbox makes flow.db read-only and blocks local sockets). Codex loads
// every *.rules file under $CODEX_HOME/rules/. Only a BARE top-level
// command matches a prefix_rule: `VAR=x flow …`, `cd … && flow …` and
// wrapper scripts stay sandboxed. Deliberately absent: a bare ["flow"],
// and every subcommand that spawns agents or rewrites harness config
// (do, auto, run, owner, skill, init, edit, connector, peer) or mutates
// tasks (add, update, done) — those go through Codex's approval prompt.
const flowRules = `# flow owns this file: flow skill install/update overwrites it and
# flow skill uninstall removes it. Do not edit; put your own rules in
# another *.rules file. It lets workspace-write Codex sessions run flow's
# message-bus and read-only commands (bare, top-level only) outside the
# sandbox, with no approval prompt.
prefix_rule(pattern=["flow", "message"], decision="allow")
prefix_rule(pattern=["flow", "inbox"], decision="allow")
prefix_rule(pattern=["flow", "broadcast"], decision="allow")
prefix_rule(pattern=["flow", "watch"], decision="allow")
prefix_rule(pattern=["flow", "show"], decision="allow")
prefix_rule(pattern=["flow", "list"], decision="allow")
prefix_rule(pattern=["flow", "transcript"], decision="allow")
`

func flowRulesPath() (string, error) {
	home, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "rules", "flow.rules"), nil
}

func installFlowRules() error {
	p, err := flowRulesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(flowRules), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

func uninstallFlowRules() error {
	p, err := flowRulesPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}

// InstallSkill writes the skill tree and the flow-owned exec-policy rules
// file (flowRules), so install, update and auto-upgrade all refresh both.
func (c *codex) InstallSkill(files fs.FS) error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	if err := installFlowRules(); err != nil {
		return err
	}
	base := filepath.Dir(p)
	return fs.WalkDir(files, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(files, rel)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", rel, err)
		}
		target := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		return os.WriteFile(target, content, 0o644)
	})
}

func (c *codex) UninstallSkill() error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	if err := uninstallFlowRules(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(p)); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(filepath.Dir(p))
}

func (c *codex) InstallSessionStartHook(command string) (bool, error) {
	return installHook("SessionStart", "startup|resume", command)
}

func (c *codex) UninstallSessionStartHook(command string) (bool, error) {
	return uninstallHook("SessionStart", command)
}

// Codex 0.166 fires UserPromptSubmit, PreToolUse, PostToolUse and Stop
// (plus PermissionRequest, PreCompact, SessionEnd, ... that flow doesn't
// use). Entries carry no matcher: UserPromptSubmit and Stop take none,
// and Codex tool names (exec_command, apply_patch, ...) differ from
// Claude's, so the tool hooks match every tool — the handlers' fast
// paths exit silently when there's nothing to say.
func (c *codex) InstallUserPromptSubmitHook(command string) (bool, error) {
	return installHook("UserPromptSubmit", "", command)
}
func (c *codex) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return uninstallHook("UserPromptSubmit", command)
}
func (c *codex) InstallPostToolUseHook(command string) (bool, error) {
	return installHook("PostToolUse", "", command)
}
func (c *codex) UninstallPostToolUseHook(command string) (bool, error) {
	return uninstallHook("PostToolUse", command)
}
func (c *codex) InstallPreToolUseHook(command string) (bool, error) {
	return installHook("PreToolUse", "", command)
}
func (c *codex) UninstallPreToolUseHook(command string) (bool, error) {
	return uninstallHook("PreToolUse", command)
}
func (c *codex) InstallStopHook(command string) (bool, error) {
	return installHook("Stop", "", command)
}
func (c *codex) UninstallStopHook(command string) (bool, error) {
	return uninstallHook("Stop", command)
}
