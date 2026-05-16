package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"flow/internal/kitty"
	flowmonitor "flow/internal/monitor"
	"flow/internal/spawner"
	macterminal "flow/internal/terminal"
	"flow/internal/warp"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type actionRequest struct {
	Kind           string `json:"kind"`
	Target         string `json:"target"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Project        string `json:"project"`
	WorkDir        string `json:"work_dir"`
	Priority       string `json:"priority"`
	Prompt         string `json:"prompt"`
	SessionID      string `json:"session_id"`
	Branch         string `json:"branch"`
	EventID        string `json:"event_id"`
	Mode           string `json:"mode"`
	Source         string `json:"source"`
	RuleKind       string `json:"rule_kind"`
	PRURL          string `json:"pr_url"`
	EntityKind     string `json:"entity_kind"`
	Provider       string `json:"provider"`
	PermissionMode string `json:"permission_mode"`
}

type actionResponse struct {
	OK          bool     `json:"ok"`
	Message     string   `json:"message"`
	Output      string   `json:"output,omitempty"`
	Agent       *uiAgent `json:"agent,omitempty"`
	Bridge      bool     `json:"bridge,omitempty"`
	AlreadyLive bool     `json:"already_live,omitempty"`
}

var (
	safeSlugRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	safeSessionRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	safeBranchRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-]*$`)
	githubPRURLRe = regexp.MustCompile(`^https://github\.com/([^/\s]+/[^/\s]+)/pull/([0-9]+)(?:[/?#].*)?$`)
)

const (
	overviewTaskSlug = "flow-overview"
	overviewTaskName = "Flow overview command center"
)

var nativeCommandStarter = startNativeCommand

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req actionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	resp, status := s.runAction(req)
	writeJSONStatus(w, resp, status)
}

func (s *Server) runAction(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	switch req.Kind {
	case "spawn", "resume", "attach":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.openBrowserTerminalBridge(target)
	case "iterm", "terminal", "warp", "kitty", "alacritty", "ghostty", "wezterm", "tmux", "vscode":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		if err := s.ensureTerminalAvailable(req.Kind); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.openTaskBridge(target, req.Kind, false)
	case "restart":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.restartBrowserTerminalBridge(target)
	case "switch-branch":
		return s.switchBranch(req)
	case "archive":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		out, err := s.runFlowCommand("archive", target)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
		return actionResponse{OK: true, Message: "archived " + target, Output: out}, http.StatusOK
	case "delete", "restore":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		ref, err := qualifiedEntityRef(req.EntityKind, target)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		out, err := s.runFlowCommand(req.Kind, ref)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
		return actionResponse{OK: true, Message: req.Kind + "d " + target, Output: out}, http.StatusOK
	case "spawn-run":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.spawnPlaybookRunBridge(target, req)
	case "create-flow":
		return s.createFlow(req)
	case "pause":
		return s.pauseTask(target)
	case "kill":
		return s.killSession(req)
	case "approve", "deny":
		return s.ackApproval(req.Kind, target)
	case "fork":
		return s.forkTask(req)
	case "edit-playbook":
		return s.editPlaybook(target)
	case "monitor-sync":
		return s.monitorSync()
	case "notification-dismiss", "notification-read":
		return s.updateNotification(req)
	case "notification-start-agent":
		return s.startAgentForNotification(req)
	case "set-rule-mode":
		return s.setRuleMode(req)
	case "overview-chat":
		return s.overviewChat(req)
	default:
		return actionResponse{OK: false, Message: "unknown action " + req.Kind}, http.StatusBadRequest
	}
}

func qualifiedEntityRef(kind, slug string) (string, error) {
	switch strings.TrimSpace(kind) {
	case "task", "project", "playbook":
		return kind + "/" + slug, nil
	case "":
		return slug, nil
	default:
		return "", fmt.Errorf("invalid entity kind %q", kind)
	}
}

