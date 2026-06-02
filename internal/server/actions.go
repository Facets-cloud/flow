package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"flow/internal/kitty"
	"flow/internal/spawner"
	macterminal "flow/internal/terminal"
	"flow/internal/warp"
	"flow/internal/workdirreg"
	"fmt"
	"mime/multipart"
	"net/http"
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
	Path           string `json:"path"`
	Description    string `json:"description"`
	Project        string `json:"project"`
	WorkDir        string `json:"work_dir"`
	Priority       string `json:"priority"`
	Prompt         string `json:"prompt"`
	SessionID      string `json:"session_id"`
	Branch         string `json:"branch"`
	EntityKind     string `json:"entity_kind"`
	Provider       string `json:"provider"`
	PermissionMode string `json:"permission_mode"`
	Mkdir          bool   `json:"mkdir"`

	AttachmentFiles []*multipart.FileHeader `json:"-"`
}

type actionResponse struct {
	OK               bool                      `json:"ok"`
	Message          string                    `json:"message"`
	Output           string                    `json:"output,omitempty"`
	Agent            *uiAgent                  `json:"agent,omitempty"`
	FloatingTerminal *floatingTerminalResponse `json:"floating_terminal,omitempty"`
	Bridge           bool                      `json:"bridge,omitempty"`
	AlreadyLive      bool                      `json:"already_live,omitempty"`
}

type floatingTerminalResponse struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Title    string `json:"title"`
}

