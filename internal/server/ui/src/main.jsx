const { useState, useEffect, useMemo, useRef, useContext, Fragment } = React;
const { ClockProvider, ClockCtx, AGENTS, DEAD_AGENT, DONE_AGENTS = [], BACKLOG, DONE_TASKS = [], PROJECTS_MC, PLAYBOOKS_MC, WORKDIRS, KB_FILES, AGENT_MEMORY_SOURCES = [], ACTIVITY_HEATMAP, TRASH = { tasks: [], projects: [], playbooks: [], total: 0 }, Icon, Dot, FlowLogo, FlowMark } = window.MC;
const FLOWDB_DEFAULT = { path: '', display_path: '~/.flow/flow.db', bytes: 0, human_size: '—', exists: false };
const { MissionControl, SessionsGrid, SessionDetail, CompletedSessionView, TasksList, TaskDetail, ProjectsList, ProjectDetail, PlaybooksList, PlaybookDetail, TrashView, KBView, MemorySourcesView, WorkdirsView, InboxView, CommandPalette, QRModal, ConfirmModal, ShortcutsOverlay, CreateFlowModal, CreateProjectModal } = window.MC_SCREENS;

const NAV = [
  { group: 'core', items: [
    { id: 'mc', label: 'Mission Control', icon: 'grid-3x3', kbd: 'g m' },
    { id: 'inbox', label: 'Inbox', icon: 'inbox', kbd: 'g i' },
    { id: 'sessions', label: 'Sessions', icon: 'box', kbd: 'g s' },
    { id: 'tasks', label: 'Tasks', icon: 'list', kbd: 'g t' },
  ]},
  { group: 'context', items: [
    { id: 'projects', label: 'Projects', icon: 'folder-tree', kbd: 'g p' },
    { id: 'playbooks', label: 'Playbooks', icon: 'play', kbd: 'g b' },
    { id: 'workdirs', label: 'Workdirs', icon: 'folder', kbd: 'g w' },
    { id: 'memories', label: 'Memories', icon: 'brain-circuit', kbd: 'g c' },
    { id: 'kb', label: 'KB', icon: 'book-open', kbd: 'g k' },
  ]},
  { group: 'review', items: [
    { id: 'trash', label: 'Trash', icon: 'trash-2', kbd: 'g x' },
  ]},
];

const TITLES = {
  mc: ['Mission Control'],
  sessions: ['Sessions'],
  tasks: ['Tasks'],
  projects: ['Projects'],
  playbooks: ['Playbooks'],
  workdirs: ['Workdirs'],
  memories: ['Memories'],
  kb: ['Knowledge base'],
  inbox: ['Inbox'],
  trash: ['Trash'],
};

const ROUTE_IDS = new Set(NAV.flatMap(group => group.items.map(item => item.id)));
const routeFromLocation = () => {
  const path = window.location.pathname.replace(/^\/+/, '').replace(/\/+$/, '');
  if (!path || path === 'overview') return 'mc';
  if (path.startsWith('session/') || path.startsWith('playbook/') || path.startsWith('project/') || path.startsWith('task/')) return path;
  return ROUTE_IDS.has(path) ? path : 'mc';
};
const pathForRoute = (route) => route === 'mc' || route === 'overview' ? '/' : `/${route}`;

const notificationLifecycleKinds = new Set(['stop','stop_failure','session_start','session_end','task_completed','task_created','subagent_start','subagent_stop']);
const classifyNotification = (n) => {
  const source = (n.source || '').toLowerCase();
  const kind = String(n.kind || '').toLowerCase();
  const level = String(n.level || '').toLowerCase();
  if (level === 'approval' || kind.includes('permission') || kind.includes('elicitation') || kind.includes('question') || kind.includes('input')) return 'attention';
  if (source === 'agent' || source === 'agent_hook' || notificationLifecycleKinds.has(kind)) return 'lifecycle';
  return 'notification';
};
const notificationSectionMeta = {
  attention: { title: 'Needs attention', icon: 'alert-circle' },
  notification: { title: 'Notifications', icon: 'bell' },
  lifecycle: { title: 'Agent lifecycle', icon: 'activity' },
  events: { title: 'Event log', icon: 'list-filter' },
};
const compactNotificationRows = (items) => {
  const seenLifecycle = new Set();
  return (items || []).filter(n => {
    if (n.category !== 'lifecycle') return true;
    const key = `${n.source || ''}:${n.title || ''}:${n.url || ''}`;
    if (seenLifecycle.has(key)) return false;
    seenLifecycle.add(key);
    return true;
  });
};
const lifecycleNotificationIsStale = (n, agentByURL) => {
  if (classifyNotification(n) !== 'lifecycle') return false;
  const agent = agentByURL.get(n.url || '');
  if (!agent) return false;
  const kind = String(n.kind || '').toLowerCase();
  if (['stop','stop_failure','session_end','task_completed'].includes(kind)) {
    return agent.status === 'running' || agent.status === 'waiting';
  }
  if (['session_start','subagent_start','subagent_stop','task_created'].includes(kind)) {
    return agent.status === 'idle' || agent.status === 'dead';
  }
  return false;
};