func (s *Server) createFlow(req actionRequest) (actionResponse, int) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return actionResponse{OK: false, Message: "name is required"}, http.StatusBadRequest
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = strings.TrimSpace(req.Target)
	}
	if err := validateSlug(slug); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	priority := strings.TrimSpace(req.Priority)
	if priority == "" {
		priority = "medium"
	}
	permissionMode, err := flowdb.NormalizePermissionMode(req.PermissionMode)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	provider, err := s.availableProvider(req.Provider)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	project := strings.TrimSpace(req.Project)
	if project == "__adhoc" {
		project = ""
	}
	if project != "" {
		if err := validateSlug(project); err != nil {
			return actionResponse{OK: false, Message: "project: " + err.Error()}, http.StatusBadRequest
		}
	}
	workDir := strings.TrimSpace(req.WorkDir)

	existing, err := flowdb.GetTask(s.cfg.DB, slug)
	if err == nil {
		return s.createFlowFromExisting(req, existing, provider, permissionMode, priority, project, workDir)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}

	args := []string{"add", "task", name, "--slug", slug, "--priority", priority}
	if provider != "claude" {
		args = append(args, "--agent", provider)
	}
	if permissionMode != "default" {
		args = append(args, "--permission-mode", permissionMode)
	}
	if project != "" {
		args = append(args, "--project", project)
	}
	if workDir != "" {
		args = append(args, "--work-dir", workDir)
	}
	out, err := s.runFlowCommand(args...)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
	}
	if strings.TrimSpace(req.Prompt) != "" {
		if err := s.writeTaskBrief(slug, name, req.Prompt); err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
	}
	if repo, number, ok := parseGitHubPRURL(req.PRURL); ok {
		_ = flowdb.UpsertTaskPRLink(s.cfg.DB, slug, repo, number, strings.TrimSpace(req.PRURL))
	}
	agent, _ := s.agentForTask(slug)
	return actionResponse{OK: true, Message: "created " + slug + "; opening browser terminal", Output: out, Agent: agent, Bridge: true}, http.StatusOK
}

func (s *Server) createFlowFromExisting(req actionRequest, task *flowdb.Task, provider, permissionMode, priority, project, workDir string) (actionResponse, int) {
	if task == nil {
		return actionResponse{OK: false, Message: "task not found"}, http.StatusInternalServerError
	}
	if !task.ArchivedAt.Valid && !task.DeletedAt.Valid && task.Status != "done" {
		agent, _ := s.agentForTask(task.Slug)
		if agent != nil {
			if err := s.ensureProviderAvailable(firstNonEmpty(agent.Provider, "claude")); err != nil {
				return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
			}
		}
		return actionResponse{
			OK:      true,
			Message: "task " + task.Slug + " already exists; opening browser terminal",
			Agent:   agent,
			Bridge:  true,
		}, http.StatusOK
	}
	if workDir == "" {
		workDir = strings.TrimSpace(task.WorkDir)
	}
	if workDir == "" {
		return actionResponse{OK: false, Message: "work_dir is required to reactivate " + task.Slug}, http.StatusBadRequest
	}
	now := flowdb.NowISO()
	var projectValue any
	if project != "" {
		projectValue = project
	}
	if _, err := s.cfg.DB.Exec(
		`UPDATE tasks SET
			name = ?,
			project_slug = ?,
			status = 'backlog',
			kind = 'regular',
			playbook_slug = NULL,
			priority = ?,
			work_dir = ?,
			waiting_on = NULL,
			permission_mode = ?,
			status_changed_at = ?,
			session_provider = ?,
			session_id = NULL,
			session_started = NULL,
			session_last_resumed = NULL,
			updated_at = ?,
			archived_at = NULL,
			deleted_at = NULL
		 WHERE slug = ?`,
		strings.TrimSpace(req.Name),
		projectValue,
		priority,
		workDir,
		permissionMode,
		now,
		provider,
		now,
		task.Slug,
	); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if strings.TrimSpace(req.Prompt) != "" {
		if err := s.writeTaskBrief(task.Slug, strings.TrimSpace(req.Name), req.Prompt); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
	}
	if repo, number, ok := parseGitHubPRURL(req.PRURL); ok {
		_ = flowdb.UpsertTaskPRLink(s.cfg.DB, task.Slug, repo, number, strings.TrimSpace(req.PRURL))
	}
	agent, _ := s.agentForTask(task.Slug)
	return actionResponse{
		OK:      true,
		Message: "reactivated " + task.Slug + "; opening browser terminal",
		Agent:   agent,
		Bridge:  true,
	}, http.StatusOK
}

func (s *Server) pauseTask(target string) (actionResponse, int) {
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	s.terminals.stop(target)
	agent, err := s.agentForTask(target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "paused " + target + "; agent is idle", Agent: agent}, http.StatusOK
}

func (s *Server) killSession(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	if target != "" {
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return actionResponse{OK: false, Message: "session_id is required"}, http.StatusBadRequest
	}
	if !safeSessionRe.MatchString(sessionID) {
		return actionResponse{OK: false, Message: "invalid session_id"}, http.StatusBadRequest
	}
	pid, err := claudePIDForSession(sessionID)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusNotFound
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return actionResponse{OK: false, Message: fmt.Sprintf("SIGTERM %d: %v", pid, err)}, http.StatusInternalServerError
	}
	if target == "" {
		target = sessionID[:8]
	}
	return actionResponse{OK: true, Message: fmt.Sprintf("sent SIGTERM to %s (pid %d)", target, pid)}, http.StatusOK
}