var (
	safeSlugRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	safeSessionRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	safeBranchRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-]*$`)
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
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		var err error
		req, err = s.multipartActionRequest(w, r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
	}
	resp, status := s.runAction(req)
	writeJSONStatus(w, resp, status)
	if status < 400 && resp.OK {
		s.publishUIChange(req.Kind)
	}
}

func (s *Server) multipartActionRequest(w http.ResponseWriter, r *http.Request) (actionRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTerminalAttachmentUploadBytes)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		return actionRequest{}, err
	}
	req := actionRequest{
		Kind:           strings.TrimSpace(r.FormValue("kind")),
		Target:         strings.TrimSpace(r.FormValue("target")),
		Slug:           strings.TrimSpace(r.FormValue("slug")),
		Name:           strings.TrimSpace(r.FormValue("name")),
		Path:           strings.TrimSpace(r.FormValue("path")),
		Description:    strings.TrimSpace(r.FormValue("description")),
		Project:        strings.TrimSpace(r.FormValue("project")),
		WorkDir:        strings.TrimSpace(r.FormValue("work_dir")),
		Priority:       strings.TrimSpace(r.FormValue("priority")),
		Prompt:         r.FormValue("prompt"),
		SessionID:      strings.TrimSpace(r.FormValue("session_id")),
		Branch:         strings.TrimSpace(r.FormValue("branch")),
		EntityKind:     strings.TrimSpace(r.FormValue("entity_kind")),
		Provider:       strings.TrimSpace(r.FormValue("provider")),
		PermissionMode: strings.TrimSpace(r.FormValue("permission_mode")),
	}
	if req.Kind == "" {
		return actionRequest{}, errors.New("kind is required")
	}
	if raw := strings.TrimSpace(r.FormValue("mkdir")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return actionRequest{}, fmt.Errorf("mkdir: %w", err)
		}
		req.Mkdir = value
	}
	if r.MultipartForm != nil {
		req.AttachmentFiles = r.MultipartForm.File["images"]
		if len(req.AttachmentFiles) == 0 {
			req.AttachmentFiles = r.MultipartForm.File["files"]
		}
	}
	return req, nil
}

func (s *Server) runAction(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	switch req.Kind {
	case "spawn":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.openBrowserTerminalBridge(target, req.Provider)
	case "resume", "attach":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.openBrowserTerminalBridge(target, "")
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
	case "restart-fresh":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.restartFreshBrowserTerminalBridge(target)
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
	case "done":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		// `flow done` flips status, snapshots git, and runs the headless KB /
		// project-update close-out sweep (the slow part — within runFlowCommand's
		// 2-minute budget). Output is returned so the UI can confirm which phases
		// actually ran.
		out, err := s.runFlowCommand("done", target)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
		return actionResponse{OK: true, Message: "marked " + target + " done", Output: out}, http.StatusOK
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
	case "destroy":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.destroyDeletedEntity(req.EntityKind, target)
	case "workdir-add", "workdir-rename", "workdir-remove":
		return s.workdirAction(req)
	case "spawn-run":
		if err := validateSlug(target); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		return s.spawnPlaybookRunBridge(target, req)
	case "create-flow":
		return s.createFlow(req)
	case "create-project":
		return s.createProject(req)
	case "create-playbook":
		return s.createPlaybook(req)
	case "create-kb":
		return s.createKB(req)
	case "update-permission-mode":
		return s.updatePermissionMode(req)
	case "update-priority":
		return s.updatePriority(req)
	case "update-provider":
		return s.updateProvider(req)
	case "update-task-name":
		return s.updateTaskName(req)
	case "pause":
		return s.pauseTask(target)
	case "clear-waiting":
		return s.clearWaiting(target)
	case "mark-read":
		return s.markInboxRead(target)
	case "kill":
		return s.killSession(req)
	case "approve", "deny":
		return s.ackApproval(req.Kind, target)
	case "fork":
		return s.forkTask(req)
	case "edit-playbook":
		return s.editPlaybook(target)
	case "nudge":
		return s.nudgeSession(target, req.Prompt)
	case "overview-chat":
		return s.overviewChat(req)
	default:
		return actionResponse{OK: false, Message: "unknown action " + req.Kind}, http.StatusBadRequest
	}
}

// nudgeSession delivers a user-typed instruction into a task's agent session
// without opening the terminal. It reuses the exact path the inbox monitor uses
// to auto-inject on new messages (see deliverInboxEvents): inject into the live
// server-managed PTY if one exists, otherwise resume the session and inject —
// but never duplicate a session the user is running in an external terminal.
func (s *Server) nudgeSession(slug, text string) (actionResponse, int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return actionResponse{OK: false, Message: "instruction is empty"}, http.StatusBadRequest
	}
	if slug == "" {
		return actionResponse{OK: false, Message: "no session specified"}, http.StatusBadRequest
	}
	// Live server PTY → inject straight away (paste + delayed Enter).
	if s.terminals != nil && s.terminals.running(slug) {
		if err := s.terminals.wakeTask(slug, text); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
		return actionResponse{OK: true, Message: "Instruction sent to session"}, http.StatusOK
	}
	task, err := flowdb.GetTask(s.cfg.DB, slug)
	if err != nil {
		return actionResponse{OK: false, Message: "session not found"}, http.StatusNotFound
	}
	// A native (user-owned terminal) session is alive: flow has no PTY to inject
	// into and must not spawn a duplicate.
	if s.taskAgentProcessLive(task) {
		return actionResponse{OK: false, Message: "Session is running in an external terminal — open it there to send."}, http.StatusConflict
	}
	if task.Status != "backlog" && task.Status != "in-progress" {
		return actionResponse{OK: false, Message: "Session is finished — reopen it to send an instruction."}, http.StatusConflict
	}
	provider := task.SessionProvider
	if provider == "" {
		provider = "claude"
	}
	if err := s.ensureProviderAvailable(provider); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	// No live session: wakeTask resumes a server PTY (--resume the prior session)
	// via attach, then injects the instruction.
	if err := s.terminals.wakeTask(slug, text); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "Resumed session and sent instruction"}, http.StatusOK
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

func (s *Server) workdirAction(req actionRequest) (actionResponse, int) {
	rawPath := strings.TrimSpace(firstNonEmpty(req.Path, req.WorkDir, req.Target))
	if rawPath == "" {
		return actionResponse{OK: false, Message: "workdir path is required"}, http.StatusBadRequest
	}
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	switch req.Kind {
	case "workdir-add", "workdir-rename":
		info, err := os.Stat(abs)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
		if !info.IsDir() {
			return actionResponse{OK: false, Message: abs + " is not a directory"}, http.StatusBadRequest
		}
		name := strings.TrimSpace(req.Name)
		description := strings.TrimSpace(req.Description)
		if req.Kind == "workdir-add" && name == "" {
			name = filepath.Base(abs)
		}
		if req.Kind == "workdir-rename" && name == "" {
			return actionResponse{OK: false, Message: "workdir name is required"}, http.StatusBadRequest
		}
		if err := workdirreg.Register(s.cfg.DB, abs, name, description); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
		verb := "registered"
		if req.Kind == "workdir-rename" {
			verb = "renamed"
		}
		return actionResponse{OK: true, Message: verb + " workdir " + abs}, http.StatusOK
	case "workdir-remove":
		if _, err := s.cfg.DB.Exec(`DELETE FROM workdirs WHERE path = ?`, abs); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
		return actionResponse{OK: true, Message: "removed workdir " + abs}, http.StatusOK
	default:
		return actionResponse{OK: false, Message: "unknown workdir action " + req.Kind}, http.StatusBadRequest
	}
}

func (s *Server) destroyDeletedEntity(kind, slug string) (actionResponse, int) {
	table, err := entityTable(kind)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	tx, err := s.cfg.DB.Begin()
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	defer tx.Rollback()

	var deletedAt sql.NullString
	if err := tx.QueryRow(fmt.Sprintf(`SELECT deleted_at FROM %s WHERE slug = ?`, table), slug).Scan(&deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: kind + " not found: " + slug}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if !deletedAt.Valid || strings.TrimSpace(deletedAt.String) == "" {
		return actionResponse{OK: false, Message: kind + " must be in trash before it can be permanently deleted"}, http.StatusConflict
	}
	if msg, err := permanentDeleteBlocker(tx, kind, slug); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	} else if msg != "" {
		return actionResponse{OK: false, Message: msg}, http.StatusConflict
	}
	if _, err := tx.Exec(`DELETE FROM search_docs WHERE entity_type = ? AND entity_slug = ?`, kind, slug); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE slug = ?`, table), slug); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if err := tx.Commit(); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	// The row is gone; reclaim the on-disk directory the entity owned
	// (tasks/<slug>, projects/<slug>, playbooks/<slug>) — its brief, updates,
	// inbox.jsonl, and workspace. Best-effort: the authoritative delete is the
	// row, so a leftover dir shouldn't fail the action, but destroy must fully
	// clean up rather than orphaning files in ~/.flow.
	if dir := s.entityDir(kind, slug); dir != "" {
		_ = os.RemoveAll(dir)
	}
	return actionResponse{OK: true, Message: "deleted " + kind + " " + slug}, http.StatusOK
}

