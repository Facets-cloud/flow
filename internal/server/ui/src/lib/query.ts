import { QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiAction, apiGet, apiGetText } from './api'
import { rpc } from './rpc'
import { events } from './events'
import { pushToast } from './toast'
import type {
  ActionRequest,
  HealthView,
  InboxConversation,
  InboxFeed,
  KBFileView,
  OverviewView,
  PlaybookView,
  ProjectView,
  QuoteView,
  SearchResponse,
  TaskView,
  TranscriptResponse,
  UiAgent,
  UiData,
  WorkdirView,
} from './types'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 4000,
      gcTime: 5 * 60 * 1000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
})

// ----- live invalidation: events + reconnect drive refetches -------------
// The anime quote is deliberately excluded: it must only refetch when the
// greeting bucket changes (its query key), never on every activity event —
// that keeps us off the animechan rate limit.
const notQuote = { predicate: (q: { queryKey: readonly unknown[] }) => q.queryKey[0] !== 'quote' }
let invalidateTimer: ReturnType<typeof setTimeout> | null = null
function scheduleInvalidate() {
  if (invalidateTimer) return
  invalidateTimer = setTimeout(() => {
    invalidateTimer = null
    queryClient.invalidateQueries(notQuote)
  }, 500)
}
events.on(() => scheduleInvalidate())
rpc.onReady(() => queryClient.invalidateQueries(notQuote))

// ----- query string helper ----------------------------------------------
function qs(params: Record<string, string | boolean | number | undefined>): string {
  const parts: string[] = []
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '' || v === false) continue
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
  }
  return parts.length ? `?${parts.join('&')}` : ''
}

// ----- queries ------------------------------------------------------------
export function useUiData() {
  // refetchInterval keeps live figures (token tallies, activity, statuses) moving
  // in near-real-time even between /ws/events pushes — token usage grows mid-turn
  // without emitting an event, so event-only invalidation would look frozen. The
  // server recomputes cheaply (transcript usage is mtime-cached); react-query
  // pauses this while the tab is hidden, so it's not wasteful.
  return useQuery({
    queryKey: ['ui-data'],
    queryFn: () => apiGet<UiData>('/api/ui-data'),
    refetchInterval: 5000,
  })
}
export function useHealth() {
  return useQuery({ queryKey: ['health'], queryFn: () => apiGet<HealthView>('/api/health') })
}
export function useOverview() {
  return useQuery({ queryKey: ['overview'], queryFn: () => apiGet<OverviewView>('/api/overview') })
}

export interface TaskFilters {
  status?: string
  project?: string
  priority?: string
  tag?: string
  kind?: string
  include_done?: boolean
  include_archived?: boolean
  since?: string
}
export function useTasks(filters: TaskFilters = {}) {
  return useQuery({
    queryKey: ['tasks', filters],
    queryFn: () =>
      apiGet<TaskView[]>(
        `/api/tasks${qs(filters as Record<string, string | boolean | number | undefined>)}`,
      ),
  })
}

export function useTask(slug: string | undefined) {
  return useQuery({
    queryKey: ['task', slug],
    enabled: !!slug,
    queryFn: () => apiGet<TaskView>(`/api/tasks/${encodeURIComponent(slug!)}`),
  })
}
export function useTaskBridge(slug: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['task-bridge', slug],
    enabled: !!slug && enabled,
    queryFn: () => apiGet<UiAgent>(`/api/tasks/${encodeURIComponent(slug!)}/bridge`),
  })
}
export function useTaskTranscript(slug: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['task-transcript', slug],
    enabled: !!slug && enabled,
    queryFn: () => apiGet<TranscriptResponse>(`/api/tasks/${encodeURIComponent(slug!)}/transcript`),
  })
}

