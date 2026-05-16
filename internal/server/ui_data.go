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
	Agents           []uiAgent       `json:"AGENTS"`
	DeadAgent        *uiAgent        `json:"DEAD_AGENT"`
	DoneAgents       []uiAgent       `json:"DONE_AGENTS"`
	Backlog          []uiBacklogTask `json:"BACKLOG"`
	DoneTasks        []uiBacklogTask `json:"DONE_TASKS"`
	KBFiles          []uiKBFile      `json:"KB_FILES"`
	Workdirs         []uiWorkdir     `json:"WORKDIRS"`
	Playbooks        []uiPlaybook    `json:"PLAYBOOKS_MC"`
	Projects         []uiProject     `json:"PROJECTS_MC"`
	ActivityHeatmap  []uiActivityDay `json:"ACTIVITY_HEATMAP"`
	Capabilities     uiCapabilities  `json:"CAPABILITIES"`
	Monitor          uiMonitor       `json:"MONITOR"`
	Trash            uiTrash         `json:"TRASH"`
	SampleTranscript []uiTranscript  `json:"SAMPLE_TRANSCRIPT"`
	SampleDiffFiles  []uiDiffFile    `json:"SAMPLE_DIFF_FILES"`
}

type uiActivityDay struct {
	Date  string   `json:"date"`
	Count int      `json:"count"`
	Tasks []string `json:"tasks,omitempty"`
}

type uiMonitor struct {
	Notifications []uiMonitorNotification `json:"notifications"`
	Events        []uiMonitorEvent        `json:"events"`
	Rules         []uiAutomationRule      `json:"rules"`
	Sources       []uiMonitorSource       `json:"sources"`
	Unread        int                     `json:"unread"`
	Approvals     int                     `json:"approvals"`
	LastSync      string                  `json:"last_sync"`
}

type uiMonitorNotification struct {
	ID        string `json:"id"`
	EventID   string `json:"event_id"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Level     string `json:"level"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Source    string `json:"source,omitempty"`
	Kind      string `json:"kind,omitempty"`
	URL       string `json:"url,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

type uiMonitorEvent struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	URL         string `json:"url,omitempty"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
	Mode        string `json:"mode,omitempty"`
}

type uiAutomationRule struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Mode   string `json:"mode"`
}

type uiMonitorSource struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	LastSync string `json:"last_sync,omitempty"`
	Message  string `json:"message,omitempty"`
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
	Branch          string         `json:"branch"`
	Branches        []string       `json:"branches,omitempty"`
	WorkDir         string         `json:"work_dir"`
	Provider        string         `json:"provider"`
	PermissionMode  string         `json:"permission_mode"`
	Priority        string         `json:"priority"`
	Status          string         `json:"status"`
	SessionID       string         `json:"session_id"`
	StartedMin      int            `json:"started_min"`
	LastActivitySec int            `json:"last_activity_sec"`
	LastAction      string         `json:"last_action"`
	WaitingFor      *uiWaitingFor  `json:"waiting_for,omitempty"`
	Diff            uiDiff         `json:"diff"`
	TokensUsed      int            `json:"tokens_used"`
	TokensMax       int            `json:"tokens_max"`
	Activity        []int          `json:"activity"`
	Tags            []string       `json:"tags"`
	Summary         string         `json:"summary"`
	NextStep        string         `json:"next_step"`
	Transcript      []uiTranscript `json:"transcript,omitempty"`
	Brief           string         `json:"brief,omitempty"`
	RecentTools     []uiRecentTool `json:"recent_tools,omitempty"`
	PRLinks         []uiPRLink     `json:"pr_links,omitempty"`
	DiffFiles       []uiDiffFile   `json:"diff_files,omitempty"`
	Terminal        uiTerminal     `json:"terminal"`
}

