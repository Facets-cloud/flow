package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"flow/internal/agents"
	"flow/internal/flowdb"
)

type uiData struct {
	Agents           []uiAgent        `json:"AGENTS"`
	DeadAgent        *uiAgent         `json:"DEAD_AGENT"`
	DoneAgents       []uiAgent        `json:"DONE_AGENTS"`
	Backlog          []uiBacklogTask  `json:"BACKLOG"`
	DoneTasks        []uiBacklogTask  `json:"DONE_TASKS"`
	KBFiles          []uiKBFile       `json:"KB_FILES"`
	MemorySources    []uiMemorySource `json:"AGENT_MEMORY_SOURCES"`
	Workdirs         []uiWorkdir      `json:"WORKDIRS"`
	Playbooks        []uiPlaybook     `json:"PLAYBOOKS_MC"`
	Projects         []uiProject      `json:"PROJECTS_MC"`
	ActivityHeatmap  []uiActivityDay  `json:"ACTIVITY_HEATMAP"`
	Stats            uiStats          `json:"STATS"`
	Capabilities     uiCapabilities   `json:"CAPABILITIES"`
	Trash            uiTrash          `json:"TRASH"`
	SampleTranscript []uiTranscript   `json:"SAMPLE_TRANSCRIPT"`
	SampleDiffFiles  []uiDiffFile     `json:"SAMPLE_DIFF_FILES"`
	FlowDB           uiFlowDB         `json:"FLOWDB"`
	User             uiUser           `json:"USER"`
}

// uiUser carries the operator's display name so the dashboard can greet
// them. Derived from the OS account; falls back gracefully when unknown.
type uiUser struct {
	Name     string `json:"name"`      // first name, for greetings
	FullName string `json:"full_name"`
	Username string `json:"username"`
}

func currentUIUser() uiUser {
	u, err := user.Current()
	if err != nil || u == nil {
		return uiUser{Name: "there"}
	}
	full := strings.TrimSpace(u.Name)
	username := strings.TrimSpace(u.Username)
	// On some platforms Username is "domain\\user" — keep the last segment.
	if i := strings.LastIndexAny(username, `\/`); i >= 0 {
		username = username[i+1:]
	}
	display := full
	if display == "" {
		display = username
	}
	first := display
	if fields := strings.Fields(display); len(fields) > 0 {
		first = fields[0]
	}
	if first == "" {
		first = "there"
	}
	// macOS often reports the account name in ALL CAPS — soften to Title case
	// so the greeting reads "Vishnu", not "VISHNU".
	if first == strings.ToUpper(first) && first != strings.ToLower(first) {
		r := []rune(strings.ToLower(first))
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		first = string(r)
	}
	return uiUser{Name: first, FullName: full, Username: username}
}

// uiFlowDB carries on-disk stats for flow.db (plus a display-friendly
// path) so the UI sidebar can show how much storage flow is using.
type uiFlowDB struct {
	Path        string `json:"path"`
	DisplayPath string `json:"display_path"`
	Bytes       int64  `json:"bytes"`
	HumanSize   string `json:"human_size"`
	Exists      bool   `json:"exists"`
}

type uiActivityDay struct {
	Date  string   `json:"date"`
	Count int      `json:"count"`
	Tasks []string `json:"tasks,omitempty"`
}

// uiStats are the at-a-glance Mission Control analytics: how consistently the
// operator has been running agents (activity-day streaks, derived from the same
// 12-week heatmap shown on the dashboard) and how many context tokens each
// provider has in play across all tracked sessions.
type uiStats struct {
	CurrentStreak  int `json:"current_streak"` // consecutive active days ending today
	LongestStreak  int `json:"longest_streak"` // longest active-day run in the window
	ActiveDays     int `json:"active_days"`    // active days within the 12-week window
	TokensTotal    int `json:"tokens_total"`
	TokensClaude   int `json:"tokens_claude"`
	TokensCodex    int `json:"tokens_codex"`
	SessionsTotal  int `json:"sessions_total"`
	SessionsClaude int `json:"sessions_claude"`
	SessionsCodex  int `json:"sessions_codex"`
}

type uiTrash struct {
	Tasks     []uiTrashItem `json:"tasks"`
	Projects  []uiTrashItem `json:"projects"`
	Playbooks []uiTrashItem `json:"playbooks"`
	Total     int           `json:"total"`
}

type uiTrashItem struct {
	Kind      string  `json:"kind"`
	Slug      string  `json:"slug"`
	Name      string  `json:"name"`
	Status    string  `json:"status,omitempty"`
	Priority  string  `json:"priority,omitempty"`
	Project   *string `json:"project,omitempty"`
	WorkDir   string  `json:"work_dir"`
	DeletedAt string  `json:"deleted_at"`
	Archived  bool    `json:"archived"`
}

type uiAgent struct {
	Slug            string         `json:"slug"`
	Name            string         `json:"name"`
	Project         *string        `json:"project"`
	Kind            string         `json:"kind"`
	PlaybookSlug    *string        `json:"playbook_slug,omitempty"`
	Parent          *TaskSummary   `json:"parent,omitempty"`
	Parents         []TaskSummary  `json:"parents,omitempty"`
	Children        []TaskSummary  `json:"children,omitempty"`
	Branch          string         `json:"branch"`
	Branches        []string       `json:"branches,omitempty"`
	WorkDir         string         `json:"work_dir"`
	Provider        string         `json:"provider"`
	PermissionMode  string         `json:"permission_mode"`
	Priority        string         `json:"priority"`
	Status          string         `json:"status"`
	TaskStatus      string         `json:"task_status"`
	RuntimeStatus   string         `json:"runtime_status"`
	RuntimeEvent    string         `json:"runtime_event,omitempty"`
	RuntimeSource   string         `json:"runtime_source,omitempty"`
	HookHealth      *uiHookHealth  `json:"hook_health,omitempty"`
	SessionID       string         `json:"session_id"`
	StartedMin      int            `json:"started_min"`
	LastActivitySec int            `json:"last_activity_sec"`
	LastAction      string         `json:"last_action"`
	WaitingFor      *uiWaitingFor  `json:"waiting_for,omitempty"`
	Diff            uiDiff         `json:"diff"`
	TokensUsed      int            `json:"tokens_used"`
	TokensMax       int            `json:"tokens_max"`
	TokensSession   int            `json:"tokens_session"`
	Activity        []int          `json:"activity"`
	Tags            []string       `json:"tags"`
	Summary         string         `json:"summary"`
	NextStep        string         `json:"next_step"`
	Transcript      []uiTranscript `json:"transcript,omitempty"`
	Brief           string         `json:"brief,omitempty"`
	RecentTools     []uiRecentTool `json:"recent_tools,omitempty"`
	DiffFiles       []uiDiffFile   `json:"diff_files,omitempty"`
	BriefPath       string         `json:"brief_path,omitempty"`
	Updates         []FileRef      `json:"updates,omitempty"`
	AuxFiles        []FileRef      `json:"aux_files,omitempty"`
	Terminal        uiTerminal     `json:"terminal"`
	// Monitored reports whether a persistent background monitor is watching
	// this task's inbox (independent of whether the session is live).
	Monitored bool `json:"monitored"`
}

type uiWaitingFor struct {
	Kind string `json:"kind"`
	Cmd  string `json:"cmd"`
	Why  string `json:"why"`
}

type uiHookHealth struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Action  string `json:"action,omitempty"`
}

type uiDiff struct {
	Add   int `json:"add"`
	Rem   int `json:"rem"`
	Files int `json:"files"`
}

type uiDiffFile struct {
	Name  string       `json:"name"`
	Add   int          `json:"add"`
	Rem   int          `json:"rem"`
	Hunks []uiDiffHunk `json:"hunks,omitempty"`
}