// entityDir resolves the ~/.flow directory that backs a trashable entity, with
// the result confined to <FlowRoot>/<tasks|projects|playbooks>/ so a crafted
// slug can never escape the data tree.
func (s *Server) entityDir(kind, slug string) string {
	if validateSlug(slug) != nil {
		return ""
	}
	var sub string
	switch strings.TrimSpace(kind) {
	case "task":
		sub = "tasks"
	case "project":
		sub = "projects"
	case "playbook":
		sub = "playbooks"
	default:
		return ""
	}
	root := strings.TrimSpace(s.cfg.FlowRoot)
	if root == "" {
		return ""
	}
	base := filepath.Clean(filepath.Join(root, sub)) + string(os.PathSeparator)
	dir := filepath.Clean(filepath.Join(root, sub, slug))
	if !strings.HasPrefix(dir, base) {
		return ""
	}
	return dir
}

func entityTable(kind string) (string, error) {
	switch strings.TrimSpace(kind) {
	case "task":
		return "tasks", nil
	case "project":
		return "projects", nil
	case "playbook":
		return "playbooks", nil
	default:
		return "", fmt.Errorf("invalid entity kind %q", kind)
	}
}

func permanentDeleteBlocker(tx *sql.Tx, kind, slug string) (string, error) {
	var count int
	switch kind {
	case "task":
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE parent_slug = ?`, slug).Scan(&count); err != nil {
			return "", err
		}
		if count > 0 {
			return fmt.Sprintf("task %s still has %d child task(s); delete or detach them first", slug, count), nil
		}
	case "project":
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE project_slug = ?`, slug).Scan(&count); err != nil {
			return "", err
		}
		if count > 0 {
			return fmt.Sprintf("project %s still has %d task(s); delete or move them first", slug, count), nil
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM playbooks WHERE project_slug = ?`, slug).Scan(&count); err != nil {
			return "", err
		}
		if count > 0 {
			return fmt.Sprintf("project %s still has %d playbook(s); delete or move them first", slug, count), nil
		}
	case "playbook":
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE playbook_slug = ?`, slug).Scan(&count); err != nil {
			return "", err
		}
		if count > 0 {
			return fmt.Sprintf("playbook %s still has %d run task(s); delete them first", slug, count), nil
		}
	}
	return "", nil
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

	// `flow add task` now requires an explicit agent (no silent claude default),
	// so always pass the resolved provider — claude included.
	args := []string{"add", "task", name, "--slug", slug, "--priority", priority, "--agent", provider}
	args = append(args, "--permission-mode", permissionMode)
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
	prompt := strings.TrimSpace(req.Prompt)
	if len(req.AttachmentFiles) > 0 {
		files, err := s.saveTaskAttachmentFiles(slug, req.AttachmentFiles)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
		prompt = promptWithAttachedImages(prompt, files)
	}
	if strings.TrimSpace(prompt) != "" {
		if err := s.writeTaskBrief(slug, name, prompt); err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
	}
	agent, _ := s.agentForTask(slug)
	return actionResponse{OK: true, Message: "created " + slug + "; opening browser terminal", Output: out, Agent: agent, Bridge: true}, http.StatusOK
}