func (s *Server) ackApproval(kind, target string) (actionResponse, int) {
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	return actionResponse{
		OK:      true,
		Message: fmt.Sprintf("%s noted for %s; answer terminal-native prompts in the opened task terminal", kind, target),
	}, http.StatusOK
}

func (s *Server) openBrowserTerminalBridge(target string) (actionResponse, int) {
	agent, err := s.agentForTask(target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	provider := firstNonEmpty(agent.Provider, "claude")
	if err := s.ensureProviderAvailable(provider); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	return actionResponse{
		OK:      true,
		Message: "opening browser terminal for " + target,
		Agent:   agent,
		Bridge:  true,
	}, http.StatusOK
}

func (s *Server) spawnPlaybookRunBridge(target string, req actionRequest) (actionResponse, int) {
	provider, err := s.availableProvider(req.Provider)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	permissionMode, err := flowdb.NormalizePermissionMode(req.PermissionMode)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	pb, err := flowdb.GetPlaybook(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "playbook not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if pb.ArchivedAt.Valid {
		return actionResponse{OK: false, Message: "playbook " + target + " is archived"}, http.StatusBadRequest
	}
	if pb.DeletedAt.Valid {
		return actionResponse{OK: false, Message: "playbook " + target + " is deleted"}, http.StatusBadRequest
	}
	if strings.TrimSpace(pb.WorkDir) == "" {
		return actionResponse{OK: false, Message: "playbook " + target + " has no work_dir"}, http.StatusBadRequest
	}
	runSlug, err := s.createPlaybookRunTask(pb, provider, permissionMode)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, err := s.agentForTask(runSlug)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{
		OK:      true,
		Message: "created playbook run " + runSlug + "; opening browser terminal",
		Agent:   agent,
		Bridge:  true,
	}, http.StatusOK
}

func (s *Server) createPlaybookRunTask(pb *flowdb.Playbook, provider, permissionMode string) (string, error) {
	root := strings.TrimSpace(s.cfg.FlowRoot)
	if root == "" {
		return "", errors.New("flow root is not configured")
	}
	pbBriefPath := filepath.Join(root, "playbooks", pb.Slug, "brief.md")
	pbBriefBytes, err := os.ReadFile(pbBriefPath)
	if err != nil {
		return "", fmt.Errorf("read playbook brief %s: %w", pbBriefPath, err)
	}
	runSlug, err := generatePlaybookRunSlug(s.cfg.DB, pb.Slug, time.Now())
	if err != nil {
		return "", err
	}
	now := flowdb.NowISO()
	_, err = s.cfg.DB.Exec(
		`INSERT INTO tasks (
			slug, name, project_slug, status, kind, playbook_slug, priority,
			work_dir, permission_mode, session_provider, status_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, 'backlog', 'playbook_run', ?, 'medium', ?, ?, ?, ?, ?, ?)`,
		runSlug,
		fmt.Sprintf("%s run %s", pb.Slug, runSlug),
		pb.ProjectSlug,
		pb.Slug,
		pb.WorkDir,
		permissionMode,
		provider,
		now, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert run task: %w", err)
	}
	runDir := filepath.Join(root, "tasks", runSlug)
	if err := os.MkdirAll(filepath.Join(runDir, "updates"), 0o755); err != nil {
		_ = deleteUnstartedPlaybookRun(s.cfg.DB, runSlug)
		return "", fmt.Errorf("mkdir %s: %w", runDir, err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "brief.md"), pbBriefBytes, 0o644); err != nil {
		_ = deleteUnstartedPlaybookRun(s.cfg.DB, runSlug)
		return "", fmt.Errorf("write run brief.md: %w", err)
	}
	return runSlug, nil
}

func generatePlaybookRunSlug(db *sql.DB, playbookSlug string, t time.Time) (string, error) {
	t = t.UTC()
	minute := fmt.Sprintf("%s--%04d-%02d-%02d-%02d-%02d",
		playbookSlug, t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
	if ok, err := taskSlugAvailable(db, minute); err != nil {
		return "", err
	} else if ok {
		return minute, nil
	}
	second := fmt.Sprintf("%s-%02d", minute, t.Second())
	if ok, err := taskSlugAvailable(db, second); err != nil {
		return "", err
	} else if ok {
		return second, nil
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", second, n)
		if ok, err := taskSlugAvailable(db, candidate); err != nil {
			return "", err
		} else if ok {
			return candidate, nil
		}
	}
	return "", errors.New("could not generate unique run slug after 1000 attempts")
}

func taskSlugAvailable(db *sql.DB, slug string) (bool, error) {
	var got string
	err := db.QueryRow(`SELECT slug FROM tasks WHERE slug = ?`, slug).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func deleteUnstartedPlaybookRun(db *sql.DB, slug string) error {
	_, err := db.Exec(`DELETE FROM tasks WHERE slug = ? AND kind = 'playbook_run' AND session_id IS NULL`, slug)
	return err
}

func (s *Server) restartBrowserTerminalBridge(target string) (actionResponse, int) {
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if task.Status == "done" {
		return actionResponse{OK: false, Message: "task " + target + " is done; move it back to in-progress before reopening"}, http.StatusBadRequest
	}
	provider := strings.TrimSpace(task.SessionProvider)
	if provider == "" {
		provider = "claude"
	}
	if err := s.ensureProviderAvailable(provider); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	s.terminals.stop(target)
	now := flowdb.NowISO()
	if strings.TrimSpace(task.SessionID.String) != "" {
		if _, err := s.cfg.DB.Exec(
			`UPDATE tasks SET
				status = 'in-progress',
				status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
				session_last_resumed = ?,
				updated_at = ?
			 WHERE slug = ?`,
			now, now, now, target,
		); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
	} else if _, err := s.cfg.DB.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, now, target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, _ := s.agentForTask(target)
	return actionResponse{OK: true, Message: "restarting browser terminal for " + target, Agent: agent, Bridge: true}, http.StatusOK
}

func (s *Server) openTaskBridge(target, terminalKind string, force bool) (actionResponse, int) {
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	provider := strings.TrimSpace(task.SessionProvider)
	if provider == "" {
		provider = "claude"
	}
	if err := s.ensureProviderAvailable(provider); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	launch, err := s.prepareTerminalLaunch(target)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if err := s.spawnNativeTerminal(terminalKind, task, launch); err != nil {
		if launch.Created {
			s.rollbackPreparedTerminalLaunch(launch)
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, _ := s.agentForTask(target)
	return actionResponse{OK: true, Message: "opened " + target + " in " + terminalLabel(terminalKind), Agent: agent}, http.StatusOK
}

func (s *Server) spawnNativeTerminal(kind string, task *flowdb.Task, launch terminalLaunch) error {
	command := agentShellCommand(launch.Provider, launch.Args)
	env := s.nativeTerminalEnv()
	title := nativeTerminalTitle(task)
	switch kind {
	case "iterm":
		return iterm.SpawnTab(title, launch.WorkDir, command, env)
	case "terminal":
		return macterminal.SpawnTab(title, launch.WorkDir, command, env)
	case "kitty":
		return kitty.SpawnTab(title, launch.WorkDir, command, env)
	case "warp":
		return warp.SpawnTab(title, launch.WorkDir, command, env)
	case "alacritty":
		return startShellTerminal("alacritty", "Alacritty", launch.WorkDir, command, env, "--working-directory", launch.WorkDir, "-e")
	case "ghostty":
		return startShellTerminal("ghostty", "Ghostty", launch.WorkDir, command, env, "--working-directory="+launch.WorkDir, "-e")
	case "wezterm":
		args := append([]string{"start", "--cwd", launch.WorkDir, "--"}, shellCommandArgs(command, env)...)
		return nativeCommandStarter("wezterm", "", args...)
	case "tmux":
		return nativeCommandStarter("tmux", "", "new-window", "-n", title, "-c", launch.WorkDir, shellCommandLine(command, env))
	case "vscode":
		return nativeCommandStarter("code", "", "-n", "--reuse-window", launch.WorkDir)
	default:
		return fmt.Errorf("unsupported terminal %q", kind)
	}
}

func (s *Server) switchBranch(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	branch := strings.TrimSpace(req.Branch)
	if err := validateBranch(branch); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if strings.TrimSpace(task.WorkDir) == "" {
		return actionResponse{OK: false, Message: "task " + target + " has no work_dir"}, http.StatusBadRequest
	}
	out, err := runGitCombined(task.WorkDir, "switch", branch)
	if err != nil && strings.Contains(branch, "/") {
		trackOut, trackErr := runGitCombined(task.WorkDir, "switch", "--track", branch)
		if trackErr == nil {
			out, err = trackOut, nil
		}
	}
	if err != nil {
		return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusConflict
	}
	agent, _ := s.agentForTask(target)
	return actionResponse{OK: true, Message: "switched " + target + " to " + branch, Output: out, Agent: agent}, http.StatusOK
}

func agentShellCommand(provider string, args []string) string {
	bin := provider
	if bin == "" || bin == "claude" {
		bin = "claude"
	}
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

func (s *Server) nativeTerminalEnv() map[string]string {
	env := map[string]string{}
	if root := strings.TrimSpace(s.cfg.FlowRoot); root != "" {
		env["FLOW_ROOT"] = root
	} else if root := os.Getenv("FLOW_ROOT"); root != "" {
		env["FLOW_ROOT"] = root
	}
	if path := pathWithCommandDir(os.Getenv("PATH"), s.cfg.CommandPath); path != "" {
		env["PATH"] = path
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func pathWithCommandDir(current, commandPath string) string {
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

func nativeTerminalTitle(task *flowdb.Task) string {
	if task.ProjectSlug.Valid && task.ProjectSlug.String != "" {
		return task.ProjectSlug.String + "/" + task.Slug
	}
	return task.Slug
}

func shellCommandLine(command string, env map[string]string) string {
	prefix := ""
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+spawner.ShellQuote(env[k]))
		}
		prefix = strings.Join(parts, " ") + " "
	}
	return prefix + command
}

func shellCommandArgs(command string, env map[string]string) []string {
	return []string{"sh", "-lc", shellCommandLine(command, env)}
}

func startShellTerminal(bin, appName, cwd, command string, env map[string]string, args ...string) error {
	fullArgs := append(append([]string{}, args...), shellCommandArgs(command, env)...)
	if err := nativeCommandStarter(bin, "", fullArgs...); err == nil {
		return nil
	}
	return nativeCommandStarter("open", "", append([]string{"-na", appName, "--args"}, fullArgs...)...)
}

func startNativeCommand(name, dir string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	if dir != "" {
		cmd.Dir = dir
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(append([]string{name}, args...), " "), err)
	}
	return cmd.Process.Release()
}

func terminalLabel(kind string) string {
	switch kind {
	case "iterm":
		return "iTerm"
	case "terminal":
		return "Terminal.app"
	case "warp":
		return "Warp"
	case "kitty":
		return "kitty"
	case "alacritty":
		return "Alacritty"
	case "ghostty":
		return "Ghostty"
	case "wezterm":
		return "WezTerm"
	case "tmux":
		return "tmux"
	case "vscode":
		return "VS Code"
	default:
		return kind
	}
}

func runGitCombined(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(text))
	}
	return text, nil
}

func validateBranch(branch string) error {
	if branch == "" {
		return errors.New("branch is required")
	}
	if !safeBranchRe.MatchString(branch) ||
		strings.Contains(branch, "..") ||
		strings.Contains(branch, "@{") ||
		strings.HasPrefix(branch, "-") ||
		strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "//") {
		return fmt.Errorf("invalid branch %q", branch)
	}
	return nil
}

func (h *terminalHub) running(slug string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.sessions[slug]
	return sess != nil && sess.running()
}

func (s *Server) agentForTask(slug string) (*uiAgent, error) {
	task, err := flowdb.GetTask(s.cfg.DB, slug)
	if err != nil {
		return nil, err
	}
	live, _ := liveAgentSessions()
	view, err := BuildTaskView(s.cfg.DB, s.cfg.FlowRoot, task, live)
	if err != nil {
		return nil, err
	}
	agent := s.uiAgent(view, live)
	if transcript := s.fullUITranscriptForTask(view); len(transcript) > 0 {
		agent.Transcript = transcript
		agent.RecentTools = recentTools(transcript)
	}
	return &agent, nil
}

func (s *Server) forkTask(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	slug := s.availableTaskSlug(target + "-fork")
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = target
	}
	priority := strings.TrimSpace(req.Priority)
	if priority == "" {
		priority = "medium"
	}
	args := []string{"add", "task", name + " fork", "--slug", slug, "--priority", priority}
	if req.Project != "" && req.Project != "__adhoc" {
		if err := validateSlug(req.Project); err != nil {
			return actionResponse{OK: false, Message: "project: " + err.Error()}, http.StatusBadRequest
		}
		args = append(args, "--project", req.Project)
	}
	if req.WorkDir != "" {
		args = append(args, "--work-dir", req.WorkDir)
	}
	out, err := s.runFlowCommand(args...)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "forked " + target + " to " + slug, Output: out}, http.StatusOK
}