// ── Notifications bell ─────────────────────────────────────────
const NotificationsBell = ({ waitingCount, liveCount, goto, action, agents = [] }) => {
  const [open, setOpen] = useState(false);
  const [liveOpen, setLiveOpen] = useState(false);
  const [inboxFeed, setInboxFeed] = useState({ entries: [] });
  const ref = useRef(null);

  const monitor = window.MC.MONITOR || { notifications: [] };

  // ── Existing monitor-driven notifications / events (unchanged) ──────────
  const agentByURL = new Map((agents || []).filter(a => a && a.slug).map(a => [`/session/${a.slug}`, a]));
  const visibleNotificationInputs = (monitor.notifications || [])
    .filter(n => n.status !== 'dismissed')
    .filter(n => !lifecycleNotificationIsStale(n, agentByURL));
  const eventIdsWithNotifications = new Set(visibleNotificationInputs.map(n => n.event_id).filter(Boolean));
  const notifs = compactNotificationRows(visibleNotificationInputs.map(n => ({
      id: n.id,
      event_id: n.event_id,
      kind: n.level || n.kind || 'info',
      level: n.level || 'info',
      icon: n.level === 'approval' ? 'alert-circle' : n.source === 'github' ? 'git-pull-request' : n.source === 'slack' ? 'message-square' : n.source === 'agent' ? 'bot' : 'bell',
      title: n.title,
      sub: n.body || `${n.source || 'flow'} · ${n.kind || ''}`,
      time: n.source === 'agent' && n.status === 'read' ? 'live' : n.created_at ? n.created_at.slice(11, 16) : 'now',
      url: n.url,
      source: n.source,
      slug: n.source === 'agent' ? n.event_id : null,
      unread: n.status === 'unread',
      dismissable: true,
      category: classifyNotification(n),
    })));
  const events = (monitor.events || [])
    .filter(e => !eventIdsWithNotifications.has(e.id))
    .slice(0, 8)
    .map(e => ({
      id: `event-${e.id}`,
      event_id: e.id,
      kind: e.severity || e.kind || 'event',
      level: e.severity || 'info',
      icon: e.source === 'github' ? 'git-pull-request' : e.source === 'slack' ? 'message-square' : e.source === 'agent_hook' ? 'activity' : 'bell',
      title: e.title,
      sub: e.body || `${e.source || 'flow'} · ${e.kind || ''}`,
      time: e.last_seen_at ? e.last_seen_at.slice(11, 16) : 'now',
      url: e.url,
      source: e.source,
      unread: false,
      dismissable: false,
      category: 'events',
    }));
  const sectionItems = ['attention','notification','lifecycle','events']
    .map(key => ({ key, ...notificationSectionMeta[key], items: (key === 'events' ? events : notifs.filter(n => n.category === key)) }))
    .filter(section => section.items.length > 0);

  // ── Actionable + live, derived from the live agent set ──────────────────
  const waitingItems = (agents || []).filter(a => a && a.status === 'waiting');
  const liveItems = (agents || []).filter(a => a && a.status === 'running');

  // ── Unread inbox conversations: poll /api/inbox so the badge + list stay
  // current even before the popover is opened. Grouped by task, newest first.
  useEffect(() => {
    let cancelled = false;
    const load = () => fetch('/api/inbox', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : { entries: [] })
      .then(data => { if (!cancelled) setInboxFeed({ entries: Array.isArray(data.entries) ? data.entries : [] }); })
      .catch(() => {});
    load();
    const timer = setInterval(load, 30000);
    return () => { cancelled = true; clearInterval(timer); };
  }, []);

  const unreadConvos = useMemo(() => {
    const map = new Map();
    (inboxFeed.entries || []).forEach(e => {
      if (!e.unread) return;
      let c = map.get(e.task_slug);
      if (!c) { c = { slug: e.task_slug, name: e.task_name || e.task_slug, source: e.source, count: 0, latestTs: '' }; map.set(e.task_slug, c); }
      c.count += 1;
      if (!c.latestTs || String(e.timestamp) > String(c.latestTs)) c.latestTs = e.timestamp;
    });
    return Array.from(map.values()).sort((a, b) => String(b.latestTs).localeCompare(String(a.latestTs)));
  }, [inboxFeed]);

  useEffect(() => {
    if (!open) return;
    const onClick = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    const onKey = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onClick);
    window.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('mousedown', onClick); window.removeEventListener('keydown', onKey); };
  }, [open]);

  const handleClick = (n) => {
    setOpen(false);
    if (n.url && n.url.startsWith('/session/')) { goto(n.url.replace(/^\/+/, '')); return; }
    if (n.url && n.url.startsWith('/')) { goto(n.url.replace(/^\/+/, '')); return; }
    if (n.slug) {
      const agent = AGENTS.find(a => a.slug === n.slug);
      if (agent) action('attach', agent);
      else goto(`session/${n.slug}`);
      return;
    }
    goto('inbox');
  };
  const openAgent = (a) => {
    setOpen(false);
    const agent = AGENTS.find(x => x.slug === a.slug) || a;
    if (agent && agent.slug) action('attach', agent);
  };
  const openInboxConvo = () => { setOpen(false); goto('inbox'); };

  const colorFor = (kind) => kind === 'approval' || kind === 'high' ? 'var(--waiting)' : kind === 'success' || kind === 'done' || kind === 'low' ? 'var(--running)' : kind === 'warning' || kind === 'medium' || kind === 'pr' ? 'var(--primary-hi)' : 'var(--text-mid)';
  const dismissableNotifs = notifs.filter(n => n.dismissable);
  const unreadNotifs = dismissableNotifs.filter(n => n.unread);

  // Badge = things that actually need the user: waiting tasks + unread inbox
  // conversations + actionable monitor notifications. Live sessions are
  // observability and never inflate the badge.
  const actionableMonitor = notifs.filter(n => n.unread && (n.category === 'attention' || n.category === 'notification')).length;
  const badgeCount = waitingItems.length + unreadConvos.length + actionableMonitor;
  const hasActionable = waitingItems.length > 0 || unreadConvos.length > 0 || sectionItems.length > 0;

  return (
    <div className="notif-wrap" ref={ref}>
      <button className="notif-bell" onClick={() => setOpen(v => !v)} aria-label="Notifications" title="Notifications">
        <Icon name="bell" size={14}/>
        {badgeCount > 0 && <span className="notif-badge mono">{badgeCount}</span>}
      </button>
      {open && (
        <div className="notif-pop">
          <div className="notif-head">
            <span className="mono" style={{fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--text-dim)'}}>Notifications</span>
            <span className="mono" style={{marginLeft: 'auto', fontSize: 10.5, color: 'var(--text-faint)'}}>{liveCount} live · {waitingCount} waiting</span>
          </div>
          <div className="notif-list">
            {!hasActionable && liveItems.length === 0 ? (
              <div className="notif-empty mono">all clear</div>
            ) : (
              <>
                {!hasActionable && (
                  <div className="notif-empty mono" style={{padding: '20px 0 8px'}}>Nothing needs you right now</div>
                )}

                {waitingItems.length > 0 && (
                  <div className="notif-section attention">
                    <div className="notif-section-head mono">
                      <Icon name="alert-circle" size={12}/>
                      <span>Waiting on you</span>
                      <span className="count">{waitingItems.length}</span>
                    </div>
                    {waitingItems.map(a => (
                      <div key={`w-${a.slug}`} className="notif-row unread" role="button" tabIndex={0} onClick={() => openAgent(a)} onKeyDown={(e) => { if (e.key === 'Enter') openAgent(a); }}>
                        <span className="notif-row-dot" style={{background: 'var(--waiting)'}}></span>
                        <span className="notif-row-ic" style={{color: 'var(--waiting)'}}><Icon name="bell" size={13}/></span>
                        <div className="notif-row-body">
                          <div className="notif-row-title">{a.name || a.slug}</div>
                          <div className="notif-row-sub mono">{a.next_step || a.summary || 'waiting for your input'}</div>
                        </div>
                        <span className="notif-row-time mono">open</span>
                      </div>
                    ))}
                  </div>
                )}

                {unreadConvos.length > 0 && (
                  <div className="notif-section notification">
                    <div className="notif-section-head mono">
                      <Icon name="inbox" size={12}/>
                      <span>Unread inbox</span>
                      <span className="count">{unreadConvos.length}</span>
                    </div>
                    {unreadConvos.map(c => (
                      <div key={`i-${c.slug}`} className="notif-row unread" role="button" tabIndex={0} onClick={openInboxConvo} onKeyDown={(e) => { if (e.key === 'Enter') openInboxConvo(); }}>
                        <span className="notif-row-dot" style={{background: 'var(--accent)'}}></span>
                        <span className="notif-row-ic" style={{color: 'var(--accent)'}}><Icon name={c.source === 'github' ? 'git-pull-request' : c.source === 'slack' ? 'message-square' : 'inbox'} size={13}/></span>
                        <div className="notif-row-body">
                          <div className="notif-row-title">{c.name}</div>
                          <div className="notif-row-sub mono">{c.count} unread{c.source ? ` · ${c.source}` : ''}</div>
                        </div>
                        <span className="notif-row-time mono">{formatInboxTimeAgo(c.latestTs)}</span>
                      </div>
                    ))}
                  </div>
                )}

                {sectionItems.map(section => (
                  <div key={section.key} className={`notif-section ${section.key}`}>
                    <div className="notif-section-head mono">
                      <Icon name={section.icon} size={12}/>
                      <span>{section.title}</span>
                      <span className="count">{section.items.length}</span>
                    </div>
                    {section.items.map(n => (
                      <div key={n.id} className={`notif-row ${n.unread ? 'unread' : ''} ${n.dismissable ? '' : 'event'}`} role={n.dismissable || n.url || n.slug ? 'button' : undefined} tabIndex={n.dismissable || n.url || n.slug ? 0 : -1} onClick={() => handleClick(n)} onKeyDown={(e) => { if (e.key === 'Enter') handleClick(n); }}>
                        <span className="notif-row-dot" style={{background: colorFor(n.kind)}}></span>
                        <span className="notif-row-ic" style={{color: colorFor(n.kind)}}><Icon name={n.icon} size={13}/></span>
                        <div className="notif-row-body">
                          <div className="notif-row-title">{n.title}</div>
                          <div className="notif-row-sub mono">{n.sub}</div>
                        </div>
                        <span className="notif-row-time mono">{n.time}</span>
                        {n.dismissable && (
                          <button className="notif-dismiss" title="Dismiss notification" aria-label={`Dismiss ${n.title}`} onClick={(e) => { e.stopPropagation(); action('notification-dismiss', { slug: n.id }); }}>
                            <Icon name="x" size={12}/>
                          </button>
                        )}
                      </div>
                    ))}
                  </div>
                ))}

                {liveItems.length > 0 && (
                  <div className="notif-section lifecycle">
                    <div className="notif-section-head mono" style={{cursor: 'pointer'}} onClick={() => setLiveOpen(v => !v)} role="button" tabIndex={0} onKeyDown={(e) => { if (e.key === 'Enter') setLiveOpen(v => !v); }}>
                      <Icon name={liveOpen ? 'chevron-down' : 'chevron-right'} size={12}/>
                      <Icon name="activity" size={12}/>
                      <span>Live sessions</span>
                      <span className="count">{liveItems.length}</span>
                    </div>
                    {liveOpen && liveItems.map(a => (
                      <div key={`l-${a.slug}`} className="notif-row event" role="button" tabIndex={0} onClick={() => openAgent(a)} onKeyDown={(e) => { if (e.key === 'Enter') openAgent(a); }}>
                        <span className="notif-row-dot" style={{background: 'var(--running)'}}></span>
                        <span className="notif-row-ic" style={{color: 'var(--running)'}}><Icon name="bot" size={13}/></span>
                        <div className="notif-row-body">
                          <div className="notif-row-title">{a.name || a.slug}</div>
                          <div className="notif-row-sub mono">{a.summary || 'running'}</div>
                        </div>
                        <span className="notif-row-time mono">live</span>
                      </div>
                    ))}
                  </div>
                )}
              </>
            )}
          </div>
          <div className="notif-foot">
            <button className="btn sm" onClick={() => { action('notification-read-all', { notification_ids: unreadNotifs.map(n => n.id) }); }} disabled={!unreadNotifs.length} style={{flex: 1, justifyContent: 'center'}}>Mark all read</button>
            <button className="btn sm" onClick={() => { action('notification-dismiss-all', { notification_ids: dismissableNotifs.map(n => n.id) }); setOpen(false); }} disabled={!dismissableNotifs.length} style={{flex: 1, justifyContent: 'center'}}>Dismiss all</button>
            <button className="btn sm" onClick={() => { setOpen(false); goto('inbox'); }} style={{flex: 1, justifyContent: 'center'}}>Open inbox</button>
          </div>
        </div>
      )}
    </div>
  );
};