func (s *Server) createFlowFromExisting(req actionRequest, task *flowdb.Task, provider, permissionMode, priority, project, workDir string) (actionResponse, int) {
	if task == nil {
		return actionResponse{OK: false, Message: "task not found"}, http.StatusInternalServerError
	}
	if !task.ArchivedAt.Valid && !task.DeletedAt.Valid && task.Status != "done" {
		if err := flowdb.EnsureTaskStartable(s.cfg.DB, task); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, taskStartErrorStatus(err)
		}
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
	prompt := strings.TrimSpace(req.Prompt)
	if len(req.AttachmentFiles) > 0 {
		files, err := s.saveTaskAttachmentFiles(task.Slug, req.AttachmentFiles)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
		prompt = promptWithAttachedImages(prompt, files)
	}
	if strings.TrimSpace(prompt) != "" {
		if err := s.writeTaskBrief(task.Slug, strings.TrimSpace(req.Name), prompt); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
	}
	agent, _ := s.agentForTask(task.Slug)
	return actionResponse{
		OK:      true,
		Message: "reactivated " + task.Slug + "; opening browser terminal",
		Agent:   agent,
		Bridge:  true,
	}, http.StatusOK
}

func promptWithAttachedImages(prompt string, files []FileRef) string {
	prompt = strings.TrimSpace(prompt)
	if len(files) == 0 {
		return prompt
	}
	var b strings.Builder
	if prompt != "" {
		b.WriteString(prompt)
		b.WriteString("\n\n")
	}
	b.WriteString("Attached images:\n")
	for _, file := range files {
		if strings.TrimSpace(file.Filename) != "" {
			b.WriteString("- ")
			b.WriteString(file.Filename)
			b.WriteString(": ")
			b.WriteString(file.Path)
			b.WriteByte('\n')
			continue
		}
		b.WriteString("- ")
		b.WriteString(file.Path)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) createProject(req actionRequest) (actionResponse, int) {
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
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		return actionResponse{OK: false, Message: "work_dir is required"}, http.StatusBadRequest
	}
	priority := strings.TrimSpace(req.Priority)
	if priority == "" {
		priority = "medium"
	}

	if _, err := flowdb.GetProject(s.cfg.DB, slug); err == nil {
		return actionResponse{OK: false, Message: "project " + slug + " already exists"}, http.StatusConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}

	args := []string{"add", "project", name, "--slug", slug, "--priority", priority, "--work-dir", workDir}
	if req.Mkdir {
		args = append(args, "--mkdir")
	}
	out, err := s.runFlowCommand(args...)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
	}
	if strings.TrimSpace(req.Description) != "" {
		if err := s.writeProjectBrief(slug, name, req.Description); err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
	}
	return actionResponse{OK: true, Message: "created project " + slug, Output: out}, http.StatusOK
}