func (s *Server) editPlaybook(target string) (actionResponse, int) {
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	pb, err := flowdb.GetPlaybook(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "playbook not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	brief := filepath.Join(s.cfg.FlowRoot, "playbooks", pb.Slug, "brief.md")
	return actionResponse{OK: true, Message: "playbook brief: " + brief}, http.StatusOK
}

func (s *Server) monitorSync() (actionResponse, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	summaries, err := (flowmonitor.Poller{DB: s.cfg.DB}).Poll(ctx, "all")
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	parts := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		label := fmt.Sprintf("%s %d events", summary.Source, summary.Events)
		if len(summary.Errors) > 0 {
			label += " (" + strings.Join(summary.Errors, "; ") + ")"
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		parts = append(parts, "no monitor sources configured")
	}
	if started, err := s.autoStartMonitorEvents(); err == nil && started > 0 {
		parts = append(parts, fmt.Sprintf("%d auto-started", started))
	} else if err != nil {
		parts = append(parts, "auto-start error: "+err.Error())
	}
	return actionResponse{OK: true, Message: "monitor sync: " + strings.Join(parts, " · ")}, http.StatusOK
}

func (s *Server) updateNotification(req actionRequest) (actionResponse, int) {
	id := strings.TrimSpace(firstNonEmpty(req.Target, req.EventID))
	if id == "" {
		return actionResponse{OK: false, Message: "notification id is required"}, http.StatusBadRequest
	}
	status := "read"
	if req.Kind == "notification-dismiss" {
		status = "dismissed"
	}
	if err := flowdb.UpdateNotificationStatus(s.cfg.DB, id, status); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	return actionResponse{OK: true, Message: "notification " + status}, http.StatusOK
}

func (s *Server) setRuleMode(req actionRequest) (actionResponse, int) {
	source := strings.TrimSpace(req.Source)
	kind := strings.TrimSpace(req.RuleKind)
	mode := strings.TrimSpace(req.Mode)
	if source == "" || kind == "" || mode == "" {
		return actionResponse{OK: false, Message: "source, rule_kind, and mode are required"}, http.StatusBadRequest
	}
	if err := flowdb.SetAutomationRuleMode(s.cfg.DB, source, kind, mode); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	return actionResponse{OK: true, Message: "rule updated: " + source + "." + kind + "=" + mode}, http.StatusOK
}

func (s *Server) startAgentForNotification(req actionRequest) (actionResponse, int) {
	eventID := strings.TrimSpace(firstNonEmpty(req.EventID, req.Target))
	if eventID == "" {
		return actionResponse{OK: false, Message: "event_id is required"}, http.StatusBadRequest
	}
	event, err := flowdb.GetMonitorEvent(s.cfg.DB, eventID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "event not found: " + eventID}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, out, err := s.createAgentTaskForMonitorEvent(*event, false)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "started agent for " + event.Title, Output: out, Agent: agent, Bridge: true}, http.StatusOK
}