const SessionFallback = ({ slug, goto, action }) => {
  const latestDone = window.MC.DEAD_AGENT;
  const doneAgent = DONE_AGENTS.find(t => t.slug === slug) || (latestDone && latestDone.slug === slug ? latestDone : null);
  const doneTask = DONE_TASKS.find(t => t.slug === slug) || doneAgent;
  const backlogTask = BACKLOG.find(t => t.slug === slug);
  const backlogTaskBlocker = backlogTask ? taskStartBlocker(backlogTask) : '';
  const active = AGENTS.filter(a => a.status === 'running' || a.status === 'waiting' || a.status === 'idle').slice(0, 4);
  const backlog = BACKLOG.filter(t => t.slug !== slug).slice(0, 4);
  const title = doneTask ? 'Task is done' : backlogTask ? 'Task is in backlog' : 'Session is not active';
  const body = doneTask
    ? `${slug} is marked done, so there is no live terminal to open. Go back to sessions or jump into another flow.`
    : backlogTask
      ? `${slug} has not been spawned yet. Start it now or choose a running session.`
      : `${slug} is not in the active session list. It may be done, archived, deleted, or filtered out.`;
  return (
    <div className="session-fallback">
      <div className="session-fallback-head">
        <FlowMark size={34} title=""/>
        <div>
          <h2 className="session-fallback-title">{title}</h2>
          <p className="session-fallback-copy">{body}</p>
        </div>
        <div className="session-fallback-actions">
          <button className="btn sm primary" onClick={() => goto('sessions')}><Icon name="arrow-left" size={11}/>Sessions</button>
          <button className="btn sm" onClick={() => goto('tasks')}><Icon name="list" size={11}/>Tasks</button>
          {backlogTask && <button className="btn sm green" disabled={!!backlogTaskBlocker} title={backlogTaskBlocker || ''} onClick={() => action('spawn', backlogTask)}><Icon name="play" size={11}/>Spawn</button>}
        </div>
      </div>
      <div className="fallback-grid">
        <section className="fallback-card">
          <div className="fallback-card-head">
            <Icon name="radio" size={12}/>
            <span>Active sessions</span>
            <span className="count mono">{active.length}</span>
          </div>
          <div className="fallback-list">
            {active.length ? active.map(a => (
              <button key={a.slug} className="fallback-item" onClick={() => goto(`session/${a.slug}`)}>
                <Dot status={a.status}/>
                <span>
                  <span className="nm mono">{a.slug}</span>
                  <span className="meta mono">{a.project || 'adhoc'} · {a.status}</span>
                </span>
                <Icon name="arrow-right" size={12}/>
              </button>
            )) : <div className="fallback-empty mono">No active sessions right now.</div>}
          </div>
        </section>
        <section className="fallback-card">
          <div className="fallback-card-head">
            <Icon name="list-plus" size={12}/>
            <span>Backlog</span>
            <span className="count mono">{backlog.length}</span>
          </div>
          <div className="fallback-list">
            {backlog.length ? backlog.map(t => {
              const blockReason = taskStartBlocker(t);
              return (
              <button key={t.slug} className="fallback-item" disabled={!!blockReason} title={blockReason || ''} onClick={() => action('spawn', t)}>
                <span className="dot idle"></span>
                <span>
                  <span className="nm mono">{t.slug}</span>
                  <span className="meta mono">{t.project || 'floating'} · {blockReason ? 'blocked' : t.priority}</span>
                </span>
                <Icon name="play" size={12}/>
              </button>
            );}) : <div className="fallback-empty mono">Backlog is clear.</div>}
          </div>
        </section>
      </div>
    </div>
  );
};