type uiDiffHunk struct {
	Header string       `json:"header"`
	Lines  []uiDiffLine `json:"lines"`
}

type uiDiffLine struct {
	Type string `json:"type"`
	N    string `json:"n"`
	Code string `json:"code"`
}

type uiRecentTool struct {
	Name string `json:"name"`
	S    string `json:"s"`
}

type uiTerminal struct {
	Banner  []uiTermLine `json:"banner"`
	Feed    []uiTermLine `json:"feed"`
	Appends []uiTermLine `json:"appends"`
	Footer  []uiTermLine `json:"footer"`
	Mode    string       `json:"mode,omitempty"`
	Message string       `json:"message,omitempty"`
}

type uiTermLine struct {
	C    string `json:"c"`
	Text string `json:"text"`
}

type uiBacklogTask struct {
	Slug      string        `json:"slug"`
	Name      string        `json:"name"`
	Project   string        `json:"project"`
	Parent    *TaskSummary  `json:"parent,omitempty"`
	Parents   []TaskSummary `json:"parents,omitempty"`
	Children  []TaskSummary `json:"children,omitempty"`
	Provider  string        `json:"provider"`
	Priority  string        `json:"priority"`
	Due       string        `json:"due,omitempty"`
	Tags      []string      `json:"tags,omitempty"`
	WaitingOn *string       `json:"waiting_on,omitempty"`
	// StartedMin is minutes since the task row was created. Lets the UI sort
	// backlog tasks by age alongside live sessions (uiAgent.StartedMin).
	StartedMin int `json:"started_min"`
}

type uiProject struct {
	Slug     string       `json:"slug"`
	Name     string       `json:"name"`
	Priority string       `json:"priority"`
	Tasks    uiTaskCounts `json:"tasks"`
	WorkDir  string       `json:"work_dir"`
}

type uiTaskCounts struct {
	Total      int `json:"total"`
	InProgress int `json:"in_progress"`
	Backlog    int `json:"backlog"`
	Done       int `json:"done"`
}

type uiPlaybook struct {
	Slug     string          `json:"slug"`
	Name     string          `json:"name"`
	Project  *string         `json:"project"`
	RunsWeek int             `json:"runs_week"`
	LastMin  *int            `json:"last_min"`
	Spark    []int           `json:"spark"`
	Runs     []uiPlaybookRun `json:"runs"`
	WorkDir  string          `json:"work_dir"`
}

// uiPlaybookRun is one run shown as a hoverable bar in the Active Playbooks
// strip — enough to tell the operator when it ran and how it ended.
type uiPlaybookRun struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type uiKBFile struct {
	Name    string      `json:"name"`
	Preview string      `json:"preview"`
	Count   int         `json:"count"`
	Entries []uiKBEntry `json:"entries"`
}

type uiKBEntry struct {
	D string `json:"d"`
	T string `json:"t"`
}

type uiMemorySource struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Scope     string `json:"scope"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Path      string `json:"path"`
	Status    string `json:"status"`
	Available bool   `json:"available"`
	Format    string `json:"format,omitempty"`
	MTime     string `json:"mtime,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Content   string `json:"content,omitempty"`
	Error     string `json:"error,omitempty"`
}

type uiWorkdir struct {
	Path      string  `json:"path"`
	Name      string  `json:"name"`
	Remote    *string `json:"remote"`
	UsedMin   int     `json:"used_min"`
	Tasks     int     `json:"tasks"`
	Untouched bool    `json:"untouched"`
}

type uiTranscript struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Input   string `json:"input,omitempty"`
	Summary string `json:"summary,omitempty"`
	Preview string `json:"preview,omitempty"`
	Lines   int    `json:"lines,omitempty"`
	Time    string `json:"time,omitempty"`
}

func (s *Server) handleUIDataJS(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	data, err := s.buildUIData()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(data)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("window.FLOW_BOOTSTRAP = "))
	_, _ = w.Write(body)
	_, _ = w.Write([]byte(";\n"))
}

func (s *Server) handleUIDataJSON(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	data, err := s.buildUIData()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}

