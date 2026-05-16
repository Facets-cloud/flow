package app

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/agents"
	"flow/internal/flowdb"
	"flow/internal/spawner"
)

const (
	sessionProviderClaude = agents.ProviderClaude
	sessionProviderCodex  = agents.ProviderCodex
)

const (
	codexModeFresh  = "fresh"
	codexModeResume = "resume"
)

// commandRunner is overridden by tests to mock Codex without spawning the real
// binary. Package-global, so callers that override it must not t.Parallel().
var commandRunner = exec.Command

func requestedSessionProvider(agent string, codexFlag, claudeFlag bool) (string, error) {
	if codexFlag && claudeFlag {
		return "", errors.New("--codex and --claude are mutually exclusive")
	}
	if agent != "" && (codexFlag || claudeFlag) {
		return "", errors.New("--agent cannot be combined with --codex or --claude")
	}
	if codexFlag {
		return sessionProviderCodex, nil
	}
	if claudeFlag {
		return sessionProviderClaude, nil
	}
	if agent != "" {
		return flowdb.NormalizeSessionProvider(agent)
	}
	if env := os.Getenv("FLOW_SESSION_AGENT"); env != "" {
		return flowdb.NormalizeSessionProvider(env)
	}
	if env := os.Getenv("FLOW_AGENT"); env != "" {
		return flowdb.NormalizeSessionProvider(env)
	}
	return "", nil
}

func hasSessionID(v sql.NullString) bool {
	return v.Valid && strings.TrimSpace(v.String) != ""
}

func buildCodexRunCommand(taskSlug, mode, sessionID, prompt, permissionMode string) (string, string, error) {
	var promptFile string
	if mode == codexModeFresh {
		var err error
		promptFile, err = writeCodexPromptFile(prompt)
		if err != nil {
			return "", "", err
		}
	}
	parts := []string{
		flowHookCodexRunCommand(),
		"--task " + spawner.ShellQuote(taskSlug),
		"--mode " + spawner.ShellQuote(mode),
		"--permission-mode " + spawner.ShellQuote(permissionMode),
	}
	if promptFile != "" {
		parts = append(parts, "--prompt-file "+spawner.ShellQuote(promptFile))
	}
	if sessionID != "" {
		parts = append(parts, "--session-id "+spawner.ShellQuote(sessionID))
	}
	return strings.Join(parts, " "), promptFile, nil
}

func flowHookCodexRunCommand() string {
	if path := flowCommandPathForSpawn(); path != "" {
		return shellQuoteArg(path) + " hook codex-run"
	}
	return "flow hook codex-run"
}

func flowCommandPathForSpawn() string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return ""
	}
	return preferredUIFlowBinary(exe)
}

