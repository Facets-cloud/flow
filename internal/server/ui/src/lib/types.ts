// TypeScript shapes mirroring the Go server view structs (internal/server).
// Loose where it buys nothing to be strict; the server is the source of truth.

export interface FileRef {
  filename: string
  path: string
  mtime: string
  size: number
}

export interface TaskSummary {
  slug: string
  name: string
  status: string
  priority: string
  project_slug: string | null
  updated_at: string
}

export interface WorkdirKnown {
  name?: string
  git_remote?: string
}

export interface TaskView {
  slug: string
  name: string
  project_slug: string | null
  status: string
  kind: string
  playbook_slug: string | null
  parent_slug: string | null
  parent?: TaskSummary
  parents?: TaskSummary[]
  children?: TaskSummary[]
  priority: string
  work_dir: string
  worktree_path?: string
  workdir_known: WorkdirKnown | null
  waiting_on: string | null
  due_date: string | null
  due_info: string | null
  assignee: string | null
  permission_mode: string
  tags: string[]
  session_id: string | null
  session_provider: string | null
  session_started: string | null
  session_last_resumed: string | null
  live: boolean
  runtime_status?: string
  days_in_status: number
  stale_days: number | null
  temporal_summary: string
  inbox_path?: string
  inbox_unread_count: number
  created_at: string
  updated_at: string
  archived_at: string | null
  deleted_at: string | null
  brief_path: string
  updates: FileRef[]
  aux_files: FileRef[]
  transcript_available: boolean
}

export interface TaskCounts {
  total: number
  in_progress: number
  backlog: number
  done: number
}

export interface ProjectView {
  slug: string
  name: string
  status: string
  priority: string
  work_dir: string
  workdir_known: WorkdirKnown | null
  created_at: string
  updated_at: string
  archived_at: string | null
  deleted_at: string | null
  task_counts: TaskCounts
  recent_tasks: TaskSummary[]
  brief_path: string
  updates: FileRef[]
  aux_files: FileRef[]
}

export interface RunSummary {
  slug: string
  name: string
  status: string
  priority: string
  created_at: string
  updated_at: string
  started_at: string | null
}

export interface PlaybookView {
  slug: string
  name: string
  project_slug: string | null
  work_dir: string
  created_at: string
  updated_at: string
  brief_path: string
  updates: FileRef[]
  aux_files: FileRef[]
  recent_runs: RunSummary[]
  run_count_7d: number
  run_days_30: number[]
}

export interface KBFileView {
  filename: string
  path: string
  mtime: string
  size: number
  entries: number
  preview: string
  content: string
}

export interface WorkdirView {
  path: string
  name: string | null
  description: string | null
  git_remote: string | null
  last_used_at: string | null
  created_at: string
  tasks_using_this: number
  untouched_30d: boolean
}

export interface FSBreadcrumb {
  name: string
  path: string
}
export interface FSEntryView {
  name: string
  path: string
  display_path: string
  is_dir: boolean
  is_git: boolean
  hidden: boolean
}
export interface FSEntriesView {
  path: string
  display_path: string
  parent: string | null
  is_git: boolean
  breadcrumbs: FSBreadcrumb[]
  entries: FSEntryView[]
}

export interface OverviewView {
  live_sessions: TaskView[]
  in_flight: TaskView[]
  high_priority_backlog: TaskView[]
  waiting: TaskView[]
  stale: TaskView[]
  active_playbooks: PlaybookView[]
}

export interface DiffCount {
  add: number
  rem: number
  files: number
}
export interface DiffLine {
  type: string
  n: string
  code: string
}
export interface DiffHunk {
  header: string
  lines: DiffLine[]
}
export interface DiffFile {
  name: string
  add: number
  rem: number
  hunks?: DiffHunk[]
}

export interface UiTranscript {
  type: string
  text?: string
  tool?: string
  input?: string
  summary?: string
  preview?: string
  lines?: number
  time?: string
}

export interface UiAgent {
  slug: string
  name: string
  project: string | null
  kind?: string
  playbook_slug?: string | null
  parent?: TaskSummary
  parents?: TaskSummary[]
  children?: TaskSummary[]
  branch: string
  branches?: string[]
  work_dir: string
  provider: string
  permission_mode: string
  priority: string
  status: string
  task_status: string
  runtime_status: string
  runtime_event?: string
  session_id: string
  started_min: number
  last_activity_sec: number
  last_action: string
  waiting_for?: { kind: string; cmd: string; why: string }
  diff: DiffCount
  tokens_used: number
  tokens_max: number
  tokens_session: number
  activity: number[]
  tags: string[]
  summary: string
  next_step: string
  recent_tools?: { name: string; s: string }[]
  hook_health?: { status: string; message: string; action?: string }
  transcript?: UiTranscript[]
  brief?: string
  diff_files?: DiffFile[]
  brief_path?: string
  updates?: FileRef[]
  aux_files?: FileRef[]
  terminal: { mode?: string; message?: string }
  monitored: boolean
}