func (s *Server) buildUIData() (uiData, error) {
	live, _ := s.cachedLiveAgentSessions()
	tasks, err := flowdb.ListTasks(s.cfg.DB, flowdb.TaskFilter{Kind: "", IncludeArchived: false})
	if err != nil {
		return uiData{}, err
	}
	taskViews, err := buildTaskViewsWithLive(s.cfg.DB, s.cfg.FlowRoot, tasks, live)
	if err != nil {
		return uiData{}, err
	}

	agents := []uiAgent{}
	backlog := []uiBacklogTask{}
	doneTasks := []uiBacklogTask{}
	doneCandidates := []uiAgent{}
	for _, tv := range taskViews {
		switch tv.Status {
		case "backlog":
			if tv.Kind == "regular" {
				backlog = append(backlog, s.uiBacklog(tv))
			}
		case "done":
			if tv.Kind == "regular" {
				doneTasks = append(doneTasks, s.uiBacklog(tv))
				doneCandidates = append(doneCandidates, s.uiAgent(tv, live))
			}
		default:
			if tv.Kind == "regular" || tv.Kind == "playbook_run" {
				agents = append(agents, s.uiAgent(tv, live))
			}
		}
	}
	sort.SliceStable(agents, func(i, j int) bool {
		ri, rj := uiStatusRank(agents[i].Status), uiStatusRank(agents[j].Status)
		if ri != rj {
			return ri < rj
		}
		pi, pj := priorityRank(agents[i].Priority), priorityRank(agents[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return agents[i].Slug < agents[j].Slug
	})
	sort.SliceStable(backlog, func(i, j int) bool {
		pi, pj := priorityRank(backlog[i].Priority), priorityRank(backlog[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return backlog[i].Slug < backlog[j].Slug
	})
	sort.SliceStable(doneCandidates, func(i, j int) bool {
		return doneCandidates[i].LastActivitySec < doneCandidates[j].LastActivitySec
	})
	sort.SliceStable(doneTasks, func(i, j int) bool {
		return doneTasks[i].Slug < doneTasks[j].Slug
	})

	projects, err := s.uiProjects()
	if err != nil {
		return uiData{}, err
	}
	playbooks, err := s.uiPlaybooks()
	if err != nil {
		return uiData{}, err
	}
	workdirs, err := s.uiWorkdirs()
	if err != nil {
		return uiData{}, err
	}
	kb := s.uiKBFiles()
	transcript := []uiTranscript{{Type: "assistant", Text: "No transcript is available for the selected flow task yet."}}
	diffFiles := []uiDiffFile{}
	if len(agents) > 0 && len(agents[0].Transcript) > 0 {
		transcript = agents[0].Transcript
	}
	if len(agents) > 0 && len(agents[0].DiffFiles) > 0 {
		diffFiles = agents[0].DiffFiles
	}
	if len(diffFiles) == 0 {
		diffFiles = []uiDiffFile{{Name: "No local git diff", Add: 0, Rem: 0}}
	}
	memorySources := s.uiAgentMemorySources(taskViews, projects, playbooks, workdirs)

	var dead *uiAgent
	if len(doneCandidates) > 0 {
		doneCandidates[0].Status = "idle"
		dead = &doneCandidates[0]
	}
	heatmap := buildActivityHeatmap(taskViews, time.Now())
	return uiData{
		Agents:           agents,
		DeadAgent:        dead,
		DoneAgents:       doneCandidates,
		Backlog:          backlog,
		DoneTasks:        doneTasks,
		KBFiles:          kb,
		MemorySources:    memorySources,
		Workdirs:         workdirs,
		Playbooks:        playbooks,
		Projects:         projects,
		ActivityHeatmap:  heatmap,
		Stats:            buildUIStats(agents, doneCandidates, heatmap, time.Now()),
		Capabilities:     s.uiCapabilities(),
		Trash:            s.uiTrash(),
		SampleTranscript: transcript,
		SampleDiffFiles:  diffFiles,
		FlowDB:           s.uiFlowDB(),
		User:             currentUIUser(),
	}, nil
}

// uiFlowDB reports the on-disk size of flow.db. Missing-file is not an
// error: the sidebar just shows "—" until `flow init` runs.
func (s *Server) uiFlowDB() uiFlowDB {
	root := strings.TrimSpace(s.cfg.FlowRoot)
	if root == "" {
		return uiFlowDB{HumanSize: "—"}
	}
	path := filepath.Join(root, "flow.db")
	out := uiFlowDB{Path: path, DisplayPath: displayUIPath(path), HumanSize: "—"}
	info, err := os.Stat(path)
	if err != nil {
		return out
	}
	out.Exists = true
	out.Bytes = info.Size()
	out.HumanSize = humanByteSize(info.Size())
	return out
}

func humanByteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	suffix := "KMGTPE"[exp]
	if val >= 100 {
		return fmt.Sprintf("%.0f %ciB", val, suffix)
	}
	return fmt.Sprintf("%.1f %ciB", val, suffix)
}

func (s *Server) uiTrash() uiTrash {
	var out uiTrash
	tasks, _ := flowdb.ListTasks(s.cfg.DB, flowdb.TaskFilter{Kind: "", IncludeArchived: true, DeletedOnly: true})
	for _, task := range tasks {
		out.Tasks = append(out.Tasks, uiTrashItem{
			Kind:      "task",
			Slug:      task.Slug,
			Name:      task.Name,
			Status:    task.Status,
			Priority:  task.Priority,
			Project:   nullStringPtr(task.ProjectSlug),
			WorkDir:   task.WorkDir,
			DeletedAt: task.DeletedAt.String,
			Archived:  task.ArchivedAt.Valid,
		})
	}
	projects, _ := flowdb.ListProjects(s.cfg.DB, flowdb.ProjectFilter{IncludeArchived: true, DeletedOnly: true})
	for _, project := range projects {
		out.Projects = append(out.Projects, uiTrashItem{
			Kind:      "project",
			Slug:      project.Slug,
			Name:      project.Name,
			Status:    project.Status,
			Priority:  project.Priority,
			WorkDir:   project.WorkDir,
			DeletedAt: project.DeletedAt.String,
			Archived:  project.ArchivedAt.Valid,
		})
	}
	playbooks, _ := flowdb.ListPlaybooks(s.cfg.DB, flowdb.PlaybookFilter{IncludeArchived: true, DeletedOnly: true})
	for _, playbook := range playbooks {
		out.Playbooks = append(out.Playbooks, uiTrashItem{
			Kind:      "playbook",
			Slug:      playbook.Slug,
			Name:      playbook.Name,
			Project:   nullStringPtr(playbook.ProjectSlug),
			WorkDir:   playbook.WorkDir,
			DeletedAt: playbook.DeletedAt.String,
			Archived:  playbook.ArchivedAt.Valid,
		})
	}
	out.Total = len(out.Tasks) + len(out.Projects) + len(out.Playbooks)
	return out
}

func (s *Server) uiAgent(tv TaskView, live map[string]bool) uiAgent {
	workDir := tv.WorkDir
	staleOverviewSession := false
	if tv.Slug == overviewTaskSlug && strings.TrimSpace(s.cfg.FlowRoot) != "" {
		if abs, err := filepath.Abs(s.cfg.FlowRoot); err == nil {
			staleOverviewSession = filepath.Clean(tv.WorkDir) != filepath.Clean(abs)
			workDir = abs
		} else {
			staleOverviewSession = filepath.Clean(tv.WorkDir) != filepath.Clean(s.cfg.FlowRoot)
			workDir = s.cfg.FlowRoot
		}
	}
	provider := "claude"
	if tv.SessionProvider != nil && *tv.SessionProvider != "" {
		provider = *tv.SessionProvider
	}
	permissionMode := tv.PermissionMode
	if permissionMode == "" {
		permissionMode = flowdb.DefaultPermissionMode
	}
	sessionID := ""
	if tv.SessionID != nil {
		sessionID = *tv.SessionID
	}
	bridgeRunning := s.terminals != nil && s.terminals.running(tv.Slug)
	sharedRunning := s.terminals != nil && s.terminals.sharedRunning(tv.Slug)
	sessionLive := tv.SessionID != nil && live[strings.ToLower(*tv.SessionID)]
	terminalMode := "idle"
	switch {
	case bridgeRunning:
		terminalMode = "browser"
	case sharedRunning:
		terminalMode = "shared"
	case sessionLive:
		terminalMode = "native"
	}
	fullTranscript := s.fullUITranscriptForTask(tv)
	transcript := fullTranscript
	if len(transcript) > 24 {
		transcript = transcript[len(transcript)-24:]
	}
	if len(transcript) == 0 {
		transcript = s.syntheticTranscript(tv)
		fullTranscript = transcript
	}
	if terminalMode == "browser" && s.transcriptAheadOfBrowserTerminal(tv.Slug, fullTranscript) {
		terminalMode = "native"
	}
	insights := s.sessionInsightsForTask(tv, provider, fullTranscript)
	hookRuntime := s.agentHookRuntimeState(tv, provider)
	status := "idle"
	runtimeSource := "task"
	runtimeEvent := ""
	switch tv.Status {
	case "in-progress":
		switch {
		case tv.WaitingOn != nil:
			status = "waiting"
			runtimeSource = "flow"
		case bridgeRunning:
			status = "running"
			runtimeSource = "browser_terminal"
		case sharedRunning:
			status = "running"
			runtimeSource = "shared_terminal"
		case sessionLive:
			status = "running"
			runtimeSource = "process"
		case tv.StaleDays != nil:
			status = "stale"
			runtimeSource = "task"
		default:
			status = "idle"
			runtimeSource = "task"
		}
	case "done":
		status = "idle"
	default:
		status = tv.Status
	}
	if staleOverviewSession {
		sessionID = ""
		status = "idle"
		runtimeSource = "task"
	}
	var hookWaiting *uiWaitingFor
	transcriptWaiting := s.codexTranscriptWaitingFor(tv, provider)
	if !staleOverviewSession && tv.Status == "in-progress" {
		if hookRuntime != nil && hookRuntime.Status != "" {
			status = hookRuntime.Status
			runtimeSource = "hook"
			runtimeEvent = hookRuntime.EventKind
			// If the hook says "running" but neither the hook nor the
			// transcript has moved in a while, the hook layer is stale
			// (a Stop hook may have failed to fire on interrupt, or
			// hooks aren't reaching the server). Demote to "idle" so
			// the UI reflects what the user observes in the terminal.
			if status == "running" && runtimeStateStaleForRunning(hookRuntime.UpdatedAt, insights.ActivityAt) {
				status = "idle"
				runtimeEvent = hookRuntime.EventKind + ":stale"
			}
		}
		if hookWaiting != nil {
			status = "waiting"
			runtimeSource = "hook"
		} else if transcriptWaiting != nil {
			status = "waiting"
			runtimeSource = "transcript"
			runtimeEvent = "request_user_input"
		}
	}
	// When the task was bootstrapped into a git worktree (see `flow do`),
	// the user is editing on the worktree's branch, not the parent repo's
	// checked-out branch. Prefer the worktree path for git diff and branch
	// queries so the UI reflects what the agent session sees. Fall back to
	// workDir if the recorded worktree no longer exists on disk (user
	// manually removed it).
	gitDir := workDir
	if tv.WorktreePath != nil && *tv.WorktreePath != "" {
		if info, err := os.Stat(*tv.WorktreePath); err == nil && info.IsDir() {
			gitDir = *tv.WorktreePath
		}
	}
	branches := []string{}
	branch := "~/.flow"
	if tv.Slug != overviewTaskSlug {
		branch = s.cachedGitBranch(gitDir)
		if branch == "" {
			branch = tv.Slug + "/main"
		} else {
			branches = s.cachedGitBranches(gitDir, branch)
		}
	}
	lastActivityAt := laterTimestamp(latestTaskActivity(tv), insights.ActivityAt)
	lastActivity := secondsSince(lastActivityAt)
	startedAt := tv.CreatedAt
	if tv.SessionStarted != nil {
		startedAt = *tv.SessionStarted
	}
	diff, files := s.cachedGitDiff(gitDir)
	summary := latestMarkdownSummary(tv.Updates)
	if summary == "" {
		summary = readMarkdownSummary(tv.BriefPath)
	}
	if summary == "" {
		summary = tv.TemporalSummary
	}
	if summary == "" {
		summary = "No recent update has been written for this task."
	}
	nextStep := "Open the task with flow do " + tv.Slug
	if tv.WaitingOn != nil {
		nextStep = "Waiting on " + *tv.WaitingOn
	}
	lastAction := insights.LastAction
	if lastAction == "" {
		lastAction = tv.TemporalSummary
	}
	if lastAction == "" {
		lastAction = "updated " + formatActivity(lastActivity)
	}
	tokensMax := insights.TokensMax
	if tokensMax <= 0 {
		tokensMax = contextWindowForProvider(provider)
	}
	tokensUsed := insights.TokensUsed
	if tokensUsed <= 0 {
		tokensUsed = estimateTokens(tv, fullTranscript, tokensMax)
	}
	if tokensUsed > tokensMax {
		tokensUsed = tokensMax
	}
	agent := uiAgent{
		Slug:            tv.Slug,
		Name:            tv.Name,
		Project:         tv.ProjectSlug,
		Kind:            tv.Kind,
		PlaybookSlug:    tv.PlaybookSlug,
		Parent:          tv.Parent,
		Parents:         tv.Parents,
		Children:        tv.Children,
		Branch:          branch,
		Branches:        branches,
		WorkDir:         workDir,
		Provider:        provider,
		PermissionMode:  permissionMode,
		Priority:        tv.Priority,
		Status:          status,
		TaskStatus:      tv.Status,
		RuntimeStatus:   status,
		RuntimeEvent:    runtimeEvent,
		RuntimeSource:   runtimeSource,
		Monitored:       s.inboxMonitors != nil && s.inboxMonitors.running(tv.Slug),
		HookHealth:      s.agentHookHealth(tv, provider, fullTranscript, hookRuntime),
		SessionID:       sessionID,
		StartedMin:      minutesSince(startedAt),
		LastActivitySec: lastActivity,
		LastAction:      lastAction,
		Diff:            diff,
		DiffFiles:       files,
		TokensUsed:      tokensUsed,
		TokensMax:       tokensMax,
		TokensSession:   insights.TokensSession,
		Activity:        toolCallActivitySeries(fullTranscript, time.Now()),
		Tags:            tv.Tags,
		Summary:         summary,
		NextStep:        nextStep,
		Transcript:      transcript,
		Brief:           readMarkdownSummary(tv.BriefPath),
		RecentTools:     recentTools(transcript),
		BriefPath:       tv.BriefPath,
		Updates:         tv.Updates,
		AuxFiles:        tv.AuxFiles,
		Terminal:        terminalSample(withTaskWorkDir(tv, workDir), provider, transcript, terminalMode),
	}
	if tv.WaitingOn != nil {
		agent.WaitingFor = &uiWaitingFor{Kind: "flow", Cmd: "flow update task " + tv.Slug + " --clear-waiting", Why: *tv.WaitingOn}
	} else if hookWaiting != nil {
		agent.WaitingFor = hookWaiting
	} else if transcriptWaiting != nil {
		agent.WaitingFor = transcriptWaiting
	}
	return agent
}

func (s *Server) codexTranscriptWaitingFor(tv TaskView, provider string) *uiWaitingFor {
	if provider != "codex" || tv.SessionID == nil || strings.TrimSpace(*tv.SessionID) == "" {
		return nil
	}
	task := &flowdb.Task{
		Slug:            tv.Slug,
		WorkDir:         tv.WorkDir,
		WorktreePath:    nullStringFromPtr(tv.WorktreePath),
		SessionProvider: provider,
		SessionID:       sql.NullString{String: *tv.SessionID, Valid: true},
		SessionPath:     nullStringFromPtr(tv.SessionPath),
	}
	path, err := sessionJSONLPath(s.cfg.DB, task)
	if err != nil {
		return nil
	}
	entry, err := s.transcripts.get(path)
	if err != nil {
		return nil
	}
	pending := entry.pending
	if pending == nil {
		return nil
	}
	return &uiWaitingFor{
		Kind: "question",
		Cmd:  "Open session " + tv.Slug,
		Why:  pending.Question,
	}
}

func withTaskWorkDir(tv TaskView, workDir string) TaskView {
	tv.WorkDir = workDir
	return tv
}

type taskSessionInsights struct {
	ActivityAt    string
	LastAction    string
	TokensUsed    int // current context-window occupancy (latest turn)
	TokensMax     int
	TokensSession int // cumulative tokens used this session (the CLI's Σ)
}

func (s *Server) sessionInsightsForTask(tv TaskView, provider string, transcript []uiTranscript) taskSessionInsights {
	insights := taskSessionInsights{TokensMax: contextWindowForProvider(provider)}
	if action := latestTranscriptAction(transcript); action != "" {
		insights.LastAction = action
	}
	if tv.SessionID == nil || strings.TrimSpace(*tv.SessionID) == "" {
		return insights
	}
	task := &flowdb.Task{
		Slug:            tv.Slug,
		WorkDir:         tv.WorkDir,
		WorktreePath:    nullStringFromPtr(tv.WorktreePath),
		SessionProvider: provider,
		SessionID:       sql.NullString{String: *tv.SessionID, Valid: true},
		SessionPath:     nullStringFromPtr(tv.SessionPath),
	}
	path, err := sessionJSONLPath(s.cfg.DB, task)
	if err != nil {
		return insights
	}
	entry, err := s.transcripts.get(path)
	if err != nil {
		return insights
	}
	insights.ActivityAt = entry.mtime.Format(time.RFC3339)
	stats := entry.usage
	insights.ActivityAt = laterTimestamp(insights.ActivityAt, stats.LastTimestamp)
	if stats.TokensUsed > 0 {
		insights.TokensUsed = stats.TokensUsed
	}
	if stats.TokensSession > 0 {
		insights.TokensSession = stats.TokensSession
	}
	if stats.TokensMax > 0 {
		insights.TokensMax = stats.TokensMax
	} else if stats.Model != "" {
		insights.TokensMax = contextWindowForModel(provider, stats.Model)
	}
	if insights.TokensUsed > insights.TokensMax {
		insights.TokensMax = insights.TokensUsed
	}
	for _, entry := range transcript {
		insights.ActivityAt = laterTimestamp(insights.ActivityAt, entry.Time)
	}
	return insights
}

// runtimeStateStaleForRunning returns true when a hook-driven "running"
// status has gone quiet — i.e., no hook events and no transcript activity
// for long enough that the running claim is no longer trustworthy. We
// require BOTH the hook updated_at and the transcript ActivityAt to be old
// before demoting to idle, so a long-running tool call (which holds the
// hook in "running" but pauses transcript writes) isn't mistakenly idled.
//
// Thresholds: hook hasn't ticked in >= 90s AND transcript hasn't moved in
// >= 30s. Cases like user-interrupt-then-walk-away land here quickly;
// genuine tool runs (Bash, web fetch, etc.) update the transcript far
// more often than every 30s.
const (
	runtimeHookStaleAfter       = 90 * time.Second
	runtimeTranscriptStaleAfter = 30 * time.Second
)

func runtimeStateStaleForRunning(hookUpdatedAt, transcriptActivityAt string) bool {
	hookAge := ageSince(hookUpdatedAt)
	if hookAge < runtimeHookStaleAfter {
		return false
	}
	if transcriptActivityAt == "" {
		return true
	}
	return ageSince(transcriptActivityAt) >= runtimeTranscriptStaleAfter
}

func ageSince(ts string) time.Duration {
	if strings.TrimSpace(ts) == "" {
		return time.Duration(1<<62 - 1)
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Duration(1<<62 - 1)
	}
	delta := time.Since(parsed)
	if delta < 0 {
		return 0
	}
	return delta
}

func contextWindowForProvider(provider string) int {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "claude-code", "claudecode":
		return 1000000
	case "codex":
		return 200000
	default:
		return 200000
	}
}

// contextWindowForModel returns the provider's context cap, refined by model
// when known. Claude Opus 4.6+ ships with a 1M context window; Sonnet, Haiku,
// and older Opus default to 200k unless the model itself signals otherwise via
// the JSONL (handled separately for Codex via model_context_window).
func contextWindowForModel(provider, model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return contextWindowForProvider(provider)
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "claude-code", "claudecode":
		if strings.Contains(m, "opus-4-6") || strings.Contains(m, "opus-4-7") {
			return 1000000
		}
		return 200000
	}
	return contextWindowForProvider(provider)
}

func latestTranscriptAction(transcript []uiTranscript) string {
	for i := len(transcript) - 1; i >= 0; i-- {
		entry := transcript[i]
		switch entry.Type {
		case "assistant":
			text := firstLine(entry.Text)
			if text == "" {
				continue
			}
			return "assistant: " + truncateText(text, 140)
		case "tool_use":
			label := strings.TrimSpace(entry.Tool)
			if entry.Input != "" {
				label = strings.TrimSpace(label + " " + entry.Input)
			}
			if label != "" {
				return "ran " + truncateText(label, 140)
			}
		case "tool_result":
			if entry.Summary != "" {
				return "tool result: " + truncateText(entry.Summary, 140)
			}
		case "user":
			if text := firstLine(entry.Text); text != "" {
				return "user: " + truncateText(text, 140)
			}
		}
	}
	return ""
}

func (s *Server) uiBacklog(tv TaskView) uiBacklogTask {
	project := "(floating)"
	if tv.ProjectSlug != nil {
		project = *tv.ProjectSlug
	}
	provider := "claude"
	if tv.SessionProvider != nil && strings.TrimSpace(*tv.SessionProvider) != "" {
		provider = *tv.SessionProvider
	}
	out := uiBacklogTask{
		Slug:       tv.Slug,
		Name:       tv.Name,
		Project:    project,
		Parent:     tv.Parent,
		Parents:    tv.Parents,
		Children:   tv.Children,
		Provider:   provider,
		Priority:   tv.Priority,
		Tags:       tv.Tags,
		WaitingOn:  tv.WaitingOn,
		StartedMin: minutesSince(tv.CreatedAt),
	}
	if tv.DueInfo != nil {
		out.Due = *tv.DueInfo
	}
	return out
}

func (s *Server) uiProjects() ([]uiProject, error) {
	projects, err := flowdb.ListProjects(s.cfg.DB, flowdb.ProjectFilter{})
	if err != nil {
		return nil, err
	}
	views, err := BuildProjectViews(s.cfg.DB, s.cfg.FlowRoot, projects)
	if err != nil {
		return nil, err
	}
	out := make([]uiProject, 0, len(views))
	for _, p := range views {
		out = append(out, uiProject{
			Slug:     p.Slug,
			Name:     p.Name,
			Priority: p.Priority,
			Tasks: uiTaskCounts{
				Total:      p.TaskCounts.Total,
				InProgress: p.TaskCounts.InProgress,
				Backlog:    p.TaskCounts.Backlog,
				Done:       p.TaskCounts.Done,
			},
			WorkDir: p.WorkDir,
		})
	}
	return out, nil
}

func (s *Server) uiPlaybooks() ([]uiPlaybook, error) {
	playbooks, err := flowdb.ListPlaybooks(s.cfg.DB, flowdb.PlaybookFilter{})
	if err != nil {
		return nil, err
	}
	views, err := BuildPlaybookViews(s.cfg.DB, s.cfg.FlowRoot, playbooks)
	if err != nil {
		return nil, err
	}
	out := make([]uiPlaybook, 0, len(views))
	for _, p := range views {
		var lastMin *int
		if len(p.RecentRuns) > 0 {
			v := minutesSince(p.RecentRuns[0].CreatedAt)
			lastMin = &v
		}
		// RecentRuns is newest-first; the strip reads oldest→newest left-to-right,
		// so take the most recent 16 and reverse them.
		const maxRuns = 16
		recent := p.RecentRuns
		if len(recent) > maxRuns {
			recent = recent[:maxRuns]
		}
		runs := make([]uiPlaybookRun, 0, len(recent))
		for i := len(recent) - 1; i >= 0; i-- {
			r := recent[i]
			runs = append(runs, uiPlaybookRun{Name: r.Name, Status: r.Status, CreatedAt: r.CreatedAt})
		}
		out = append(out, uiPlaybook{
			Slug:     p.Slug,
			Name:     p.Name,
			Project:  p.ProjectSlug,
			RunsWeek: p.RunCount7d,
			LastMin:  lastMin,
			Spark:    lastSevenFromThirty(p.RunDays30),
			Runs:     runs,
			WorkDir:  p.WorkDir,
		})
	}
	return out, nil
}

func (s *Server) uiWorkdirs() ([]uiWorkdir, error) {
	workdirs, err := flowdb.ListWorkdirs(s.cfg.DB)
	if err != nil {
		return nil, err
	}
	views := BuildWorkdirViews(s.cfg.DB, workdirs)
	out := make([]uiWorkdir, 0, len(views))
	for _, w := range views {
		name := filepath.Base(w.Path)
		usedMin := 0
		if w.LastUsedAt != nil {
			usedMin = minutesSince(*w.LastUsedAt)
		}
		out = append(out, uiWorkdir{
			Path:      w.Path,
			Name:      name,
			Remote:    w.GitRemote,
			UsedMin:   usedMin,
			Tasks:     w.TasksUsingThis,
			Untouched: w.Untouched30d,
		})
	}
	return out, nil
}

func (s *Server) uiKBFiles() []uiKBFile {
	out := []uiKBFile{}
	for _, path := range kbFiles(s.cfg.FlowRoot) {
		view := BuildKBFileView(path)
		entries := readKBEntries(path)
		if entries == nil {
			entries = []uiKBEntry{}
		}
		count := view.Entries
		if count == 0 {
			count = len(entries)
		}
		preview := view.Preview
		if preview == "" && len(entries) > 0 {
			preview = entries[len(entries)-1].D + " - " + entries[len(entries)-1].T
		}
		out = append(out, uiKBFile{Name: view.Filename, Preview: preview, Count: count, Entries: entries})
	}
	return out
}

func (s *Server) uiTranscriptForTask(tv TaskView) []uiTranscript {
	return s.uiTranscriptForTaskLimit(tv, 24)
}

func (s *Server) fullUITranscriptForTask(tv TaskView) []uiTranscript {
	return s.uiTranscriptForTaskLimit(tv, 0)
}

func (s *Server) uiTranscriptForTaskLimit(tv TaskView, limit int) []uiTranscript {
	if tv.SessionID == nil {
		return nil
	}
	provider := "claude"
	if tv.SessionProvider != nil && strings.TrimSpace(*tv.SessionProvider) != "" {
		provider = *tv.SessionProvider
	}
	t := &flowdb.Task{
		Slug:            tv.Slug,
		WorkDir:         tv.WorkDir,
		WorktreePath:    nullStringFromPtr(tv.WorktreePath),
		SessionProvider: provider,
		SessionID:       sql.NullString{String: *tv.SessionID, Valid: true},
		SessionPath:     nullStringFromPtr(tv.SessionPath),
	}
	path, err := sessionJSONLPath(s.cfg.DB, t)
	if err != nil {
		return nil
	}
	entry, err := s.transcripts.get(path)
	if err != nil {
		return nil
	}
	entries := entry.entries
	var out []uiTranscript
	for _, e := range entries {
		switch e.Type {
		case "user", "assistant", "thinking":
			if e.Text != "" {
				out = append(out, uiTranscript{Type: e.Type, Text: e.Text, Time: e.Timestamp})
			}
		case "tool_use":
			out = append(out, uiTranscript{Type: "tool_use", Tool: e.ToolName, Input: e.ToolInputSummary, Time: e.Timestamp})
		case "tool_result":
			out = append(out, uiTranscript{Type: "tool_result", Tool: "result", Summary: firstLine(e.ToolResultText), Preview: e.ToolResultText, Time: e.Timestamp})
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (s *Server) syntheticTranscript(tv TaskView) []uiTranscript {
	var out []uiTranscript
	brief := readMarkdownSummary(tv.BriefPath)
	if brief != "" {
		out = append(out, uiTranscript{Type: "user", Text: brief})
	}
	for _, update := range tv.Updates {
		if body := readMarkdownSummary(update.Path); body != "" {
			out = append(out, uiTranscript{Type: "assistant", Text: body})
		}
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, uiTranscript{Type: "assistant", Text: tv.Name})
	}
	return out
}

func latestMarkdownSummary(files []FileRef) string {
	for _, f := range files {
		if s := readMarkdownSummary(f.Path); s != "" {
			return s
		}
	}
	return ""
}

func readMarkdownSummary(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		return truncateText(line, 180)
	}
	return ""
}

var kbEntryRe = regexp.MustCompile(`^-\s*([0-9]{4}-[0-9]{2}-[0-9]{2})\s*(?:[-—])\s*(.*)$`)

func readKBEntries(path string) []uiKBEntry {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []uiKBEntry
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		m := kbEntryRe.FindStringSubmatch(line)
		if len(m) == 3 {
			out = append(out, uiKBEntry{D: m[1], T: m[2]})
		} else {
			out = append(out, uiKBEntry{D: "", T: strings.TrimPrefix(line, "- ")})
		}
	}
	return out
}

func latestTaskActivity(tv TaskView) string {
	latest := tv.UpdatedAt
	for _, f := range tv.Updates {
		if f.MTime > latest {
			latest = f.MTime
		}
	}
	return latest
}

func laterTimestamp(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	at, aErr := time.Parse(time.RFC3339, a)
	bt, bErr := time.Parse(time.RFC3339, b)
	if aErr == nil && bErr == nil {
		if bt.After(at) {
			return b
		}
		return a
	}
	if b > a {
		return b
	}
	return a
}

func buildActivityHeatmap(tasks []TaskView, now time.Time) []uiActivityDay {
	now = now.In(time.Local)
	// Align the grid to a Sunday-start so the row labels (Mon/Wed/Fri)
	// are correct regardless of what day "today" is. End on the current
	// week's Saturday — future days in this week get count=0.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekdayOffset := int(today.Weekday()) // Sun=0..Sat=6
	thisWeekSunday := today.AddDate(0, 0, -weekdayOffset)
	start := thisWeekSunday.AddDate(0, 0, -77) // 11 prior full weeks + this week = 12 weeks
	days := make([]uiActivityDay, 84)
	index := make(map[string]int, len(days))
	seenTasks := make(map[string]map[string]bool)
	// Count is the number of DISTINCT tasks touched on a day, not the raw sum of
	// timestamp events. A single task carries several timestamps (created,
	// updated, session started/resumed, + each update file) that often share the
	// same instant — summing them inflates the number and double-counts. Distinct
	// tasks/day matches the tooltip's task list and reads honestly.
	distinct := make(map[string]map[string]bool)
	for i := range days {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		days[i] = uiActivityDay{Date: date}
		index[date] = i
	}
	// distinct counting is keyed by slug (stable, collision-free); the displayed
	// task list uses the human name so the tooltip never leaks a raw slug
	// (e.g. slack-<channel>-<ts>) — see the "no raw Slack IDs in UI" rule.
	add := func(ts time.Time, slug, name string) {
		date := ts.In(time.Local).Format("2006-01-02")
		if _, ok := index[date]; !ok {
			return
		}
		if distinct[date] == nil {
			distinct[date] = map[string]bool{}
		}
		distinct[date][slug] = true
		if seenTasks[date] == nil {
			seenTasks[date] = map[string]bool{}
		}
		if !seenTasks[date][slug] && len(days[index[date]].Tasks) < 5 {
			label := name
			if label == "" {
				label = slug
			}
			days[index[date]].Tasks = append(days[index[date]].Tasks, label)
			seenTasks[date][slug] = true
		}
	}
	addString := func(ts string, slug, name string) {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			add(t, slug, name)
		}
	}
	for _, task := range tasks {
		addString(task.CreatedAt, task.Slug, task.Name)
		addString(task.UpdatedAt, task.Slug, task.Name)
		if task.SessionStarted != nil {
			addString(*task.SessionStarted, task.Slug, task.Name)
		}
		if task.SessionLastResumed != nil {
			addString(*task.SessionLastResumed, task.Slug, task.Name)
		}
		for _, update := range task.Updates {
			if t, ok := activityTimeForFile(update); ok {
				add(t, task.Slug, task.Name)
			}
		}
		// Only a genuinely live session counts as activity "now". Merely being
		// in-progress (open but idle, last touched days ago) must NOT light up
		// today — that falsely inflates today's count with stale tasks. Real
		// created/updated/session/update timestamps already place those tasks on
		// the days they were actually touched.
		if task.Live {
			add(now, task.Slug, task.Name)
		}
	}
	for i := range days {
		days[i].Count = len(distinct[days[i].Date])
	}
	return days
}

