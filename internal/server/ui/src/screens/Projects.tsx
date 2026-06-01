import { useState } from 'react'
import { useLocation } from 'wouter'
import { Archive, FolderGit2, Plus, Trash2 } from 'lucide-react'
import { useAction, useProjects } from '../lib/query'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { confirmAction } from '../lib/confirm'
import { CreateProjectModal } from '../components/modals'
import { EmptyState, ErrorNote, Loading } from '../components/ui'

// Chat/Slack-derived task names often carry a trailing URL or permalink
// (e.g. "#channel - https://chat.google.com/room/…"). Strip that noise so
// the project tile reads cleanly; fall back to the raw name if empty.
function cleanTaskTitle(name: string): string {
  const cleaned = name
    .replace(/https?:\/\/\S+/g, '')
    .replace(/\s*[-–—·:|]\s*$/, '')
    .replace(/\s+/g, ' ')
    .trim()
  return cleaned || name
}

export function Projects() {
  useDocumentTitle('Projects')
  const [, navigate] = useLocation()
  const { data, isLoading, error } = useProjects()
  const action = useAction()
  const [createOpen, setCreateOpen] = useState(false)

  const archive = async (e: React.MouseEvent, slug: string, name: string) => {
    e.stopPropagation()
    const ok = await confirmAction({
      title: 'Archive this project?',
      body: `"${name}" and its tasks stay intact, but the project leaves your active list. You can unarchive it later.`,
      confirmLabel: 'Archive',
      danger: true,
    })
    if (ok) action.mutate({ kind: 'archive', target: slug, entity_kind: 'project' })
  }

  const trash = async (e: React.MouseEvent, slug: string, name: string) => {
    e.stopPropagation()
    const ok = await confirmAction({
      title: 'Move this project to trash?',
      body: `"${name}" will be soft-deleted and hidden from your lists. Its tasks and files remain, and you can restore it from Trash later.`,
      confirmLabel: 'Move to trash',
      danger: true,
    })
    if (ok) action.mutate({ kind: 'delete', target: slug, entity_kind: 'project' })
  }

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">workspaces</div>
          <h1 className="h-xl">Projects</h1>
        </div>
        <div className="spacer" />
        <button className="btn" onClick={() => setCreateOpen(true)}>
          <Plus size={15} /> New project
        </button>
      </div>

      {isLoading ? (
        <Loading rows={4} />
      ) : error ? (
        <ErrorNote error={error} />
      ) : !data || data.length === 0 ? (
        <EmptyState icon={<FolderGit2 size={30} />} title="No projects yet" hint="Group related tasks and playbooks under a project." />
      ) : (
        <div className="grid cards stagger">
          {data.map((p) => (
            <article key={p.slug} className="card acard" onClick={() => navigate(`/project/${p.slug}`)}>
              <div className="acard-top">
                <FolderGit2 size={17} className="dim" style={{ marginTop: 1 }} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="acard-title clip">{p.name}</div>
                  <div className="acard-ref clip">{p.work_dir}</div>
                </div>
                <span className={`prio ${p.priority}`}>{p.priority}</span>
                <button
                  className="btn icon ghost sm row-action"
                  title="Archive project"
                  aria-label="Archive project"
                  onClick={(e) => archive(e, p.slug, p.name)}
                >
                  <Archive size={14} />
                </button>
                <button
                  className="btn icon ghost sm row-action"
                  title="Move to trash"
                  aria-label="Move project to trash"
                  onClick={(e) => trash(e, p.slug, p.name)}
                >
                  <Trash2 size={14} />
                </button>
              </div>
              <div className="row gap" style={{ gap: 18, fontSize: 12.5 }}>
                <span className="num"><b>{p.task_counts.in_progress}</b> <span className="faint">active</span></span>
                <span className="num"><b>{p.task_counts.backlog}</b> <span className="faint">queued</span></span>
                <span className="num"><b>{p.task_counts.done}</b> <span className="faint">done</span></span>
              </div>
              {p.recent_tasks?.length > 0 && (
                <div className="acard-recent">
                  {p.recent_tasks.slice(0, 3).map((t) => (
                    <div key={t.slug} className="acard-recent-row" title={t.name}>
                      <span className={`prio ${t.priority}`} />
                      <span className="clip">{cleanTaskTitle(t.name)}</span>
                    </div>
                  ))}
                </div>
              )}
            </article>
          ))}
        </div>
      )}
      <CreateProjectModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  )
}