func (s *Server) autoStartMonitorEvents() (int, error) {
	events, err := flowdb.ListMonitorEvents(s.cfg.DB, 100)
	if err != nil {
		return 0, err
	}
	started := 0
	for _, event := range events {
		if event.Status == "started" || event.Status == "done" || event.Status == "ignored" {
			continue
		}
		mode, err := flowdb.AutomationModeFor(s.cfg.DB, event.Source, event.Kind)
		if err != nil {
			return started, err
		}
		switch mode {
		case "auto_agent", "auto_agent_draft_only":
			agent, _, err := s.createAgentTaskForMonitorEvent(event, true)
			if err != nil {
				return started, err
			}
			if agent != nil {
				started++
			}
		}
	}
	return started, nil
}

func (s *Server) createAgentTaskForMonitorEvent(event flowdb.MonitorEvent, startTerminal bool) (*uiAgent, string, error) {
	slug := s.availableTaskSlug(monitorTaskSlug(event))
	name := truncateText(event.Title, 80)
	workDir := s.defaultMonitorWorkdir()
	project := s.projectForMonitorEvent(event)
	if project != "" {
		if p, err := flowdb.GetProject(s.cfg.DB, project); err == nil && strings.TrimSpace(p.WorkDir) != "" {
			workDir = p.WorkDir
		}
	}
	args := []string{"add", "task", name, "--slug", slug, "--priority", monitorPriority(event.Severity), "--work-dir", workDir}
	if project != "" {
		args = append(args, "--project", project)
	}
	out, err := s.runFlowCommand(args...)
	if err != nil {
		return nil, out, err
	}
	if err := s.writeTaskBrief(slug, name, monitorTaskBrief(event)); err != nil {
		return nil, out, err
	}
	if repo, number, ok := githubPRFromEvent(event); ok {
		_ = flowdb.UpsertTaskPRLink(s.cfg.DB, slug, repo, number, nullStringValue(event.URL))
	}
	_ = flowdb.UpdateMonitorEventStatus(s.cfg.DB, event.ID, "started")
	_ = flowdb.UpdateNotificationStatus(s.cfg.DB, "notif-"+event.ID, "actioned")
	if startTerminal {
		_, _ = s.terminals.attach(slug, 120, 32)
	}
	agent, _ := s.agentForTask(slug)
	return agent, out, nil
}