// buildUIStats derives the Mission Control analytics strip: activity-day streaks
// from the heatmap, plus per-provider context-token and session totals across
// every tracked session (live + done, deduped by slug).
func buildUIStats(live, done []uiAgent, heatmap []uiActivityDay, now time.Time) uiStats {
	var st uiStats
	today := now.In(time.Local).Format("2006-01-02")
	// Restrict to days up to and including today. The heatmap grid runs to the
	// end of the current week, so the trailing future days (count 0) would
	// otherwise read as a broken streak.
	onByDay := make([]bool, 0, len(heatmap))
	for _, d := range heatmap {
		if d.Date > today {
			continue
		}
		on := d.Count > 0
		onByDay = append(onByDay, on)
		if on {
			st.ActiveDays++
		}
	}
	// Longest run of consecutive active days anywhere in the window.
	run := 0
	for _, on := range onByDay {
		if on {
			run++
			if run > st.LongestStreak {
				st.LongestStreak = run
			}
		} else {
			run = 0
		}
	}
	// Current streak: count back from today. A not-yet-active today doesn't break
	// the streak (you may simply not have started yet) — skip it once, then count
	// consecutive active days backwards.
	for i := len(onByDay) - 1; i >= 0; i-- {
		if onByDay[i] {
			st.CurrentStreak++
		} else if i == len(onByDay)-1 {
			continue // grace: today hasn't been touched yet
		} else {
			break
		}
	}
	// Token + session totals per provider, deduped by slug so a session present
	// in both the live and done lists isn't counted twice.
	seen := make(map[string]bool)
	tally := func(list []uiAgent) {
		for _, a := range list {
			if a.Slug == "" || seen[a.Slug] {
				continue
			}
			seen[a.Slug] = true
			// TokensUsed is each session's current context-window occupancy (the
			// "X/258k" bar on its card). We sum that rather than a cumulative
			// per-turn total: agents re-read their whole cached context every
			// turn, so summing per-turn usage counts the same tokens dozens of
			// times and balloons far past what was ever actually consumed.
			st.TokensTotal += a.TokensUsed
			st.SessionsTotal++
			if a.Provider == agents.ProviderCodex {
				st.TokensCodex += a.TokensUsed
				st.SessionsCodex++
			} else {
				st.TokensClaude += a.TokensUsed
				st.SessionsClaude++
			}
		}
	}
	tally(live)
	tally(done)
	return st
}