func (s *Server) createPlaybook(req actionRequest) (actionResponse, int) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return actionResponse{OK: false, Message: "name is required"}, http.StatusBadRequest
	}
	slug := firstNonEmpty(req.Slug, req.Target)
	if err := validateSlug(slug); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		return actionResponse{OK: false, Message: "work_dir is required"}, http.StatusBadRequest
	}
	if _, err := flowdb.GetPlaybook(s.cfg.DB, slug); err == nil {
		return actionResponse{OK: false, Message: "playbook " + slug + " already exists"}, http.StatusConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}

	args := []string{"add", "playbook", name, "--slug", slug, "--work-dir", workDir}
	if req.Project != "" {
		if err := validateSlug(req.Project); err != nil {
			return actionResponse{OK: false, Message: "project: " + err.Error()}, http.StatusBadRequest
		}
		args = append(args, "--project", req.Project)
	}
	if req.Mkdir {
		args = append(args, "--mkdir")
	}
	out, err := s.runFlowCommand(args...)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
	}
	definition := firstNonEmpty(req.Description, req.Prompt)
	if definition != "" {
		if err := s.writePlaybookBrief(slug, name, definition); err != nil {
			return actionResponse{OK: false, Message: err.Error(), Output: out}, http.StatusInternalServerError
		}
	}
	return actionResponse{OK: true, Message: "created playbook " + slug, Output: out}, http.StatusOK
}

func (s *Server) createKB(req actionRequest) (actionResponse, int) {
	filename := strings.TrimSpace(firstNonEmpty(req.Slug, req.Target, req.Path))
	if filename == "" {
		return actionResponse{OK: false, Message: "filename is required"}, http.StatusBadRequest
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".md") {
		filename += ".md"
	}
	if !validFilename(filename) {
		return actionResponse{OK: false, Message: "invalid KB filename"}, http.StatusBadRequest
	}
	title := strings.TrimSpace(req.Name)
	if title == "" {
		title = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	path := filepath.Join(s.cfg.FlowRoot, "kb", filename)
	cleanBase := filepath.Join(filepath.Clean(s.cfg.FlowRoot), "kb") + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(path), cleanBase) {
		return actionResponse{OK: false, Message: "invalid KB path"}, http.StatusBadRequest
	}
	if _, err := os.Stat(path); err == nil {
		return actionResponse{OK: false, Message: "KB document " + filename + " already exists"}, http.StatusConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	body := markdownWithHeading(title, firstNonEmpty(req.Description, req.Prompt))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "created KB document " + filename}, http.StatusOK
}

func (s *Server) updatePermissionMode(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	mode, err := flowdb.NormalizePermissionMode(req.PermissionMode)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if task.PermissionMode == mode {
		return actionResponse{OK: true, Message: "permission mode unchanged (" + mode + ")"}, http.StatusOK
	}

	if _, err := s.cfg.DB.Exec(
		`UPDATE tasks SET permission_mode = ?, updated_at = ? WHERE slug = ?`,
		mode, flowdb.NowISO(), task.Slug,
	); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}

	browserLive := s.terminals.running(task.Slug)
	sharedLive := s.terminals.sharedRunning(task.Slug)
	if browserLive {
		s.terminals.stop(task.Slug)
	} else if sharedLive {
		_ = sharedTerminalKillSession(sharedTerminalSessionName(task.Slug))
		s.terminals.sharedRunningCache.invalidate(task.Slug)
	}

	terminatedNative := false
	if !browserLive && !sharedLive && task.SessionID.Valid && strings.TrimSpace(task.SessionID.String) != "" && safeSessionRe.MatchString(task.SessionID.String) {
		if pid, perr := claudePIDForSession(task.SessionID.String); perr == nil {
			if kerr := syscall.Kill(pid, syscall.SIGTERM); kerr == nil {
				terminatedNative = true
			}
		}
	}

	msg := "permission mode set to " + mode
	if browserLive || sharedLive {
		msg += "; restarting browser terminal"
		agent, err := s.agentForTask(task.Slug)
		if err != nil {
			return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
		}
		return actionResponse{OK: true, Message: msg, Agent: agent, Bridge: true, AlreadyLive: true}, http.StatusOK
	}
	if terminatedNative {
		msg += "; current session terminated, reattach to apply"
	}
	return actionResponse{OK: true, Message: msg, AlreadyLive: terminatedNative}, http.StatusOK
}

