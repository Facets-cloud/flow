import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import { useLocation } from 'wouter'
import {
  Archive,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  Check,
  CheckCircle2,
  ChevronDown,
  Circle,
  Coins,
  GitBranch,
  GitFork,
  Loader2,
  Maximize2,
  Minimize2,
  MoreHorizontal,
  PanelRightClose,
  PanelRightOpen,
  Pause,
  Pencil,
  Play,
  Radar,
  RotateCcw,
  Sparkles,
  TerminalSquare,
  X as XIcon,
} from 'lucide-react'
import {
  queryClient,
  useMarkdown,
  useTask,
  useTaskBridge,
  useTaskTranscript,
  useUiData,
} from '../lib/query'
import { apiAction } from '../lib/api'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { pushToast } from '../lib/toast'
import { confirmAction } from '../lib/confirm'
import type { DiffFile, TranscriptEntry, UiAgent } from '../lib/types'
import { TaskTerminal } from '../components/Terminal'
import { Md } from '../components/Markdown'
import { Modal } from '../components/Modal'
import { AgentPicker, PermissionPicker } from '../components/pickers'
import { TerminalIcon } from '../components/TerminalIcon'
import { ErrorNote, Loading, ProviderIcon, StatusBadge, TokenBar } from '../components/ui'
import { compact, dateTime, fromMinutes, fromSeconds } from '../lib/format'

type Tab = 'brief' | 'diff' | 'transcript' | 'updates'

