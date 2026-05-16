package server

import "database/sql"

type Config struct {
	DB          *sql.DB
	FlowRoot    string
	Version     string
	CommandPath string
}

type Server struct {
	cfg       Config
	terminals *terminalHub
}

type HealthView struct {
	OK       bool   `json:"ok"`
	Version  string `json:"version"`
	FlowRoot string `json:"flow_root"`
}

type FileRef struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
	MTime    string `json:"mtime"`
	Size     int64  `json:"size"`
}

type FSEntriesView struct {
	Path        string         `json:"path"`
	DisplayPath string         `json:"display_path"`
	Parent      *string        `json:"parent"`
	IsGit       bool           `json:"is_git"`
	Breadcrumbs []FSBreadcrumb `json:"breadcrumbs"`
	Entries     []FSEntryView  `json:"entries"`
}

type FSBreadcrumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type FSEntryView struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	DisplayPath string `json:"display_path"`
	IsDir       bool   `json:"is_dir"`
	IsGit       bool   `json:"is_git"`
	Hidden      bool   `json:"hidden"`
}

type WorkdirKnown struct {
	Name      *string `json:"name,omitempty"`
	GitRemote *string `json:"git_remote,omitempty"`
}

type TaskView struct {
	Slug                string        `json:"slug"`
	Name                string        `json:"name"`
	ProjectSlug         *string       `json:"project_slug"`
	Status              string        `json:"status"`
	Kind                string        `json:"kind"`
	PlaybookSlug        *string       `json:"playbook_slug"`
	Priority            string        `json:"priority"`
	WorkDir             string        `json:"work_dir"`
	WorkdirKnown        *WorkdirKnown `json:"workdir_known"`
	WaitingOn           *string       `json:"waiting_on"`
	DueDate             *string       `json:"due_date"`
	DueInfo             *string       `json:"due_info"`
	Assignee            *string       `json:"assignee"`
	PermissionMode      string        `json:"permission_mode"`
	Tags                []string      `json:"tags"`
	SessionID           *string       `json:"session_id"`
	SessionProvider     *string       `json:"session_provider"`
	SessionStarted      *string       `json:"session_started"`
	SessionLastResumed  *string       `json:"session_last_resumed"`
	Live                bool          `json:"live"`
	DaysInStatus        int           `json:"days_in_status"`
	StaleDays           *int          `json:"stale_days"`
	TemporalSummary     string        `json:"temporal_summary"`
	CreatedAt           string        `json:"created_at"`
	UpdatedAt           string        `json:"updated_at"`
	ArchivedAt          *string       `json:"archived_at"`
	DeletedAt           *string       `json:"deleted_at"`
	BriefPath           string        `json:"brief_path"`
	Updates             []FileRef     `json:"updates"`
	AuxFiles            []FileRef     `json:"aux_files"`
	TranscriptAvailable bool          `json:"transcript_available"`
}

type TaskSummary struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	ProjectSlug *string `json:"project_slug"`
	UpdatedAt   string  `json:"updated_at"`
}

type TaskCounts struct {
	Total      int `json:"total"`
	InProgress int `json:"in_progress"`
	Backlog    int `json:"backlog"`
	Done       int `json:"done"`
}

type ProjectView struct {
	Slug         string        `json:"slug"`
	Name         string        `json:"name"`
	Status       string        `json:"status"`
	Priority     string        `json:"priority"`
	WorkDir      string        `json:"work_dir"`
	WorkdirKnown *WorkdirKnown `json:"workdir_known"`
	CreatedAt    string        `json:"created_at"`
	UpdatedAt    string        `json:"updated_at"`
	ArchivedAt   *string       `json:"archived_at"`
	DeletedAt    *string       `json:"deleted_at"`
	TaskCounts   TaskCounts    `json:"task_counts"`
	RecentTasks  []TaskSummary `json:"recent_tasks"`
	BriefPath    string        `json:"brief_path"`
	Updates      []FileRef     `json:"updates"`
	AuxFiles     []FileRef     `json:"aux_files"`
}

type RunSummary struct {
	Slug       string  `json:"slug"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Priority   string  `json:"priority"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	StartedAt  *string `json:"started_at"`
	ArchivedAt *string `json:"archived_at"`
	DeletedAt  *string `json:"deleted_at"`
}

type PlaybookView struct {
	Slug        string       `json:"slug"`
	Name        string       `json:"name"`
	ProjectSlug *string      `json:"project_slug"`
	WorkDir     string       `json:"work_dir"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	ArchivedAt  *string      `json:"archived_at"`
	DeletedAt   *string      `json:"deleted_at"`
	BriefPath   string       `json:"brief_path"`
	Updates     []FileRef    `json:"updates"`
	AuxFiles    []FileRef    `json:"aux_files"`
	RecentRuns  []RunSummary `json:"recent_runs"`
	RunCount7d  int          `json:"run_count_7d"`
	RunDays30   []int        `json:"run_days_30"`
}

type KBFileView struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
	MTime    string `json:"mtime"`
	Size     int64  `json:"size"`
	Entries  int    `json:"entries"`
	Preview  string `json:"preview"`
}

type WorkdirView struct {
	Path           string  `json:"path"`
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	GitRemote      *string `json:"git_remote"`
	LastUsedAt     *string `json:"last_used_at"`
	CreatedAt      string  `json:"created_at"`
	TasksUsingThis int     `json:"tasks_using_this"`
	Untouched30d   bool    `json:"untouched_30d"`
}

type OverviewView struct {
	LiveSessions        []TaskView     `json:"live_sessions"`
	InFlight            []TaskView     `json:"in_flight"`
	HighPriorityBacklog []TaskView     `json:"high_priority_backlog"`
	Waiting             []TaskView     `json:"waiting"`
	Stale               []TaskView     `json:"stale"`
	ActivePlaybooks     []PlaybookView `json:"active_playbooks"`
}

type SearchResponse struct {
	Query     string         `json:"query"`
	Tasks     []SearchResult `json:"tasks"`
	Projects  []SearchResult `json:"projects"`
	Playbooks []SearchResult `json:"playbooks"`
	Updates   []SearchResult `json:"updates"`
}

type SearchResult struct {
	Type    string `json:"type"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type TranscriptResponse struct {
	Available bool              `json:"available"`
	Message   string            `json:"message,omitempty"`
	Entries   []TranscriptEntry `json:"entries"`
}

type TranscriptEntry struct {
	Type             string `json:"type"`
	Text             string `json:"text,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolInputSummary string `json:"tool_input_summary,omitempty"`
	ToolResultText   string `json:"tool_result_text,omitempty"`
	IsError          bool   `json:"is_error,omitempty"`
	ByteOffset       int64  `json:"byte_offset"`
	Timestamp        string `json:"timestamp,omitempty"`
}

type WorkspaceNode struct {
	Name     string          `json:"name"`
	Path     string          `json:"path"`
	Type     string          `json:"type"`
	Size     int64           `json:"size,omitempty"`
	Children []WorkspaceNode `json:"children,omitempty"`
}

type WorkspaceView struct {
	Root      string          `json:"root"`
	Exists    bool            `json:"exists"`
	Truncated bool            `json:"truncated"`
	Nodes     []WorkspaceNode `json:"nodes"`
}