// updateProvider switches a not-yet-started task's agent (claude ↔ codex). The
// provider is sticky on the task row; the browser/native terminal launch reads
// tasks.session_provider when it spawns (see terminal_bridge.startSessionLocked
// and buildTerminalLaunch), so persisting the choice is all that's needed — the
// spawn path is unchanged. applyBacklogProviderChoice enforces the same
// "only before a session starts" guard and provider-availability check the
// new-task form relies on, so a session that has already launched is rejected.
func (s *Server) updateProvider(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	provider, err := flowdb.NormalizeSessionProvider(req.Provider)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	if err := s.applyBacklogProviderChoice(target, provider); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	return actionResponse{OK: true, Message: "agent set to " + provider}, http.StatusOK
}

func (s *Server) updatePriority(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	priority, err := flowdb.NormalizePriority(req.Priority)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if task.Priority == priority {
		return actionResponse{OK: true, Message: "priority unchanged (" + priority + ")"}, http.StatusOK
	}
	if _, err := s.cfg.DB.Exec(
		`UPDATE tasks SET priority = ?, updated_at = ? WHERE slug = ?`,
		priority, flowdb.NowISO(), task.Slug,
	); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "priority set to " + priority}, http.StatusOK
}

func (s *Server) updateTaskName(req actionRequest) (actionResponse, int) {
	target := firstNonEmpty(req.Target, req.Slug)
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return actionResponse{OK: false, Message: "task name is required"}, http.StatusBadRequest
	}
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if task.Name == name {
		return actionResponse{OK: true, Message: "task name unchanged"}, http.StatusOK
	}
	if _, err := s.cfg.DB.Exec(
		`UPDATE tasks SET name = ?, updated_at = ? WHERE slug = ?`,
		name, flowdb.NowISO(), task.Slug,
	); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "task name updated"}, http.StatusOK
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

