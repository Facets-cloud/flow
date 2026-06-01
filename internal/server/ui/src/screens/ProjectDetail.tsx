import { useState } from 'react'
import { useLocation } from 'wouter'
import { ArrowLeft, FileText } from 'lucide-react'
import { useMarkdown, useProject, useProjectTasks } from '../lib/query'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { BriefPanel } from '../components/BriefPanel'
import { Md } from '../components/Markdown'
import { ErrorNote, Loading, ProviderIcon, StatusDot } from '../components/ui'
import { ago, dateTime } from '../lib/format'
import type { FileRef } from '../lib/types'

export function ProjectDetail({ slug }: { slug: string }) {
  const [, navigate] = useLocation()
  const { data: project, isLoading, error } = useProject(slug)
  const { data: tasks } = useProjectTasks(slug)
  useDocumentTitle(project?.name)

  if (isLoading) return <div className="page"><Loading /></div>
  if (error) return <div className="page"><ErrorNote error={error} /></div>
  if (!project) return null

  return (
    <div className="page">
      <button className="btn ghost sm" style={{ marginBottom: 14 }} onClick={() => navigate('/projects')}>
        <ArrowLeft size={14} /> Projects
      </button>

      <div className="detail-head">
        <div style={{ flex: 1 }}>
          <div className="eyebrow">project</div>
          <div className="detail-title">{project.name}</div>
          <div className="detail-ref">{project.slug}</div>
        </div>
        <span className={`prio ${project.priority}`}>{project.priority}</span>
      </div>

      <div className="meta-grid" style={{ marginBottom: 24 }}>
        <div className="meta-cell"><div className="meta-k">working dir</div><div className="meta-v mono clip" title={project.work_dir}>{project.work_dir}</div></div>
        <div className="meta-cell"><div className="meta-k">active</div><div className="meta-v num">{project.task_counts.in_progress}</div></div>
        <div className="meta-cell"><div className="meta-k">backlog</div><div className="meta-v num">{project.task_counts.backlog}</div></div>
        <div className="meta-cell"><div className="meta-k">done</div><div className="meta-v num">{project.task_counts.done}</div></div>
      </div>

      <div className="grid two">
        <section className="section">
          <div className="section-head"><span className="eyebrow">Brief</span></div>
          <div className="card" style={{ padding: 18 }}>
            <BriefPanel
              getPath={`/api/projects/${encodeURIComponent(slug)}/brief`}
              putPath={`/api/projects/${encodeURIComponent(slug)}/brief`}
              empty="No project brief yet. Click Edit to add one — agents read this on startup."
            />
          </div>
        </section>

        <section className="section">
          <div className="section-head">
            <span className="eyebrow">Tasks</span>
            <span className="section-count">{tasks?.length ?? 0}</span>
          </div>
          <div className="rows">
            {(tasks ?? []).map((t) => (
              <div key={t.slug} className="lrow" onClick={() => navigate(`/session/${t.slug}`)}>
                <StatusDot status={t.live ? 'running' : t.status} />
                <ProviderIcon provider={t.session_provider} size={14} />
                <div className="lrow-main">
                  <div className="lrow-title clip">{t.name}</div>
                  <div className="lrow-sub clip">{t.status} · {ago(t.updated_at)}</div>
                </div>
                <span className={`prio ${t.priority}`} />
              </div>
            ))}
            {(!tasks || tasks.length === 0) && <div className="lrow"><span className="faint">No tasks in this project yet.</span></div>}
          </div>
        </section>
      </div>

      <section className="section" style={{ marginTop: 24 }}>
        <div className="section-head">
          <span className="eyebrow">Updates</span>
          <span className="section-count">{project.updates?.length ?? 0}</span>
        </div>
        <ProjectUpdates slug={slug} updates={project.updates ?? []} />
        {project.aux_files?.length > 0 && (
          <div className="row gap wrap" style={{ gap: 8, marginTop: 12 }}>
            <span className="eyebrow" style={{ marginRight: 4 }}>files</span>
            {project.aux_files.map((f) => (
              <span key={f.filename} className="tag" title={`${f.path} · ${f.size} bytes`}>
                <FileText size={11} /> {f.filename}
              </span>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

// Collapsible project update log — same shape as the SessionDetail UpdatesTab
// (the brief flags [[entity-detail-shared-component]] as the eventual shared
// primitive; until then this mirrors that pattern). The newest update opens by
// default; markdown is fetched per-file on expand.
function ProjectUpdates({ slug, updates }: { slug: string; updates: FileRef[] }) {
  const [openFile, setOpenFile] = useState<string | null>(updates[0]?.filename ?? null)
  if (updates.length === 0) {
    return <div className="rows"><div className="lrow"><span className="faint">No updates logged for this project.</span></div></div>
  }
  return (
    <div className="col" style={{ gap: 8 }}>
      {updates.map((u) => (
        <div key={u.filename} className="card" style={{ overflow: 'hidden' }}>
          <button
            className="row gap"
            style={{ width: '100%', padding: '9px 12px', justifyContent: 'flex-start' }}
            onClick={() => setOpenFile(openFile === u.filename ? null : u.filename)}
          >
            <span className="mono clip" style={{ flex: 1, fontSize: 12, textAlign: 'left' }}>{u.filename}</span>
            <span className="faint" style={{ fontSize: 11 }}>{dateTime(u.mtime)}</span>
          </button>
          {openFile === u.filename && (
            <div style={{ padding: '4px 12px 12px', borderTop: '1px solid var(--border-faint)' }}>
              <ProjectUpdateBody slug={slug} filename={u.filename} />
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

function ProjectUpdateBody({ slug, filename }: { slug: string; filename: string }) {
  const { data, isLoading } = useMarkdown(
    `/api/projects/${encodeURIComponent(slug)}/updates/${encodeURIComponent(filename)}`,
  )
  if (isLoading) return <Loading label="update" />
  return <Md source={data || ''} />
}