func (s *Server) overviewChat(req actionRequest) (actionResponse, int) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return actionResponse{OK: false, Message: "prompt is required"}, http.StatusBadRequest
	}
	slug := overviewTaskSlug
	s.terminals.stop(slug)
	if err := s.prepareOverviewTask(prompt); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if _, err := s.terminals.attach(slug, 120, 32); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, _ := s.agentForTask(slug)
	return actionResponse{OK: true, Message: "opened overview agent", Agent: agent, Bridge: true}, http.StatusOK
}

func (s *Server) prepareOverviewTask(prompt string) error {
	flowRoot := strings.TrimSpace(s.cfg.FlowRoot)
	if flowRoot == "" {
		return errors.New("flow root is not configured")
	}
	absRoot, err := filepath.Abs(flowRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(absRoot, "tasks", overviewTaskSlug, "updates"), 0o755); err != nil {
		return err
	}
	if err := flowdb.UpsertWorkdir(s.cfg.DB, absRoot, "flow root", "", ""); err != nil {
		return err
	}
	now := flowdb.NowISO()
	_, err = flowdb.GetTask(s.cfg.DB, overviewTaskSlug)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.cfg.DB.Exec(
			`INSERT INTO tasks (
				slug, name, status, kind, priority, work_dir, status_changed_at, created_at, updated_at
			) VALUES (?, ?, 'backlog', 'regular', 'medium', ?, ?, ?, ?)`,
			overviewTaskSlug, overviewTaskName, absRoot, now, now, now,
		)
	} else if err == nil {
		_, err = s.cfg.DB.Exec(
			`UPDATE tasks SET
				name = ?,
				project_slug = NULL,
				status = 'backlog',
				kind = 'regular',
				playbook_slug = NULL,
				priority = 'medium',
				work_dir = ?,
				waiting_on = NULL,
				session_provider = 'claude',
				session_id = NULL,
				session_started = NULL,
				session_last_resumed = NULL,
				status_changed_at = ?,
				updated_at = ?
			 WHERE slug = ?`,
			overviewTaskName, absRoot, now, now, overviewTaskSlug,
		)
	}
	if err != nil {
		return err
	}
	return s.writeTaskBrief(overviewTaskSlug, overviewTaskName, overviewBrief(prompt))
}