export function SessionDetail({ slug }: { slug: string }) {
  const [, navigate] = useLocation()
  const { data: task, isLoading, error } = useTask(slug)
  const { data: agent } = useTaskBridge(slug)
  useDocumentTitle(task?.name)
  const [open, setOpen] = useState(false)
  const [restartKey, setRestartKey] = useState(0)
  const [termStatus, setTermStatus] = useState('')
  const [termConn, setTermConn] = useState<'open' | 'closed'>('closed')
  const [tab, setTab] = useState<Tab>('brief')
  const [busy, setBusy] = useState<string | null>(null)
  const [side, setSide] = useState(false) // side panel collapsed by default — terminal maximized
  const [full, setFull] = useState(false) // terminal fullscreen
  const [diffModal, setDiffModal] = useState(false)
  const [transcriptModal, setTranscriptModal] = useState(false)
  const [updatesModal, setUpdatesModal] = useState(false)
  const [reopened, setReopened] = useState(false) // user revisited a done task → allow the live terminal to mount
  const [doneRunning, setDoneRunning] = useState(false) // flow-done close-out in progress → show inline step panel
  const [editingName, setEditingName] = useState(false)
  const [nameDraft, setNameDraft] = useState('')
  const renameCommitted = useRef(false)

  const { data: ui } = useUiData()
  const done = task?.status === 'done'
  // Once a done task has been "revisited", let the live terminal mount even
  // though task.status is still done — the backend resumes its prior session.
  const canTerminal = open && (!done || reopened)
  const liveMode = agent?.terminal?.mode
  useEffect(() => {
    if (!done && (liveMode === 'browser' || liveMode === 'shared' || agent?.status === 'running')) {
      setOpen(true)
    }
  }, [liveMode, agent?.status, done])

  // Esc exits fullscreen terminal.
  useEffect(() => {
    if (!full) return
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setFull(false)
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [full])

  // Close any open ⋯/split menu when clicking outside it.
  useEffect(() => {
    const onDown = (e: globalThis.MouseEvent) => {
      document.querySelectorAll('details.menu[open]').forEach((d) => {
        if (!d.contains(e.target as Node)) d.removeAttribute('open')
      })
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [])

  const run = async (
    kind: string,
    extra: Record<string, unknown> = {},
    opts: { reconnect?: boolean; close?: boolean; goto?: 'home' } = {},
  ) => {
    setBusy(kind)
    try {
      const resp = await apiAction({ kind, target: slug, ...extra })
      pushToast('ok', resp.message || `${kind} ok`)
      queryClient.invalidateQueries()
      if (opts.close) setOpen(false)
      if (opts.reconnect || resp.bridge) {
        setOpen(true)
        setRestartKey((k) => k + 1)
      }
      if (opts.goto === 'home') navigate('/sessions')
      if (resp.agent && kind === 'fork') navigate(`/session/${resp.agent.slug}`)
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : `${kind} failed`)
    } finally {
      setBusy(null)
    }
  }

  // Revisit a done task: reopen the prior session in the browser terminal. The
  // backend resumes the existing session id, so the agent reloads its full
  // context/transcript and we pick up from where we left off.
  const revisit = () => {
    setReopened(true)
    setOpen(true)
    run('resume', {}, { reconnect: true })
  }

  const closeMenu = (e: MouseEvent) =>
    (e.currentTarget as HTMLElement).closest('details')?.removeAttribute('open')

  const beginRename = () => {
    setNameDraft(task?.name ?? '')
    renameCommitted.current = false
    setEditingName(true)
  }
  const saveName = () => {
    if (renameCommitted.current) return
    renameCommitted.current = true
    setEditingName(false)
    const trimmed = nameDraft.trim()
    if (trimmed && trimmed !== task?.name) run('update-task-name', { name: trimmed })
  }
  const cancelRename = () => {
    renameCommitted.current = true // suppress the blur-triggered save
    setEditingName(false)
  }

  if (isLoading) return <div className="page"><Loading label="loading session" /></div>
  if (error) return <div className="page"><ErrorNote error={error} /></div>
  if (!task) return null

  const provider = agent?.provider || task.session_provider || 'claude'
  const status = agent?.status || task.status
  const monitored = !!agent?.monitored

  // The agent is locked once a session exists (running, idle, or done all carry
  // a session) — the backend rejects the change. Only a never-started backlog
  // task can still switch claude ↔ codex via the inline toolbar picker. The WS
  // terminal reads tasks.session_provider at launch, so Start just uses whatever
  // the picker last persisted.
  const providers = ui?.CAPABILITIES?.providers ?? []
  const canChooseAgent = task.status === 'backlog' && !task.session_id && !task.session_started

  return (
    <div className="page flush">
      <div className={`session${side ? '' : ' no-side'}`}>
        {/* -------- main column: header + terminal -------- */}
        <div className="session-main">
          <div className="detail-head" style={{ marginBottom: 4 }}>
            <ProviderIcon provider={provider} size={22} />
            <div style={{ flex: 1, minWidth: 0 }}>
              {editingName ? (
                <input
                  className="input inline-rename"
                  style={{ fontSize: 19, fontWeight: 600, height: 34 }}
                  autoFocus
                  value={nameDraft}
                  onChange={(e) => setNameDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      saveName()
                    } else if (e.key === 'Escape') {
                      e.preventDefault()
                      cancelRename()
                    }
                  }}
                  onBlur={saveName}
                />
              ) : (
                <div className="cell-name">
                  <span className="detail-title clip">{task.name}</span>
                  <button
                    className="btn icon ghost sm"
                    title="Rename task"
                    aria-label="Rename task"
                    onClick={beginRename}
                  >
                    <Pencil size={14} />
                  </button>
                </div>
              )}
              <div className="detail-ref">
                {task.slug}
                {task.project_slug ? ` · ${task.project_slug}` : ''}
              </div>
              {(agent?.branch || task.worktree_path) && (
                <div className="detail-branch" title="Current git branch">
                  <GitBranch size={12} />
                  <span className="mono clip">{agent?.branch || '—'}</span>
                </div>
              )}
            </div>
            {agent && agent.tokens_session > 0 && (
              <span
                className="tag tok-pill"
                title={`${agent.tokens_session.toLocaleString()} tokens used this session · context ${agent.tokens_used.toLocaleString()} / ${agent.tokens_max.toLocaleString()}`}
              >
                <Coins size={12} /> {compact(agent.tokens_session)} tok
              </span>
            )}
            {monitored && (
              <span className="badge mon" title="A background monitor is watching this task's inbox">
                <Radar size={12} /> monitored
              </span>
            )}
            <StatusBadge status={status} />
          </div>

          <div className="toolbar" style={{ marginBottom: 4 }}>
            {!done && !open && (
              <button className="btn primary sm" disabled={busy === 'open'} onClick={() => setOpen(true)}>
                <Play size={14} /> {task.session_id ? 'Resume session' : 'Start session'}
              </button>
            )}
            <div className="spacer" />
            {canChooseAgent && (
              <AgentPicker
                value={task.session_provider || 'claude'}
                onChange={(v) => run('update-provider', { provider: v })}
                providers={providers}
              />
            )}
            <PermissionPicker
              value={task.permission_mode || 'default'}
              onChange={(v) => run('update-permission-mode', { permission_mode: v })}
            />
            <Seg
              value={task.priority}
              onChange={(v) => run('update-priority', { priority: v })}
              options={[
                { v: 'high', icon: <ArrowUp size={15} color="var(--danger)" />, title: 'Priority: high' },
                { v: 'medium', icon: <ArrowRight size={15} color="var(--warn)" />, title: 'Priority: medium' },
                { v: 'low', icon: <ArrowDown size={15} color="var(--info)" />, title: 'Priority: low' },
              ]}
            />
            {ui?.CAPABILITIES?.terminals?.length ? (
              <details className="menu">
                <summary className="btn ghost sm" title="Open this session in a native terminal">
                  <TerminalSquare size={14} /> Open in terminal <ChevronDown size={13} />
                </summary>
                <div className="menu-pop right">
                  {ui.CAPABILITIES.terminals.map((t) => (
                    <button
                      key={t.id}
                      className="menu-item"
                      disabled={!t.available}
                      title={t.available ? `Open in ${t.label}` : t.reason || `${t.label} not available on this system`}
                      onClick={(e) => {
                        closeMenu(e)
                        if (t.available) run(t.id)
                      }}
                    >
                      <TerminalIcon id={t.id} size={15} /> {t.label}
                      {!t.available && <span className="menu-item-note">unavailable</span>}
                    </button>
                  ))}
                </div>
              </details>
            ) : null}
            <details className="menu">
              <summary className="btn icon ghost sm" title="More actions">
                <MoreHorizontal size={16} />
              </summary>
              <div className="menu-pop right">
                {!done && (
                  <button
                    className="menu-item"
                    onClick={async (e) => {
                      closeMenu(e)
                      const ok = await confirmAction({
                        title: 'Mark this task done?',
                        body: `"${task.name}" will be closed and a close-out sweep will distill KB & project updates from its transcript.`,
                        confirmLabel: 'Mark done',
                      })
                      if (ok) setDoneRunning(true)
                    }}
                  >
                    <CheckCircle2 size={14} /> Mark done
                  </button>
                )}
                {!done && (
                  <button className="menu-item" onClick={(e) => { closeMenu(e); setOpen(true); run('restart', {}, { reconnect: true }) }}>
                    <RotateCcw size={14} /> Restart
                  </button>
                )}
                {!done && (
                  <button className="menu-item" onClick={(e) => { closeMenu(e); setOpen(true); run('restart-fresh', {}, { reconnect: true }) }}>
                    <Sparkles size={14} /> Restart with fresh session
                  </button>
                )}
                {!done && open && (
                  <button className="menu-item" onClick={(e) => { closeMenu(e); run('pause', {}, { close: true }) }}>
                    <Pause size={14} /> Pause session
                  </button>
                )}
                <button className="menu-item" disabled title="Fork task — coming soon">
                  <GitFork size={14} /> Fork task
                  <span className="menu-item-note">coming soon</span>
                </button>
                <button
                  className="menu-item danger"
                  onClick={async (e) => {
                    closeMenu(e)
                    const ok = await confirmAction({
                      title: 'Archive this task?',
                      body: `"${task.name}" will be moved out of your active queue. You can unarchive it later.`,
                      confirmLabel: 'Archive',
                      danger: true,
                    })
                    if (ok) run('archive', {}, { goto: 'home' })
                  }}
                >
                  <Archive size={14} /> Archive
                </button>
              </div>
            </details>
            <button
              className="btn icon ghost sm"
              title={side ? 'Hide side panel' : 'Show side panel'}
              onClick={() => setSide((s) => !s)}
            >
              {side ? <PanelRightClose size={15} /> : <PanelRightOpen size={15} />}
            </button>
          </div>

          <div className={`term-shell${full ? ' fullscreen' : ''}`}>
            <div className="term-bar">
              <span
                className={`term-conn ${termConn === 'open' ? 'on' : 'off'}`}
                title={termConn === 'open' ? 'Terminal connected' : 'Terminal disconnected'}
              />
              <span className="mono clip">
                {provider} · {agent?.session_id || task.session_id || 'no session'}
              </span>
              <div className="spacer" />
              <span className="faint clip" style={{ maxWidth: 280 }}>
                {/* When open the verbose status just repeats "connected …" + the
                    session id (already shown on the left), so collapse to one
                    word; surface termStatus only for errors/close reasons. */}
                {termConn === 'open' ? 'connected' : termStatus || 'disconnected'}
              </span>
              {canTerminal && (
                <button
                  className="btn icon ghost sm"
                  title={full ? 'Exit full view (Esc)' : 'Full view'}
                  onClick={() => setFull((f) => !f)}
                >
                  {full ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
                </button>
              )}
            </div>
            {doneRunning && <DoneProgress slug={slug} onClose={() => setDoneRunning(false)} />}
            {canTerminal ? (
              <TaskTerminal
                key={`${slug}#${restartKey}`}
                slug={slug}
                restartKey={restartKey}
                onStatus={(kind, msg) => {
                  setTermStatus(kind === 'error' ? `error: ${msg}` : msg)
                  if (kind === 'open') setTermConn('open')
                  else if (kind === 'closed' || kind === 'error') setTermConn('closed')
                }}
              />
            ) : (
              <div className="term-placeholder">
                <TerminalSquare size={34} />
                {done ? (
                  <div className="col" style={{ alignItems: 'center', gap: 10 }}>
                    <div className="col" style={{ alignItems: 'center', gap: 4 }}>
                      <div className="dim">This task is done.</div>
                      <div className="faint" style={{ fontSize: 12 }}>
                        Its transcript and diff are on the right.
                      </div>
                    </div>
                    {task.session_id && (
                      <button className="btn primary" disabled={busy === 'resume'} onClick={revisit}>
                        <RotateCcw size={15} /> Revisit session
                      </button>
                    )}
                    {task.session_id && (
                      <div className="faint" style={{ fontSize: 11.5, maxWidth: 360, textAlign: 'center' }}>
                        Reopens the {provider} session and reloads its full context, so you continue right where
                        you left off.
                      </div>
                    )}
                  </div>
                ) : (
                  <>
                    <div className="dim">Session idle</div>
                    <button className="btn primary" onClick={() => setOpen(true)}>
                      <Play size={15} /> {task.session_id ? 'Resume in browser' : 'Launch in browser'}
                    </button>
                    <div className="faint" style={{ fontSize: 11.5, maxWidth: 360, textAlign: 'center' }}>
                      A live {provider} terminal attaches here over WebSocket. Keystrokes and resize stream to the PTY.
                    </div>
                  </>
                )}
              </div>
            )}
          </div>
        </div>

        {/* -------- side column: meta + tabs -------- */}
        {side && (
          <div className="session-side card" style={{ padding: 0 }}>
            <SideInfo task={task} agent={agent} />
            <div className="tabs" style={{ padding: '0 12px' }}>
              {(['brief', 'diff', 'transcript', 'updates'] as Tab[]).map((t) => (
                <button key={t} className={`tab${tab === t ? ' active' : ''}`} onClick={() => setTab(t)}>
                  {t === 'diff' && agent?.diff?.files ? `diff (${agent.diff.files})` : t}
                </button>
              ))}
            </div>
            <div className="tab-body" style={{ padding: '14px 14px' }}>
              {tab === 'brief' && <BriefTab slug={slug} summary={agent?.summary} />}
              {tab === 'diff' && <DiffTab files={agent?.diff_files} onExpand={() => setDiffModal(true)} />}
              {tab === 'transcript' && (
                <TranscriptTab
                  slug={slug}
                  active={tab === 'transcript'}
                  fallback={agent?.transcript}
                  onExpand={() => setTranscriptModal(true)}
                />
              )}
              {tab === 'updates' && (
                <UpdatesTab slug={slug} updates={task.updates} onExpand={() => setUpdatesModal(true)} />
              )}
            </div>
          </div>
        )}
      </div>

      <Modal open={diffModal} onClose={() => setDiffModal(false)} title={`Changes · ${agent?.diff?.files ?? 0} files`} width={1100}>
        <DiffTab files={agent?.diff_files} />
      </Modal>

      <Modal open={transcriptModal} onClose={() => setTranscriptModal(false)} title="Transcript" width={1000}>
        <TranscriptTab slug={slug} active={transcriptModal} fallback={agent?.transcript} full />
      </Modal>

      <Modal open={updatesModal} onClose={() => setUpdatesModal(false)} title="Updates" width={900}>
        <UpdatesTab slug={slug} updates={task.updates} startOpen />
      </Modal>
    </div>
  )
}

// Inline close-out progress shown when "Mark done" runs `flow done`. The CLI
// performs three phases (git snapshot → status flip → headless KB/project
// sweep) as one synchronous call; we surface them as live steps. The first two
// are quick, so they advance on a short cadence; the sweep step holds in
// "running" for the real duration until the action resolves.
const DONE_STEPS = [
  'Saving git close-out snapshot',
  'Marking task as done',
  'Distilling KB + project updates',
]
function DoneProgress({ slug, onClose }: { slug: string; onClose: () => void }) {
  const [phase, setPhase] = useState(0)
  const [state, setState] = useState<'running' | 'done' | 'error'>('running')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const timers = [
      window.setTimeout(() => !cancelled && setPhase((p) => Math.max(p, 1)), 850),
      window.setTimeout(() => !cancelled && setPhase((p) => Math.max(p, 2)), 1650),
    ]
    ;(async () => {
      try {
        // The close-out sweep (headless claude) can run well past the 30s
        // default RPC timeout; give it the backend's full 2-minute budget + buffer.
        await apiAction({ kind: 'done', target: slug }, 180000)
        if (cancelled) return
        setPhase(DONE_STEPS.length)
        setState('done')
        queryClient.invalidateQueries()
        window.setTimeout(() => !cancelled && onClose(), 1200)
      } catch (e) {
        if (cancelled) return
        setState('error')
        setError(e instanceof Error ? e.message : 'close-out failed')
      }
    })()
    return () => {
      cancelled = true
      timers.forEach(clearTimeout)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug])

  return (
    <div className="done-progress">
      <div className="done-card">
        <div className="done-head">
          {state === 'error' ? (
            <XIcon size={18} className="danger-text" />
          ) : state === 'done' ? (
            <CheckCircle2 size={18} style={{ color: 'var(--ok)' }} />
          ) : (
            <Loader2 size={18} className="spin" style={{ color: 'var(--accent)' }} />
          )}
          <span className="h-lg">
            {state === 'error' ? 'Close-out failed' : state === 'done' ? 'Task closed' : 'Closing task…'}
          </span>
        </div>
        <div className="done-steps">
          {DONE_STEPS.map((label, i) => {
            const st =
              state === 'error' && i === phase
                ? 'error'
                : state === 'done' || i < phase
                ? 'done'
                : i === phase
                ? 'running'
                : 'pending'
            return (
              <div key={i} className={`done-step ${st}`}>
                <span className="done-step-icon">
                  {st === 'done' ? (
                    <Check size={13} />
                  ) : st === 'running' ? (
                    <Loader2 size={13} className="spin" />
                  ) : st === 'error' ? (
                    <XIcon size={13} />
                  ) : (
                    <Circle size={13} />
                  )}
                </span>
                {label}
              </div>
            )
          })}
        </div>
        {error ? (
          <>
            <div className="error-note" style={{ marginTop: 4 }}>{error}</div>
            <button className="btn sm" style={{ alignSelf: 'flex-end' }} onClick={onClose}>Close</button>
          </>
        ) : (
          <div className="faint" style={{ fontSize: 11.5 }}>
            Distilling durable learnings into your KB &amp; project log — this can take a moment.
          </div>
        )}
      </div>
    </div>
  )
}

