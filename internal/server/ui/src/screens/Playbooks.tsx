import { useEffect, useState } from 'react'
import { useLocation } from 'wouter'
import { Archive, ChevronDown, Play, Repeat, Trash2 } from 'lucide-react'
import { usePlaybooks, useAction, useUiData } from '../lib/query'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { confirmAction } from '../lib/confirm'
import { AgentPicker, PermissionPicker } from '../components/pickers'
import { EmptyState, ErrorNote, Loading, Sparkline } from '../components/ui'
import { ago } from '../lib/format'
import type { ToolCapability } from '../lib/types'

export function Playbooks() {
  useDocumentTitle('Playbooks')
  const [, navigate] = useLocation()
  const { data, isLoading, error } = usePlaybooks()
  const { data: ui } = useUiData()
  const action = useAction()
  const providers = ui?.CAPABILITIES?.providers ?? []

  // Close any open run-options popover when clicking outside it (same idiom as
  // the SessionDetail more-actions menu — native <details> won't self-close).
  useEffect(() => {
    const onDown = (e: globalThis.MouseEvent) => {
      document.querySelectorAll('details.menu[open]').forEach((d) => {
        if (!d.contains(e.target as Node)) d.removeAttribute('open')
      })
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [])

  const runWith = (slug: string, opts: { provider?: string; permission_mode?: string }) => {
    action.mutate(
      { kind: 'spawn-run', target: slug, ...opts },
      { onSuccess: (d) => d.agent && navigate(`/session/${d.agent.slug}`) },
    )
  }

  const trash = async (e: React.MouseEvent, slug: string, name: string) => {
    e.stopPropagation()
    const ok = await confirmAction({
      title: 'Move this playbook to trash?',
      body: `"${name}" will be soft-deleted and hidden from your lists. Past runs are unaffected and you can restore it from Trash later.`,
      confirmLabel: 'Move to trash',
      danger: true,
    })
    if (ok) action.mutate({ kind: 'delete', target: slug, entity_kind: 'playbook' })
  }

  const archive = async (e: React.MouseEvent, slug: string, name: string) => {
    e.stopPropagation()
    const ok = await confirmAction({
      title: 'Archive this playbook?',
      body: `"${name}" will leave your active list. Past runs are unaffected and you can unarchive it later.`,
      confirmLabel: 'Archive',
      danger: true,
    })
    if (ok) action.mutate({ kind: 'archive', target: slug, entity_kind: 'playbook' })
  }

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">repeatable workflows</div>
          <h1 className="h-xl">Playbooks</h1>
        </div>
      </div>

      {isLoading ? (
        <Loading rows={4} />
      ) : error ? (
        <ErrorNote error={error} />
      ) : !data || data.length === 0 ? (
        <EmptyState icon={<Repeat size={30} />} title="No playbooks" hint="Playbooks are reusable task templates an agent runs on demand." />
      ) : (
        <div className="grid cards stagger">
          {data.map((p) => (
            <article key={p.slug} className="card acard" onClick={() => navigate(`/playbook/${p.slug}`)}>
              <div className="acard-top">
                <Repeat size={16} className="dim" style={{ marginTop: 2 }} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="acard-title clip">{p.name}</div>
                  <div className="acard-ref clip">{p.project_slug || 'no project'}</div>
                </div>
                <PlaybookRunControl
                  providers={providers}
                  pending={action.isPending}
                  onRun={(opts) => runWith(p.slug, opts)}
                />
                <button
                  className="btn icon ghost sm row-action"
                  title="Archive playbook"
                  aria-label="Archive playbook"
                  onClick={(e) => archive(e, p.slug, p.name)}
                >
                  <Archive size={14} />
                </button>
                <button
                  className="btn icon ghost sm row-action"
                  title="Move to trash"
                  aria-label="Move playbook to trash"
                  onClick={(e) => trash(e, p.slug, p.name)}
                >
                  <Trash2 size={14} />
                </button>
              </div>
              <div className="acard-foot" style={{ borderTop: 'none', paddingTop: 0 }}>
                <span className="num" style={{ fontSize: 12.5 }}>
                  <b>{p.run_count_7d}</b> <span className="faint">runs · 7d</span>
                </span>
                <div className="spacer" />
                <Sparkline data={p.run_days_30?.slice(-14) ?? []} />
              </div>
              {p.recent_runs?.[0] && (
                <div className="faint" style={{ fontSize: 11.5 }}>last run {ago(p.recent_runs[0].created_at)}</div>
              )}
            </article>
          ))}
        </div>
      )}
    </div>
  )
}

// Split "Run" button: a plain click spawns a run with the stored defaults
// (claude / default), the caret opens a popover to pick agent + permission mode
// before launching — surfacing spawn-run's Provider/PermissionMode inputs the
// quick button hard-defaulted. stopPropagation keeps clicks off the card's
// navigate-to-detail handler.
function PlaybookRunControl({
  providers,
  pending,
  onRun,
}: {
  providers: ToolCapability[]
  pending: boolean
  onRun: (opts: { provider?: string; permission_mode?: string }) => void
}) {
  const [provider, setProvider] = useState('claude')
  const [perm, setPerm] = useState('default')
  return (
    <div className="split-run" onClick={(e) => e.stopPropagation()}>
      <button className="btn primary sm split-main" onClick={() => onRun({})} disabled={pending}>
        <Play size={13} /> Run
      </button>
      <details className="menu">
        <summary className="btn primary sm split-caret" title="Run options">
          <ChevronDown size={13} />
        </summary>
        <div className="menu-pop right run-opts">
          <div className="run-opts-row">
            <span className="eyebrow">Agent</span>
            <AgentPicker value={provider} onChange={setProvider} providers={providers} />
          </div>
          <div className="run-opts-row">
            <span className="eyebrow">Permissions</span>
            <PermissionPicker value={perm} onChange={setPerm} />
          </div>
          <button
            className="btn primary sm"
            disabled={pending}
            onClick={(e) => {
              ;(e.currentTarget as HTMLElement).closest('details')?.removeAttribute('open')
              onRun({ provider, permission_mode: perm })
            }}
          >
            <Play size={13} /> Run with options
          </button>
        </div>
      </details>
    </div>
  )
}