func (s *Server) defaultMonitorWorkdir() string {
	workdirs, err := flowdb.ListWorkdirs(s.cfg.DB)
	if err == nil && len(workdirs) > 0 {
		return workdirs[0].Path
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return cwd
	}
	return s.cfg.FlowRoot
}

func (s *Server) projectForMonitorEvent(event flowdb.MonitorEvent) string {
	repo, _, ok := githubPRFromEvent(event)
	if !ok || repo == "" {
		return ""
	}
	projects, err := flowdb.ListProjects(s.cfg.DB, flowdb.ProjectFilter{})
	if err != nil {
		return ""
	}
	repoBase := repo
	if idx := strings.LastIndex(repoBase, "/"); idx >= 0 {
		repoBase = repoBase[idx+1:]
	}
	for _, project := range projects {
		if strings.Contains(strings.ToLower(project.WorkDir), strings.ToLower(repoBase)) {
			return project.Slug
		}
		if remote, err := runGitCombined(project.WorkDir, "remote", "get-url", "origin"); err == nil {
			remote = strings.ToLower(remote)
			if strings.Contains(remote, strings.ToLower(repo)) || strings.Contains(remote, strings.ToLower(repoBase)) {
				return project.Slug
			}
		}
	}
	return ""
}

func githubPRFromEvent(event flowdb.MonitorEvent) (string, int, bool) {
	if event.Source != "github" {
		return "", 0, false
	}
	parts := strings.Split(event.SourceID, ":")
	if len(parts) >= 3 {
		n, err := strconv.Atoi(parts[len(parts)-1])
		if err == nil && n > 0 {
			repo := strings.Join(parts[1:len(parts)-1], ":")
			if repo != "" {
				return repo, n, true
			}
		}
	}
	return parseGitHubPRURL(nullStringValue(event.URL))
}