// Icon segmented control: shows every option as an icon, highlighting the
// active one — used for permission mode and priority.
function Seg({
  value,
  onChange,
  options,
}: {
  value: string
  onChange: (v: string) => void
  options: { v: string; icon: ReactNode; title: string }[]
}) {
  return (
    <div className="iconseg" role="group">
      {options.map((o) => (
        <button
          key={o.v}
          className={`iconseg-btn${value === o.v ? ' active' : ''}`}
          title={o.title}
          aria-pressed={value === o.v}
          onClick={() => onChange(o.v)}
        >
          {o.icon}
        </button>
      ))}
    </div>
  )
}

function SideInfo({ task, agent }: { task: ReturnType<typeof useTask>['data']; agent?: UiAgent }) {
  if (!task) return null
  return (
    <div style={{ padding: 14, borderBottom: '1px solid var(--border)' }}>
      <div className="meta-grid">
        <div className="meta-cell">
          <div className="meta-k">work dir</div>
          <div className="meta-v mono clip" title={task.work_dir}>{task.work_dir}</div>
        </div>
        <div className="meta-cell">
          <div className="meta-k">branch</div>
          <div className="meta-v mono clip">{agent?.branch || '—'}</div>
        </div>
        <div className="meta-cell">
          <div className="meta-k">last activity</div>
          <div className="meta-v">{agent ? `${fromSeconds(agent.last_activity_sec)} ago` : '—'}</div>
        </div>
        <div className="meta-cell">
          <div className="meta-k">uptime</div>
          <div className="meta-v">{agent ? fromMinutes(agent.started_min) : '—'}</div>
        </div>
      </div>
      {agent && (
        <div className="row gap" style={{ gap: 9, marginTop: 12 }}>
          <TokenBar used={agent.tokens_used} max={agent.tokens_max} />
          <span className="faint mono" style={{ fontSize: 10.5 }}>
            {compact(agent.tokens_used)}/{compact(agent.tokens_max)} ctx
          </span>
        </div>
      )}
      {task.tags?.length > 0 && (
        <div className="row gap wrap" style={{ gap: 6, marginTop: 12 }}>
          {task.tags.map((t) => <span key={t} className="tag">{t}</span>)}
        </div>
      )}
    </div>
  )
}