func (s *Server) clearWaiting(target string) (actionResponse, int) {
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	if _, err := flowdb.GetTask(s.cfg.DB, target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if _, err := s.cfg.DB.Exec(`UPDATE tasks SET waiting_on = NULL, updated_at = ? WHERE slug = ?`, flowdb.NowISO(), target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, err := s.agentForTask(target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "cleared waiting block for " + target, Agent: agent}, http.StatusOK
}

func (s *Server) markInboxRead(target string) (actionResponse, int) {
	if err := validateSlug(target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	if _, err := flowdb.GetTask(s.cfg.DB, target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	now := flowdb.NowISO()
	if _, err := s.cfg.DB.Exec(`UPDATE tasks SET inbox_seen_at = ?, updated_at = ? WHERE slug = ?`, now, now, target); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	return actionResponse{OK: true, Message: "marked inbox read for " + target}, http.StatusOK
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

func (s *Server) openBrowserTerminalBridge(target, providerChoice string) (actionResponse, int) {
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	if task.Status != "done" {
		if err := flowdb.EnsureTaskStartable(s.cfg.DB, task); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, taskStartErrorStatus(err)
		}
	}
	if err := s.applyBacklogProviderChoice(target, providerChoice); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return actionResponse{OK: false, Message: "task not found: " + target}, http.StatusNotFound
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
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

func (s *Server) applyBacklogProviderChoice(target, rawProvider string) error {
	if strings.TrimSpace(rawProvider) == "" {
		return nil
	}
	provider, err := s.availableProvider(rawProvider)
	if err != nil {
		return err
	}
	task, err := flowdb.GetTask(s.cfg.DB, target)
	if err != nil {
		return err
	}
	currentProvider := strings.TrimSpace(task.SessionProvider)
	if currentProvider == "" {
		currentProvider = "claude"
	}
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionStarted.Valid {
		if currentProvider == provider {
			return nil
		}
		return fmt.Errorf("provider can only be changed before a session starts")
	}
	now := flowdb.NowISO()
	_, err = s.cfg.DB.Exec(
		`UPDATE tasks SET session_provider = ?, updated_at = ?
		 WHERE slug = ? AND status = 'backlog' AND session_id IS NULL AND session_started IS NULL`,
		provider, now, target,
	)
	return err
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
	if err := flowdb.EnsureTaskStartable(s.cfg.DB, task); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, taskStartErrorStatus(err)
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

func (s *Server) restartFreshBrowserTerminalBridge(target string) (actionResponse, int) {
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
	if err := flowdb.EnsureTaskStartable(s.cfg.DB, task); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, taskStartErrorStatus(err)
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
	if _, err := s.cfg.DB.Exec(
		`UPDATE tasks SET
			status = 'backlog',
			status_changed_at = CASE WHEN status != 'backlog' THEN ? ELSE status_changed_at END,
			session_id = NULL,
			session_started = NULL,
			session_last_resumed = NULL,
			session_path = NULL,
			updated_at = ?
		 WHERE slug = ?`,
		now, now, target,
	); err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, _ := s.agentForTask(target)
	return actionResponse{OK: true, Message: "starting fresh browser terminal for " + target, Agent: agent, Bridge: true}, http.StatusOK
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
	// A done task is still resumable: opening it in a native terminal revisits
	// the prior session (loading its context), mirroring the browser bridge
	// which already special-cases done. Only gate startability for non-done.
	if task.Status != "done" {
		if err := flowdb.EnsureTaskStartable(s.cfg.DB, task); err != nil {
			return actionResponse{OK: false, Message: err.Error()}, taskStartErrorStatus(err)
		}
	}
	sharedName, hasBrowserShared := s.terminals.sharedSessionName(target)
	createdShared := false
	if s.terminals.running(target) && !hasBrowserShared {
		return actionResponse{OK: false, Message: "browser terminal for " + target + " is already running without shared terminal multiplexing; stop it before opening a native shared terminal"}, http.StatusConflict
	}
	launch, err := s.prepareTerminalLaunch(target)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, taskStartErrorStatus(err)
	}
	if sharedName == "" {
		sharedName, createdShared, err = s.ensureSharedTerminalSession(launch, 120, 32)
		if err != nil {
			if launch.Created {
				s.rollbackPreparedTerminalLaunch(launch)
			}
			return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
		}
	}
	command := "tmux attach-session -t " + shellQuoteArg(sharedName)
	if err := s.spawnNativeTerminalCommand(terminalKind, nativeTerminalTitle(task), launch.WorkDir, command, s.nativeTerminalEnv()); err != nil {
		if createdShared {
			_ = sharedTerminalKillSession(sharedName)
		}
		if launch.Created {
			s.rollbackPreparedTerminalLaunch(launch)
		}
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	agent, _ := s.agentForTask(target)
	if agent != nil {
		agent.Status = "running"
		agent.Terminal.Mode = "shared"
		agent.Terminal.Message = terminalModeMessage(firstNonEmpty(agent.Provider, "claude"), "shared")
	}
	return actionResponse{OK: true, Message: "opened " + target + " in " + terminalLabel(terminalKind), Agent: agent}, http.StatusOK
}

func taskStartErrorStatus(err error) int {
	var blocker *flowdb.TaskStartBlocker
	if errors.As(err, &blocker) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (s *Server) spawnNativeTerminal(kind string, task *flowdb.Task, launch terminalLaunch) error {
	command := agentShellCommand(launch.Provider, launch.Args)
	env := s.nativeTerminalEnv()
	title := nativeTerminalTitle(task)
	return s.spawnNativeTerminalCommand(kind, title, launch.WorkDir, command, env)
}

func (s *Server) spawnNativeTerminalCommand(kind, title, workDir, command string, env map[string]string) error {
	switch kind {
	case "iterm":
		return iterm.SpawnTab(title, workDir, command, env)
	case "terminal":
		return macterminal.SpawnTab(title, workDir, command, env)
	case "kitty":
		return kitty.SpawnTab(title, workDir, command, env)
	case "warp":
		return warp.SpawnTab(title, workDir, command, env)
	case "alacritty":
		return startShellTerminal("alacritty", "Alacritty", workDir, command, env, "--working-directory", workDir, "-e")
	case "ghostty":
		return startShellTerminal("ghostty", "Ghostty", workDir, command, env, "--working-directory="+workDir, "-e")
	case "wezterm":
		args := append([]string{"start", "--cwd", workDir, "--"}, shellCommandArgs(command, env)...)
		return nativeCommandStarter("wezterm", "", args...)
	case "tmux":
		return nativeCommandStarter("tmux", "", "new-window", "-n", title, "-c", workDir, shellCommandLine(command, env))
	case "vscode":
		return nativeCommandStarter("code", "", "-n", "--reuse-window", workDir)
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
	// The branch and diff just changed; clear the per-workdir cache so the
	// agent snapshot we're about to return reflects the new HEAD.
	s.caches.invalidateWorkdir(task.WorkDir)
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
	if hookURL := strings.TrimSpace(s.cfg.HookURL); hookURL != "" {
		env["FLOW_HOOK_URL"] = hookURL
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
	live, _ := s.cachedLiveAgentSessions()
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
	// `flow add task` requires an explicit agent. Inherit the source task's
	// provider when the caller didn't specify one; availableProvider falls back
	// to an installed provider when both are empty.
	forkProvider := strings.TrimSpace(req.Provider)
	if forkProvider == "" {
		if src, serr := flowdb.GetTask(s.cfg.DB, target); serr == nil {
			forkProvider = strings.TrimSpace(src.SessionProvider)
		}
	}
	provider, err := s.availableProvider(forkProvider)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusBadRequest
	}
	args := []string{"add", "task", name + " fork", "--slug", slug, "--priority", priority, "--agent", provider}
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
	// Return the freshly-created fork as Agent so the UI can navigate straight
	// to its session (SessionDetail keys its post-fork navigation off resp.agent).
	agent, _ := s.agentForTask(slug)
	return actionResponse{OK: true, Message: "forked " + target + " to " + slug, Output: out, Agent: agent}, http.StatusOK
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

func (s *Server) overviewChat(req actionRequest) (actionResponse, int) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return actionResponse{OK: false, Message: "prompt is required"}, http.StatusBadRequest
	}
	launch, err := s.prepareOverviewFloatingLaunch(req)
	if err != nil {
		return actionResponse{OK: false, Message: err.Error()}, http.StatusInternalServerError
	}
	terminal := s.terminals.registerFloatingLaunch(launch, "Ask Flow")
	return actionResponse{OK: true, Message: "opened floating overview agent", FloatingTerminal: &terminal}, http.StatusOK
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

func (s *Server) writeProjectBrief(slug, name, description string) error {
	path := filepath.Join(s.cfg.FlowRoot, "projects", slug, "brief.md")
	if !strings.HasPrefix(filepath.Clean(path), filepath.Join(s.cfg.FlowRoot, "projects")+string(os.PathSeparator)) {
		return errors.New("invalid project brief path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create project brief dir: %w", err)
	}
	body := fmt.Sprintf("# %s\n\n%s\n", name, strings.TrimSpace(description))
	return os.WriteFile(path, []byte(body), 0o644)
}

func (s *Server) writePlaybookBrief(slug, name, definition string) error {
	path := filepath.Join(s.cfg.FlowRoot, "playbooks", slug, "brief.md")
	if !strings.HasPrefix(filepath.Clean(path), filepath.Join(s.cfg.FlowRoot, "playbooks")+string(os.PathSeparator)) {
		return errors.New("invalid playbook brief path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create playbook brief dir: %w", err)
	}
	return os.WriteFile(path, []byte(markdownWithHeading(name, definition)), 0o644)
}

func markdownWithHeading(title, body string) string {
	text := strings.TrimSpace(body)
	if text == "" {
		return fmt.Sprintf("# %s\n", strings.TrimSpace(title))
	}
	if strings.HasPrefix(text, "# ") {
		return text + "\n"
	}
	return fmt.Sprintf("# %s\n\n%s\n", strings.TrimSpace(title), text)
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