type uiWaitingFor struct {
	Kind string `json:"kind"`
	Cmd  string `json:"cmd"`
	Why  string `json:"why"`
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

type uiPRLink struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	URL      string `json:"url"`
	State    string `json:"state"`
	MergedAt string `json:"merged_at,omitempty"`
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
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Project   string   `json:"project"`
	Provider  string   `json:"provider"`
	Priority  string   `json:"priority"`
	Due       string   `json:"due,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	WaitingOn *string  `json:"waiting_on,omitempty"`
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
	Slug     string  `json:"slug"`
	Name     string  `json:"name"`
	Project  *string `json:"project"`
	RunsWeek int     `json:"runs_week"`
	LastMin  *int    `json:"last_min"`
	Spark    []int   `json:"spark"`
	WorkDir  string  `json:"work_dir"`
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
	live, _ := liveAgentSessions()
	tasks, err := flowdb.ListTasks(s.cfg.DB, flowdb.TaskFilter{Kind: "", IncludeArchived: false})
	if err != nil {
		return uiData{}, err
	}
	taskViews, err := BuildTaskViews(s.cfg.DB, s.cfg.FlowRoot, tasks)
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

	var dead *uiAgent
	if len(doneCandidates) > 0 {
		doneCandidates[0].Status = "idle"
		dead = &doneCandidates[0]
	}
	return uiData{
		Agents:           agents,
		DeadAgent:        dead,
		DoneAgents:       doneCandidates,
		Backlog:          backlog,
		DoneTasks:        doneTasks,
		KBFiles:          kb,
		Workdirs:         workdirs,
		Playbooks:        playbooks,
		Projects:         projects,
		ActivityHeatmap:  buildActivityHeatmap(taskViews, time.Now()),
		Capabilities:     detectCapabilities(),
		Monitor:          s.uiMonitor(agents),
		Trash:            s.uiTrash(),
		SampleTranscript: transcript,
		SampleDiffFiles:  diffFiles,
	}, nil
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

func (s *Server) uiMonitor(agents []uiAgent) uiMonitor {
	_ = flowdb.EnsureDefaultAutomationRules(s.cfg.DB)
	rules, _ := flowdb.ListAutomationRules(s.cfg.DB)
	events, _ := flowdb.ListMonitorEvents(s.cfg.DB, 50)
	notifications, _ := flowdb.ListMonitorNotifications(s.cfg.DB, 50)
	ruleModes := map[string]string{}
	uiRules := make([]uiAutomationRule, 0, len(rules))
	for _, rule := range rules {
		ruleModes[rule.Source+"."+rule.Kind] = rule.Mode
		uiRules = append(uiRules, uiAutomationRule{ID: rule.ID, Source: rule.Source, Kind: rule.Kind, Mode: rule.Mode})
	}
	eventByID := map[string]flowdb.MonitorEvent{}
	hookAttentionURLs := map[string]bool{}
	uiEvents := make([]uiMonitorEvent, 0, len(events))
	lastSync := ""
	for _, event := range events {
		eventByID[event.ID] = event
		if event.Source == agentHookMonitorSource && (event.Status == "new" || event.Status == "notified") && agentHookAttentionKind(event.Kind) {
			if url := nullStringValue(event.URL); url != "" {
				hookAttentionURLs[url] = true
			}
		}
		if event.LastSeenAt > lastSync {
			lastSync = event.LastSeenAt
		}
		mode := ruleModes[event.Source+"."+event.Kind]
		uiEvents = append(uiEvents, uiMonitorEvent{
			ID:          event.ID,
			Source:      event.Source,
			Kind:        event.Kind,
			SourceID:    event.SourceID,
			Title:       event.Title,
			Body:        nullStringValue(event.Body),
			URL:         nullStringValue(event.URL),
			Severity:    event.Severity,
			Status:      event.Status,
			FirstSeenAt: event.FirstSeenAt,
			LastSeenAt:  event.LastSeenAt,
			Mode:        mode,
		})
	}
	agentNotifications := agentAttentionNotifications(agents)
	agentNotificationIDs := make([]string, 0, len(agentNotifications))
	for _, notification := range agentNotifications {
		agentNotificationIDs = append(agentNotificationIDs, notification.ID)
	}
	agentNotificationStates, _ := flowdb.NotificationStateMap(s.cfg.DB, agentNotificationIDs)
	uiNotifications := make([]uiMonitorNotification, 0, len(notifications)+len(agentNotifications))
	unread := 0
	approvals := 0
	for _, notification := range agentNotifications {
		if notification.Level == "approval" && hookAttentionURLs[notification.URL] {
			continue
		}
		if state := agentNotificationStates[notification.ID]; state != "" {
			if state == "dismissed" {
				continue
			}
			notification.Status = state
		}
		if notification.Status == "unread" {
			unread++
		}
		if notification.Level == "approval" && notification.Status == "unread" {
			approvals++
		}
		uiNotifications = append(uiNotifications, notification)
	}
	for _, notification := range notifications {
		event := eventByID[notification.EventID]
		if notification.Status == "unread" {
			unread++
		}
		if notification.Level == "approval" && notification.Status == "unread" {
			approvals++
		}
		uiNotifications = append(uiNotifications, uiMonitorNotification{
			ID:        notification.ID,
			EventID:   notification.EventID,
			Title:     notification.Title,
			Body:      nullStringValue(notification.Body),
			Level:     notification.Level,
			Status:    notification.Status,
			CreatedAt: notification.CreatedAt,
			Source:    event.Source,
			Kind:      event.Kind,
			URL:       nullStringValue(event.URL),
			Mode:      ruleModes[event.Source+"."+event.Kind],
		})
	}
	return uiMonitor{
		Notifications: uiNotifications,
		Events:        uiEvents,
		Rules:         uiRules,
		Sources: []uiMonitorSource{
			{ID: "agents", Label: "Claude/Codex sessions", Status: "live", LastSync: "realtime", Message: "watching active agent sessions"},
			{ID: "github", Label: "gh CLI", Status: "configured", LastSync: lastSeenForSource(events, "github")},
			{ID: "slack", Label: "Slack API", Status: slackMonitorStatus(), LastSync: lastSeenForSource(events, "slack"), Message: slackMonitorMessage()},
		},
		Unread:    unread,
		Approvals: approvals,
		LastSync:  lastSync,
	}
}

func agentAttentionNotifications(agents []uiAgent) []uiMonitorNotification {
	out := []uiMonitorNotification{}
	for _, agent := range agents {
		if agent.Status == "running" {
			out = append(out, uiMonitorNotification{
				ID:      "agent-" + agent.Slug + "-running",
				EventID: agent.Slug,
				Title:   agent.Name + " is running",
				Body:    agentSessionLabel(agent.Provider, agent.SessionID) + " is live. Last action: " + agent.LastAction,
				Level:   "log",
				Status:  "read",
				Source:  "agent",
				Kind:    agent.Provider + " running",
				URL:     "/session/" + agent.Slug,
				Mode:    "realtime",
			})
		}
		if agent.Status == "waiting" && agent.WaitingFor != nil && (agent.WaitingFor.Kind == "question" || agent.WaitingFor.Kind == "permission" || agent.WaitingFor.Kind == "agent") {
			title := agent.Name + " needs your answer"
			if agent.WaitingFor.Kind == "permission" {
				title = agent.Name + " needs permission"
			}
			out = append(out, uiMonitorNotification{
				ID:      "agent-" + agent.Slug + "-" + agent.WaitingFor.Kind,
				EventID: agent.Slug,
				Title:   title,
				Body:    agent.WaitingFor.Why,
				Level:   "approval",
				Status:  "unread",
				Source:  "agent",
				Kind:    agent.Provider + " " + agent.WaitingFor.Kind,
				URL:     "/session/" + agent.Slug,
				Mode:    "realtime",
			})
			continue
		}
		if agent.SessionID != "" && (agent.Status == "idle" || agent.Status == "stale") {
			out = append(out, uiMonitorNotification{
				ID:      "agent-" + agent.Slug + "-stopped",
				EventID: agent.Slug,
				Title:   agent.Name + " is not running",
				Body: truncateText(
					agent.Provider+" session "+shortSessionID(agent.SessionID)+" is bound to an in-progress task but no live process is attached. Last action: "+agent.LastAction,
					220,
				),
				Level:  "notify",
				Status: "unread",
				Source: "agent",
				Kind:   agent.Provider + " stopped",
				URL:    "/session/" + agent.Slug,
				Mode:   "realtime",
			})
		}
	}
	return out
}

func shortSessionID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func agentSessionLabel(provider, id string) string {
	if strings.TrimSpace(id) == "" {
		return provider + " session pending capture"
	}
	return provider + " session " + shortSessionID(id)
}

func slackMonitorStatus() string {
	if strings.TrimSpace(os.Getenv("FLOW_SLACK_POLL_CMD")) != "" {
		return "custom command"
	}
	if slackMonitorTokenConfigured() {
		return "token configured"
	}
	return "needs token"
}

func slackMonitorMessage() string {
	if strings.TrimSpace(os.Getenv("FLOW_SLACK_POLL_CMD")) != "" {
		return "using FLOW_SLACK_POLL_CMD custom JSON command"
	}
	if slackMonitorTokenConfigured() {
		return "polling Slack Web API with configured token"
	}
	return "set FLOW_SLACK_TOKEN or SLACK_USER_TOKEN; installed Slack CLI has no inbox command"
}

func slackMonitorTokenConfigured() bool {
	return strings.TrimSpace(os.Getenv("FLOW_SLACK_TOKEN")) != "" ||
		strings.TrimSpace(os.Getenv("SLACK_USER_TOKEN")) != "" ||
		strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN")) != "" ||
		strings.TrimSpace(os.Getenv("SLACK_TOKEN")) != ""
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
		permissionMode = "default"
	}
	sessionID := ""
	if tv.SessionID != nil {
		sessionID = *tv.SessionID
	}
	bridgeRunning := s.terminals != nil && s.terminals.running(tv.Slug)
	sessionLive := tv.SessionID != nil && live[strings.ToLower(*tv.SessionID)]
	terminalMode := "idle"
	switch {
	case bridgeRunning:
		terminalMode = "browser"
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
	status := "idle"
	switch tv.Status {
	case "in-progress":
		switch {
		case tv.WaitingOn != nil:
			status = "waiting"
		case bridgeRunning:
			status = "running"
		case sessionLive:
			status = "running"
		case tv.StaleDays != nil:
			status = "stale"
		default:
			status = "idle"
		}
	case "done":
		status = "idle"
	default:
		status = tv.Status
	}
	if staleOverviewSession {
		sessionID = ""
		status = "idle"
	}
	hookWaiting := s.agentHookWaitingFor(tv)
	if !staleOverviewSession && tv.Status == "in-progress" && hookWaiting != nil {
		status = "waiting"
	}
	branches := []string{}
	branch := "~/.flow"
	if tv.Slug != overviewTaskSlug {
		branch = gitBranch(workDir)
		if branch == "" {
			branch = tv.Slug + "/main"
		} else {
			branches = gitBranches(workDir, branch)
		}
	}
	lastActivityAt := laterTimestamp(latestTaskActivity(tv), insights.ActivityAt)
	lastActivity := secondsSince(lastActivityAt)
	startedAt := tv.CreatedAt
	if tv.SessionStarted != nil {
		startedAt = *tv.SessionStarted
	}
	diff, files := gitDiff(workDir)
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
		Branch:          branch,
		Branches:        branches,
		WorkDir:         workDir,
		Provider:        provider,
		PermissionMode:  permissionMode,
		Priority:        tv.Priority,
		Status:          status,
		SessionID:       sessionID,
		StartedMin:      minutesSince(startedAt),
		LastActivitySec: lastActivity,
		LastAction:      lastAction,
		Diff:            diff,
		DiffFiles:       files,
		TokensUsed:      tokensUsed,
		TokensMax:       tokensMax,
		Activity:        activitySeries(tv.Slug, lastActivity, status),
		Tags:            tv.Tags,
		Summary:         summary,
		NextStep:        nextStep,
		Transcript:      transcript,
		Brief:           readMarkdownSummary(tv.BriefPath),
		RecentTools:     recentTools(transcript),
		PRLinks:         s.uiPRLinks(tv.Slug),
		Terminal:        terminalSample(withTaskWorkDir(tv, workDir), provider, transcript, terminalMode),
	}
	if tv.WaitingOn != nil {
		agent.WaitingFor = &uiWaitingFor{Kind: "flow", Cmd: "flow update task " + tv.Slug + " --clear-waiting", Why: *tv.WaitingOn}
	} else if hookWaiting != nil {
		agent.WaitingFor = hookWaiting
	}
	return agent
}

func withTaskWorkDir(tv TaskView, workDir string) TaskView {
	tv.WorkDir = workDir
	return tv
}

type taskSessionInsights struct {
	ActivityAt string
	LastAction string
	TokensUsed int
	TokensMax  int
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
		SessionProvider: provider,
		SessionID:       sql.NullString{String: *tv.SessionID, Valid: true},
	}
	path, err := sessionJSONLPath(task)
	if err != nil {
		return insights
	}
	if st, err := os.Stat(path); err == nil {
		insights.ActivityAt = st.ModTime().Format(time.RFC3339)
	}
	stats := sessionTranscriptUsageStats(path)
	insights.ActivityAt = laterTimestamp(insights.ActivityAt, stats.LastTimestamp)
	if stats.TokensUsed > 0 {
		insights.TokensUsed = stats.TokensUsed
	}
	if insights.TokensMax <= 0 && stats.TokensMax > 0 {
		insights.TokensMax = stats.TokensMax
	}
	for _, entry := range transcript {
		insights.ActivityAt = laterTimestamp(insights.ActivityAt, entry.Time)
	}
	return insights
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

func (s *Server) uiPRLinks(taskSlug string) []uiPRLink {
	links, err := flowdb.ListTaskPRLinks(s.cfg.DB, taskSlug)
	if err != nil {
		return nil
	}
	out := make([]uiPRLink, 0, len(links))
	for _, link := range links {
		out = append(out, uiPRLink{
			Repo:     link.Repo,
			Number:   link.PRNumber,
			URL:      link.PRURL,
			State:    link.State,
			MergedAt: nullStringValue(link.MergedAt),
		})
	}
	return out
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
		Slug:      tv.Slug,
		Name:      tv.Name,
		Project:   project,
		Provider:  provider,
		Priority:  tv.Priority,
		Tags:      tv.Tags,
		WaitingOn: tv.WaitingOn,
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
		out = append(out, uiPlaybook{
			Slug:     p.Slug,
			Name:     p.Name,
			Project:  p.ProjectSlug,
			RunsWeek: p.RunCount7d,
			LastMin:  lastMin,
			Spark:    lastSevenFromThirty(p.RunDays30),
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
		if w.Name != nil && *w.Name != "" {
			name = *w.Name
		}
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
		SessionProvider: provider,
		SessionID:       sql.NullString{String: *tv.SessionID, Valid: true},
	}
	path, err := sessionJSONLPath(t)
	if err != nil {
		return nil
	}
	entries, err := parseTranscriptFile(path)
	if err != nil {
		return nil
	}
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
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -83)
	days := make([]uiActivityDay, 84)
	index := make(map[string]int, len(days))
	seenTasks := make(map[string]map[string]bool)
	for i := range days {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		days[i] = uiActivityDay{Date: date}
		index[date] = i
	}
	add := func(ts time.Time, slug string) {
		date := ts.In(time.Local).Format("2006-01-02")
		i, ok := index[date]
		if !ok {
			return
		}
		days[i].Count++
		if seenTasks[date] == nil {
			seenTasks[date] = map[string]bool{}
		}
		if !seenTasks[date][slug] && len(days[i].Tasks) < 5 {
			days[i].Tasks = append(days[i].Tasks, slug)
			seenTasks[date][slug] = true
		}
	}
	addString := func(ts string, slug string) {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			add(t, slug)
		}
	}
	for _, task := range tasks {
		addString(task.CreatedAt, task.Slug)
		addString(task.UpdatedAt, task.Slug)
		if task.SessionStarted != nil {
			addString(*task.SessionStarted, task.Slug)
		}
		if task.SessionLastResumed != nil {
			addString(*task.SessionLastResumed, task.Slug)
		}
		for _, update := range task.Updates {
			if t, ok := activityTimeForFile(update); ok {
				add(t, task.Slug)
			}
		}
		if task.Status == "in-progress" || task.Live {
			add(now, task.Slug)
		}
	}
	return days
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

// activitySeries is a synthetic last-60-minute activity hint derived from flow state.
func activitySeries(slug string, lastActivitySec int, status string) []int {
	out := make([]int, 60)
	if lastActivitySec < 0 {
		lastActivitySec = 0
	}
	lastMinute := lastActivitySec / 60
	for i := range out {
		minuteAgo := len(out) - 1 - i
		switch {
		case status == "running" && minuteAgo < 12:
			out[i] = 5 + (12-minuteAgo)/3
		case status == "waiting" && minuteAgo < 8:
			out[i] = 3
		}
		delta := minuteAgo - lastMinute
		if delta < 0 {
			delta = -delta
		}
		if lastMinute < len(out) && delta <= 2 {
			level := 5 - delta
			if level > out[i] {
				out[i] = level
			}
		}
	}
	if status == "idle" && lastMinute >= len(out) && lastActivitySec < 6*3600 {
		out[0] = 1
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

func lastSeenForSource(events []flowdb.MonitorEvent, source string) string {
	last := ""
	for _, event := range events {
		if event.Source == source && event.LastSeenAt > last {
			last = event.LastSeenAt
		}
	}
	return last
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