function BriefTab({ slug, summary }: { slug: string; summary?: string }) {
  const { data, isLoading } = useMarkdown(`/api/tasks/${encodeURIComponent(slug)}/brief`)
  if (isLoading) return <Loading label="brief" />
  if (!data?.trim()) return <div className="faint">{summary || 'No brief written for this task.'}</div>
  return <Md source={data} />
}

function DiffTab({ files, onExpand }: { files?: DiffFile[]; onExpand?: () => void }) {
  if (!files || files.length === 0) return <div className="faint">No local git changes.</div>
  return (
    <div>
      {onExpand && (
        <div className="row" style={{ marginBottom: 10 }}>
          <span className="faint mono" style={{ fontSize: 11 }}>{files.length} files changed</span>
          <div className="spacer" />
          <button className="btn ghost sm" onClick={onExpand}>
            <Maximize2 size={13} /> Full view
          </button>
        </div>
      )}
      {files.map((f) => (
        <div key={f.name} className="diff-file">
          <div className="diff-file-head">
            <span className="clip" style={{ flex: 1 }}>{f.name}</span>
            <span className="diffstat"><span className="add">+{f.add}</span> <span className="rem">−{f.rem}</span></span>
          </div>
          <div className="diff-code">
            {(f.hunks ?? []).map((h, hi) => (
              <div key={hi}>
                <div className="diff-hunk-head">{h.header}</div>
                {h.lines.map((l, li) => (
                  <div key={li} className={`diff-line ${l.type}`}>
                    <span className="ln">{l.n}</span>
                    <span className="cd">{l.type === 'add' ? '+' : l.type === 'rem' ? '−' : ' '}{l.code}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

function TranscriptTab({
  slug,
  active,
  fallback,
  onExpand,
  full,
}: {
  slug: string
  active: boolean
  fallback?: UiAgent['transcript']
  onExpand?: () => void
  full?: boolean
}) {
  const { data, isLoading } = useTaskTranscript(slug, active)
  if (isLoading && !fallback) return <Loading label="transcript" />
  const entries: TranscriptEntry[] = data?.available ? data.entries : []
  if (entries.length === 0) {
    if (fallback && fallback.length) {
      return (
        <div className="tx">
          {fallback.map((e, i) => (
            <div key={i} className={`tx-entry ${e.type}`}>
              <div className="tx-role">{e.type}</div>
              {e.tool ? <div className="tx-tool">{e.tool}({e.input})</div> : <Md source={e.text || e.summary || ''} className="tx-md" />}
            </div>
          ))}
        </div>
      )
    }
    return <div className="faint">{data?.message || 'No transcript captured yet.'}</div>
  }
  // Compact (side panel) shows the tail; full view (modal) shows everything.
  const shown = full ? entries : entries.slice(-80)
  return (
    <div>
      {onExpand && (
        <div className="row" style={{ marginBottom: 10 }}>
          <span className="faint mono" style={{ fontSize: 11 }}>
            {entries.length} entries{entries.length > shown.length ? ` · showing last ${shown.length}` : ''}
          </span>
          <div className="spacer" />
          <button className="btn ghost sm" onClick={onExpand}>
            <Maximize2 size={13} /> Full view
          </button>
        </div>
      )}
      <div className="tx">
        {shown.map((e, i) => (
          <div key={i} className={`tx-entry ${e.type}`}>
            <div className="tx-role">{e.type}{e.timestamp ? ` · ${dateTime(e.timestamp)}` : ''}</div>
            {e.type === 'tool_use' ? (
              <div className="tx-tool">{e.tool_name} {e.tool_input_summary}</div>
            ) : e.type === 'tool_result' ? (
              <pre className="mono" style={{ fontSize: 11.5, whiteSpace: 'pre-wrap', color: e.is_error ? 'var(--danger)' : 'var(--text-2)' }}>{(e.tool_result_text || '').slice(0, 1200)}</pre>
            ) : (
              <Md source={e.text || ''} className="tx-md" />
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

function UpdatesTab({
  slug,
  updates,
  onExpand,
  startOpen,
}: {
  slug: string
  updates: { filename: string; path: string; mtime: string }[]
  onExpand?: () => void
  startOpen?: boolean
}) {
  // In the full-view modal every update is expanded; in the side panel only the
  // most recent one opens, the rest are collapsible.
  const [openFile, setOpenFile] = useState<string | null>(startOpen ? null : updates[0]?.filename ?? null)
  if (!updates || updates.length === 0) return <div className="faint">No updates logged for this task.</div>
  return (
    <div className="col" style={{ gap: 8 }}>
      {onExpand && (
        <div className="row" style={{ marginBottom: 2 }}>
          <span className="faint mono" style={{ fontSize: 11 }}>{updates.length} update{updates.length === 1 ? '' : 's'}</span>
          <div className="spacer" />
          <button className="btn ghost sm" onClick={onExpand}>
            <Maximize2 size={13} /> Full view
          </button>
        </div>
      )}
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
          {(startOpen || openFile === u.filename) && (
            <div style={{ padding: '4px 12px 12px', borderTop: '1px solid var(--border-faint)' }}>
              <UpdateBody slug={slug} filename={u.filename} />
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

function UpdateBody({ slug, filename }: { slug: string; filename: string }) {
  const { data, isLoading } = useMarkdown(
    `/api/tasks/${encodeURIComponent(slug)}/updates/${encodeURIComponent(filename)}`,
  )
  if (isLoading) return <Loading label="update" />
  return <Md source={data || ''} />
}