func parseGitHubPRURL(raw string) (string, int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, false
	}
	match := githubPRURLRe.FindStringSubmatch(raw)
	if len(match) == 3 {
		n, err := strconv.Atoi(match[2])
		if err != nil || n <= 0 {
			return "", 0, false
		}
		return match[1], n, true
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Host, "api.github.com") {
		return "", 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "repos" || parts[3] != "pulls" {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[4])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return parts[1] + "/" + parts[2], n, true
}

func monitorTaskSlug(event flowdb.MonitorEvent) string {
	base := strings.ToLower(event.Source + "-" + event.Kind + "-" + event.SourceID)
	base = strings.NewReplacer("/", "-", ":", "-", "#", "", " ", "-", "_", "-").Replace(base)
	base = strings.Trim(base, "-.")
	if len(base) > 46 {
		base = base[:46]
	}
	if base == "" || !safeSlugRe.MatchString(base) {
		return "monitor-event"
	}
	return base
}

func monitorPriority(severity string) string {
	if severity == "high" {
		return "high"
	}
	if severity == "low" {
		return "low"
	}
	return "medium"
}

func monitorTaskBrief(event flowdb.MonitorEvent) string {
	body := nullStringValue(event.Body)
	url := nullStringValue(event.URL)
	return fmt.Sprintf(
		"Source event from flow monitor.\n\nSource: %s\nKind: %s\nSeverity: %s\nURL: %s\n\nTitle:\n%s\n\nBody:\n%s\n\nStart by inspecting the source context, then propose or perform the smallest useful next step. Draft external replies/comments only; do not send them without approval.",
		event.Source, event.Kind, event.Severity, url, event.Title, body,
	)
}

func overviewBrief(prompt string) string {
	return "You are the Flow overview command-center agent. Help the user decide what to do today, inspect Flow/GitHub/Slack monitor context when relevant, and route work into Flow tasks or sessions.\n\nLatest user request:\n" + prompt
}

func (s *Server) availableTaskSlug(base string) string {
	slug := base
	for i := 0; i < 50; i++ {
		if err := validateSlug(slug); err != nil {
			return base
		}
		_, err := flowdb.GetTask(s.cfg.DB, slug)
		if errors.Is(err, sql.ErrNoRows) {
			return slug
		}
		if err != nil {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i+2)
	}
	return fmt.Sprintf("%s-%d", base, time.Now().Unix())
}

func (s *Server) writeTaskBrief(slug, name, prompt string) error {
	path := filepath.Join(s.cfg.FlowRoot, "tasks", slug, "brief.md")
	if !strings.HasPrefix(filepath.Clean(path), filepath.Join(s.cfg.FlowRoot, "tasks")+string(os.PathSeparator)) {
		return errors.New("invalid task brief path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create task brief dir: %w", err)
	}
	body := fmt.Sprintf("# %s\n\n%s\n", name, strings.TrimSpace(prompt))
	return os.WriteFile(path, []byte(body), 0o644)
}

func claudePIDForSession(sessionID string) (int, error) {
	out, err := psRunner()
	if err != nil {
		return 0, fmt.Errorf("ps: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(strings.ToLower(line), "claude") {
			continue
		}
		matches := claudeSessionArgRe.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) < 2 || !strings.EqualFold(match[1], sessionID) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				return 0, errors.New("matched session but could not read pid")
			}
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				return 0, fmt.Errorf("matched session but invalid pid %q", fields[0])
			}
			return pid, nil
		}
	}
	return 0, errors.New("no live Claude process found for session " + sessionID[:8])
}

func (s *Server) runFlowCommand(args ...string) (string, error) {
	exe := s.cfg.CommandPath
	if exe == "" {
		return "", errors.New("flow command path is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	text := string(out)
	if ctx.Err() == context.DeadlineExceeded {
		return text, errors.New("flow command timed out")
	}
	if err != nil {
		return text, fmt.Errorf("%s: %w", strings.Join(append([]string{exe}, args...), " "), err)
	}
	return text, nil
}

func validateSlug(slug string) error {
	if slug == "" {
		return errors.New("target slug is required")
	}
	if !safeSlugRe.MatchString(slug) || strings.Contains(slug, "..") {
		return fmt.Errorf("invalid slug %q", slug)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func writeJSONStatus(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