func activityTimeForFile(file FileRef) (time.Time, bool) {
	if len(file.Filename) >= len("2006-01-02") {
		if t, err := time.ParseInLocation("2006-01-02", file.Filename[:10], time.Local); err == nil {
			return t.Add(12 * time.Hour), true
		}
	}
	if t, err := time.Parse(time.RFC3339, file.MTime); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func minutesSince(ts string) int {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	min := int(time.Since(t) / time.Minute)
	if min < 0 {
		return 0
	}
	return min
}

func secondsSince(ts string) int {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	sec := int(time.Since(t) / time.Second)
	if sec < 0 {
		return 0
	}
	return sec
}

func formatActivity(seconds int) string {
	switch {
	case seconds < 60:
		return strconv.Itoa(seconds) + "s ago"
	case seconds < 3600:
		return strconv.Itoa(seconds/60) + "m ago"
	case seconds < 86400:
		return strconv.Itoa(seconds/3600) + "h ago"
	default:
		return strconv.Itoa(seconds/86400) + "d ago"
	}
}

// toolCallActivitySeries returns a 60-cell activity series for the agent
// tile, where each cell counts transcript entries observed in the
// corresponding minute of the last hour. Cell 0 is 59 minutes ago;
// cell 59 is the current minute. Anything older than 60 minutes is
// dropped. Every timestamped entry counts (tool calls, assistant/user
// turns, tool results) so the strip is meaningful for both Claude and
// Codex — Codex transcripts rarely surface as discrete tool_use events.
func toolCallActivitySeries(transcript []uiTranscript, now time.Time) []int {
	out := make([]int, 60)
	if len(transcript) == 0 {
		return out
	}
	// Anchor the window to the session's most recent activity rather than
	// wall-clock now. A session that crashed or went idle days ago still shows
	// the bars from its final active hour instead of an empty strip — the tile
	// chart is meaningful regardless of how long ago the session ran.
	anchor := time.Time{}
	for _, e := range transcript {
		if strings.TrimSpace(e.Time) == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, e.Time); err == nil && t.After(anchor) {
			anchor = t
		}
	}
	if anchor.IsZero() || anchor.After(now) {
		anchor = now
	}
	cutoff := anchor.Add(-time.Duration(len(out)) * time.Minute)
	for _, e := range transcript {
		if strings.TrimSpace(e.Time) == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Time)
		if err != nil {
			continue
		}
		if t.Before(cutoff) || t.After(anchor.Add(time.Minute)) {
			continue
		}
		minutesAgo := int(anchor.Sub(t) / time.Minute)
		if minutesAgo < 0 {
			minutesAgo = 0
		}
		if minutesAgo >= len(out) {
			continue
		}
		out[len(out)-1-minutesAgo]++
	}
	return out
}