export interface ToolCapability {
  id: string
  label: string
  available: boolean
  reason?: string
  path?: string
  status?: string
}
export interface Capabilities {
  providers: ToolCapability[]
  terminals: ToolCapability[]
  integrations: ToolCapability[]
}

export interface BacklogTask {
  slug: string
  name: string
  project: string
  parent?: TaskSummary
  children?: TaskSummary[]
  provider: string
  priority: string
  due?: string
  tags?: string[]
  waiting_on?: string | null
  started_min: number
}

export interface ProjectMC {
  slug: string
  name: string
  priority: string
  tasks: TaskCounts
  work_dir: string
}

export interface PlaybookRun {
  name: string
  status: string
  created_at: string
}

export interface PlaybookMC {
  slug: string
  name: string
  project: string | null
  runs_week: number
  last_min: number | null
  spark: number[]
  runs?: PlaybookRun[]
  work_dir: string
}

export interface KBFile {
  name: string
  preview: string
  count: number
  entries: { d: string; t: string }[]
}

export interface MemorySource {
  id: string
  provider: string
  scope: string
  kind: string
  label: string
  path: string
  status: string
  available: boolean
  format?: string
  mtime?: string
  size?: number
  content?: string
  error?: string
}

export interface Workdir {
  path: string
  name: string
  remote: string | null
  used_min: number
  tasks: number
  untouched: boolean
}

export interface ActivityDay {
  date: string
  count: number
  tasks?: string[]
}

export interface QuoteView {
  quote: string
  anime: string
  character: string
}

export interface TrashItem {
  kind: string
  slug: string
  name: string
  status?: string
  priority?: string
  project?: string | null
  work_dir: string
  deleted_at: string
  archived: boolean
}

export interface UiStats {
  current_streak: number
  longest_streak: number
  active_days: number
  tokens_total: number
  tokens_claude: number
  tokens_codex: number
  sessions_total: number
  sessions_claude: number
  sessions_codex: number
}

export interface UiData {
  AGENTS: UiAgent[]
  DEAD_AGENT: UiAgent | null
  DONE_AGENTS: UiAgent[]
  BACKLOG: BacklogTask[]
  DONE_TASKS: BacklogTask[]
  KB_FILES: KBFile[]
  AGENT_MEMORY_SOURCES: MemorySource[]
  WORKDIRS: Workdir[]
  PLAYBOOKS_MC: PlaybookMC[]
  PROJECTS_MC: ProjectMC[]
  ACTIVITY_HEATMAP: ActivityDay[]
  STATS: UiStats
  CAPABILITIES: Capabilities
  TRASH: { tasks: TrashItem[]; projects: TrashItem[]; playbooks: TrashItem[]; total: number }
  FLOWDB: { path: string; display_path: string; bytes: number; human_size: string; exists: boolean }
  USER: { name: string; full_name: string; username: string }
}

export interface TranscriptEntry {
  type: string
  text?: string
  tool_name?: string
  tool_input_summary?: string
  tool_result_text?: string
  is_error?: boolean
  byte_offset: number
  timestamp?: string
}
export interface TranscriptResponse {
  available: boolean
  message?: string
  entries: TranscriptEntry[]
}

export interface SearchResult {
  type: string
  scope?: string
  slug: string
  name: string
  url: string
  snippet: string
  source_path?: string
}
export interface SearchResponse {
  query: string
  tasks?: SearchResult[]
  projects?: SearchResult[]
  playbooks?: SearchResult[]
  updates?: SearchResult[]
  transcripts?: SearchResult[]
  memories?: SearchResult[]
}

export interface InboxConversationMessage {
  source: string
  kind: string
  sender_name: string
  timestamp: string
  title: string
  body: string
  permalink?: string
  reaction?: string
}
export interface InboxConversation {
  slug: string
  name: string
  project_slug?: string
  status: string
  provider: string
  live: boolean
  monitored: boolean
  source: string
  channel_name?: string
  messages: InboxConversationMessage[]
}
export interface InboxFeedEntry {
  task_slug: string
  task_name: string
  project_slug?: string
  status: string
  timestamp: string
  sender: string
  body: string
  body_snippet: string
  unread: boolean
  source?: string
  live: boolean
  monitored: boolean
}
export interface InboxFeed {
  entries: InboxFeedEntry[]
  unread_count: number
  task_count: number
  generated_at: string
}

export interface ActionResponse {
  ok: boolean
  message: string
  output?: string
  agent?: UiAgent
  bridge?: boolean
  already_live?: boolean
}

export interface ActionRequest {
  kind: string
  target?: string
  slug?: string
  name?: string
  path?: string
  description?: string
  project?: string
  work_dir?: string
  priority?: string
  prompt?: string
  session_id?: string
  branch?: string
  entity_kind?: string
  provider?: string
  permission_mode?: string
  mkdir?: boolean
}