const ProviderChoiceModal = ({ target, providers, onPick, onClose }) => {
  const fallbackProviders = [
    { id: 'claude', label: 'Claude Code', available: true },
    { id: 'codex', label: 'Codex', available: true },
  ];
  const options = (providers && providers.length ? providers : fallbackProviders)
    .filter(p => p && (p.id === 'claude' || p.id === 'codex'));
  const availOptions = options.filter(p => p.available);
  const initialProvider = (() => {
    if (target?.provider && availOptions.some(p => p.id === target.provider)) return target.provider;
    const claudeOpt = availOptions.find(p => p.id === 'claude');
    if (claudeOpt) return 'claude';
    return availOptions[0]?.id || 'claude';
  })();
  const [provider, setProvider] = useState(initialProvider);
  const initialMode = (target?.permission_mode === 'bypass') ? target.permission_mode : 'auto';
  const [permissionMode, setPermissionMode] = useState(initialMode);

  const PERMISSION_MODES = [
    { id: 'default', label: 'Default',    icon: 'shield',     sub: 'Sandboxed · prompts for risky operations' },
    { id: 'auto',    label: 'Auto',       icon: 'fast-forward', sub: 'No approval prompts · Codex keeps sandboxing' },
    { id: 'bypass',  label: 'Bypass',     icon: 'shield-off', sub: 'Skip approvals and sandboxing (powerful + risky)' },
  ];

  const name = target?.name || target?.slug || 'this backlog task';
  const providerAvailable = availOptions.some(p => p.id === provider);

  return (
    <div className="modal-scrim centered" onClick={onClose}>
      <div className="modal" style={{width: 480}} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <Icon name="play" size={14}/>
          <span>Start backlog task</span>
          <button className="modal-close" onClick={onClose}><Icon name="x" size={12}/></button>
        </div>
        <div className="modal-body" style={{display: 'flex', flexDirection: 'column', gap: 14}}>
          <div style={{color: 'var(--text)', fontSize: 13}}>{name}</div>

          <div>
            <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 6}}>Agent</div>
            <div style={{display: 'grid', gap: 6}}>
              {options.map(p => (
                <button
                  key={p.id}
                  type="button"
                  className={`btn ${provider === p.id && p.available ? 'primary' : ''} ${p.available ? '' : 'disabled'}`}
                  disabled={!p.available}
                  title={p.available ? '' : (p.reason || `${p.id} unavailable`)}
                  onClick={() => p.available && setProvider(p.id)}
                  style={{justifyContent: 'flex-start', padding: '9px 11px'}}
                >
                  <ProviderMark provider={p.id} size={14}/>
                  <span>{p.label || p.id}</span>
                  <span className="mono" style={{marginLeft: 'auto', fontSize: 10.5, color: p.available ? 'var(--text-faint)' : 'var(--error)'}}>
                    {p.available ? 'available' : (p.reason || 'unavailable')}
                  </span>
                </button>
              ))}
            </div>
          </div>

          <div>
            <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 6}}>Permission mode</div>
            <div style={{display: 'grid', gap: 6}}>
              {PERMISSION_MODES.map(m => (
                <button
                  key={m.id}
                  type="button"
                  className={`btn ${permissionMode === m.id ? 'primary' : ''}`}
                  onClick={() => setPermissionMode(m.id)}
                  style={{flexDirection: 'column', alignItems: 'stretch', gap: 2, padding: '8px 11px', whiteSpace: 'normal'}}
                >
                  <div style={{display: 'flex', alignItems: 'center', gap: 8, width: '100%'}}>
                    <Icon name={m.icon} size={12}/>
                    <span>{m.label}</span>
                    {m.id === 'bypass' && <span className="mono" style={{marginLeft: 'auto', fontSize: 10, color: 'var(--waiting)', letterSpacing: '0.06em'}}>RISKY</span>}
                  </div>
                  <div className="mono" style={{fontSize: 10.5, color: permissionMode === m.id ? 'rgba(255,255,255,0.8)' : 'var(--text-faint)', textTransform: 'none', letterSpacing: 0, lineHeight: 1.4}}>{m.sub}</div>
                </button>
              ))}
            </div>
          </div>
        </div>
        <div className="modal-foot">
          <button className="btn sm" onClick={onClose}>Cancel</button>
          <button
            className="btn sm primary"
            disabled={!providerAvailable}
            title={providerAvailable ? '' : `${provider} unavailable`}
            onClick={() => providerAvailable && onPick(provider, permissionMode)}
          >
            <Icon name="play" size={11}/>Start
          </button>
        </div>
      </div>
    </div>
  );
};