// estimateTokens is only used when the provider transcript has no usage event.
func estimateTokens(tv TaskView, transcript []uiTranscript, max int) int {
	total := len(tv.Name) + len(tv.Slug)
	for _, e := range transcript {
		total += len(e.Text) + len(e.Input) + len(e.Summary) + len(e.Preview)
	}
	total = total/4 + len(tv.Updates)*300
	if total < 1200 {
		total = 1200
	}
	if max <= 0 {
		max = 200000
	}
	if total > max {
		total = max
	}
	return total
}

func recentTools(transcript []uiTranscript) []uiRecentTool {
	var out []uiRecentTool
	for i := len(transcript) - 1; i >= 0 && len(out) < 5; i-- {
		e := transcript[i]
		if e.Type != "tool_use" {
			continue
		}
		out = append(out, uiRecentTool{Name: e.Tool, S: e.Input})
	}
	return out
}

func terminalSample(tv TaskView, provider string, transcript []uiTranscript, mode string) uiTerminal {
	session := "no session"
	if tv.SessionID != nil {
		session = *tv.SessionID
	}
	if strings.TrimSpace(mode) == "" {
		mode = "idle"
	}
	name := "session"
	marker := "✻"
	if provider == "codex" {
		name = "codex"
		marker = "◇"
	}
	feed := []uiTermLine{}
	if tv.WaitingOn != nil {
		feed = append(feed,
			uiTermLine{C: "approval", Text: "┌─ Flow waiting ───────────────────────────────────────────────┐"},
			uiTermLine{C: "approval", Text: "│  Waiting on: " + truncateText(*tv.WaitingOn, 45)},
			uiTermLine{C: "approval", Text: "└───────────────────────────────────────────────────────────────┘"},
		)
	}
	for _, e := range transcript {
		switch e.Type {
		case "user":
			feed = append(feed, uiTermLine{C: "user", Text: "> " + e.Text})
		case "assistant":
			feed = append(feed, uiTermLine{C: "assistant", Text: marker + " " + e.Text})
		case "thinking":
			feed = append(feed, uiTermLine{C: "thinking", Text: marker + " " + e.Text})
		case "tool_use":
			feed = append(feed, uiTermLine{C: "tool", Text: "  " + marker + " " + e.Tool + "(" + e.Input + ")"})
		case "tool_result":
			feed = append(feed, uiTermLine{C: "tool-out", Text: "    └─ " + firstNonEmpty(e.Summary, e.Preview)})
		}
		if len(feed) >= 18 {
			break
		}
	}
	if len(feed) == 0 {
		feed = append(feed, uiTermLine{C: "assistant", Text: marker + " " + tv.Name})
	}
	return uiTerminal{
		Banner: []uiTermLine{
			{C: "banner", Text: fmt.Sprintf("%s %s · %s", marker, name, tv.WorkDir)},
			{C: "banner", Text: "session: " + session},
			{C: "space", Text: ""},
		},
		Feed:    feed,
		Appends: []uiTermLine{},
		Footer: []uiTermLine{
			{C: "space", Text: ""},
			{C: "status", Text: fmt.Sprintf("%s flow · %s · %s", marker, tv.Status, tv.Slug)},
		},
		Mode:    mode,
		Message: terminalModeMessage(provider, mode),
	}
}

