import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useLocation } from 'wouter'
import {
  Bell,
  BookText,
  Brain,
  Database,
  FolderGit2,
  HardDrive,
  Inbox,
  LayoutDashboard,
  ListTodo,
  Moon,
  Plus,
  Radar,
  Repeat,
  Search,
  Settings,
  Sun,
  TerminalSquare,
  Trash2,
} from 'lucide-react'
import { rpc, type ConnStatus } from '../lib/rpc'
import { getTheme, toggleTheme, type Theme } from '../lib/theme'
import { useInbox, useUiData } from '../lib/query'
import { ago } from '../lib/format'
import { pushToast } from '../lib/toast'
import { SourceIcon } from './ui'
import { CommandPalette } from './CommandPalette'
import { CreateTaskModal } from './modals'
import { Toaster } from './Toaster'
import { ConfirmHost } from './ConfirmHost'

interface NavDef {
  to: string
  label: string
  icon: ReactNode
  match: (p: string) => boolean
  badge?: number
  tone?: string
}

type NotificationItem = { key: string; title: string; sub: string }
type BrowserNotificationPermission = NotificationPermission | 'unsupported'

function currentBrowserNotificationPermission(): BrowserNotificationPermission {
  if (typeof window === 'undefined' || !('Notification' in window)) return 'unsupported'
  return Notification.permission
}

// Pops a toast the moment a new notification arrives (a session starts waiting,
// or an unread inbox message lands) — so you notice without watching the bell.
// Seeds on first load so pre-existing items don't toast on page open.
function useNotificationToasts(ui?: ReturnType<typeof useUiData>['data'], inbox?: ReturnType<typeof useInbox>['data']) {
  const seen = useRef<Set<string> | null>(null)
  const notifyDesktop = useCallback((it: NotificationItem) => {
    if (currentBrowserNotificationPermission() !== 'granted' || typeof document === 'undefined' || !document.hidden) return
    try {
      new Notification(`flow: ${it.title}`, { body: it.sub, tag: it.key })
    } catch {
      // Browser notification availability can change under us; keep in-app
      // toasts as the reliable fallback.
    }
  }, [])
  useEffect(() => {
    const items: NotificationItem[] = []
    for (const a of ui?.AGENTS ?? []) {
      if (a.status === 'waiting') {
        items.push({ key: `w:${a.slug}`, title: a.name, sub: a.waiting_for?.why || 'Awaiting your input' })
      }
    }
    for (const e of inbox?.entries ?? []) {
      if (e.unread) {
        items.push({ key: `u:${e.task_slug}:${e.timestamp}`, title: e.task_name, sub: e.body_snippet || 'New message' })
      }
    }
    if (seen.current === null) {
      // first observation — remember what's already there, don't toast it
      seen.current = new Set(items.map((i) => i.key))
      return
    }
    for (const it of items) {
      if (!seen.current.has(it.key)) {
        seen.current.add(it.key)
        pushToast('info', `${it.title} — ${it.sub}`)
        notifyDesktop(it)
      }
    }
  }, [ui, inbox, notifyDesktop])
}