const App = () => {
  const [route, setRoute] = useState(() => routeFromLocation());
  const [focus, setFocus] = useState(null);
  const [palette, setPalette] = useState(false);
  const [qr, setQR] = useState(false);
  const [help, setHelp] = useState(false);
	  const [createOpen, setCreateOpen] = useState(false);
	  const [createPreselect, setCreatePreselect] = useState(null);
	  const [createProjectOpen, setCreateProjectOpen] = useState(false);
	  const [confirm, setConfirm] = useState(null);
	  const [toast, setToast] = useState(null);
	  const [sort, setSort] = useState('priority');
	  const [dataTick, setDataTick] = useState(0);
	  const [bridgeAgents, setBridgeAgents] = useState({});
	  const [providerPick, setProviderPick] = useState(null);
	  const [gitDiffOpen, setGitDiffOpen] = useState(() => {
	    try { return localStorage.getItem('flow.gitDiffOpen') === '1'; } catch (_) { return false; }
	  });

	  useEffect(() => {
	    try { localStorage.setItem('flow.gitDiffOpen', gitDiffOpen ? '1' : '0'); } catch (_) {}
	  }, [gitDiffOpen]);

	  const [artifactsOpen, setArtifactsOpen] = useState(() => {
	    try { return localStorage.getItem('flow.artifactsOpen') === '1'; } catch (_) { return false; }
	  });

	  useEffect(() => {
	    try { localStorage.setItem('flow.artifactsOpen', artifactsOpen ? '1' : '0'); } catch (_) {}
	  }, [artifactsOpen]);

	  useEffect(() => {
	    const splash = document.getElementById('boot-splash');
	    if (!splash) return;
	    splash.classList.add('boot-splash-out');
	    const timer = setTimeout(() => splash.remove(), 220);
	    return () => clearTimeout(timer);
	  }, []);

	  const replaceArray = (target, next) => {
	    target.splice(0, target.length, ...(Array.isArray(next) ? next : []));
	  };
	  const applyTrash = (freshTrash) => {
	    if (!freshTrash) return;
	    TRASH.tasks = Array.isArray(freshTrash.tasks) ? freshTrash.tasks : [];
	    TRASH.projects = Array.isArray(freshTrash.projects) ? freshTrash.projects : [];
	    TRASH.playbooks = Array.isArray(freshTrash.playbooks) ? freshTrash.playbooks : [];
	    TRASH.total = Number.isFinite(freshTrash.total) ? freshTrash.total : (TRASH.tasks.length + TRASH.projects.length + TRASH.playbooks.length);
	    window.MC.TRASH = TRASH;
	  };
	  const applyUIData = (fresh) => {
	    if (!fresh) return;
	    replaceArray(AGENTS, fresh.AGENTS);
	    replaceArray(DONE_AGENTS, fresh.DONE_AGENTS);
	    replaceArray(BACKLOG, fresh.BACKLOG);
	    replaceArray(DONE_TASKS, fresh.DONE_TASKS);
	    replaceArray(KB_FILES, fresh.KB_FILES);
	    replaceArray(AGENT_MEMORY_SOURCES, fresh.AGENT_MEMORY_SOURCES);
	    replaceArray(WORKDIRS, fresh.WORKDIRS);
	    replaceArray(PLAYBOOKS_MC, fresh.PLAYBOOKS_MC);
	    replaceArray(PROJECTS_MC, fresh.PROJECTS_MC);
	    replaceArray(ACTIVITY_HEATMAP, fresh.ACTIVITY_HEATMAP);
	    window.MC.DEAD_AGENT = fresh.DEAD_AGENT || null;
	    window.MC.DONE_AGENTS = DONE_AGENTS;
	    window.MC.CAPABILITIES = fresh.CAPABILITIES || window.MC.CAPABILITIES;
	    window.MC.MONITOR = fresh.MONITOR || window.MC.MONITOR;
	    window.MC.FLOWDB = fresh.FLOWDB || window.MC.FLOWDB;
	    window.MC.DONE_TASKS = DONE_TASKS;
	    window.MC.AGENT_MEMORY_SOURCES = AGENT_MEMORY_SOURCES;
	    applyTrash(fresh.TRASH);
	    setFocus(current => {
	      if (!current || !current.slug) return current;
	      return AGENTS.find(agent => agent.slug === current.slug) || current;
	    });
	    setDataTick(v => v + 1);
	  };
	  const refreshUIData = async () => {
	    try {
	      const resp = await fetch('/api/ui-data', { cache: 'no-store' });
	      if (!resp.ok) return;
	      const fresh = await resp.json();
	      applyUIData(fresh);
	    } catch (_) {
	      // Keep the last known snapshot if a refresh races server startup.
	    }
	  };

	  const goto = (r, opts = {}) => {
	    setRoute(r);
	    setFocus(null);
	    const nextPath = pathForRoute(r);
	    if (window.location.pathname !== nextPath) {
	      const method = opts.replace ? 'replaceState' : 'pushState';
	      window.history[method](null, '', nextPath);
	    }
	  };
  const upsertAgent = (agent) => {
    if (!agent || !agent.slug) return;
    const idx = AGENTS.findIndex(a => a.slug === agent.slug);
    if (idx >= 0) AGENTS[idx] = agent;
    else AGENTS.unshift(agent);
    const backlogIdx = BACKLOG.findIndex(b => b.slug === agent.slug);
    if (backlogIdx >= 0) BACKLOG.splice(backlogIdx, 1);
    setFocus(f => f && f.slug === agent.slug ? agent : f);
    setDataTick(v => v + 1);
  };
  const serverAction = async (kind, target = {}) => {
    const payload = {
      kind,
      target: target.slug,
      slug: target.slug,
      name: target.name,
      path: target.path,
      description: target.description,
      project: target.project,
      work_dir: target.workdir || target.work_dir,
      priority: target.priority,
      prompt: target.prompt,
      session_id: target.session_id,
      branch: target.branch,
      event_id: target.event_id,
      mode: target.mode,
      source: target.source,
      rule_kind: target.rule_kind,
      pr_url: target.pr_url,
      entity_kind: target.kind,
      provider: target.provider,
      permission_mode: target.permission_mode,
      mkdir: target.mkdir,
	      notification_ids: target.notification_ids,
	      read_only: target.read_only,
	      rule_update: target.rule_update,
	    };
    const resp = await fetch('/api/actions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    const data = await resp.json().catch(() => ({ message: resp.statusText }));
    if (!resp.ok) throw new Error(data.message || resp.statusText);
    if (data.agent) upsertAgent(data.agent);
    setToast(data.message || `${kind} completed`);
    return data;
  };
  const serverFormAction = async (kind, formData) => {
    if (!(formData instanceof FormData)) throw new Error('formData is required');
    formData.set('kind', kind);
    const resp = await fetch('/api/actions', {
      method: 'POST',
      body: formData,
    });
    const data = await resp.json().catch(() => ({ message: resp.statusText }));
    if (!resp.ok) throw new Error(data.message || data.error || resp.statusText);
    if (data.agent) upsertAgent(data.agent);
    setToast(data.message || `${kind} completed`);
    return data;
  };
  const capabilityList = (group) => {
    const caps = (window.MC && window.MC.CAPABILITIES) || {};
    return Array.isArray(caps[group]) ? caps[group] : [];
  };
  const capabilityFor = (group, id) => capabilityList(group).find(item => item.id === id) || { id, label: id, available: true };
  const capabilityAvailable = (group, id) => {
    const list = capabilityList(group);
    return !list.length || !!capabilityFor(group, id).available;
  };
  const capabilityReason = (group, id) => capabilityFor(group, id).reason || 'not available';

  const action = (kind, target) => {
    const isBacklogTarget = (t = {}) => {
      if (!t || t.hasAgent) return false;
      return t.status_outer === 'backlog' || BACKLOG.some(b => b.slug === t.slug);
    };
    const ensureProvider = (t = {}) => {
      const provider = t.provider || 'claude';
      if (capabilityAvailable('providers', provider)) return true;
      setToast(`${provider} unavailable: ${capabilityReason('providers', provider)}`);
      return false;
    };
    const ensureTerminal = (terminal) => {
      if (capabilityAvailable('terminals', terminal)) return true;
      setToast(`${terminal} unavailable: ${capabilityReason('terminals', terminal)}`);
      return false;
    };
    const openBridge = (bridgeKind, bridgeTarget) => {
      if (!ensureProvider(bridgeTarget)) return null;
      const bridgeVerb = bridgeKind === 'restart-fresh'
        ? 'starting fresh session for'
        : bridgeKind === 'restart'
        ? 'restarting'
        : 'opening';
      setToast(`${bridgeVerb} ${bridgeTarget.slug}…`);
      return serverAction(bridgeKind, bridgeTarget)
        .then(data => {
          if (data.agent && data.agent.slug) {
            setBridgeAgents(prev => ({ ...prev, [data.agent.slug]: data.agent }));
          }
          goto(`session/${(data.agent && data.agent.slug) || bridgeTarget.slug}`);
          return data;
        })
        .catch(err => {
          setToast(err.message);
          return null;
        });
    };
    if (kind === 'approve' || kind === 'deny' || kind === 'pause' || kind === 'clear-waiting') { return serverAction(kind, target).catch(err => { setToast(err.message); return null; }); }
    if (kind === 'resume' || kind === 'attach') { return openBridge(kind, target); }
    if (kind === 'iterm' || kind === 'terminal' || kind === 'warp' || kind === 'kitty' || kind === 'alacritty' || kind === 'ghostty' || kind === 'wezterm' || kind === 'tmux' || kind === 'vscode') {
      if (!ensureTerminal(kind) || !ensureProvider(target)) return null;
      setToast(`opening ${target.slug} in ${target._terminal || kind}…`);
      return serverAction(kind, target).catch(err => { setToast(err.message); return null; });
    }
    if (kind === 'switch-branch') { setToast(`switching ${target.slug} to ${target.branch}…`); return serverAction(kind, target).catch(err => { setToast(err.message); return null; }); }
    if (kind === 'restart') { return openBridge('restart', target); }
    if (kind === 'restart-fresh') { return openBridge('restart-fresh', target); }
    if (kind === 'archive') { setToast(`archiving ${target.slug}…`); serverAction('archive', target).then(refreshUIData).catch(err => setToast(err.message)); return; }
    if (kind === 'delete') {
      const slug = (target && (target.slug || target.name)) || '';
      const entity = (target && target.kind) || 'task';
      if (!slug) {
        setToast(`archiving…`);
        serverAction('delete', target).then(refreshUIData).catch(err => setToast(err.message));
        return;
      }
      setConfirm({
        title: <>Archive {entity} <span className="mono" style={{color: 'var(--text)'}}>{slug}</span></>,
        body: (
          <>
            This will move <span className="mono" style={{color: 'var(--text)'}}>{slug}</span>
            {target.name && target.name !== slug ? <> (<span>{target.name}</span>)</> : null} to the trash.
            <div style={{marginTop: 6, color: 'var(--text-dim)'}}>You can restore it from Trash, or delete it permanently there.</div>
          </>
        ),
        confirm: `Archive ${entity}`,
        danger: false,
        requireText: slug,
        requireLabel: <>Type <code className="modal-confirm-token">{slug}</code> to confirm archive.</>,
        onConfirm: () => {
          setToast(`archiving ${slug}…`);
          serverAction('delete', target).then(refreshUIData).catch(err => setToast(err.message));
        },
      });
      return;
    }
    if (kind === 'destroy') {
      const slug = (target && target.slug) || '';
      const entity = (target && target.kind) || 'item';
      if (!slug) return;
      setConfirm({
        title: <>Delete {entity} <span className="mono" style={{color: 'var(--text)'}}>{slug}</span></>,
        body: (
          <>
            This permanently deletes <span className="mono" style={{color: 'var(--text)'}}>{slug}</span>.
            <div style={{marginTop: 6, color: 'var(--text-dim)'}}>This is only available from Trash and cannot be undone.</div>
          </>
        ),
        confirm: `Delete ${entity}`,
        danger: true,
        requireText: slug,
        requireLabel: <>Type <code className="modal-confirm-token">{slug}</code> to confirm permanent deletion.</>,
        onConfirm: () => {
          setToast(`deleting ${slug}…`);
          serverAction('destroy', target).then(refreshUIData).catch(err => setToast(err.message));
        },
      });
      return;
    }
    if (kind === 'restore') {
      setToast(`restoring ${target.slug}…`);
      serverAction('restore', target).then(refreshUIData).catch(err => setToast(err.message));
      return;
    }
    if (kind === 'workdir-add' || kind === 'workdir-rename') {
      setToast(kind === 'workdir-add' ? 'registering workdir…' : 'renaming workdir…');
      serverAction(kind, target).then(refreshUIData).catch(err => setToast(err.message));
      return;
    }
    if (kind === 'workdir-remove') {
      const path = (target && target.path) || '';
      if (!path) return;
      setConfirm({
        title: <>Remove workdir <span className="mono" style={{color: 'var(--text)'}}>{target.name || path}</span></>,
        body: (
          <>
            This unregisters <span className="mono" style={{color: 'var(--text)'}}>{path}</span> from Flow.
            <div style={{marginTop: 6, color: 'var(--text-dim)'}}>It does not delete files from disk.</div>
          </>
        ),
        confirm: 'Remove workdir',
        danger: true,
        onConfirm: () => {
          setToast('removing workdir…');
          serverAction('workdir-remove', target).then(refreshUIData).catch(err => setToast(err.message));
        },
      });
      return;
    }
    if (kind === 'update-permission-mode-confirm') {
      const mode = target && target.permission_mode;
      const slug = (target && target.slug) || '';
      if (!mode || !slug) return;
      const live = !!(target && target._live);
      const apply = () => serverAction('update-permission-mode', { slug, permission_mode: mode, _live: live, provider: target.provider })
        .then(data => {
          if (data.bridge && data.agent) {
            setBridgeAgents(prev => ({ ...prev, [data.agent.slug]: data.agent }));
            goto(`session/${data.agent.slug}`);
            window.dispatchEvent(new CustomEvent('flow-terminal-restart', { detail: { slug: data.agent.slug } }));
          }
          return refreshUIData();
        })
        .catch(err => setToast(err.message));
      if (!live) { apply(); return; }
      setConfirm({
        title: <>Switch permissions to <span className="mono" style={{color: 'var(--text)'}}>{mode}</span>?</>,
        body: (
          <>
            This terminates the running session for <span className="mono" style={{color: 'var(--text)'}}>{slug}</span>.
            <div style={{marginTop: 6, color: 'var(--text-dim)'}}>The terminal restarts immediately with the new mode.</div>
          </>
        ),
        confirm: `Switch to ${mode}`,
        danger: mode === 'bypass',
        onConfirm: apply,
      });
      return;
    }
    if (kind === 'investigate') { openBridge('attach', target); return; }
    if (kind === 'spawn') {
      const blockReason = taskStartBlocker(target);
      if (blockReason) {
        setToast(blockReason);
        return null;
      }
      if (isBacklogTarget(target) && !target._providerChosen) {
        if (!anyProviderAvailable()) {
          setToast('No supported agent binary found on PATH');
          return null;
        }
        setProviderPick(target);
        return null;
      }
      openBridge('spawn', target);
      return;
    }
    if (kind === 'spawn-run') {
      if (!ensureProvider(target)) return null;
      setToast(`spawning playbook run · ${target.slug}`);
      return serverAction('spawn-run', target)
        .then(data => {
          if (data.agent && data.agent.slug) {
            setBridgeAgents(prev => ({ ...prev, [data.agent.slug]: data.agent }));
            goto(`session/${data.agent.slug}`);
          }
          return data;
        })
        .catch(err => {
          setToast(err.message);
          return null;
        });
    }
    if (kind === 'spawn-prompt') { setPalette(false); setCreateOpen(true); return; }
    if (kind === 'create-flow-images') {
      if (!ensureProvider(target)) return null;
      setToast(`opening ${target.slug}…`);
      return serverFormAction('create-flow', target.formData)
        .then(data => {
          if (data.agent && data.agent.slug) {
            setBridgeAgents(prev => ({ ...prev, [data.agent.slug]: data.agent }));
          }
          goto(`session/${(data.agent && data.agent.slug) || target.slug}`);
          return data;
        })
        .catch(err => {
          setToast(err.message);
          return null;
        });
    }
    if (kind === 'create-flow') { return openBridge('create-flow', target); }
    if (kind === 'update-permission-mode') {
      serverAction('update-permission-mode', target)
        .then(data => {
          if (data.bridge && data.agent) {
            setBridgeAgents(prev => ({ ...prev, [data.agent.slug]: data.agent }));
            goto(`session/${data.agent.slug}`);
            window.dispatchEvent(new CustomEvent('flow-terminal-restart', { detail: { slug: data.agent.slug } }));
          }
          return refreshUIData();
        })
        .catch(err => setToast(err.message));
      return;
    }
    if (kind === 'update-priority') {
      serverAction('update-priority', target)
        .then(() => refreshUIData())
        .catch(err => setToast(err.message));
      return;
    }
    if (kind === 'update-task-name') {
      return serverAction('update-task-name', target)
        .then(data => refreshUIData().then(() => data))
        .catch(err => { setToast(err.message); return null; });
    }
    if (kind === 'create-project-open') { setCreateProjectOpen(true); return; }
    if (kind === 'create-project') {
      setToast(`creating project · ${target.slug}`);
      serverAction('create-project', target)
        .then(async (data) => {
          await refreshUIData();
          if (data && data.ok && target.slug) goto(`project/${target.slug}`);
          return data;
        })
        .catch(err => setToast(err.message));
      return;
    }
    if (kind === 'overview-chat') { openBridge('overview-chat', target); return; }
	    if (kind === 'monitor-sync' || kind === 'monitor-ignore-event' || kind === 'notification-dismiss' || kind === 'notification-dismiss-all' || kind === 'notification-read' || kind === 'notification-read-all' || kind === 'set-rule-mode') {
	      serverAction(kind, target).then(refreshUIData).catch(err => setToast(err.message));
	      return;
	    }
    if (kind === 'notification-start-agent') {
      setToast('starting agent from notification…');
      serverAction(kind, target)
        .then(data => data.agent && goto(`session/${data.agent.slug}`))
        .catch(err => setToast(err.message));
      return;
    }
  };

  // Toast auto-dismiss
	  useEffect(() => {
	    if (!toast) return;
	    const t = setTimeout(() => setToast(null), 2400);
	    return () => clearTimeout(t);
	  }, [toast]);

	  // External toast bus — other bundles (e.g. the terminal pane in
	  // assets/c906f...js, where TerminalPane lives outside this React
	  // tree) dispatch `flow:toast` CustomEvents instead of prop-drilling
	  // setToast across modules.
	  useEffect(() => {
	    const onToast = (e) => {
	      const message = e && e.detail && e.detail.message;
	      if (message) setToast(message);
	    };
	    window.addEventListener('flow:toast', onToast);
	    return () => window.removeEventListener('flow:toast', onToast);
	  }, []);

	  useEffect(() => {
	    if (!window.EventSource) {
	      refreshUIData();
	      return;
	    }
	    let closed = false;
	    const stream = new EventSource('/api/events');
	    stream.addEventListener('ui-data', (event) => {
	      if (closed) return;
	      try { applyUIData(JSON.parse(event.data)); } catch (_) {}
	    });
	    stream.addEventListener('ui-error', (event) => {
	      if (closed) return;
	      try { setToast(JSON.parse(event.data).message || 'UI stream error'); } catch (_) {}
	    });
	    return () => { closed = true; stream.close(); };
	  }, []);

	  // Inbox-item WS hook → ui-data refresh. The inbox SyncStatusStrip
	  // listens on /ws/events and dispatches `flow:ui-data:refresh` when a
	  // fresh slack/github event arrives so the inbox list updates without
	  // a manual reload. Decoupled via CustomEvent so the WS layer doesn't
	  // need a direct callback into the app shell. Debounced to one refresh
	  // per 250ms in case a poll cycle emits a burst of new items.
	  useEffect(() => {
	    let pending = null;
	    const onRefresh = () => {
	      if (pending) return;
	      pending = setTimeout(() => { pending = null; refreshUIData(); }, 250);
	    };
	    window.addEventListener('flow:ui-data:refresh', onRefresh);
	    return () => {
	      window.removeEventListener('flow:ui-data:refresh', onRefresh);
	      if (pending) clearTimeout(pending);
	    };
	  }, []);

	  useEffect(() => {
	    const onPopState = () => {
	      setRoute(routeFromLocation());
	      setFocus(null);
	    };
	    window.addEventListener('popstate', onPopState);
	    return () => window.removeEventListener('popstate', onPopState);
	  }, []);

  // Keyboard shortcuts
  useEffect(() => {
    let pending = null;
    const onKey = (e) => {
      const tag = (e.target.tagName || '').toLowerCase();
      // Cmd/Ctrl+K is a global gesture and must work even when an editable
      // element has focus — including xterm.js's hidden helper textarea,
      // which would otherwise swallow it.
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); setPalette(true); return; }
      if (tag === 'input' || tag === 'textarea') { if (e.key === 'Escape') e.target.blur(); return; }
      if (e.key === '/') { e.preventDefault(); setPalette(true); return; }
      if (e.key === '?') { e.preventDefault(); setHelp(true); return; }
      if (e.key === 'Escape') { setFocus(null); setPalette(false); setQR(false); setHelp(false); setCreateOpen(false); setCreatePreselect(null); setConfirm(null); setProviderPick(null); return; }
      if (e.key === 'g') { pending = 'g'; setTimeout(() => { pending = null; }, 800); return; }
      if (pending === 'g') {
        pending = null;
        if (e.key === 'm') { goto('mc'); }
        if (e.key === 's') { goto('sessions'); }
        if (e.key === 't') { goto('tasks'); }
        if (e.key === 'p') { goto('projects'); }
        if (e.key === 'b') { goto('playbooks'); }
        if (e.key === 'c') { goto('memories'); }
        if (e.key === 'k') { goto('kb'); }
        if (e.key === 'w') { goto('workdirs'); }
        if (e.key === 'i') { goto('inbox'); }
        if (e.key === 'n') { goto('monitor'); }
        if (e.key === 'x') { goto('trash'); }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const isSession = route.startsWith('session/');
  const sessionSlug = isSession ? route.slice(8) : '';
  const rawSessionAgent = isSession ? AGENTS.find(a => a.slug === sessionSlug) : null;
  const doneAgent = isSession ? DONE_AGENTS.find(a => a.slug === sessionSlug) || ((window.MC.DEAD_AGENT && window.MC.DEAD_AGENT.slug === sessionSlug) ? window.MC.DEAD_AGENT : null) : null;
  const bridgeAgent = isSession ? bridgeAgents[sessionSlug] : null;
  const rawSessionTranscriptCount = (rawSessionAgent?.transcript || []).length;
  const bridgeTranscriptCount = (bridgeAgent?.transcript || []).length;
  const mergedTerminalMode = (rawMode, bridgeMode) => {
    if (bridgeMode === 'browser' || bridgeMode === 'shared') return bridgeMode;
    if (rawMode === 'browser' || rawMode === 'shared') return rawMode;
    if (rawMode === 'native' || bridgeMode === 'native') return 'native';
    return bridgeMode || rawMode;
  };
  const sessionAgent = rawSessionAgent && bridgeAgent
    ? { ...rawSessionAgent, ...bridgeAgent, terminal: { ...(rawSessionAgent.terminal || {}), ...(bridgeAgent.terminal || {}), mode: mergedTerminalMode(rawSessionAgent.terminal?.mode, bridgeAgent.terminal?.mode) } }
    : rawSessionAgent || (!doneAgent ? bridgeAgent : null);
  const displayAgents = AGENTS.map(agent => {
    const bridge = bridgeAgents[agent.slug];
    if (!bridge) return agent;
    return { ...agent, ...bridge, terminal: { ...(agent.terminal || {}), ...(bridge.terminal || {}), mode: mergedTerminalMode(agent.terminal?.mode, bridge.terminal?.mode) } };
  });
  Object.values(bridgeAgents).forEach(agent => {
    if (agent && agent.slug && !displayAgents.some(existing => existing.slug === agent.slug)) displayAgents.push(agent);
  });
  const isPlaybook = route.startsWith('playbook/');
  const playbookSlug = isPlaybook ? route.slice(9) : null;
  const isProject = route.startsWith('project/');
  const projectSlug = isProject ? route.slice(8) : null;
  const isTask = route.startsWith('task/');
  const taskSlug = isTask ? route.slice(5) : null;
  const openAddTaskForProject = (slug) => {
    const proj = PROJECTS_MC.find(p => p.slug === slug);
    setCreatePreselect({ project: slug, workDir: proj?.work_dir || '' });
    setCreateOpen(true);
  };
  const closeCreateModal = () => {
    setCreateOpen(false);
    setCreatePreselect(null);
  };
  const liveRunning = displayAgents.filter(a => a.status === 'running').length;
  const waiting = displayAgents.filter(a => a.status === 'waiting').length;
  const doneTranscriptCount = (doneAgent?.transcript || []).length;
  const completedAgent = doneAgent ? (bridgeTranscriptCount > doneTranscriptCount ? bridgeAgent : doneAgent) : null;

  useEffect(() => {
    if (!isSession || !sessionSlug) return;
    let cancelled = false;
    const loadBridge = () => {
      fetch(`/api/tasks/${encodeURIComponent(sessionSlug)}/bridge`, { cache: 'no-store' })
        .then(resp => resp.ok ? resp.json() : null)
        .then(agent => {
          if (cancelled || !agent || !agent.slug) return;
          setBridgeAgents(prev => {
            const existing = prev[agent.slug];
            const existingCount = (existing?.transcript || []).length;
            const nextCount = (agent.transcript || []).length;
            const existingMode = existing?.terminal?.mode;
            const nextMode = agent?.terminal?.mode;
            const existingWait = existing?.waiting_for ? JSON.stringify(existing.waiting_for) : '';
            const nextWait = agent?.waiting_for ? JSON.stringify(agent.waiting_for) : '';
            if (existing && existingCount >= nextCount && existingMode === nextMode && existing.status === agent.status && existing.task_status === agent.task_status && existing.runtime_event === agent.runtime_event && existingWait === nextWait && existing.last_action === agent.last_action && existing.last_activity_sec === agent.last_activity_sec) return prev;
            return { ...prev, [agent.slug]: agent };
          });
          const doneIdx = DONE_AGENTS.findIndex(a => a.slug === agent.slug);
          if (doneIdx >= 0) DONE_AGENTS[doneIdx] = agent;
          if (window.MC.DEAD_AGENT && window.MC.DEAD_AGENT.slug === agent.slug) window.MC.DEAD_AGENT = agent;
        })
        .catch(() => {});
    };
    loadBridge();
    const timer = setInterval(loadBridge, 2500);
    return () => { cancelled = true; clearInterval(timer); };
  }, [isSession, sessionSlug]);

  const navCounts = {
    mc: AGENTS.length,
    sessions: AGENTS.length,
    tasks: AGENTS.length + BACKLOG.length,
    projects: PROJECTS_MC.length,
    playbooks: PLAYBOOKS_MC.length,
    workdirs: WORKDIRS.length,
    memories: AGENT_MEMORY_SOURCES.length,
    kb: KB_FILES.length,
    // Inbox badge: count only ACTIONABLE slack/github items so the
    // sidebar number tracks what's actually waiting on you in the Inbox
    // view. Dismissed notifications drop out automatically because
    // ListMonitorNotifications already filters WHERE status != 'dismissed'.
    // Includes (a) unread notifications attached to slack/github events
    // and (b) events with a "ping" outcome (rule routed to user-attention).
    inbox: (window.MC.MONITOR && (
      ((window.MC.MONITOR.notifications || []).filter(n =>
        n.status !== 'dismissed' &&
        (n.source === 'slack' || n.source === 'github')
      ).length) +
      ((window.MC.MONITOR.events || []).filter(e =>
        e.outcome && e.outcome.action === 'ping' &&
        (e.source === 'slack' || e.source === 'github')
      ).length)
    )) || 0,
	    trash: (TRASH && TRASH.total) || 0,
  };

  const crumbs = isSession
    ? [{ l: 'Sessions', r: 'sessions' }, { l: sessionAgent?.slug || sessionSlug, cur: true }]
    : [{ l: TITLES[route]?.[0], cur: true }];

  return (
    <div className="app" data-theme="dark">
      <aside className="sidebar">
        <div className="brand">
          <FlowLogo size={32}/>
        </div>
        <nav className="nav">
          {NAV.map(grp => (
            <div key={grp.group} className="nav-group">
              <div className="nav-section">{grp.group}</div>
              {grp.items.map(it => (
                <a
                  key={it.id}
                  className={`${route === it.id || (it.id === 'sessions' && isSession) ? 'active' : ''} ${it.soon ? 'soon' : ''}`}
                  onClick={() => { if (it.soon) { setToast(`${it.label.replace(/\u00b7/g,'·')} · coming soon`); return; } goto(it.id); }}
                  title={it.soon ? 'Coming soon' : undefined}
                >
                  <Icon name={it.icon} size={14}/>
                  <span>{it.label}</span>
                  {it.soon
                    ? <span className="soon-badge mono">soon</span>
                    : it.kbd ? <span className="kbd">{it.kbd}</span>
                    : navCounts[it.id] ? <span className="ct">{navCounts[it.id]}</span> : null}
                </a>
              ))}
            </div>
          ))}
        </nav>
        <div className="sidebar-foot">
          <button className="btn sm" onClick={() => setQR(true)} style={{justifyContent: 'flex-start'}}><Icon name="qr-code" size={12}/>Remote · phone</button>
          <button className="btn sm" onClick={() => setHelp(true)} style={{justifyContent: 'flex-start'}}><Icon name="keyboard" size={12}/>Shortcuts <span className="kbd" style={{marginLeft: 'auto'}}>?</span></button>
          {(() => {
            const db = window.MC.FLOWDB || FLOWDB_DEFAULT;
            const path = db.display_path || '~/.flow/flow.db';
            const size = db.exists ? db.human_size : (db.human_size || '—');
            const title = db.path ? `${db.path}\n${db.bytes || 0} bytes` : path;
            return (
              <div
                className="mono"
                style={{fontSize: 9.5, color: 'var(--text-faint)', textAlign: 'center', paddingTop: 4, display: 'flex', justifyContent: 'center', alignItems: 'center', gap: 6}}
                title={title}
              >
                <span>{path}</span>
                <span style={{opacity: 0.5}}>·</span>
                <span>{size}</span>
              </div>
            );
          })()}
        </div>
      </aside>

      <header className="topbar">
        <div className="crumbs">
          {crumbs.map((c, i) => (
            <span key={i} className="crumb-seg">
              {i > 0 && <span className="sep">›</span>}
              {c.cur ? <span className="cur mono">{c.l}</span> : <span style={{cursor:'pointer'}} onClick={() => c.r && goto(c.r)}>{c.l}</span>}
            </span>
          ))}
        </div>
        <div className="search" onClick={() => setPalette(true)}>
          <Icon name="search" size={13} className="ic"/>
          <input placeholder="Search briefs, updates, memories, tasks, or commands" readOnly/>
          <span className="kbd">Cmd/Ctrl K</span>
        </div>
        <div className="right">
          <NotificationsBell waitingCount={waiting} liveCount={liveRunning} goto={goto} action={action} agents={displayAgents}/>
          <button className="icon-btn" onClick={() => setHelp(true)} title="Shortcuts">?</button>
        </div>
      </header>

      <main className="main">
        {route === 'mc' && <MissionControl focus={focus} setFocus={setFocus} action={action} sort={sort} setSort={setSort} goto={goto}/>}
        {route === 'sessions' && <SessionsGrid setFocus={setFocus} action={action} goto={goto}/>}
        {isSession && sessionAgent && <SessionDetail key={`session-${sessionAgent.slug}`} agent={sessionAgent} goto={goto} action={action} gitDiffOpen={gitDiffOpen} toggleGitDiff={() => setGitDiffOpen(v => !v)} artifactsOpen={artifactsOpen} toggleArtifacts={() => setArtifactsOpen(v => !v)}/>}
        {isSession && !sessionAgent && completedAgent && <CompletedSessionView key={`done-${completedAgent.slug}`} agent={completedAgent} goto={goto} action={action} gitDiffOpen={gitDiffOpen} toggleGitDiff={() => setGitDiffOpen(v => !v)} artifactsOpen={artifactsOpen} toggleArtifacts={() => setArtifactsOpen(v => !v)}/>}
        {isSession && !sessionAgent && !completedAgent && <SessionFallback slug={sessionSlug} goto={goto} action={action}/>}
        {route === 'tasks' && <TasksList setFocus={setFocus} action={action} goto={goto}/>}
        {route === 'inbox' && <InboxView action={action} goto={goto}/>}
        {isTask && <TaskDetail key={`task-${taskSlug}`} slug={taskSlug} goto={goto} action={action} refreshKey={dataTick}/>}
        {route === 'projects' && <ProjectsList goto={goto} action={action}/>}
        {isProject && <ProjectDetail slug={projectSlug} goto={goto} action={action} onAddTask={openAddTaskForProject} refreshKey={dataTick}/>}
        {route === 'playbooks' && <PlaybooksList action={action} goto={goto}/>}
        {isPlaybook && <PlaybookDetail slug={playbookSlug} goto={goto} action={action}/>}
        {route === 'workdirs' && <WorkdirsView action={action}/>}
        {route === 'memories' && <MemorySourcesView/>}
        {route === 'kb' && <KBView/>}
        {route === 'trash' && <TrashView action={action}/>}
      </main>

      {focus && <window.MC.FocusDrawer agent={focus} onClose={() => setFocus(null)} goto={goto} action={action}/>}
      {palette && <CommandPalette onClose={() => setPalette(false)} goto={goto} action={action}/>}
      {qr && <QRModal onClose={() => setQR(false)}/>}
      {help && <ShortcutsOverlay onClose={() => setHelp(false)}/>}
      {createOpen && <CreateFlowModal onClose={closeCreateModal} projects={PROJECTS_MC.map(p => p.slug)} action={action} preselect={createPreselect}/>}
      {createProjectOpen && <CreateProjectModal onClose={() => setCreateProjectOpen(false)} action={action}/>}
      {providerPick && <ProviderChoiceModal target={providerPick} providers={capabilityList('providers')} onClose={() => setProviderPick(null)} onPick={(provider, permission_mode) => { const target = providerPick; setProviderPick(null); action('spawn', { ...target, provider, permission_mode, _providerChosen: true }); }}/>}
      {confirm && <ConfirmModal {...confirm} onClose={() => setConfirm(null)}/>}
      {toast && (
        <div style={{position: 'fixed', bottom: 20, left: '50%', transform: 'translateX(-50%)', background: 'var(--surface)', border: '1px solid var(--border-strong)', padding: '8px 14px', borderRadius: 4, fontFamily: 'var(--mono)', fontSize: 12, color: 'var(--text)', zIndex: 200, boxShadow: '0 8px 24px rgba(0,0,0,0.4)'}}>
          {toast}
        </div>
      )}
    </div>
  );
};

const Root = () => (
  <ClockProvider><App/></ClockProvider>
);

ReactDOM.createRoot(document.getElementById('root')).render(<Root/>);