func flowSessionEnv(root string) map[string]string {
	env := map[string]string{}
	if strings.TrimSpace(root) != "" {
		env["FLOW_ROOT"] = root
	}
	if path := pathWithFlowCommandDir(os.Getenv("PATH"), flowCommandPathForSpawn()); path != "" {
		env["PATH"] = path
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func pathWithFlowCommandDir(current, commandPath string) string {
	commandPath = strings.TrimSpace(commandPath)
	if commandPath == "" {
		return current
	}
	dir := filepath.Dir(commandPath)
	if dir == "." || dir == "" {
		return current
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for _, part := range filepath.SplitList(current) {
		if part == dir {
			return current
		}
	}
	if current == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + current
}

func agentShellCommand(bin string, args []string) string {
	parts := []string{bin}
	for _, arg := range args {
		parts = append(parts, shellQuoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuoteArg(arg string) string {
	if arg != "" {
		safe := true
		for _, r := range arg {
			if (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') ||
				strings.ContainsRune("@%_+=:,./-", r) {
				continue
			}
			safe = false
			break
		}
		if safe {
			return arg
		}
	}
	return spawner.ShellQuote(arg)
}

func claudePermissionArgs(mode string) []string {
	switch strings.TrimSpace(mode) {
	case "auto":
		return []string{"--permission-mode", "auto"}
	case "bypass":
		return []string{"--dangerously-skip-permissions"}
	default:
		return nil
	}
}

func writeCodexPromptFile(prompt string) (string, error) {
	f, err := os.CreateTemp("", "flow-codex-prompt-*.txt")
	if err != nil {
		return "", fmt.Errorf("create codex prompt file: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write codex prompt file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close codex prompt file: %w", err)
	}
	return path, nil
}

func cmdHookCodexRun(args []string) int {
	fs := flagSet("hook codex-run")
	taskSlug := fs.String("task", "", "flow task slug")
	mode := fs.String("mode", codexModeFresh, "fresh or resume")
	sessionID := fs.String("session-id", "", "Codex session/thread id for resume")
	prompt := fs.String("prompt", "", "bootstrap prompt")
	promptFile := fs.String("prompt-file", "", "path to bootstrap prompt file")
	permissionModeFlag := fs.String("permission-mode", "default", "agent permission mode: default|auto|bypass")
	dangerSkip := fs.Bool("dangerously-skip-permissions", false, "pass --dangerously-bypass-approvals-and-sandbox to codex")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	permissionMode, err := flowdb.NormalizePermissionMode(*permissionModeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	if *dangerSkip {
		permissionMode = "bypass"
	}
	if *taskSlug == "" {
		fmt.Fprintln(os.Stderr, "error: hook codex-run requires --task")
		return 2
	}
	if *mode != codexModeFresh && *mode != codexModeResume {
		fmt.Fprintf(os.Stderr, "error: --mode must be fresh or resume (got %q)\n", *mode)
		return 2
	}
	if *mode == codexModeResume && *sessionID == "" {
		fmt.Fprintln(os.Stderr, "error: resume mode requires --session-id")
		return 2
	}
	if *promptFile != "" {
		data, err := os.ReadFile(*promptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: read codex prompt file: %v\n", err)
			return 1
		}
		_ = os.Remove(*promptFile)
		*prompt = string(data)
	}
	if *mode == codexModeFresh && *prompt == "" {
		fmt.Fprintln(os.Stderr, "error: hook codex-run requires --prompt or --prompt-file")
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: get cwd: %v\n", err)
		return 1
	}
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	started := time.Now().Add(-2 * time.Second)
	codexArgs, err := codexCLIArgs(*mode, *sessionID, *prompt, cwd, root, permissionMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	cmd := commandRunner("codex", codexArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"FLOW_TASK="+*taskSlug,
		"FLOW_SESSION_PROVIDER="+sessionProviderCodex,
	)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error: start codex: %v\n", err)
		return 1
	}

	stopCapture := make(chan struct{})
	if *mode == codexModeFresh {
		go pollCaptureCodexSession(*taskSlug, cwd, started, stopCapture)
	}
	waitErr := cmd.Wait()
	if *mode == codexModeFresh {
		close(stopCapture)
		if captured, err := captureCodexSessionForTaskFromRoot(*taskSlug, cwd, started); err == nil && captured != "" {
			fmt.Fprintf(os.Stderr, "flow: captured codex session %s\n", captured)
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "warning: capture codex session: %v\n", err)
		}
	}
	if waitErr != nil {
		fmt.Fprintf(os.Stderr, "error: codex exited: %v\n", waitErr)
		return 1
	}
	return 0
}

func codexCLIArgs(mode, sessionID, prompt, cwd, flowRootPath, permissionMode string) ([]string, error) {
	if mode == codexModeFresh {
		args := []string{"--no-alt-screen"}
		if cwd != "" {
			args = append(args, "-C", cwd)
		}
		args = appendCodexWritableRoots(args, cwd, flowRootPath)
		args = append(args, codexPermissionArgs(permissionMode)...)
		return append(args, prompt), nil
	}
	if sessionID == "" {
		return nil, errors.New("codex resume requires a session id")
	}
	args := []string{"resume", "--include-non-interactive", "--no-alt-screen"}
	if cwd != "" {
		args = append(args, "-C", cwd)
	}
	args = appendCodexWritableRoots(args, cwd, flowRootPath)
	args = append(args, codexPermissionArgs(permissionMode)...)
	return append(args, sessionID), nil
}

func appendCodexWritableRoots(args []string, cwd, flowRootPath string) []string {
	flowRootPath = strings.TrimSpace(flowRootPath)
	if flowRootPath == "" {
		return args
	}
	if cwd != "" {
		if absCWD, err := filepath.Abs(cwd); err == nil {
			cwd = absCWD
		}
		if absRoot, err := filepath.Abs(flowRootPath); err == nil {
			flowRootPath = absRoot
		}
		if cwd == flowRootPath {
			return args
		}
	}
	return append(args, "--add-dir", flowRootPath)
}

func codexPermissionArgs(mode string) []string {
	switch strings.TrimSpace(mode) {
	case "auto":
		return []string{"--ask-for-approval", "never", "--sandbox", "workspace-write"}
	case "bypass":
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	default:
		return []string{"--ask-for-approval", "on-request", "--sandbox", "workspace-write"}
	}
}

func pollCaptureCodexSession(taskSlug, workDir string, started time.Time, stop <-chan struct{}) {
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case <-stop:
			return
		case <-deadline:
			return
		case <-ticker.C:
			captured, _ := captureCodexSessionForTaskFromRoot(taskSlug, workDir, started)
			if captured != "" {
				return
			}
		}
	}
}

func captureCodexSessionForTaskFromRoot(taskSlug, workDir string, started time.Time) (string, error) {
	dbPath, err := flowDBPath()
	if err != nil {
		return "", err
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	return agents.CaptureCodexSessionForTaskSince(db, taskSlug, workDir, started)
}