export function Shell({ children }: { children: ReactNode }) {
  const [loc] = useLocation()
  const [theme, setTheme] = useState<Theme>(getTheme())
  const [conn, setConn] = useState<ConnStatus>(rpc.getStatus())
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const { data: ui } = useUiData()
  const { data: inbox } = useInbox()
  useNotificationToasts(ui, inbox)

  useEffect(() => rpc.onStatus(setConn), [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey
      if (mod && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((o) => !o)
      } else if (mod && e.key.toLowerCase() === 'n' && !createOpen) {
        e.preventDefault()
        setCreateOpen(true)
      } else if (e.key === '/' && document.activeElement === document.body) {
        e.preventDefault()
        setPaletteOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [createOpen])

  const running = (ui?.AGENTS ?? []).filter((a) => a.status === 'running').length
  const waiting = (ui?.AGENTS ?? []).filter((a) => a.status === 'waiting').length
  const monitored = (ui?.AGENTS ?? []).filter((a) => a.monitored).length
  const backlog = ui?.BACKLOG?.length ?? 0
  const unread = inbox?.unread_count ?? 0

  const groups: { label: string; items: NavDef[] }[] = [
    {
      label: 'Workspace',
      items: [
        { to: '/', label: 'Mission Control', icon: <LayoutDashboard size={16} />, match: (p) => p === '/' },
        {
          to: '/sessions',
          label: 'Sessions',
          icon: <TerminalSquare size={16} />,
          match: (p) => p === '/sessions' || p.startsWith('/session/'),
          badge: running || undefined,
          tone: 'var(--ok)',
        },
        { to: '/tasks', label: 'Tasks', icon: <ListTodo size={16} />, match: (p) => p === '/tasks', badge: backlog || undefined },
        {
          to: '/inbox',
          label: 'Inbox',
          icon: <Inbox size={16} />,
          match: (p) => p === '/inbox',
          badge: unread || undefined,
          tone: 'var(--accent-hi)',
        },
      ],
    },
    {
      label: 'Library',
      items: [
        { to: '/projects', label: 'Projects', icon: <FolderGit2 size={16} />, match: (p) => p === '/projects' || p.startsWith('/project/') },
        { to: '/playbooks', label: 'Playbooks', icon: <Repeat size={16} />, match: (p) => p === '/playbooks' || p.startsWith('/playbook/') },
        { to: '/kb', label: 'Knowledge', icon: <BookText size={16} />, match: (p) => p === '/kb' },
        { to: '/memories', label: 'Memories', icon: <Brain size={16} />, match: (p) => p === '/memories' },
      ],
    },
    {
      label: 'System',
      items: [
        { to: '/workdirs', label: 'Workdirs', icon: <HardDrive size={16} />, match: (p) => p === '/workdirs' },
        { to: '/settings', label: 'Settings', icon: <Settings size={16} />, match: (p) => p === '/settings' },
        { to: '/trash', label: 'Trash', icon: <Trash2 size={16} />, match: (p) => p === '/trash' },
      ],
    },
  ]
  const allNav = groups.flatMap((g) => g.items)
  const title = allNav.find((n) => n.match(loc))?.label ?? 'flow'
  const connLabel = conn === 'open' ? 'live' : conn === 'connecting' ? 'connecting' : 'offline'

  return (
    <div className="shell">
      <aside className="rail">
        <Link href="/" className="rail-brand">
          <img src="/flow-mark.svg" width={23} height={23} alt="flow" />
          <span className="rail-wordmark">
            flow<span className="accent">.</span>
          </span>
        </Link>
        <nav className="rail-nav">
          {groups.map((g) => (
            <div className="rail-group" key={g.label}>
              <div className="rail-group-label">{g.label}</div>
              {g.items.map((n) => (
                <Link key={n.to} href={n.to} className={`rail-item${n.match(loc) ? ' active' : ''}`}>
                  <span className="rail-item-icon">{n.icon}</span>
                  <span className="rail-item-label">{n.label}</span>
                  {n.badge ? (
                    <span className="rail-badge" style={n.tone ? { color: n.tone, borderColor: 'currentColor' } : undefined}>
                      {n.badge}
                    </span>
                  ) : null}
                </Link>
              ))}
            </div>
          ))}
        </nav>
        <div className="rail-foot">
          {waiting > 0 && (
            <Link href="/sessions" className="rail-stat warn">
              <span className="dot waiting" /> {waiting} awaiting input
            </Link>
          )}
          {monitored > 0 && (
            <div className="rail-stat">
              <Radar size={13} /> {monitored} monitored
            </div>
          )}
          <div className="rail-foot-bar">
            <div className="rail-stat faint">
              <Database size={12} /> {ui?.FLOWDB?.exists ? ui.FLOWDB.human_size : '—'}
            </div>
            <div className="spacer" />
            <button
              type="button"
              className="btn icon ghost sm theme-toggle"
              onClick={(e) => {
                // Center of the button is the origin of the circular theme wipe.
                const r = e.currentTarget.getBoundingClientRect()
                setTheme(toggleTheme({ x: r.left + r.width / 2, y: r.top + r.height / 2 }))
              }}
              aria-label="Toggle theme"
              title="Toggle theme"
            >
              {/* key={theme} remounts the icon each swap so the spin-in plays. */}
              <span key={theme} className="theme-toggle-icon">
                {theme === 'dark' ? <Sun size={15} /> : <Moon size={15} />}
              </span>
            </button>
          </div>
        </div>
      </aside>

      <div className="main">
        <header className="topbar">
          <div className="topbar-title">
            <h1 className="topbar-h">{title}</h1>
          </div>
          <div className="spacer" />
          <button type="button" className="searchbtn" onClick={() => setPaletteOpen(true)}>
            <Search size={15} className="dim" />
            <span className="dim">Search…</span>
            <span className="kbd">⌘K</span>
          </button>
          <div className={`conn conn-${conn}`} title={`data channel: ${connLabel}`}>
            <span className="dot" />
            <span className="conn-label">{connLabel}</span>
          </div>
          <NotificationsBell />
          <button type="button" className="btn primary" onClick={() => setCreateOpen(true)}>
            <Plus size={16} /> New task
          </button>
        </header>
        <main className="content">{children}</main>
      </div>

      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
      <CreateTaskModal open={createOpen} onClose={() => setCreateOpen(false)} />
      <Toaster />
      <ConfirmHost />
    </div>
  )
}

// Notification bell — surfaces what needs you: sessions awaiting input and
// unread inbox messages. Badge = total; the panel navigates to each.
function NotificationsBell() {
  const [, navigate] = useLocation()
  const { data: ui } = useUiData()
  const { data: inbox } = useInbox()
  const [desktopPermission, setDesktopPermission] = useState<BrowserNotificationPermission>(currentBrowserNotificationPermission)
  const waiting = (ui?.AGENTS ?? []).filter((a) => a.status === 'waiting')
  const unread = (inbox?.entries ?? []).filter((e) => e.unread).slice(0, 10)
  const count = waiting.length + unread.length

  useEffect(() => {
    const onDown = (e: globalThis.MouseEvent) => {
      document.querySelectorAll('details.menu[open]').forEach((d) => {
        if (!d.contains(e.target as Node)) d.removeAttribute('open')
      })
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [])

  const close = (e: { currentTarget: EventTarget & HTMLElement }) =>
    e.currentTarget.closest('details')?.removeAttribute('open')
  const enableDesktop = async () => {
    if (typeof window === 'undefined' || !('Notification' in window)) {
      setDesktopPermission('unsupported')
      return
    }
    const next = await Notification.requestPermission()
    setDesktopPermission(next)
  }

  return (
    <details className="menu">
      <summary className="btn icon ghost notif-trigger" title="Notifications" aria-label="Notifications">
        <Bell size={16} />
        {count > 0 && <span className="notif-badge">{count > 99 ? '99+' : count}</span>}
      </summary>
      <div className="menu-pop right notif-pop">
        <div className="notif-head">
          Notifications {count > 0 && <span className="faint">· {count}</span>}
        </div>
        {desktopPermission === 'default' && (
          <button type="button" className="notif-permission" onClick={enableDesktop}>
            Enable desktop alerts
          </button>
        )}
        {desktopPermission === 'denied' && (
          <div className="notif-permission muted">Desktop alerts blocked in browser settings.</div>
        )}
        {count === 0 && <div className="notif-empty">You're all caught up.</div>}
        {waiting.map((a) => (
          <button type="button" key={`w-${a.slug}`} className="notif-item" onClick={(e) => { close(e); navigate(`/session/${a.slug}`) }}>
            <span className="dot waiting" style={{ marginTop: 4 }} />
            <div className="lrow-main">
              <div className="notif-title clip">{a.name}</div>
              <div className="notif-sub clip">{a.waiting_for?.why || 'Awaiting your input'}</div>
            </div>
          </button>
        ))}
        {unread.map((m, i) => (
          <button type="button" key={`u-${m.task_slug}-${i}`} className="notif-item" onClick={(e) => { close(e); navigate('/inbox') }}>
            <span style={{ marginTop: 2 }}><SourceIcon source={m.source} size={13} /></span>
            <div className="lrow-main">
              <div className="notif-title clip">{m.task_name}</div>
              <div className="notif-sub clip">{m.body_snippet || 'New message'}</div>
            </div>
            <span className="faint mono" style={{ fontSize: 10 }}>{ago(m.timestamp)}</span>
          </button>
        ))}
      </div>
    </details>
  )
}