func terminalModeMessage(provider, mode string) string {
	if provider == "" {
		provider = agents.ProviderClaude
	}
	switch mode {
	case "browser":
		return provider + " is attached to the browser terminal"
	case "shared":
		return provider + " is attached to a shared terminal"
	case "native":
		return provider + " is attached to a native terminal"
	default:
		return provider + " transcript snapshot"
	}
}

func (s *Server) transcriptAheadOfBrowserTerminal(slug string, transcript []uiTranscript) bool {
	if s.terminals == nil || len(transcript) == 0 {
		return false
	}
	lastOutput, ok := s.terminals.lastOutputAt(slug)
	if !ok {
		return false
	}
	var latest time.Time
	for _, entry := range transcript {
		if strings.TrimSpace(entry.Time) == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, entry.Time)
		if err != nil {
			continue
		}
		if t.After(latest) {
			latest = t
		}
	}
	return !latest.IsZero() && latest.After(lastOutput.Add(2*time.Second))
}

func gitBranch(dir string) string {
	out, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// cachedGitBranch wraps gitBranch with a per-server TTL cache so the hot
// uiAgent loop doesn't fork `git rev-parse` once per task per SSE tick.
func (s *Server) cachedGitBranch(dir string) string {
	if s == nil || s.caches == nil || dir == "" {
		return gitBranch(dir)
	}
	if v, ok := s.caches.gitBranch.get(dir); ok {
		return v
	}
	v := gitBranch(dir)
	s.caches.gitBranch.set(dir, v)
	return v
}

// cachedGitBranches wraps gitBranches with a per-server TTL cache. Keyed by
// dir+current so a branch switch (which changes `current`) immediately gets a
// fresh list; same-branch repeats within the 5s window are free.
func (s *Server) cachedGitBranches(dir, current string) []string {
	if s == nil || s.caches == nil || dir == "" {
		return gitBranches(dir, current)
	}
	key := dir + "\x00" + current
	if v, ok := s.caches.gitBranches.get(key); ok {
		return v
	}
	v := gitBranches(dir, current)
	s.caches.gitBranches.set(key, v)
	return v
}

// cachedGitDiff wraps gitDiff with a per-server TTL cache. gitDiff fans out
// into 3-12 git invocations depending on the diff size, so caching across an
// SSE tick is the highest-leverage win in this whole file.
func (s *Server) cachedGitDiff(dir string) (uiDiff, []uiDiffFile) {
	if s == nil || s.caches == nil || dir == "" {
		return gitDiff(dir)
	}
	if v, ok := s.caches.gitDiff.get(dir); ok {
		return v.diff, v.files
	}
	diff, files := gitDiff(dir)
	s.caches.gitDiff.set(dir, gitDiffSnapshot{diff: diff, files: files})
	return diff, files
}

func gitBranches(dir, current string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(branch string) {
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == "HEAD" || branch == "origin" || strings.HasSuffix(branch, "/HEAD") || seen[branch] {
			return
		}
		seen[branch] = true
		out = append(out, branch)
	}
	add(current)
	if branches, err := runGit(dir, "branch", "--format=%(refname:short)"); err == nil {
		for _, line := range strings.Split(string(branches), "\n") {
			add(line)
		}
	}
	if branches, err := runGit(dir, "branch", "-r", "--format=%(refname:short)"); err == nil {
		for _, line := range strings.Split(string(branches), "\n") {
			add(line)
		}
	}
	return out
}

func gitDiff(dir string) (uiDiff, []uiDiffFile) {
	var diff uiDiff
	filesByName := map[string]*uiDiffFile{}
	order := []string{}
	addNumstat := func(cached bool) {
		args := []string{"diff", "--numstat"}
		if cached {
			args = append(args, "--cached")
		}
		out, err := runGit(dir, args...)
		if err != nil {
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) < 3 {
				continue
			}
			add, _ := strconv.Atoi(parts[0])
			rem, _ := strconv.Atoi(parts[1])
			name := parts[2]
			f := filesByName[name]
			if f == nil {
				f = &uiDiffFile{Name: name}
				filesByName[name] = f
				order = append(order, name)
			}
			f.Add += add
			f.Rem += rem
			diff.Add += add
			diff.Rem += rem
		}
	}
	addNumstat(false)
	addNumstat(true)
	for _, name := range order {
		f := filesByName[name]
		if len(f.Hunks) == 0 {
			f.Hunks = gitDiffHunks(dir, name, false)
		}
		if len(f.Hunks) == 0 {
			f.Hunks = gitDiffHunks(dir, name, true)
		}
		diff.Files++
	}
	for _, name := range gitUntrackedFiles(dir) {
		if _, ok := filesByName[name]; ok {
			continue
		}
		f := untrackedDiffFile(dir, name)
		filesByName[name] = &f
		order = append(order, name)
		diff.Add += f.Add
		diff.Rem += f.Rem
		diff.Files++
	}
	files := make([]uiDiffFile, 0, len(order))
	for _, name := range order {
		if f := filesByName[name]; f != nil {
			files = append(files, *f)
		}
	}
	return diff, files
}

