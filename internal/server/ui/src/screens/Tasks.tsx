import { useState } from 'react'
import { useLocation } from 'wouter'
import { Archive, CornerLeftUp, GitFork, Loader2, Pencil, Trash2 } from 'lucide-react'
import { useAction, useTasks, type TaskFilters } from '../lib/query'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { confirmAction } from '../lib/confirm'
import { EmptyState, ErrorNote, Loading, ProviderIcon, StatusDot } from '../components/ui'
import { ago, dueTone } from '../lib/format'
import type { TaskView } from '../lib/types'

const STATUSES = [
  { v: '', label: 'All open' },
  { v: 'backlog', label: 'Backlog' },
  { v: 'in-progress', label: 'In progress' },
  { v: 'done', label: 'Done' },
]
const PRIOS = [
  { v: '', label: 'Any' },
  { v: 'high', label: 'High' },
  { v: 'medium', label: 'Medium' },
  { v: 'low', label: 'Low' },
]
const SORTS = [
  { v: 'recent', label: 'Recent' },
  { v: 'due', label: 'Due' },
] as const
type SortKey = (typeof SORTS)[number]['v']

export function Tasks() {
  useDocumentTitle('Tasks')
  const [, navigate] = useLocation()
  const [status, setStatus] = useState('')
  const [priority, setPriority] = useState('')
  const [sort, setSort] = useState<SortKey>('recent')

  const filters: TaskFilters = {
    status: status || undefined,
    priority: priority || undefined,
    include_done: status === 'done',
  }
  const { data, isLoading, error } = useTasks(filters)
  const tasks = (data ?? [])
    .filter((t) => t.kind !== 'playbook_run' || status === 'done')
    .slice()
    .sort((a, b) => {
      // Due sort: dated tasks first by soonest due (overdue = earliest date,
      // sorts first), undated last; recent sort: newest-created first.
      if (sort === 'due') {
        if (a.due_date && b.due_date) return a.due_date < b.due_date ? -1 : a.due_date > b.due_date ? 1 : 0
        if (a.due_date) return -1
        if (b.due_date) return 1
      }
      return Date.parse(b.created_at) - Date.parse(a.created_at)
    })

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">work queue</div>
          <h1 className="h-xl">Tasks</h1>
        </div>
      </div>

      <div className="row gap wrap" style={{ marginBottom: 18, gap: 16 }}>
        <div className="segmented">
          {STATUSES.map((s) => (
            <button key={s.v} className={status === s.v ? 'active' : ''} onClick={() => setStatus(s.v)}>
              {s.label}
            </button>
          ))}
        </div>
        <div className="segmented">
          {PRIOS.map((p) => (
            <button key={p.v} className={priority === p.v ? 'active' : ''} onClick={() => setPriority(p.v)}>
              {p.label}
            </button>
          ))}
        </div>
        <div className="segmented">
          {SORTS.map((s) => (
            <button key={s.v} className={sort === s.v ? 'active' : ''} onClick={() => setSort(s.v)}>
              {s.label}
            </button>
          ))}
        </div>
      </div>

      {isLoading ? (
        <Loading rows={6} />
      ) : error ? (
        <ErrorNote error={error} />
      ) : tasks.length === 0 ? (
        <EmptyState title="No tasks match" hint="Adjust the filters or create a new task." />
      ) : (
        <div className="card" style={{ padding: '6px 14px 4px' }}>
          <table className="tbl fixed">
            <colgroup>
              <col style={{ width: 28 }} />
              <col />
              <col style={{ width: 152 }} />
              <col style={{ width: 100 }} />
              <col style={{ width: 64 }} />
              <col style={{ width: 108 }} />
              <col style={{ width: 38 }} />
              <col style={{ width: 104 }} />
              <col style={{ width: 60 }} />
              <col style={{ width: 60 }} />
            </colgroup>
            <thead>
              <tr>
                <th />
                <th>Task</th>
                <th>Dependencies</th>
                <th>Project</th>
                <th>Priority</th>
                <th>Due</th>
                <th>Agent</th>
                <th>Tags</th>
                <th style={{ textAlign: 'right' }}>Updated</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {tasks.map((t) => (
                <TaskRow key={t.slug} task={t} onOpen={() => navigate(`/session/${t.slug}`)} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function TaskRow({ task, onOpen }: { task: TaskView; onOpen: () => void }) {
  const action = useAction()
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState(task.name)

  const cancel = () => {
    setName(task.name)
    setEditing(false)
  }
  const save = () => {
    const trimmed = name.trim()
    if (!trimmed || trimmed === task.name) {
      cancel()
      return
    }
    action.mutate(
      { kind: 'update-task-name', slug: task.slug, name: trimmed },
      { onSuccess: () => setEditing(false), onError: cancel },
    )
  }

  const archive = async (e: React.MouseEvent) => {
    e.stopPropagation()
    const ok = await confirmAction({
      title: 'Archive this task?',
      body: `"${task.name}" will be moved out of your active queue. You can unarchive it later.`,
      confirmLabel: 'Archive',
      danger: true,
    })
    if (ok) action.mutate({ kind: 'archive', target: task.slug })
  }

  const trash = async (e: React.MouseEvent) => {
    e.stopPropagation()
    const ok = await confirmAction({
      title: 'Move this task to trash?',
      body: `"${task.name}" will be soft-deleted and hidden from your lists. You can restore it from Trash later.`,
      confirmLabel: 'Move to trash',
      danger: true,
    })
    if (ok) action.mutate({ kind: 'delete', target: task.slug, entity_kind: 'task' })
  }

  const childCount = task.children?.length ?? 0
  const parentName = task.parent?.name || task.parent_slug

  return (
    <tr onClick={() => !editing && onOpen()}>
      <td>
        <StatusDot status={task.live ? 'running' : task.waiting_on ? 'waiting' : task.status} />
      </td>
      <td>
        {editing ? (
          <input
            className="input inline-rename"
            autoFocus
            value={name}
            onClick={(e) => e.stopPropagation()}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                save()
              } else if (e.key === 'Escape') {
                e.preventDefault()
                cancel()
              }
            }}
            onBlur={save}
          />
        ) : (
          <div className="cell-name">
            <span className="clip" style={{ fontWeight: 500 }}>{task.name}</span>
            <button
              className="btn icon ghost sm rename-btn"
              title="Rename task"
              aria-label="Rename task"
              onClick={(e) => {
                e.stopPropagation()
                setName(task.name)
                setEditing(true)
              }}
            >
              {action.isPending ? <Loader2 size={12} className="spin" /> : <Pencil size={12} />}
            </button>
          </div>
        )}
        <div className="mono faint clip" style={{ fontSize: 11 }}>
          {task.slug}{task.assignee ? ` · @${task.assignee}` : ''}
        </div>
      </td>
      <td>
        {parentName || childCount > 0 ? (
          <div className="cell-deps">
            {parentName && (
              <span className="dep-chip depends" title={`Depends on ${parentName}`}>
                <CornerLeftUp size={11} /> <span className="clip">{parentName}</span>
              </span>
            )}
            {childCount > 0 && (
              <span className="dep-chip blocks" title={`Blocks ${childCount} task${childCount === 1 ? '' : 's'}`}>
                <GitFork size={11} /> blocks {childCount}
              </span>
            )}
          </div>
        ) : (
          <span className="faint">—</span>
        )}
      </td>
      <td className="dim clip">{task.project_slug || <span className="faint">—</span>}</td>
      <td><span className={`prio ${task.priority}`}>{task.priority}</span></td>
      <td>
        {task.due_info ? (
          <span
            className={`badge ${dueTone(task.due_date, task.due_info)}`}
            style={{ whiteSpace: 'nowrap', height: 'auto', padding: '2px 7px', fontSize: 11 }}
            title={task.due_date ? `Due ${task.due_date}` : undefined}
          >
            {task.due_info}
          </span>
        ) : (
          <span className="faint">—</span>
        )}
      </td>
      <td><ProviderIcon provider={task.session_provider} size={14} /></td>
      <td>
        <div className="cell-tags">
          {(task.tags ?? []).slice(0, 3).map((tag) => <span key={tag} className="tag">{tag}</span>)}
        </div>
      </td>
      <td className="dim mono" style={{ textAlign: 'right', fontSize: 11.5 }}>{ago(task.updated_at)}</td>
      <td style={{ textAlign: 'right' }}>
        <button
          className="btn icon ghost sm row-action"
          title="Archive task"
          aria-label="Archive task"
          disabled={action.isPending}
          onClick={archive}
        >
          <Archive size={13} />
        </button>
        <button
          className="btn icon ghost sm row-action"
          title="Move to trash"
          aria-label="Move to trash"
          disabled={action.isPending}
          onClick={trash}
        >
          <Trash2 size={13} />
        </button>
      </td>
    </tr>
  )
}
