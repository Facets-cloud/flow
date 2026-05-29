package server

import (
	"database/sql"

	"flow/internal/monitor"
)

type Config struct {
	DB          *sql.DB
	FlowRoot    string
	Version     string
	CommandPath string
	HookURL     string
}

type Server struct {
	cfg            Config
	terminals      *terminalHub
	events         *eventHub
	reconcile      *livenessReconciler
	transcripts    *transcriptCache
	caches         *uiCaches
	slackListener  *monitor.SlackListener
	githubListener *monitor.GitHubListener
	inboxMonitors  *inboxMonitorManager
	dbWatcher      *dbWatcher
	// nameResolver maps Slack user/channel IDs to display names for the
	// Inbox UI, caching lookups across requests. Nil when no Slack token is
	// configured; all of its methods are nil-safe.
	nameResolver *monitor.SlackNameResolver
	// monitorReconcile keeps persistent background monitors converged with the
	// set of tasks that need one (origin/branch-linked + active), restoring
	// them on boot and recreating any that die.
	monitorReconcile *monitorReconciler
	// respawn debounces agent respawns triggered by inbox events.
	respawn *respawnGate
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
	ParentSlug          *string       `json:"parent_slug"`
	Parent              *TaskSummary  `json:"parent,omitempty"`
	Parents             []TaskSummary `json:"parents,omitempty"`
	Children            []TaskSummary `json:"children,omitempty"`
	Priority            string        `json:"priority"`
	WorkDir             string        `json:"work_dir"`
	WorktreePath        *string       `json:"worktree_path,omitempty"`
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
	SessionPath         *string       `json:"session_path,omitempty"`
	Live                bool          `json:"live"`
	RuntimeStatus       *string       `json:"runtime_status,omitempty"`
	DaysInStatus        int           `json:"days_in_status"`
	StaleDays           *int          `json:"stale_days"`
	TemporalSummary     string        `json:"temporal_summary"`
	InboxPath           string        `json:"inbox_path,omitempty"`
	InboxUnreadCount    int           `json:"inbox_unread_count"`
	InboxSeenAt         *string       `json:"inbox_seen_at,omitempty"`
	CreatedAt           string        `json:"created_at"`
	UpdatedAt           string        `json:"updated_at"`
	ArchivedAt          *string       `json:"archived_at"`
	DeletedAt           *string       `json:"deleted_at"`
	BriefPath           string        `json:"brief_path"`
	Updates             []FileRef     `json:"updates"`
	AuxFiles            []FileRef     `json:"aux_files"`
	TranscriptAvailable bool          `json:"transcript_available"`
}

// InboxEntry is one parsed message from a task's inbox.md.
type InboxEntry struct {
	Timestamp string `json:"timestamp"`
	Sender    string `json:"sender"`
	Body      string `json:"body"`
}