export function useProjects() {
  return useQuery({ queryKey: ['projects'], queryFn: () => apiGet<ProjectView[]>('/api/projects') })
}
export function useProject(slug: string | undefined) {
  return useQuery({
    queryKey: ['project', slug],
    enabled: !!slug,
    queryFn: () => apiGet<ProjectView>(`/api/projects/${encodeURIComponent(slug!)}`),
  })
}
export function useProjectTasks(slug: string | undefined) {
  return useQuery({
    queryKey: ['project-tasks', slug],
    enabled: !!slug,
    queryFn: () =>
      apiGet<TaskView[]>(`/api/projects/${encodeURIComponent(slug!)}/tasks?include_done=1`),
  })
}

export function usePlaybooks() {
  return useQuery({ queryKey: ['playbooks'], queryFn: () => apiGet<PlaybookView[]>('/api/playbooks') })
}
export function usePlaybook(slug: string | undefined) {
  return useQuery({
    queryKey: ['playbook', slug],
    enabled: !!slug,
    queryFn: () => apiGet<PlaybookView>(`/api/playbooks/${encodeURIComponent(slug!)}`),
  })
}

export function useKB() {
  return useQuery({ queryKey: ['kb'], queryFn: () => apiGet<KBFileView[]>('/api/kb') })
}
export function useWorkdirs() {
  return useQuery({ queryKey: ['workdirs'], queryFn: () => apiGet<WorkdirView[]>('/api/workdirs') })
}
export function useInbox() {
  return useQuery({ queryKey: ['inbox'], queryFn: () => apiGet<InboxFeed>('/api/inbox') })
}
// Keyed by hour bucket ("YYYY-MM-DD-HH"): a new quote is fetched only when the
// hour flips. staleTime Infinity means it's never refetched within the hour no
// matter how many times the page reloads. The server caches per bucket too, so
// animechan is hit at most once per hour across all clients.
export function useQuote(bucket: string) {
  return useQuery({
    queryKey: ['quote', bucket],
    staleTime: Infinity,
    gcTime: Infinity,
    retry: 0,
    queryFn: () => apiGet<QuoteView>(`/api/quote?bucket=${encodeURIComponent(bucket)}`),
  })
}
export function useInboxConversation(slug: string | undefined) {
  return useQuery({
    queryKey: ['inbox-convo', slug],
    enabled: !!slug,
    queryFn: () =>
      apiGet<InboxConversation>(`/api/inbox/conversation?slug=${encodeURIComponent(slug!)}`),
  })
}

/** Generic markdown fetch (briefs, updates, kb files). */
export function useMarkdown(path: string | undefined, staleMs = 15000) {
  return useQuery({
    queryKey: ['md', path],
    enabled: !!path,
    staleTime: staleMs,
    queryFn: () => apiGetText(path!),
  })
}

export function useSearch(query: string, scope = 'all') {
  const q = query.trim()
  // Transcripts are huge (whole session JSONL); FTS over them costs seconds for
  // common terms (e.g. "facets" 6.7s vs 0.3s without). They're searched ONLY
  // when the Transcripts chip is active — matching the backend's opt-in design —
  // so the default ⌘K search stays instant. Other chips (tasks/projects/etc.)
  // are filtered client-side from the briefs+updates+memories result.
  const inScopes = scope === 'transcripts' ? 'transcripts' : 'briefs,updates,memories'
  return useQuery({
    queryKey: ['search', q, inScopes],
    enabled: q.length >= 2,
    staleTime: 2000,
    // A cold-scope build can transiently 500 under rapid typing (DB write
    // contention). Retry quickly so a blip recovers at fetch time instead of
    // caching an error that sticks as a permanent "No matches" for that term.
    retry: 3,
    retryDelay: 250,
    queryFn: () =>
      apiGet<SearchResponse>(
        // `in` takes document scopes (briefs cover task/project/playbook briefs);
        // entity-type names like "tasks" are invalid and 400.
        `/api/search?q=${encodeURIComponent(q)}&in=${inScopes}&limit=8`,
      ),
  })
}

// ----- action mutation ----------------------------------------------------
export function useAction() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (req: ActionRequest) => apiAction(req),
    onSuccess: (data) => {
      if (data.message) pushToast('ok', data.message)
      qc.invalidateQueries()
    },
    onError: (err: Error) => {
      pushToast('error', err.message || 'action failed')
    },
  })
}