var gitHunkHeaderRe = regexp.MustCompile(`^@@ -([0-9]+)(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

func gitDiffHunks(dir, file string, cached bool) []uiDiffHunk {
	args := []string{"diff", "--unified=3"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--", file)
	out, err := runGit(dir, args...)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(out), "\n")
	hunks := []uiDiffHunk{}
	oldLine, newLine := 0, 0
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if len(hunks) >= 6 {
				break
			}
			match := gitHunkHeaderRe.FindStringSubmatch(line)
			oldLine, newLine = 0, 0
			if len(match) == 3 {
				oldLine, _ = strconv.Atoi(match[1])
				newLine, _ = strconv.Atoi(match[2])
			}
			hunks = append(hunks, uiDiffHunk{Header: line})
			continue
		}
		if len(hunks) == 0 || strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		if len(hunks[len(hunks)-1].Lines) >= 120 {
			continue
		}
		kind, num, code := "ctx", "", line
		switch {
		case strings.HasPrefix(line, "+"):
			kind = "add"
			code = strings.TrimPrefix(line, "+")
			num = strconv.Itoa(newLine)
			newLine++
		case strings.HasPrefix(line, "-"):
			kind = "rem"
			code = strings.TrimPrefix(line, "-")
			num = strconv.Itoa(oldLine)
			oldLine++
		default:
			code = strings.TrimPrefix(line, " ")
			if oldLine > 0 {
				num = strconv.Itoa(oldLine)
			}
			oldLine++
			newLine++
		}
		hunks[len(hunks)-1].Lines = append(hunks[len(hunks)-1].Lines, uiDiffLine{Type: kind, N: num, Code: code})
	}
	return hunks
}

func gitUntrackedFiles(dir string) []string {
	out, err := runGit(dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	if len(files) > 20 {
		files = files[:20]
	}
	return files
}

func untrackedDiffFile(dir, name string) uiDiffFile {
	f := uiDiffFile{Name: name}
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 128*1024 {
		f.Hunks = []uiDiffHunk{{Header: "@@ untracked file @@", Lines: []uiDiffLine{{Type: "add", Code: "untracked file"}}}}
		return f
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return f
	}
	lines := strings.Split(string(body), "\n")
	if len(lines) > 120 {
		lines = lines[:120]
	}
	h := uiDiffHunk{Header: "@@ untracked file @@"}
	for i, line := range lines {
		if strings.ContainsRune(line, '\x00') {
			h.Lines = []uiDiffLine{{Type: "add", Code: "binary or non-text file"}}
			break
		}
		h.Lines = append(h.Lines, uiDiffLine{Type: "add", N: strconv.Itoa(i + 1), Code: line})
	}
	f.Add = len(h.Lines)
	f.Hunks = []uiDiffHunk{h}
	return f
}

func runGit(dir string, args ...string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("empty workdir")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, errors.New("workdir unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	return cmd.Output()
}

func lastSevenFromThirty(in []int) []int {
	if len(in) >= 7 {
		return append([]int(nil), in[len(in)-7:]...)
	}
	out := make([]int, 7)
	copy(out[7-len(in):], in)
	return out
}

func uiStatusRank(status string) int {
	switch status {
	case "waiting":
		return 0
	case "running":
		return 1
	case "idle":
		return 2
	case "stale":
		return 3
	default:
		return 4
	}
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func nullStringValue(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return truncateText(line, 140)
		}
	}
	return ""
}