// MonitorSyncStateView is one source's poll status as the Inbox UI sees
// it. LastSyncAt is an RFC3339 string when the source has been polled at
// least once, empty otherwise. LastError is empty when LastStatus is "ok"
// or "unknown".
type MonitorSyncStateView struct {
	Source     string `json:"source"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
	LastStatus string `json:"last_status"`
	LastError  string `json:"last_error,omitempty"`
	IsSyncing  bool   `json:"is_syncing"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// MonitorSyncStateResponse wraps the list so future fields (e.g.
// next_scheduled_poll_at, default_interval) can be added without
// breaking the existing []shape contract.
type MonitorSyncStateResponse struct {
	States []MonitorSyncStateView `json:"states"`
}

// InboxView is the GET /api/tasks/<slug>/inbox response shape.
type InboxView struct {
	Slug        string       `json:"slug"`
	Path        string       `json:"path"`
	UnreadCount int          `json:"unread_count"`
	SeenAt      *string      `json:"seen_at,omitempty"`
	Entries     []InboxEntry `json:"entries"`
}

// InboxFeedEntry is one inbox.md row enriched with the task it belongs to.
// Used by the global /api/inbox aggregation. BodySnippet is a truncated
// preview (full body still in Body so the UI can expand on demand).
type InboxFeedEntry struct {
	TaskSlug    string  `json:"task_slug"`
	TaskName    string  `json:"task_name"`
	ProjectSlug *string `json:"project_slug,omitempty"`
	Status      string  `json:"status"`
	Timestamp   string  `json:"timestamp"`
	Sender      string  `json:"sender"`
	Body        string  `json:"body"`
	BodySnippet string  `json:"body_snippet"`
	Unread      bool    `json:"unread"`
	// Source is "slack" | "github" | "" — derived from the event, used by the
	// grouped conversation list to show a source icon without re-parsing.
	Source string `json:"source,omitempty"`
	// Live reports whether the task's session is currently running, so the
	// conversation list can show a live indicator. Matches TaskView.Live.
	Live bool `json:"live"`
	// Monitored reports whether a persistent background monitor is currently
	// running for the task (independent of whether a session is live).
	Monitored bool `json:"monitored"`
}

// InboxConversation is the GET /api/inbox/conversation?slug=<task> response:
// one task's full thread of inbox events, with every Slack ID already
// resolved to a human-readable name. Powers the Inbox right pane.
type InboxConversation struct {
	Slug        string                     `json:"slug"`
	Name        string                     `json:"name"`
	ProjectSlug *string                    `json:"project_slug,omitempty"`
	Status      string                     `json:"status"`
	Provider    string                     `json:"provider"`
	Live        bool                       `json:"live"`
	Monitored   bool                       `json:"monitored"`
	Source      string                     `json:"source"`                 // slack | github | mixed | ""
	ChannelName string                     `json:"channel_name,omitempty"` // resolved; never a raw ID
	Messages    []InboxConversationMessage `json:"messages"`               // chronological (oldest first)
}

// InboxConversationMessage is one rendered message in a conversation thread.
// SenderName and Body are guaranteed free of raw Slack IDs.
type InboxConversationMessage struct {
	Source     string `json:"source"`      // slack | github
	Kind       string `json:"kind"`        // message, app_mention, pr_review_comment, …
	SenderName string `json:"sender_name"` // resolved display name or login; never a raw ID
	Timestamp  string `json:"timestamp"`   // RFC3339 (enqueued_at) when available
	Title      string `json:"title"`       // humanised kind, e.g. "PR review requested"
	Body       string `json:"body"`        // message text with mentions/links cleaned
	Permalink  string `json:"permalink,omitempty"`
	Reaction   string `json:"reaction,omitempty"`
}

// InboxFeed is the GET /api/inbox response shape — a global aggregation of
// every task's inbox.md entries, newest first.
type InboxFeed struct {
	Entries     []InboxFeedEntry `json:"entries"`
	UnreadCount int              `json:"unread_count"`
	TaskCount   int              `json:"task_count"`
	GeneratedAt string           `json:"generated_at"`
}

// LifecycleEvent is one row of the per-session event timeline.
type LifecycleEvent struct {
	Time     string `json:"time"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Body     string `json:"body,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// LifecycleView is the GET /api/tasks/<slug>/lifecycle response shape.
type LifecycleView struct {
	Slug   string           `json:"slug"`
	Events []LifecycleEvent `json:"events"`
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
	Query       string         `json:"query"`
	Tasks       []SearchResult `json:"tasks"`
	Projects    []SearchResult `json:"projects"`
	Playbooks   []SearchResult `json:"playbooks"`
	Updates     []SearchResult `json:"updates"`
	Transcripts []SearchResult `json:"transcripts"`
	Memories    []SearchResult `json:"memories"`
}

type SearchResult struct {
	Type       string `json:"type"`
	Scope      string `json:"scope,omitempty"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet"`
	SourcePath string `json:"source_path,omitempty"`
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
