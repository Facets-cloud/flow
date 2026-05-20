// Mission Control — screens + app shell
const {
  AGENTS, DEAD_AGENT, DONE_AGENTS = [], BACKLOG, DONE_TASKS = [], KB_FILES, AGENT_MEMORY_SOURCES = [], WORKDIRS, PLAYBOOKS_MC, PROJECTS_MC, ACTIVITY_HEATMAP, TRASH,
  SAMPLE_TRANSCRIPT, TERMINAL_SAMPLES, SAMPLE_DIFF_FILES,
  formatAge, formatActivity, fmtTokens, shortUUID, rerenderIcons,
  Icon, FlowMark, FlowLogo, SkeletonRows, StatusPill, TaskStatePill, PriorityPill, AgentChip, ProviderMark, BranchChip, Dot, PixelIndicator, Sparkline,
  DependencyBadges, AgentTile, TranscriptView, ActivityHeatmap, FocusDrawer, ClockProvider, ClockCtx,
} = window.MC;

const capabilityList = (group) => {
  const caps = (window.MC && window.MC.CAPABILITIES) || {};
  return Array.isArray(caps[group]) ? caps[group] : [];
};
const capabilityFor = (group, id) => capabilityList(group).find(item => item.id === id) || { id, label: id, available: true };
const isCapabilityAvailable = (group, id) => {
  const items = capabilityList(group);
  if (!items.length) return true;
  return !!capabilityFor(group, id).available;
};
const capabilityReason = (group, id) => capabilityFor(group, id).reason || 'not available';
const defaultAvailableProvider = () => {
  const providers = capabilityList('providers');
  if (!providers.length) return 'claude';
  return (providers.find(p => p.available) || providers[0] || { id: 'claude' }).id;
};
const anyProviderAvailable = () => {
  const providers = capabilityList('providers');
  return !providers.length || providers.some(p => p.available);
};
const taskStartBlocker = (task = {}) => {
  // Multi-parent: prefer the parents array; fall back to legacy single parent.
  let parents = Array.isArray(task.parents) && task.parents.length
    ? task.parents
    : (task.parent ? [task.parent] : (task.parent_slug ? [{ slug: task.parent_slug, status: 'unknown' }] : []));
  const pending = parents.filter(p => p && p.status !== 'done');
  const allDone = parents.length > 0 && pending.length === 0;

  const waiting = String(task.waiting_on || '').trim();
  // Stale waiting_on note (intake-time description like "depends on <slug> …"):
  // when ALL parents are done AND the note mentions at least one parent slug,
  // treat it as resolved. Unrelated waiting_on notes still block.
  const waitingLower = waiting.toLowerCase();
  const waitingIsParentEcho = waiting && allDone && parents.some(p =>
    p && p.slug && waitingLower.includes(String(p.slug).toLowerCase())
  );
  if (waiting && !waitingIsParentEcho) return `Blocked: ${waiting}`;
  if (pending.length === 1) {
    const p = pending[0];
    return `Depends on ${p.slug}${p.status ? ` (${p.status})` : ''}`;
  }
  if (pending.length > 1) {
    return `Blocked by ${pending.length} dependencies: ${pending.map(p => p.slug).join(', ')}`;
  }
  return '';
};
const missionGreeting = () => {
  const hour = new Date().getHours();
  if (hour < 12) return 'Good morning';
  if (hour < 17) return 'Good afternoon';
  return 'Good evening';
};

// ───────── Mission Control ──────────────────────────────────────────────
const MissionControl = ({ focus, setFocus, action, sort, setSort, goto }) => {
  const sorted = useMemo(() => {
    const list = [...AGENTS];
    const order = { running: 0, waiting: 1, idle: 2, stale: 3, dead: 4 };
    if (sort === 'priority') {
      const p = { high: 0, medium: 1, low: 2 };
      list.sort((a, b) => p[a.priority] - p[b.priority] || (order[a.status] - order[b.status]));
    } else if (sort === 'activity') {
      list.sort((a, b) => a.last_activity_sec - b.last_activity_sec);
    } else if (sort === 'age') {
      list.sort((a, b) => b.started_min - a.started_min);
    } else if (sort === 'alpha') {
      list.sort((a, b) => a.slug.localeCompare(b.slug));
    }
    // waiting always pinned to top
    list.sort((a, b) => (a.status === 'waiting' ? -1 : 0) - (b.status === 'waiting' ? -1 : 0));
    return list;
  }, [sort, AGENTS.length, AGENTS.map(a => `${a.slug}:${a.status}:${a.last_activity_sec}`).join('|')]);

  const counts = useMemo(() => {
    return AGENTS.reduce((acc, a) => { acc[a.status] = (acc[a.status] || 0) + 1; return acc; }, {});
  }, [AGENTS.length, AGENTS.map(a => a.status).join('|')]);
  const monitor = monitorState();
  const unread = (monitor.notifications || []).filter(n => n.status === 'unread').length;

  return (
    <div>
      <div className="hero mc-landing">
        <div className="hero-greeting">
          <div className="overview-kicker mono">Mission Control</div>
          <h1>{missionGreeting()}, Vishnu</h1>
          <p>{counts.running || 0} running · {counts.waiting || 0} waiting on you · {unread} unread notifications</p>
          <div className="hero-actions">
            <button className="btn sm primary" onClick={() => action('spawn-prompt')}><Icon name="plus" size={11}/>New task</button>
            <button className="btn sm" onClick={() => goto && goto('inbox')}><Icon name="inbox" size={11}/>Inbox</button>
          </div>
        </div>
        <div className="hero-stats">
          <div className="stat running">
            <div className="num">{counts.running || 0}</div>
            <div className="lbl">running</div>
          </div>
          <div className="stat waiting">
            <div className="num">{counts.waiting || 0}</div>
            <div className="lbl">waiting on you</div>
          </div>
          <div className="stat idle">
            <div className="num">{counts.idle || 0}</div>
            <div className="lbl">idle</div>
          </div>
          <div className="stat stale">
            <div className="num">{counts.stale || 0}</div>
            <div className="lbl">stale</div>
          </div>
        </div>
        <ActivityHeatmap data={ACTIVITY_HEATMAP}/>
      </div>

      <div className="section-head">
        <h2>Agents</h2>
        <span className="count mono">{AGENTS.length} tracked · {counts.waiting || 0} needs you</span>
        <div className="right">
          <div className="tab-strip">
            {['priority','activity','age','alpha'].map(s => (
              <button key={s} className={sort===s?'active':''} onClick={() => setSort(s)}>{s}</button>
            ))}
          </div>
        </div>
      </div>

      <div className="agent-grid">
        {sorted.length
          ? sorted.map(a => <AgentTile key={a.slug} agent={a} onOpen={setFocus} onAction={action}/>)
          : <BrandEmpty title="No agents running" body="Create a flow or spawn a backlog item to start a session."/>}
      </div>

      <div className="section-head" style={{marginTop: 28}}>
        <h2>Backlog · spawn an agent</h2>
        <span className="count mono">{BACKLOG.length} ready</span>
      </div>
      <div className="ribbon">
        {BACKLOG.length ? BACKLOG.map(b => {
          const blockReason = taskStartBlocker(b);
          const available = anyProviderAvailable() && !blockReason;
          return (
            <div
              key={b.slug}
              className={`ribbon-chip ${available ? '' : 'disabled'}`}
              title={blockReason || (available ? 'Choose Claude or Codex' : 'No supported agent binary found on PATH')}
              onClick={() => available && action('spawn', b)}
            >
              <div className="top">
                <PriorityPill priority={b.priority}/>
                <span className="mono" style={{fontSize: 11}}>{b.slug}</span>
                <span style={{marginLeft: 'auto', display: 'inline-flex', gap: 3}} title="Choose Claude or Codex">
                  <ProviderMark provider="claude" size={12}/>
                  <ProviderMark provider="codex" size={12}/>
                </span>
              </div>
              <div className="nm">{b.name}</div>
              <div className="meta">{b.project}{b.due ? ` · due ${b.due}` : ''}{blockReason ? ' · blocked' : (available ? ' · choose provider' : ' · no provider available')}</div>
              <DependencyBadges task={b} compact/>
            </div>
          );
        }) : <BrandEmpty compact title="Backlog is clear" body="New task briefs will appear here when they are ready to spawn."/>}
      </div>
    </div>
  );
};

// ───────── Monitor helpers ─────────────────────────────────────────────
const monitorState = () => window.MC.MONITOR || { notifications: [], events: [], rules: [], sources: [], unread: 0, approvals: 0 };

const OverviewCard = ({ title, count, actionLabel, onAction, children }) => (
  <section className="overview-card">
    <div className="overview-card-head">
      <h2>{title}</h2>
      <span className="count mono">{count}</span>
      {actionLabel && <button className="btn sm" onClick={onAction}>{actionLabel}</button>}
    </div>
    <div className="overview-card-body">{children}</div>
  </section>
);

const EmptyLine = ({ text }) => (
  <div className="overview-empty">
    <FlowMark size={20} title=""/>
    <span className="mono">{text}</span>
  </div>
);

const BrandEmpty = ({ title, body, compact = false }) => (
  <div className={`empty brand-empty ${compact ? 'compact' : ''}`}>
    <FlowMark size={compact ? 24 : 34} title=""/>
    <h3>{title}</h3>
    {body && <p>{body}</p>}
  </div>
);

const githubRepoFromNotification = (n) => {
  const urlMatch = String(n.url || '').match(/github\.com\/([^/]+\/[^/#?]+)/i);
  if (urlMatch) return urlMatch[1];
  const titleMatch = String(n.title || '').match(/:\s*([^#]+?)\s+#\d+/);
  if (titleMatch) return titleMatch[1].trim();
  const body = String(n.body || '').trim();
  if (/^[\w.-]+\/[\w.-]+$/.test(body)) return body;
  return 'GitHub';
};

const notificationGroupMeta = (n) => {
  const source = (n.source || 'flow').toLowerCase();
  if (source === 'github') {
    const repo = githubRepoFromNotification(n);
    return { key: `github:${repo}`, label: repo, source: 'github', icon: 'git-pull-request' };
  }
  if (source === 'slack') {
    const match = (n.title || '').match(/\bin\s+(.+)$/);
    const where = match ? match[1] : (n.kind || 'Slack');
    return { key: `slack:${where}`, label: where, source: 'slack', icon: 'message-square' };
  }
  if (source === 'agent') {
    return { key: `agent:${n.kind || 'session'}`, label: n.kind || 'Agent sessions', source: 'agent', icon: 'bot' };
  }
  return { key: `${source}:${n.kind || 'notification'}`, label: n.kind || source, source, icon: 'bell' };
};

const groupedNotifications = (items) => {
  const groups = [];
  const byKey = new Map();
  (items || []).forEach(n => {
    const meta = notificationGroupMeta(n);
    if (!byKey.has(meta.key)) {
      const group = { ...meta, items: [] };
      byKey.set(meta.key, group);
      groups.push(group);
    }
    byKey.get(meta.key).items.push(n);
  });
  return groups;
};

const NotificationGroup = ({ group, action }) => {
  const [open, setOpen] = useState(true);
  const unread = group.items.filter(n => n.status === 'unread').length;
  const approvals = group.items.filter(n => n.level === 'approval').length;
  return (
    <div className={`notif-group source-${group.source}`}>
      <button className="notif-group-head" onClick={() => setOpen(v => !v)} aria-expanded={open}>
        <Icon name={open ? 'chevron-down' : 'chevron-right'} size={12}/>
        <Icon name={group.icon} size={12}/>
        <span className="mono">{group.label}</span>
        <span className="notif-group-count mono">{group.items.length} items</span>
        {unread > 0 && <span className="notif-group-badge mono">{unread} unread</span>}
        {approvals > 0 && <span className="notif-group-badge approval mono">{approvals} approvals</span>}
      </button>
      {open && group.items.map(n => (
        <div key={n.id} className={`overview-row ${n.level}`}>
          <span className="source mono">{n.kind || n.source || 'flow'}</span>
          <div className="body"><div>{n.title}</div>{n.body && <p>{n.body}</p>}<p className="mono">{n.mode || 'notify'}</p></div>
          <div className="row-actions">
            {n.url && <a className="btn sm" href={n.url} target="_blank" rel="noreferrer">Open</a>}
            {n.source !== 'agent' && <button className="btn sm primary" onClick={() => action('notification-start-agent', { event_id: n.event_id })}>Start agent</button>}
            <button className="btn sm" onClick={() => action('notification-dismiss', { slug: n.id })}>Dismiss</button>
          </div>
        </div>
      ))}
    </div>
  );
};

// Inbox scope: only external work signals that can be triaged into an agent
// spawn live here. Agent-hook events (Claude/Codex runtime telemetry —
// permission prompts, stop/start, idle) are observability data for the
// session UI, not work to triage; surfacing them in the inbox conflates
// "what does the platform need from me?" with "what's my agent doing right
// now?" If you add a new external source (linear, pagerduty, ...) and it
// should appear in the inbox, list it here. Anything not in this allowlist
// stays in monitor_events / monitor_notifications but is invisible to the
// inbox view-model.
const INBOX_SOURCES = new Set(['slack', 'github']);
const isInboxSource = (source) => INBOX_SOURCES.has(String(source || '').toLowerCase());

const monitorRuleFor = (monitor, source, kind) => (monitor.rules || []).find(r => r.source === source && r.kind === kind) || null;
const monitorOutcomeMeta = (action) => ({
  spawn: { label: 'spawned', icon: 'radio', cls: 'spawn' },
  draft: { label: 'drafted', icon: 'file-text', cls: 'draft' },
  ping: { label: 'needs attention', icon: 'alert-circle', cls: 'ping' },
  ignore: { label: 'ignored', icon: 'circle-off', cls: 'ignore' },
}[action || ''] || { label: 'unrouted', icon: 'circle', cls: 'none' });
const monitorItemTime = (item) => item.last_seen_at || item.created_at || item.first_seen_at || '';
const monitorItemNeedsReview = (item) => {
  const action = item.outcome?.action || '';
  const note = String(item.outcome?.note || '').toLowerCase();
  return item.level === 'approval' || action === 'ping' || note.includes('approval') || note.includes('secret') || note.includes('write') || note.includes('reply') || note.includes('push') || note.includes('merge');
};
const buildInboxItems = (monitor) => {
  const notifByEvent = new Map();
  // Pre-filter events to inbox sources so the eventIDs set and the
  // subsequent extraNotifications filter both operate on the same scoped
  // universe. Without this pre-filter, an agent_hook event with an
  // attached notification would slip in via the "orphan notification"
  // branch even though we'd already excluded its event.
  const inboxEvents = (monitor.events || []).filter(e => isInboxSource(e.source));
  const eventIDs = new Set(inboxEvents.map(e => e.id));
  (monitor.notifications || []).forEach(n => {
    if (n.event_id && !notifByEvent.has(n.event_id)) notifByEvent.set(n.event_id, n);
  });
  const eventItems = inboxEvents.map(e => {
    const n = notifByEvent.get(e.id) || {};
    const rule = monitorRuleFor(monitor, e.source, e.kind);
    return {
      id: e.id,
      event_id: e.id,
      notification_id: n.id,
      notification_status: n.status,
      source: e.source || n.source || 'flow',
      kind: e.kind || n.kind || 'event',
      title: e.title || n.title || 'Incoming item',
      body: e.body || n.body || '',
      url: e.url || n.url || '',
      severity: e.severity || n.level || 'info',
      level: n.level || e.severity || 'info',
      status: e.status || n.status || 'new',
      first_seen_at: e.first_seen_at || n.created_at || '',
      last_seen_at: e.last_seen_at || n.created_at || '',
      mode: e.mode || n.mode || rule?.mode || '',
      outcome: e.outcome || n.outcome || null,
      rule,
      durable: true,
    };
  });
  // Orphan notifications (no event_id, or event_id we filtered out) also
  // get the source scope. This is what keeps agent_hook attention pings
  // out of the inbox even when they're attached as a notification rather
  // than an event row.
  const extraNotifications = (monitor.notifications || [])
    .filter(n => isInboxSource(n.source))
    .filter(n => !n.event_id || !eventIDs.has(n.event_id))
    .map(n => ({
      id: n.id,
      event_id: n.event_id,
      notification_id: n.id,
      notification_status: n.status,
      source: n.source || 'agent',
      kind: n.kind || 'attention',
      title: n.title || 'Incoming item',
      body: n.body || '',
      url: n.url || '',
      severity: n.level || 'info',
      level: n.level || 'info',
      status: n.status || 'unread',
      created_at: n.created_at || '',
      mode: n.mode || '',
      outcome: n.outcome || null,
      rule: monitorRuleFor(monitor, n.source, n.kind),
      durable: false,
    }));
  return [...eventItems, ...extraNotifications].sort((a, b) => String(monitorItemTime(b)).localeCompare(String(monitorItemTime(a))));
};

const InboxItemRow = ({ item, action, goto }) => {
  const outcome = monitorOutcomeMeta(item.outcome?.action);
  const rule = item.rule;
  const taskSlug = item.outcome?.task_slug;
  const needsReview = monitorItemNeedsReview(item);
  // Cleaner header reads "Slack DM · 2m ago" instead of three separate
  // mono badges. The kind is conventionally lowercase in the DB
  // (`dm` / `mention` / `pr_review_requested`); we lift it to title case
  // for display while preserving the raw value for filter/search.
  const kindDisplay = String(item.kind || 'event').replace(/_/g, ' ');
  const timeAgo = formatSyncAgo(monitorItemTime(item));
  return (
    <article className={`inbox-item ${outcome.cls} ${needsReview ? 'needs-review' : ''}`}>
      <div className="inbox-item-rail">
        <span className={`inbox-outcome ${outcome.cls}`} title={outcome.label}>
          <Icon name={outcome.icon} size={14}/>
        </span>
      </div>
      <div className="inbox-item-main">
        <div className="inbox-item-top">
          <span className="inbox-source mono"><Icon name={sourceIcon(item.source)} size={11}/>{sourceLabel(item.source)}</span>
          <span className="inbox-kind mono">{kindDisplay}</span>
          <span className="inbox-time mono">{timeAgo}</span>
          {needsReview && <span className="pill waiting">needs approval</span>}
        </div>
        <h3>{item.title}</h3>
        {item.body && (
          <div className="inbox-untrusted">
            <div className="inbox-untrusted-head mono"><Icon name="shield-alert" size={11}/>Untrusted source text</div>
            <pre>{item.body}</pre>
          </div>
        )}
        <div className="inbox-route-line mono">
          <span><Icon name="route" size={11}/>{outcome.label}</span>
          {taskSlug && <span><Icon name="hash" size={11}/>{taskSlug}</span>}
          {item.outcome?.note && <span title={item.outcome.note}>note: {item.outcome.note}</span>}
        </div>
        <details className="inbox-rule-details">
          <summary className="mono">routing rule</summary>
          <div className="inbox-rule-line mono">
            {rule ? (
              <>
                <span>{rule.source}.{rule.kind}</span>
                <span>{rule.mode}</span>
                <span>{rule.provider || 'claude'}</span>
                <span>{rule.read_only ? 'read-only' : 'approval required'}</span>
                {(rule.project_slug || rule.work_dir) && <span>{rule.project_slug || rule.work_dir}</span>}
              </>
            ) : <span>no rule matched — defaults apply</span>}
          </div>
        </details>
      </div>
      <div className="inbox-actions">
        {item.url && <a className="btn sm" href={item.url} target="_blank" rel="noreferrer"><Icon name="external-link" size={11}/>Open in {sourceLabel(item.source)}</a>}
        {taskSlug
          ? <button className="btn sm primary" onClick={() => goto(`session/${taskSlug}`)}><Icon name="arrow-right" size={11}/>Open task</button>
          : item.event_id && <button className="btn sm primary" onClick={() => action('notification-start-agent', { event_id: item.event_id })}><Icon name="shield-check" size={11}/>Approve inspect</button>}
        {item.notification_id && item.notification_status === 'unread' && <button className="btn sm" onClick={() => action('notification-read', { slug: item.notification_id })}>Mark read</button>}
        {item.event_id
          ? <button className="btn sm" onClick={() => action('monitor-ignore-event', { event_id: item.event_id })}><Icon name="archive" size={11}/>Ignore</button>
          : item.notification_id && <button className="btn sm" onClick={() => action('notification-dismiss', { slug: item.notification_id })}>Dismiss</button>}
      </div>
    </article>
  );
};

const RuleEditor = ({ rule, action }) => {
  const [draft, setDraft] = useState(() => ({
    mode: rule.mode || 'notify',
    provider: rule.provider || '',
    project_slug: rule.project_slug || '',
    work_dir: rule.work_dir || '',
    prompt_template: rule.prompt_template || '',
    read_only: rule.read_only !== false,
  }));
  useEffect(() => {
    setDraft({
      mode: rule.mode || 'notify',
      provider: rule.provider || '',
      project_slug: rule.project_slug || '',
      work_dir: rule.work_dir || '',
      prompt_template: rule.prompt_template || '',
      read_only: rule.read_only !== false,
    });
  }, [rule.id, rule.mode, rule.provider, rule.project_slug, rule.work_dir, rule.prompt_template, rule.read_only]);
  const modes = ['off','log','notify','approval','auto_task','auto_agent','auto_agent_draft_only','summarize'];
  const save = () => action('set-rule-mode', {
    source: rule.source,
    rule_kind: rule.kind,
    mode: draft.mode,
    provider: draft.provider,
    project: draft.project_slug,
    work_dir: draft.work_dir,
    prompt: draft.prompt_template,
    read_only: draft.read_only,
    rule_update: true,
  });
  return (
    <details className="rule-card">
      <summary>
        <span className="mono">{rule.source}.{rule.kind}</span>
        <span className={`rule-mode mono ${draft.mode}`}>{draft.mode}</span>
        <span className={`rule-readonly mono ${draft.read_only ? 'on' : 'off'}`}>{draft.read_only ? 'read-only' : 'gated'}</span>
      </summary>
      <div className="rule-edit-grid">
        <label><span>Mode</span><select className="form-input mono" value={draft.mode} onChange={e => setDraft(d => ({ ...d, mode: e.target.value }))}>{modes.map(m => <option key={m} value={m}>{m}</option>)}</select></label>
        <label><span>Provider</span><select className="form-input mono" value={draft.provider} onChange={e => setDraft(d => ({ ...d, provider: e.target.value }))}><option value="">claude default</option><option value="claude">claude</option><option value="codex">codex</option></select></label>
        <label><span>Project</span><input className="form-input mono" value={draft.project_slug} onChange={e => setDraft(d => ({ ...d, project_slug: e.target.value }))} placeholder="project slug"/></label>
        <label><span>Workdir</span><input className="form-input mono" value={draft.work_dir} onChange={e => setDraft(d => ({ ...d, work_dir: e.target.value }))} placeholder="/path/to/repo"/></label>
        <label className="rule-edit-wide"><span>Prompt template</span><textarea className="form-input mono" rows="3" value={draft.prompt_template} onChange={e => setDraft(d => ({ ...d, prompt_template: e.target.value }))} placeholder="Trusted instruction for inspect/report-only work."/></label>
        <label className="rule-check"><input type="checkbox" checked={draft.read_only} onChange={e => setDraft(d => ({ ...d, read_only: e.target.checked }))}/><span>Only auto-open read-only inspect/report work</span></label>
        <button className="btn sm primary" onClick={save}><Icon name="save" size={11}/>Save rule</button>
      </div>
    </details>
  );
};

const InboxRulePanel = ({ monitor, action }) => (
  <section className="overview-card inbox-rule-panel">
    <div className="overview-card-head"><h2>Rule routing</h2><span className="count mono">{(monitor.rules || []).length}</span></div>
    <div className="inbox-rule-list">
      {(monitor.rules || []).map(rule => <RuleEditor key={rule.id} rule={rule} action={action}/>)}
    </div>
  </section>
);

// SOURCE_META keeps the per-source presentation (label + icon) in one place
// so SyncStatusStrip, the filter chips, and InboxItemRow all agree on how
// a source name renders. Add an entry when a new INBOX_SOURCES member ships.
const SOURCE_META = {
  slack:  { label: 'Slack',  icon: 'message-square' },
  github: { label: 'GitHub', icon: 'git-pull-request' },
};
const sourceLabel = (source) => SOURCE_META[String(source || '').toLowerCase()]?.label || source;
const sourceIcon = (source) => SOURCE_META[String(source || '').toLowerCase()]?.icon || 'bell';
const isSlackLegacyPollingError = (s) => {
  if (String(s?.source || '').toLowerCase() !== 'slack') return false;
  const err = String(s?.last_error || '').toLowerCase();
  return err.includes('conversations.history') ||
    err.includes('users.conversations') ||
    err.includes('search.messages') ||
    err.includes('slack polling');
};

// fireDesktopNotification fans a single inbox_item event through the Web
// Notifications API, which on macOS routes to the system Notification
// Center automatically (and obeys Do Not Disturb / Focus filters). Three
// guardrails:
//
//   1. The permission must be `granted`. We never spam the user with a
//      permission prompt — that's a separate user-gesture-triggered call
//      in EnableDesktopNotificationsButton below.
//   2. needs_review must be true. Without this gate every Slack DM would
//      pop, which is exactly the spam pattern flow tries to avoid.
//   3. Tab visibility is NOT a gate — Slack itself notifies you when its
//      tab is in the foreground; we mirror that behavior. If you want
//      silence-when-focused, mute via the OS Focus / DND.
//
// The notification's tag dedups across rapid arrivals of the same event:
// if the same event_id fires twice in 200ms, Chrome / Safari collapse them.
const fireDesktopNotification = (item) => {
  if (typeof window === 'undefined' || !('Notification' in window)) return;
  if (Notification.permission !== 'granted') return;
  if (!item || !item.needs_review) return;
  const title = `flow: ${sourceLabel(item.source)} — needs your attention`;
  const body = item.title ? `${item.title}${item.body ? '\n' + item.body.slice(0, 140) : ''}` : (item.body || 'New item in your inbox');
  try {
    const n = new Notification(title, {
      body,
      tag: `flow-inbox-${item.event_id || item.id}`,
      icon: '/assets/flow-mark.svg',
      requireInteraction: false,
    });
    n.onclick = () => {
      try { window.focus(); } catch {}
      try { window.location.href = '/inbox'; } catch {}
      n.close();
    };
  } catch (e) {
    // Some browsers (Firefox in private mode, Safari with restrictions)
    // throw on construction. Swallow — the inbox row still updates,
    // we just lose the OS-level ping.
  }
};

// EnableDesktopNotificationsButton surfaces the permission gate. Browser
// vendors require requestPermission() to be called from a user-gesture
// event handler (not from useEffect on mount) — that's why this lives as
// its own button. Renders three states:
//
//   - 'unsupported'  → no API in this browser; render nothing
//   - 'default'      → user hasn't decided yet; offer the prompt
//   - 'granted'      → notifications are on; show a quiet confirmation
//   - 'denied'       → user said no; show a hint that they need to change
//                      it in browser settings (we can't re-prompt)
const EnableDesktopNotificationsButton = () => {
  const supported = typeof window !== 'undefined' && 'Notification' in window;
  const [perm, setPerm] = useState(supported ? Notification.permission : 'unsupported');
  if (!supported) return null;
  const request = () => {
    Notification.requestPermission().then(result => setPerm(result));
  };
  if (perm === 'granted') {
    return (
      <span className="inbox-notif-state granted mono" title="macOS desktop notifications enabled for needs-review items">
        <Icon name="bell" size={11}/>notifications on
      </span>
    );
  }
  if (perm === 'denied') {
    return (
      <span className="inbox-notif-state denied mono" title="Re-enable in your browser's site settings to receive desktop notifications">
        <Icon name="bell-off" size={11}/>notifications blocked
      </span>
    );
  }
  return (
    <button className="btn sm" onClick={request} title="Get macOS desktop notifications for items that need your attention">
      <Icon name="bell" size={11}/>Enable desktop notifications
    </button>
  );
};

// formatSyncAgo renders an RFC3339 timestamp as "23s ago" / "5m ago".
// Returns "never" on empty input — chosen over an em-dash because users
// reading "synced never" understand it instantly, whereas "synced —"
// reads as a parse failure. The buckets cap at days so a fresh install
// that's been sitting for weeks doesn't sprout absurd minute counts.
const formatSyncAgo = (isoTime) => {
  if (!isoTime) return 'never';
  const t = new Date(isoTime).getTime();
  if (isNaN(t)) return 'never';
  const sec = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (sec < 5)     return 'just now';
  if (sec < 60)    return `${sec}s ago`;
  if (sec < 3600)  return `${Math.floor(sec/60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec/3600)}h ago`;
  return `${Math.floor(sec/86400)}d ago`;
};

// SyncStatusStrip layers two signals so the UI is correct AND live:
//
//   1. WebSocket subscription on /ws/events?types=monitor_sync — primary
//      signal. The server publishes a `monitor_sync` envelope
//      (eventMonitorSync) on every per-source transition. GitHub still
//      polls; Slack transitions are Socket Mode event deliveries.
//
//   2. /api/monitor/sync-state on a 30s backstop loop — catches state
//      drift if the WS connection dropped mid-event, AND seeds the
//      initial render before the first WS event lands.
//
//   3. A 1s "now" tick that re-renders the "Xs ago" label so it counts
//      up smoothly even without new server-side events.
//
// merge semantics: WS events merge by source (latest wins per source);
// API responses replace the whole array. Either way the rendered list
// is the union of known sources.
const SyncStatusStrip = ({ action }) => {
  const [statesBySource, setStatesBySource] = useState({});
  const [, setNow] = useState(0); // intentional re-render trigger for time labels
  useEffect(() => {
    let cancelled = false;
    const seedFromAPI = () => {
      fetch('/api/monitor/sync-state')
        .then(r => r.ok ? r.json() : Promise.reject(r.status))
        .then(data => {
          if (cancelled) return;
          const next = {};
          (data.states || []).forEach(s => { next[s.source] = s; });
          setStatesBySource(next);
        })
        .catch(() => { /* network blip — backstop will try again */ });
    };
    seedFromAPI();
    const poll = setInterval(seedFromAPI, 30000);
    const tick = setInterval(() => setNow(n => n + 1), 1000);

    // Live updates: open a filtered WS subscription so we only receive
    // the two event types the inbox cares about. The events_hub fans out
    // per type filter server-side, so we don't pay for events we don't
    // render.
    //   monitor_sync → "syncing now…" / "synced X ago" badges
    //   inbox_item   → a fresh slack/github event landed; trigger a
    //                  re-fetch of /api/ui-data AND maybe a desktop
    //                  notification when needs_review is true.
    const wsURL = (() => {
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      return `${proto}//${window.location.host}/ws/events?types=monitor_sync,inbox_item`;
    })();
    let ws;
    let reconnectTimer;
    const connect = () => {
      try {
        ws = new WebSocket(wsURL);
      } catch (e) {
        // Some browsers throw synchronously on invalid URLs; fall back to
        // polling-only via the 30s loop above. Schedule a reconnect in 5s
        // in case it was transient.
        reconnectTimer = setTimeout(connect, 5000);
        return;
      }
      ws.onmessage = (evt) => {
        if (cancelled) return;
        let env;
        try { env = JSON.parse(evt.data); } catch { return; }
        if (env.type === 'monitor_sync' && env.monitor_sync) {
          const s = env.monitor_sync;
          setStatesBySource(prev => ({
            ...prev,
            [s.source]: {
              source: s.source,
              is_syncing: !!s.is_syncing,
              last_status: s.last_status || 'unknown',
              last_sync_at: s.last_sync_at || prev[s.source]?.last_sync_at || '',
              last_error: s.last_error || '',
            },
          }));
          return;
        }
        if (env.type === 'inbox_item' && env.inbox_item) {
          // Two effects per fresh arrival:
          //   1. macOS desktop notification (gated on permission +
          //      needs_review inside fireDesktopNotification).
          //   2. Tell the app shell to re-fetch /api/ui-data so the
          //      inbox list picks up the new row without waiting for a
          //      page reload. Dispatched as a CustomEvent so we don't
          //      have to thread a callback through every component layer.
          fireDesktopNotification(env.inbox_item);
          try {
            window.dispatchEvent(new CustomEvent('flow:ui-data:refresh', {
              detail: { reason: 'inbox_item', event_id: env.inbox_item.event_id }
            }));
          } catch {}
          return;
        }
      };
      ws.onclose = () => {
        if (cancelled) return;
        // 5s reconnect attempt mirrors typical browser-driven WS retries.
        // The 30s API poll still ticks during this window so the UI
        // doesn't go totally stale.
        reconnectTimer = setTimeout(connect, 5000);
      };
      ws.onerror = () => { /* let onclose handle the reconnect */ };
    };
    connect();

    return () => {
      cancelled = true;
      clearInterval(poll);
      clearInterval(tick);
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (ws) try { ws.close(); } catch {}
    };
  }, []);
  const states = Object.values(statesBySource).sort((a, b) =>
    String(a.source).localeCompare(String(b.source))
  );
  const statusClass = (s) => {
    if (isSlackLegacyPollingError(s)) return 'ok';
    return s.last_status || 'unknown';
  };
  const renderStatus = (s) => {
    const source = String(s.source || '').toLowerCase();
    if (source === 'slack') {
      if (isSlackLegacyPollingError(s)) return <><span className="inbox-sync-dot ok"/>listening for events</>;
      if (s.is_syncing) return <><span className="inbox-sync-dot syncing"/>receiving event...</>;
      if (s.last_status === 'error') {
        return <><span className="inbox-sync-dot error"/>socket error: {s.last_error || 'see server log'}</>;
      }
      if (s.last_status === 'ok' && s.last_sync_at) {
        return <><span className="inbox-sync-dot ok"/>listening - updated {formatSyncAgo(s.last_sync_at)}</>;
      }
      return <><span className="inbox-sync-dot ok"/>listening for events</>;
    }
    if (s.is_syncing) return <><span className="inbox-sync-dot syncing"/>syncing now...</>;
    if (s.last_status === 'error') return <><span className="inbox-sync-dot error"/>error: {s.last_error || 'see server log'}</>;
    if (s.last_status === 'ok') return <><span className="inbox-sync-dot ok"/>synced {formatSyncAgo(s.last_sync_at)}</>;
    return <><span className="inbox-sync-dot unknown"/>not synced yet</>;
  };
  return (
    <div className="inbox-sync-strip">
      <div className="inbox-sync-sources">
        {states.length === 0 && <div className="inbox-sync-empty mono">Loading sync state…</div>}
        {states.map(s => (
          <div key={s.source} className={`inbox-sync-source status-${statusClass(s)} ${s.is_syncing ? 'is-syncing' : ''}`}>
            <Icon name={sourceIcon(s.source)} size={14}/>
            <div className="inbox-sync-meta">
              <div className="inbox-sync-name mono">{sourceLabel(s.source)}</div>
              <div className="inbox-sync-sub mono">{renderStatus(s)}</div>
            </div>
          </div>
        ))}
      </div>
      <div className="inbox-sync-actions">
        <EnableDesktopNotificationsButton/>
        <button className="btn sm primary" onClick={() => action('monitor-sync', {})}>
          <Icon name="refresh-cw" size={11}/>Check GitHub
        </button>
      </div>
    </div>
  );
};

// InboxSettingsDrawer is the gear-icon affordance. Wraps InboxRulePanel
// in a click-outside-to-close overlay. Keeping the rule editor as a
// separate component (vs inlining) preserves the prior contract so any
// existing CSS that targets `.inbox-rule-panel` keeps working.
const InboxSettingsDrawer = ({ monitor, action, onClose }) => (
  <div className="inbox-settings-overlay" onClick={onClose}>
    <div className="inbox-settings-drawer" onClick={e => e.stopPropagation()}>
      <div className="inbox-settings-head">
        <h2>Routing rules</h2>
        <button className="btn sm" onClick={onClose} aria-label="Close"><Icon name="x" size={12}/></button>
      </div>
      <p className="inbox-settings-help">
        Each rule decides what happens to an incoming Slack or GitHub signal:
        log it silently, queue for your approval, or auto-spawn an inspect-only agent.
        Write-shaped automation must be opted in here per (source, kind) — no auto-spawn defaults.
      </p>
      <InboxRulePanel monitor={monitor} action={action}/>
    </div>
  </div>
);

const InboxView = ({ action, goto }) => {
  const monitor = monitorState();
  const items = buildInboxItems(monitor);
  const [filter, setFilter] = useState('all');
  const [q, setQ] = useState('');
  const [showSettings, setShowSettings] = useState(false);

  // Counts drive the chip badges. Computed once per render — items is
  // already pre-filtered to INBOX_SOURCES so we don't need to re-filter
  // here, just bucket by per-source label.
  const counts = {
    all:    items.length,
    slack:  items.filter(i => String(i.source).toLowerCase() === 'slack').length,
    github: items.filter(i => String(i.source).toLowerCase() === 'github').length,
    needs:  items.filter(monitorItemNeedsReview).length,
  };
  const filters = [
    ['all',    'All',             counts.all],
    ['slack',  'Slack',           counts.slack],
    ['github', 'GitHub',          counts.github],
    ['needs',  'Needs approval',  counts.needs],
  ];

  const query = q.trim().toLowerCase();
  const filtered = items.filter(item => {
    const src = String(item.source).toLowerCase();
    if (filter === 'slack'  && src !== 'slack')  return false;
    if (filter === 'github' && src !== 'github') return false;
    if (filter === 'needs'  && !monitorItemNeedsReview(item)) return false;
    if (!query) return true;
    return [item.source, item.kind, item.title, item.body, item.status, item.mode, item.outcome?.action, item.outcome?.note, item.outcome?.task_slug]
      .filter(Boolean).join(' ').toLowerCase().includes(query);
  });

  return (
    <div className="inbox-page">
      <div className="inbox-header">
        <div>
          <div className="overview-kicker mono">Inbox</div>
          <h1>Incoming work</h1>
          <p>Slack events and GitHub checks routed for triage. Approve to spawn an inspect/report agent, or let rules auto-spawn what you've allowlisted.</p>
        </div>
        <button className="btn sm" onClick={() => setShowSettings(true)} title="Routing rules">
          <Icon name="settings" size={12}/>Routing rules
        </button>
      </div>

      <SyncStatusStrip action={action}/>

      <div className="inbox-safety">
        <Icon name="shield-alert" size={14}/>
        <span>Approving an agent here only allows inspect/report work. Replies, edits, commits, pushes, PR actions, infra/API writes, and secret disclosure still require explicit approval.</span>
      </div>

      <div className="inbox-toolbar">
        <div className="tab-strip">
          {filters.map(([id, label, count]) => (
            <button key={id} className={filter===id ? 'active' : ''} onClick={() => setFilter(id)}>
              {label}<span className="mono">{count}</span>
            </button>
          ))}
        </div>
        <div className="inbox-search">
          <Icon name="search" size={12}/>
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="Filter inbox..."/>
        </div>
      </div>

      <div className="inbox-list">
        {filtered.length
          ? filtered.map(item => <InboxItemRow key={`${item.id}-${item.notification_id || ''}`} item={item} action={action} goto={goto}/>)
          : <BrandEmpty title="No incoming work" body="Slack DMs/mentions arrive through Socket Mode. GitHub PR reviews / CI alerts appear after checks run."/>
        }
      </div>

      {showSettings && <InboxSettingsDrawer monitor={monitor} action={action} onClose={() => setShowSettings(false)}/>}
    </div>
  );
};

// Monitor view removed in the Inbox-merger redesign. The Notifications /
// Autonomy-settings panels are folded into Inbox: the items list shows
// scoped (slack+github) signals, the sync strip shows per-source state,
// and the rules table lives behind a settings affordance in the inbox
// toolbar. monitorState() and groupedNotifications() are still exported
// for the Inbox helpers (group meta inference, rule lookups).

// ───────── Sessions grid ────────────────────────────────────────────────
const SessionsGrid = ({ setFocus, action, goto }) => {
  const allSessions = AGENTS;
  const projects = Array.from(new Set(PROJECTS_MC.map(p => p.slug).concat(allSessions.map(a => a.project).filter(Boolean))));
  const [filter, setFilter] = useState({
    status: new Set(['running','waiting','idle','stale','dead']),
    provider: 'all',
    projects: new Set(projects.concat(['__adhoc'])),
  });
  const [createOpen, setCreateOpen] = useState(false);

  const list = allSessions.filter(a =>
    filter.status.has(a.status)
    && (filter.provider === 'all' || a.provider === filter.provider)
    && (filter.projects.has(a.project || '__adhoc'))
  );

  // Group: adhoc first, then by project
  const adhoc = list.filter(a => !a.project);
  const byProject = projects
    .map(p => ({ project: p, items: list.filter(a => a.project === p) }))
    .filter(g => g.items.length > 0);

  const toggleStatus = (s) => setFilter(f => {
    const n = new Set(f.status); n.has(s) ? n.delete(s) : n.add(s); return { ...f, status: n };
  });
  const toggleProject = (p) => setFilter(f => {
    const n = new Set(f.projects); n.has(p) ? n.delete(p) : n.add(p); return { ...f, projects: n };
  });

  return (
    <div>
      <div className="action-bar">
        <div className="filter-group">
          <span className="filter-label">status</span>
          {['running','waiting','idle','stale','dead'].map(s => (
            <button key={s} className={`pill ${s}`} onClick={() => toggleStatus(s)} style={{opacity: filter.status.has(s) ? 1 : 0.35, cursor: 'pointer', border: filter.status.has(s) ? null : '1px dashed var(--border)'}}>{s}</button>
          ))}
        </div>
        <div className="filter-group">
          <span className="filter-label">provider</span>
          {['all','claude','codex'].map(p => (
            <button
              key={p}
              className={`btn sm ${filter.provider===p?'primary':''}`}
              onClick={() => setFilter(f => ({...f, provider: p}))}
              aria-label={p === 'claude' ? 'provider' : undefined}
            >
              {p !== 'all' && <ProviderMark provider={p} size={11}/>}
              {p === 'all' ? 'all' : p === 'codex' ? 'codex' : null}
            </button>
          ))}
        </div>
        <div className="filter-group">
          <span className="filter-label">project</span>
          <button className={`btn sm ${filter.projects.has('__adhoc')?'primary':''}`} onClick={() => toggleProject('__adhoc')}>adhoc</button>
          {projects.map(p => (
            <button key={p} className={`btn sm ${filter.projects.has(p)?'primary':''}`} onClick={() => toggleProject(p)}>{p}</button>
          ))}
        </div>
        <div className="mono right-meta">{list.length} of {allSessions.length}</div>
        <button className="btn sm primary" onClick={() => setCreateOpen(true)}><Icon name="plus" size={11}/>Create flow</button>
      </div>

      {adhoc.length > 0 && (
        <div className="session-group">
          <div className="group-head">
            <Icon name="zap" size={12}/>
            <span className="group-title">Adhoc</span>
            <span className="group-count mono">{adhoc.length}</span>
            <span className="group-sub">Sessions without a project</span>
          </div>
          <div className="agent-grid big">
            {adhoc.map(a => <AgentTile key={a.slug} agent={a} onOpen={setFocus} onAction={action} big/>)}
          </div>
        </div>
      )}

      {byProject.map(g => {
        const pmeta = PROJECTS_MC.find(p => p.slug === g.project);
        return (
          <div key={g.project} id={`proj-${g.project}`} className="session-group">
            <div className="group-head">
              <Icon name="folder" size={12}/>
              <span className="group-title mono">{g.project}</span>
              <span className="group-count mono">{g.items.length}</span>
              {pmeta && <span className="group-sub">{pmeta.name}</span>}
              <button className="btn sm" style={{marginLeft: 'auto'}} onClick={() => goto && goto('projects')}><Icon name="external-link" size={10}/>Open project</button>
            </div>
            <div className="agent-grid big">
              {g.items.map(a => <AgentTile key={a.slug} agent={a} onOpen={setFocus} onAction={action} big/>)}
            </div>
          </div>
        );
      })}

      {list.length === 0 && (
        <BrandEmpty title="No sessions match" body="Adjust the filters or create a new flow session."/>
      )}

      {createOpen && <CreateFlowModal onClose={() => setCreateOpen(false)} projects={projects} action={action}/>}
    </div>
  );
};

// ───────── Directory picker ─────────────────────────────────────────────
// Mocked filesystem for the picker. Tree of folders the user could land on.
const RECENT_PATHS = WORKDIRS.map(w => w.path).slice(0, 8);

const DirectoryPicker = ({ initial, onPick, onClose }) => {
  const startPath = (initial || '~').replace(/\/+$/, '') || '~';
  const [cwd, setCwd] = useState(startPath);
  const [selected, setSelected] = useState(startPath);
  const [showHidden, setShowHidden] = useState(false);
  const [state, setState] = useState({ path: startPath, display_path: startPath, parent: null, breadcrumbs: [], entries: [], is_git: false, loading: true, error: '' });

  useEffect(() => {
    let active = true;
    setState(s => ({ ...s, loading: true, error: '' }));
    fetch(`/api/fs/entries?path=${encodeURIComponent(cwd || '~')}`)
      .then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))))
      .then(data => {
        if (!active) return;
        setState({ ...data, loading: false, error: '' });
        setSelected(prev => (!prev || prev === cwd ? data.path : prev));
      })
      .catch(err => {
        if (!active) return;
        setState(s => ({ ...s, loading: false, error: err.message || String(err), entries: [] }));
      });
    return () => { active = false; };
  }, [cwd]);

  const entries = (state.entries || []).filter(e => showHidden || !e.hidden);
  const selectedEntry = (state.entries || []).find(e => e.path === selected);
  const selectedDisplay = selected === state.path ? state.display_path : selectedEntry?.display_path || selected;
  const selectedIsGit = selected === state.path ? state.is_git : !!selectedEntry?.is_git;
  const canChoose = selected === state.path || !selectedEntry || selectedEntry.is_dir;

  const goUp = () => {
    if (!state.parent) return;
    setCwd(state.parent);
    setSelected(state.parent);
  };

  const enter = (entry) => {
    if (!entry.is_dir) return;
    setCwd(entry.path);
    setSelected(entry.path);
  };

  return (
    <div className="modal-scrim centered" style={{zIndex: 60}} onClick={onClose}>
      <div className="modal dp-picker-modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <Icon name="folder-open" size={14}/>
          <span>Choose work directory</span>
          <span className="mono dim" style={{marginLeft: 8, fontSize: 11}}>pick a git repo or any folder</span>
          <button className="modal-close" onClick={onClose}><Icon name="x" size={12}/></button>
        </div>

        <div className="dp-toolbar">
          <button className="btn sm" onClick={goUp} disabled={!state.parent} title="Go up"><Icon name="chevron-up" size={11}/></button>
          <div className="dp-crumbs mono">
            {(state.breadcrumbs || []).map((c, i) => {
              return (
                <Fragment key={i}>
                  {i > 0 && <span className="dp-crumb-sep">/</span>}
                  <button className="dp-crumb" onClick={() => { setCwd(c.path); setSelected(c.path); }}>{c.name}</button>
                </Fragment>
              );
            })}
          </div>
          <button className={`dp-toggle mono ${showHidden ? 'on' : ''}`} onClick={() => setShowHidden(v => !v)} title="Show hidden files">
            <Icon name={showHidden ? 'eye' : 'eye-off'} size={11}/> .hidden
          </button>
        </div>

        <div className="dp-body">
          <div className="dp-sidebar">
            <div className="dp-side-head mono">Recent</div>
            {RECENT_PATHS.map(p => (
              <button key={p} className={`dp-side-item mono ${selected === p ? 'on' : ''}`} onClick={() => { setCwd(p); setSelected(p); }} title={p}>
                <Icon name="clock" size={11}/>
                <span className="dp-side-name">{p.split('/').pop()}</span>
              </button>
            ))}
            <div className="dp-side-head mono" style={{marginTop: 14}}>Places</div>
            <button className={`dp-side-item mono ${state.display_path === '~' ? 'on' : ''}`} onClick={() => { setCwd('~'); setSelected('~'); }}><Icon name="home" size={11}/><span className="dp-side-name">Home</span></button>
            <button className={`dp-side-item mono ${state.display_path === '~/facets/codebases' ? 'on' : ''}`} onClick={() => { setCwd('~/facets/codebases'); setSelected('~/facets/codebases'); }}><Icon name="folder-tree" size={11}/><span className="dp-side-name">codebases</span></button>
            <button className={`dp-side-item mono ${state.display_path === '~/Downloads' ? 'on' : ''}`} onClick={() => { setCwd('~/Downloads'); setSelected('~/Downloads'); }}><Icon name="download" size={11}/><span className="dp-side-name">Downloads</span></button>
          </div>

          <div className="dp-list">
            {state.loading ? (
              <SkeletonRows rows={8}/>
            ) : state.error ? (
              <div className="dp-empty mono">{state.error}</div>
            ) : entries.length === 0 ? (
              <div className="dp-empty mono">empty folder</div>
            ) : (
              entries.map((e) => (
                <div
                  key={e.path}
                  className={`dp-row ${selected === e.path ? 'on' : ''} ${e.is_dir ? '' : 'file'}`}
                  onClick={() => e.is_dir && setSelected(e.path)}
                  onDoubleClick={() => enter(e)}
                >
                  <Icon name={e.is_git ? 'folder-git' : e.is_dir ? 'folder' : 'file'} size={13}/>
                  <span className="dp-row-name mono">{e.name}</span>
                  {e.is_git && <span className="dp-row-badge mono">git</span>}
                  {e.is_dir && <Icon name="chevron-right" size={11}/>}
                </div>
              ))
            )}
          </div>
        </div>

        <div className="modal-foot">
          <div className="dp-path mono" title={selected}>
            <Icon name={selectedIsGit ? 'folder-git' : 'folder'} size={12}/>
            <span>{selectedDisplay}</span>
            {selectedIsGit && <span className="dp-git-badge mono">git repo</span>}
          </div>
          <div style={{flex: 1}}></div>
          <button className="btn sm" onClick={onClose}>Cancel</button>
          <button className="btn sm primary" disabled={!canChoose} onClick={() => canChoose && onPick(selected)}>
            <Icon name="check" size={11}/> Choose
          </button>
        </div>
      </div>
    </div>
  );
};

// ───────── Create flow modal ────────────────────────────────────────────
const CreateFlowModal = ({ onClose, projects, action, preselect }) => {
  const [name, setName] = useState('');
  const [project, setProject] = useState(preselect?.project || '__adhoc');
  const [branch, setBranch] = useState('');
  const [provider, setProvider] = useState(defaultAvailableProvider());
  const [permissionMode, setPermissionMode] = useState('default');
  const [priority, setPriority] = useState('medium');
  const [prompt, setPrompt] = useState('');
  const [workdir, setWorkdir] = useState(preselect?.workDir || WORKDIRS[0]?.path || '');
  const [prUrl, setPrUrl] = useState('');
  const [pickerOpen, setPickerOpen] = useState(false);

  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'new-flow';
  const providerIsAvailable = isCapabilityAvailable('providers', provider);
  const canSubmit = name.trim().length > 2 && prompt.trim().length > 0 && providerIsAvailable && anyProviderAvailable();
  const permissionCopy = provider === 'codex'
    ? {
      default: 'Codex approval on-request with workspace-write sandbox',
      auto: 'Codex approval never with workspace-write sandbox',
      bypass: 'Codex bypasses approvals and sandbox',
    }
    : {
      default: 'Claude default permission prompts',
      auto: 'Claude auto permission mode',
      bypass: 'Claude dangerously skips permissions',
    };

  const submit = () => {
    if (!canSubmit) return;
    action('create-flow', { slug, name, project: project === '__adhoc' ? null : project, branch: branch || `${slug}/main`, provider, permission_mode: permissionMode, priority, prompt, work_dir: workdir, pr_url: prUrl });
    onClose();
  };

  useEffect(() => {
    if (isCapabilityAvailable('providers', provider)) return;
    const next = defaultAvailableProvider();
    if (next && next !== provider && isCapabilityAvailable('providers', next)) setProvider(next);
  }, [provider]);

  return (
    <div className="modal-scrim centered" onClick={onClose}>
      <div className="modal create-flow" style={{width: 620}} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <Icon name="plus" size={14}/>
          <span>Create flow</span>
          <span className="mono dim" style={{marginLeft: 8, fontSize: 11}}>spawn a new agent session</span>
          <button className="modal-close" onClick={onClose}><Icon name="x" size={12}/></button>
        </div>
        <div className="modal-body" style={{padding: 16, display: 'flex', flexDirection: 'column', gap: 14}}>
          <label className="form-row">
            <span className="form-label">Task name</span>
            <input className="form-input" value={name} onChange={e => setName(e.target.value)} placeholder="Fix login callback handling" autoFocus/>
            {name && <span className="form-hint mono">slug: <b>{slug}</b></span>}
          </label>

          <div className="form-grid">
            <label className="form-row">
              <span className="form-label">Provider</span>
              <div className="seg">
                <button
                  className={`seg-btn ${provider==='claude'?'on':''}`}
                  disabled={!isCapabilityAvailable('providers', 'claude')}
                  onClick={() => setProvider('claude')}
                  title={isCapabilityAvailable('providers', 'claude') ? 'Claude Code' : capabilityReason('providers', 'claude')}
                  aria-label="provider"
                >
                  <ProviderMark provider="claude" size={14}/>
                </button>
                <button
                  className={`seg-btn ${provider==='codex'?'on':''}`}
                  disabled={!isCapabilityAvailable('providers', 'codex')}
                  onClick={() => setProvider('codex')}
                  title={isCapabilityAvailable('providers', 'codex') ? 'Codex' : capabilityReason('providers', 'codex')}
                >
                  <ProviderMark provider="codex" size={14}/> codex
                </button>
              </div>
              {!anyProviderAvailable() && <span className="form-hint mono" style={{color: 'var(--dead)'}}>No supported agent binary found on PATH.</span>}
            </label>
            <label className="form-row">
              <span className="form-label">Priority</span>
              <div className="seg">
                {['low','medium','high'].map(p => (
                  <button key={p} className={`seg-btn ${priority===p?'on':''}`} onClick={() => setPriority(p)}>{p}</button>
                ))}
              </div>
            </label>
          </div>

          <label className="form-row">
            <span className="form-label">Permissions</span>
            <div className="seg">
              <button className={`seg-btn ${permissionMode==='default'?'on':''}`} onClick={() => setPermissionMode('default')} title={permissionCopy.default}>
                <Icon name="lock" size={12}/> default
              </button>
              <button className={`seg-btn ${permissionMode==='auto'?'on':''}`} onClick={() => setPermissionMode('auto')} title={permissionCopy.auto}>
                <Icon name="zap" size={12}/> auto
              </button>
              <button className={`seg-btn ${permissionMode==='bypass'?'on':''}`} onClick={() => setPermissionMode('bypass')} title={permissionCopy.bypass}>
                <Icon name="shield-off" size={12}/> bypass
              </button>
            </div>
          </label>

          <div className="form-grid">
            <label className="form-row">
              <span className="form-label">Project</span>
              <select className="form-input" value={project} onChange={e => setProject(e.target.value)}>
                <option value="__adhoc">— adhoc (no project) —</option>
                {projects.map(p => <option key={p} value={p}>{p}</option>)}
              </select>
            </label>
            <label className="form-row">
              <span className="form-label">Branch</span>
              <input className="form-input mono" value={branch} onChange={e => setBranch(e.target.value)} placeholder={`${slug}/main`}/>
            </label>
          </div>

          <label className="form-row">
            <span className="form-label">Work dir</span>
            <div className="path-picker" onClick={() => setPickerOpen(true)} title="Choose directory…">
              <Icon name="folder" size={13}/>
              <span className="path-picker-text mono">{workdir || 'Choose a directory…'}</span>
              <span className="path-picker-btn mono">Browse…</span>
            </div>
          </label>
          {pickerOpen && <DirectoryPicker initial={workdir} onPick={(p) => { setWorkdir(p); setPickerOpen(false); }} onClose={() => setPickerOpen(false)}/>}

          <label className="form-row">
            <span className="form-label">PR link <span className="mono dim">optional</span></span>
            <input className="form-input mono" value={prUrl} onChange={e => setPrUrl(e.target.value)} placeholder="https://github.com/org/repo/pull/123"/>
          </label>

          <label className="form-row">
            <span className="form-label">Initial prompt</span>
            <textarea className="form-input" rows={4} value={prompt} onChange={e => setPrompt(e.target.value)} placeholder="Tell the agent what to do. Include the repo, expected behavior, and how you want it verified."/>
          </label>
        </div>
        <div className="modal-foot">
          <span className="mono dim" style={{fontSize: 11}}>
            <kbd className="kbd">esc</kbd> cancel · <kbd className="kbd">⌘↵</kbd> spawn
          </span>
          <div style={{marginLeft: 'auto', display: 'flex', gap: 8}}>
            <button className="btn sm" onClick={onClose}>Cancel</button>
            <button className="btn sm primary" disabled={!canSubmit} onClick={submit}>
              <Icon name="play" size={11}/>Spawn <ProviderMark provider={provider} size={12}/>{provider === 'codex' ? 'codex' : null}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};

// ───────── Create project modal ─────────────────────────────────────
const CreateProjectModal = ({ onClose, action }) => {
  const [name, setName] = useState('');
  const [slugEdited, setSlugEdited] = useState(false);
  const [slugInput, setSlugInput] = useState('');
  const [workdir, setWorkdir] = useState(WORKDIRS[0]?.path || '');
  const [mkdir, setMkdir] = useState(false);
  const [priority, setPriority] = useState('medium');
  const [description, setDescription] = useState('');
  const [pickerOpen, setPickerOpen] = useState(false);

  const derivedSlug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  const slug = (slugEdited ? slugInput : derivedSlug) || 'new-project';
  const canSubmit = name.trim().length > 1 && /^[a-z0-9][a-z0-9._-]*$/.test(slug) && workdir.trim().length > 0;

  const submit = () => {
    if (!canSubmit) return;
    action('create-project', { slug, name: name.trim(), work_dir: workdir, priority, mkdir, description: description.trim() });
    onClose();
  };

  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape') { onClose(); return; }
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); submit(); }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [name, slug, workdir, priority, mkdir, description]);

  return (
    <div className="modal-scrim centered" onClick={onClose}>
      <div className="modal create-flow" style={{width: 580}} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <Icon name="folder-plus" size={14}/>
          <span>Create project</span>
          <span className="mono dim" style={{marginLeft: 8, fontSize: 11}}>group related tasks under one roof</span>
          <button className="modal-close" onClick={onClose}><Icon name="x" size={12}/></button>
        </div>
        <div className="modal-body" style={{padding: 16, display: 'flex', flexDirection: 'column', gap: 14}}>
          <label className="form-row">
            <span className="form-label">Project name</span>
            <input className="form-input" value={name} onChange={e => setName(e.target.value)} placeholder="Budgeting app" autoFocus/>
          </label>

          <label className="form-row">
            <span className="form-label">Slug</span>
            <input
              className="form-input mono"
              value={slugEdited ? slugInput : derivedSlug}
              onChange={e => { setSlugEdited(true); setSlugInput(e.target.value); }}
              placeholder="auto-generated from name"
            />
            <span className="form-hint mono dim">lowercase, digits, dot/dash/underscore</span>
          </label>

          <label className="form-row">
            <span className="form-label">Priority</span>
            <div className="seg">
              {['low','medium','high'].map(p => (
                <button key={p} className={`seg-btn ${priority===p?'on':''}`} onClick={() => setPriority(p)}>{p}</button>
              ))}
            </div>
          </label>

          <label className="form-row">
            <span className="form-label">Work dir</span>
            <div className="path-picker" onClick={() => setPickerOpen(true)} title="Choose directory…">
              <Icon name="folder" size={13}/>
              <span className="path-picker-text mono">{workdir || 'Choose a directory…'}</span>
              <span className="path-picker-btn mono">Browse…</span>
            </div>
            <label className="mono dim" style={{display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, marginTop: 6, cursor: 'pointer'}}>
              <input type="checkbox" checked={mkdir} onChange={e => setMkdir(e.target.checked)}/>
              Create the directory if it doesn't exist
            </label>
          </label>
          {pickerOpen && <DirectoryPicker initial={workdir} onPick={(p) => { setWorkdir(p); setPickerOpen(false); }} onClose={() => setPickerOpen(false)}/>}

          <label className="form-row">
            <span className="form-label">Description <span className="mono dim">optional</span></span>
            <textarea className="form-input" rows={4} value={description} onChange={e => setDescription(e.target.value)} placeholder="What is this project for? What scope does it cover?"/>
            <span className="form-hint mono dim">Becomes the body of the project brief. Leave blank to fill in later.</span>
          </label>
        </div>
        <div className="modal-foot">
          <span className="mono dim" style={{fontSize: 11}}>
            <kbd className="kbd">esc</kbd> cancel · <kbd className="kbd">⌘↵</kbd> create
          </span>
          <div style={{marginLeft: 'auto', display: 'flex', gap: 8}}>
            <button className="btn sm" onClick={onClose}>Cancel</button>
            <button className="btn sm primary" disabled={!canSubmit} onClick={submit}>
              <Icon name="plus" size={11}/>Create project
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};

// ───────── Terminal launcher dropdown (Open in …) ─────────────────────
const SUPPORTED_TERMINALS = [
  { id: 'iterm', label: 'iTerm', icon: 'terminal', os: 'macOS', preferred: true },
  { id: 'terminal', label: 'Terminal.app', icon: 'square-terminal', os: 'macOS' },
  { id: 'warp', label: 'Warp', icon: 'wind', os: 'macOS · Linux' },
  { id: 'kitty', label: 'kitty', icon: 'cat', os: 'macOS · Linux' },
  { id: 'alacritty', label: 'Alacritty', icon: 'square', os: 'cross-platform' },
  { id: 'ghostty', label: 'Ghostty', icon: 'ghost', os: 'macOS · Linux' },
  { id: 'wezterm', label: 'WezTerm', icon: 'monitor', os: 'cross-platform' },
  { id: 'tmux', label: 'tmux (new window)', icon: 'columns-3', os: 'attach in-place' },
  { id: 'vscode', label: 'VS Code terminal', icon: 'code', os: 'editor' },
];

const TerminalDropdown = ({ action, agent }) => {
  const [open, setOpen] = useState(false);
  const [preferred, setPreferred] = useState('iterm');
  const ref = useRef(null);
  const terminals = SUPPORTED_TERMINALS.map(t => ({ ...t, available: isCapabilityAvailable('terminals', t.id), reason: capabilityReason('terminals', t.id) }));
  const pref = terminals.find(t => t.id === preferred && t.available) || terminals.find(t => t.available) || terminals[0];

  useEffect(() => {
    if (!open) return;
    const onClick = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    const onKey = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onClick);
    window.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('mousedown', onClick); window.removeEventListener('keydown', onKey); };
  }, [open]);

  const launch = (t) => {
    if (!t || !t.available) return;
    setPreferred(t.id);
    setOpen(false);
    action(t.id, { ...agent, _terminal: t.label });
  };

  return (
    <div className="term-launcher" ref={ref}>
      <button className="btn sm term-launcher-main" onClick={() => launch(pref)} disabled={!pref.available} title={pref.available ? `Open in ${pref.label}` : pref.reason}>
        <Icon name="external-link" size={11}/>Open in {pref.label}
      </button>
      <button className="btn sm term-launcher-caret" onClick={() => setOpen(v => !v)} aria-label="Pick terminal" aria-expanded={open}>
        <Icon name="chevron-down" size={11}/>
      </button>
      {open && (
        <div className="term-launcher-menu">
          <div className="term-launcher-head mono">Open agent in…</div>
          {terminals.map(t => (
            <button key={t.id} className={`term-launcher-item ${preferred === t.id ? 'on' : ''}`} disabled={!t.available} title={t.available ? '' : t.reason} onClick={() => launch(t)}>
              <Icon name={t.icon} size={13}/>
              <span className="term-launcher-label">{t.label}</span>
              <span className="term-launcher-os mono">{t.available ? t.os : 'unavailable'}</span>
              {preferred === t.id && <Icon name="check" size={11}/>}
            </button>
          ))}
          <div className="term-launcher-foot mono">
            {terminals.some(t => t.available) ? 'Choice remembered for this session' : 'No supported terminal launcher found'}
          </div>
        </div>
      )}
    </div>
  );
};

const BranchSwitcher = ({ agent, action }) => {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const ref = useRef(null);
  const current = agent.branch || 'unknown';
  const hasGitBranches = Array.isArray(agent.branches) && agent.branches.length > 0;
  const branches = (hasGitBranches ? agent.branches : [current]).filter(Boolean);
  const filtered = branches
    .filter((branch, idx, arr) => arr.indexOf(branch) === idx)
    .filter(branch => branch.toLowerCase().includes(query.trim().toLowerCase()))
    .slice(0, 80);

  useEffect(() => {
    if (!open) return;
    const onClick = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    const onKey = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onClick);
    window.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('mousedown', onClick); window.removeEventListener('keydown', onKey); };
  }, [open]);

  const switchTo = (branch) => {
    setOpen(false);
    setQuery('');
    if (branch && branch !== current) action('switch-branch', { ...agent, branch });
  };

  if (!hasGitBranches) {
    return (
      <div className="branch-switcher">
        <button className="branch-switcher-btn" title="This workdir is not a git repository" disabled>
          <Icon name="folder" size={11}/>
          <span className="mono">{current}</span>
        </button>
      </div>
    );
  }

  return (
    <div className="branch-switcher" ref={ref}>
      <button className="branch-switcher-btn" onClick={() => setOpen(v => !v)} title="Switch git branch" aria-expanded={open}>
        <Icon name="git-branch" size={11}/>
        <span className="mono">{current}</span>
        <Icon name="chevron-down" size={10}/>
      </button>
      {open && (
        <div className="branch-menu">
          <div className="branch-search">
            <Icon name="search" size={12}/>
            <input autoFocus value={query} onChange={e => setQuery(e.target.value)} placeholder="Search branches…"/>
          </div>
          <div className="branch-list">
            {filtered.length ? filtered.map(branch => (
              <button key={branch} className={`branch-item ${branch === current ? 'on' : ''}`} onClick={() => switchTo(branch)}>
                <Icon name={branch === current ? 'check' : 'git-branch'} size={12}/>
                <span className="mono">{branch}</span>
              </button>
            )) : <div className="branch-empty mono">No branches match</div>}
          </div>
        </div>
      )}
    </div>
  );
};

// ───────── Session Detail (terminal bridge) ─────────────────────────────
const isTerminalLiveStatus = (status = '') => status === 'interactive' || status.startsWith('connected');

const NativeTranscriptPanel = ({ agent }) => (
  <div className="pane terminal-pane">
    <div className="pane-head">
      <Icon name="terminal" size={11}/>
      <ProviderMark provider={agent.provider || 'claude'} size={12}/>
      <span>{agent.provider === 'codex' ? 'codex' : 'claude'} · native terminal</span>
      <div className="right">
        <Dot status="running"/>
        <span className="terminal-status mono">synced transcript</span>
      </div>
    </div>
    <div className="pane-body">
      <TranscriptView entries={agent.transcript || []} live provider={agent.provider || 'claude'}/>
    </div>
  </div>
);

const SessionDetail = ({ agent, goto, action, gitDiffOpen = false, toggleGitDiff = () => {}, artifactsOpen = false, toggleArtifacts = () => {} }) => {
  const [liveAgent, setLiveAgent] = useState(agent);
  const [terminalStatus, setTerminalStatus] = useState('connecting');
  const [terminalRestartKey, setTerminalRestartKey] = useState(0);
  const current = liveAgent || agent;

  useEffect(() => {
    setLiveAgent(agent);
  }, [agent]);

  useEffect(() => {
    setTerminalStatus('connecting');
    setTerminalRestartKey(0);
  }, [agent.slug, agent.session_id]);

  const provider = current.provider || 'claude';
  const terminalMode = current.terminal?.mode || 'idle';
  const completedTask = current.task_status === 'done' || current.status === 'done';
  const nativeTranscriptMode = terminalMode === 'native' && completedTask;
  const providerAvailable = isCapabilityAvailable('providers', provider);
  const providerReason = capabilityReason('providers', provider);
  const canRestartTerminal = providerAvailable && !nativeTranscriptMode && terminalStatus !== 'connecting' && !isTerminalLiveStatus(terminalStatus);
  const restartTitle = canRestartTerminal
    ? 'Restart terminal'
    : !providerAvailable
      ? providerReason
      : nativeTranscriptMode
      ? 'Session is active in a native terminal'
      : isTerminalLiveStatus(terminalStatus)
      ? 'Terminal is running'
      : 'Terminal is connecting';
  const restartTerminal = () => {
    if (!canRestartTerminal) return;
    const result = action('restart', current);
    if (result && typeof result.then === 'function') {
      result.then((data) => {
        if (!data || data.ok === false) return;
        setTerminalStatus('connecting');
        setTerminalRestartKey(v => v + 1);
        window.dispatchEvent(new Event('flow-terminal-focus'));
      });
    }
  };

  return (
    <div>
      <div className="action-bar">
        <Dot status={current.status}/>
        <span className="mono" style={{fontSize: 14, fontWeight: 500}}>{current.slug}</span>
        <span style={{color: 'var(--text-dim)'}}>{current.name}</span>
        <StatusPill status={current.status}/>
        <TaskStatePill status={current.task_status}/>
        <AgentChip provider={current.provider}/>
        <BranchSwitcher agent={current} action={action}/>
        {(current.pr_links || []).map(pr => (
          <a key={`${pr.repo}-${pr.number}`} className={`pr-chip ${pr.state}`} href={pr.url} target="_blank" rel="noreferrer" title={`${pr.repo} #${pr.number}`}>
            <Icon name="git-pull-request" size={10}/>
            <span className="mono">{pr.repo} #{pr.number}</span>
            <span className="mono dim">{pr.state}</span>
          </a>
        ))}
        <div style={{marginLeft: 'auto', display: 'flex', gap: 6}}>
          <button className={`btn sm ${gitDiffOpen ? 'primary' : ''}`} onClick={toggleGitDiff} title={gitDiffOpen ? 'Hide git diff panel' : 'Show git diff panel'}>
            <Icon name="git-compare" size={11}/>
            Git diff
            {(current.diff?.files || 0) > 0 && <span className="mono" style={{marginLeft: 4, opacity: 0.75}}>{current.diff.files}</span>}
          </button>
          <button className={`btn sm ${artifactsOpen ? 'primary' : ''}`} onClick={toggleArtifacts} title={artifactsOpen ? 'Hide artifacts panel' : 'Show task artifacts (brief, updates, sidecar files)'}>
            <Icon name="file-text" size={11}/>
            Artifacts
            {artifactCountFor(current) > 0 && <span className="mono" style={{marginLeft: 4, opacity: 0.75}}>{artifactCountFor(current)}</span>}
          </button>
          <div className="seg" title={`Agent permissions (current: ${current.permission_mode || 'default'})`}>
            {['default', 'auto', 'bypass'].map(m => {
              const cur = (current.permission_mode || 'default') === m;
              return (
                <button
                  key={m}
                  className={`seg-btn ${cur ? 'on' : ''}`}
                  title={cur ? `Permissions: ${m} (current)` : `Switch to ${m} — terminates the running session; reattach to apply`}
                  onClick={() => {
                    if (cur) return;
                    if (!window.confirm(`Switch permissions to ${m}?\n\nThis terminates the running session; reattach to spawn a fresh one with the new mode.`)) return;
                    action('update-permission-mode', { slug: current.slug, permission_mode: m, provider: current.provider });
                  }}
                >{m}</button>
              );
            })}
          </div>
          <button className="btn sm" onClick={() => goto('mc')}><Icon name="arrow-left" size={11}/>Detach</button>
          <button className="btn sm" onClick={restartTerminal} disabled={!canRestartTerminal} title={restartTitle}><Icon name="refresh-cw" size={11}/>Restart</button>
          <TerminalDropdown action={action} agent={current}/>
        </div>
      </div>
      {current.hook_health && (
        <div className="hook-health">
          <Icon name="shield-alert" size={14}/>
          <div>
            <strong>Codex hooks need attention</strong>
            <p>{current.hook_health.message}</p>
            {current.hook_health.action && <div className="mono">{current.hook_health.action}</div>}
          </div>
        </div>
      )}
      <DependencyBadges task={current}/>
      <div className={`bridge-layout${(gitDiffOpen || artifactsOpen) ? '' : ' single'}`}>
        {providerAvailable ? (
          nativeTranscriptMode
            ? <NativeTranscriptPanel agent={current}/>
            : <TaskTerminal key={`${current.slug}:${current.session_id || ''}:${terminalRestartKey}`} agent={current} onStatus={setTerminalStatus}/>
        ) : (
          <div className="pane terminal-pane">
            <div className="pane-head">
              <Icon name="terminal" size={11}/>
              <ProviderMark provider={provider} size={12}/>
              <span>{provider} terminal unavailable</span>
              <div className="right">
                <Dot status="idle"/>
                <span className="terminal-status mono">disabled</span>
              </div>
            </div>
            <div className="bridge-empty">
              <Icon name="circle-off" size={22}/>
              <span className="mono">{providerReason}</span>
            </div>
          </div>
        )}
        {(gitDiffOpen || artifactsOpen) && (
          <div className="bridge-side">
            {gitDiffOpen && (
              <CollapsiblePanel icon="git-compare" title="Git diff" count={`${current.diff?.files || 0} files · +${current.diff?.add || 0} / -${current.diff?.rem || 0}`} defaultOpen>
                <DiffSidecar agent={current}/>
              </CollapsiblePanel>
            )}
            {artifactsOpen && (
              <CollapsiblePanel icon="file-text" title="Artifacts" count={`${artifactCountFor(current)} files`} defaultOpen>
                <ArtifactsSidecar agent={current}/>
              </CollapsiblePanel>
            )}
          </div>
        )}
      </div>
    </div>
  );
};

const CompletedSessionView = ({ agent, goto, gitDiffOpen = false, toggleGitDiff = () => {}, artifactsOpen = false, toggleArtifacts = () => {} }) => (
  <div>
    <div className="action-bar">
      <Icon name="check-circle" size={14} style={{color: 'var(--running)'}}/>
      <span className="mono" style={{fontSize: 14, fontWeight: 500}}>{agent.slug}</span>
      <span style={{color: 'var(--text-dim)'}}>{agent.name}</span>
      <StatusPill status="done"/>
      <AgentChip provider={agent.provider}/>
      {agent.branch && <BranchChip name={agent.branch}/>}
      {(agent.pr_links || []).map(pr => (
        <a key={`${pr.repo}-${pr.number}`} className={`pr-chip ${pr.state}`} href={pr.url} target="_blank" rel="noreferrer" title={`${pr.repo} #${pr.number}`}>
          <Icon name="git-pull-request" size={10}/>
          <span className="mono">{pr.repo} #{pr.number}</span>
          <span className="mono dim">{pr.state}</span>
        </a>
      ))}
      <span className="bridge-poll mono">completed task snapshot</span>
      <div style={{marginLeft: 'auto', display: 'flex', gap: 6}}>
        <button className={`btn sm ${gitDiffOpen ? 'primary' : ''}`} onClick={toggleGitDiff} title={gitDiffOpen ? 'Hide git diff panel' : 'Show git diff panel'}>
          <Icon name="git-compare" size={11}/>
          Git diff
          {(agent.diff?.files || 0) > 0 && <span className="mono" style={{marginLeft: 4, opacity: 0.75}}>{agent.diff.files}</span>}
        </button>
        <button className={`btn sm ${artifactsOpen ? 'primary' : ''}`} onClick={toggleArtifacts} title={artifactsOpen ? 'Hide artifacts panel' : 'Show task artifacts (brief, updates, sidecar files)'}>
          <Icon name="file-text" size={11}/>
          Artifacts
          {artifactCountFor(agent) > 0 && <span className="mono" style={{marginLeft: 4, opacity: 0.75}}>{artifactCountFor(agent)}</span>}
        </button>
        <button className="btn sm primary" onClick={() => goto('sessions')}><Icon name="arrow-left" size={11}/>Sessions</button>
        <button className="btn sm" onClick={() => goto('tasks')}><Icon name="list" size={11}/>Tasks</button>
      </div>
    </div>
    <DependencyBadges task={agent}/>
    <div className={`completed-layout${(gitDiffOpen || artifactsOpen) ? '' : ' single'}`}>
      <div className="pane">
        <div className="pane-head">
          <Icon name="message-square-text" size={11}/>
          <span>Transcript</span>
          <span className="bridge-panel-count mono">{(agent.transcript || []).length} entries</span>
        </div>
        <div className="pane-body">
          <TranscriptView entries={agent.transcript || []} live={false} provider={agent.provider}/>
        </div>
      </div>
      {(gitDiffOpen || artifactsOpen) && (
        <div className="bridge-side">
          <CollapsiblePanel icon="layers" title="Metadata" count={agent.provider || 'agent'} defaultOpen>
            <ContextSummary agent={agent}/>
          </CollapsiblePanel>
          {gitDiffOpen && (
            <CollapsiblePanel icon="git-compare" title="Git diff" count={`${agent.diff?.files || 0} files · +${agent.diff?.add || 0} / -${agent.diff?.rem || 0}`} defaultOpen>
              <DiffSidecar agent={agent}/>
            </CollapsiblePanel>
          )}
          {artifactsOpen && (
            <CollapsiblePanel icon="file-text" title="Artifacts" count={`${artifactCountFor(agent)} files`} defaultOpen>
              <ArtifactsSidecar agent={agent}/>
            </CollapsiblePanel>
          )}
        </div>
      )}
    </div>
  </div>
);

const CollapsiblePanel = ({ icon, title, count, defaultOpen = true, children }) => {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className={`bridge-panel ${open ? 'open' : 'closed'}`}>
      <button className="bridge-panel-head" onClick={() => setOpen(v => !v)} aria-expanded={open}>
        <Icon name={open ? 'chevron-down' : 'chevron-right'} size={12}/>
        <Icon name={icon} size={12}/>
        <span>{title}</span>
        {count && <span className="bridge-panel-count mono">{count}</span>}
      </button>
      {open && <div className="bridge-panel-body">{children}</div>}
    </div>
  );
};

const DiffSidecar = ({ agent }) => {
  const files = agent.diff_files || [];
  const [closedFiles, setClosedFiles] = useState(() => new Set());
  const toggleFile = (name) => {
    setClosedFiles(prev => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };
  if (!files.length) {
    return (
      <div className="bridge-empty">
        <Icon name="git-compare" size={16}/>
        <span>No local git diff in this task workdir.</span>
      </div>
    );
  }
  return (
    <div className="bridge-diff">
      <div className="bridge-diff-meta">
        <span><Icon name="git-branch" size={11}/>{agent.branch}</span>
        <span className="mono">{agent.work_dir}</span>
      </div>
      {files.map(f => {
        const open = !closedFiles.has(f.name);
        return (
          <div key={f.name} className={`bridge-diff-file ${open ? 'open' : 'closed'}`}>
            <button
              type="button"
              className="bridge-diff-file-head"
              onClick={() => toggleFile(f.name)}
              aria-expanded={open}
              aria-label={`${open ? 'Collapse' : 'Expand'} diff for ${f.name}`}
              title={`${open ? 'Collapse' : 'Expand'} diff for ${f.name}`}
            >
              <Icon name={open ? 'chevron-down' : 'chevron-right'} size={12}/>
              <Icon name="file-code" size={12}/>
              <span className="nm mono">{f.name}</span>
              <span className="add mono">+{f.add}</span>
              <span className="rem mono">-{f.rem}</span>
            </button>
            {open && (f.hunks || []).slice(0, 3).map((h, hi) => (
              <div className="bridge-hunk" key={hi}>
                <div className="bridge-hunk-head mono">{h.header}</div>
                {(h.lines || []).slice(0, 36).map((l, li) => (
                  <div key={li} className={`bridge-diff-line ${l.type || 'ctx'}`}>
                    <span className="num">{l.n || ''}</span>
                    <span className="code">{l.code || ''}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        );
      })}
    </div>
  );
};

const artifactCountFor = (agent) => {
  if (!agent) return 0;
  const updates = (agent.updates || []).length;
  const aux = (agent.aux_files || []).length;
  const brief = agent.brief_path ? 1 : 0;
  return updates + aux + brief;
};

const ArtifactsSidecar = ({ agent }) => {
  const slug = agent.slug;
  const files = [];
  if (agent.brief_path) {
    files.push({ filename: 'brief.md', mtime: '', route: 'brief', kind: 'brief' });
  }
  for (const u of (agent.updates || [])) {
    files.push({ ...u, route: 'updates', kind: 'update' });
  }
  for (const a of (agent.aux_files || [])) {
    files.push({ ...a, route: 'aux', kind: 'aux' });
  }
  const fetchArtifact = (file) => {
    const url = file.route === 'brief'
      ? `/api/tasks/${encodeURIComponent(slug)}/brief`
      : `/api/tasks/${encodeURIComponent(slug)}/${file.route}/${encodeURIComponent(file.filename)}`;
    return fetch(url).then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))));
  };
  const minutesSinceISO = (iso) => {
    if (!iso) return null;
    const ts = Date.parse(iso);
    if (!Number.isFinite(ts)) return null;
    return Math.max(0, (Date.now() - ts) / 60000);
  };
  if (!files.length) {
    return (
      <div className="bridge-empty">
        <Icon name="file-text" size={16}/>
        <span>No artifacts found for this task.</span>
      </div>
    );
  }
  return (
    <ReadableFiles
      files={files}
      empty="No artifacts yet"
      fetchFile={fetchArtifact}
      minutesSinceISO={minutesSinceISO}
      sourceKey={slug}
    />
  );
};

const ContextSummary = ({ agent }) => (
  <div className="bridge-context">
    <div className="meta-card">
      <h4>Recent tools</h4>
      {(agent.recent_tools || []).length ? agent.recent_tools.map((r, i) => (
        <div key={i} className="bridge-tool-row">
          <span className="tool mono">{r.name}</span>
          <span className="summary mono">{r.s}</span>
        </div>
      )) : <div className="mono dim" style={{fontSize: 11.5}}>No tool calls recorded for this task.</div>}
    </div>
    <div className="meta-card">
      <h4>Brief</h4>
      <p style={{margin: 0, color: 'var(--text-mid)', fontSize: 12.5}}>{agent.brief || agent.summary || 'No brief text found.'}</p>
    </div>
    <div className="meta-card">
      <h4>Metadata</h4>
      <dl className="kv">
        <dt>session</dt><dd>{shortUUID(agent.session_id)}</dd>
        <dt>started</dt><dd>{formatAge(agent.started_min)} ago</dd>
        <dt>context</dt><dd title="Provider-reported context usage from the session JSONL.">{fmtTokens(agent.tokens_used)} / {fmtTokens(agent.tokens_max)} ({Math.min(100, Math.round((agent.tokens_used / Math.max(1, agent.tokens_max)) * 100))}%)</dd>
        <dt>work_dir</dt><dd style={{fontSize: 10.5}}>{agent.work_dir}</dd>
      </dl>
    </div>
  </div>
);

const TERMINAL_SCROLLBACK_LINES = 200000;
const TERMINAL_FIT_DELAYS_MS = [0, 40, 160, 420, 900];
const TERMINAL_GENERATED_INPUT_RE = /\x1b\[(?:\?[0-9;]*|>[0-9;]*)c/g;

const stripTerminalGeneratedInput = (data = '') => data.replace(TERMINAL_GENERATED_INPUT_RE, '');
const terminalAttachmentExt = (type = '') => {
  const clean = String(type).split(';')[0].trim().toLowerCase();
  if (clean === 'image/png') return '.png';
  if (clean === 'image/jpeg') return '.jpg';
  if (clean === 'image/gif') return '.gif';
  if (clean === 'image/webp') return '.webp';
  if (clean === 'application/pdf') return '.pdf';
  if (clean === 'text/plain') return '.txt';
  return '';
};

const terminalClipboardFiles = (clipboardData) => {
  const files = [];
  if (!clipboardData) return files;
  for (const file of Array.from(clipboardData.files || [])) {
    if (file) files.push(file);
  }
  for (const item of Array.from(clipboardData.items || [])) {
    if (!item || item.kind !== 'file') continue;
    const file = item.getAsFile && item.getAsFile();
    if (file && !files.some(existing => existing === file || (existing.name && existing.name === file.name && existing.size === file.size))) {
      files.push(file);
    }
  }
  return files;
};

const terminalUploadName = (file, index) => {
  if (file && file.name) return file.name;
  return `clipboard-${Date.now()}-${index + 1}${terminalAttachmentExt(file?.type)}`;
};

const TaskTerminal = ({ agent, onStatus }) => {
  const [termStatus, setTermStatus] = useState('connecting');
  const [fullscreen, setFullscreen] = useState(false);
  const ref = useRef(null);
  const termRef = useRef(null);
  const fitRef = useRef(null);
  const wsRef = useRef(null);
  const lastSizeRef = useRef('');

  useEffect(() => {
    if (onStatus) onStatus(termStatus);
  }, [termStatus, onStatus]);

  useEffect(() => {
    if (!fullscreen) return;
    const onKey = (e) => { if (e.key === 'Escape') setFullscreen(false); };
    window.addEventListener('keydown', onKey);
    document.body.style.overflow = 'hidden';
    return () => { window.removeEventListener('keydown', onKey); document.body.style.overflow = ''; };
  }, [fullscreen]);

  useEffect(() => {
    const host = ref.current;
    const XTerm = window.Terminal;
    if (!host || !XTerm) {
      setTermStatus('xterm.js unavailable');
      return;
    }
    host.innerHTML = '';
    const terminalFont = '"JetBrainsMono Nerd Font", "FiraCode Nerd Font", "MesloLGS NF", "JetBrains Mono", "SFMono-Regular", Menlo, Monaco, monospace';
    const term = new XTerm({
      cols: 120,
      rows: 32,
      allowProposedApi: true,
      allowTransparency: true,
      altClickMovesCursor: true,
      customGlyphs: true,
      cursorBlink: true,
      convertEol: false,
      drawBoldTextInBrightColors: true,
      fontFamily: terminalFont,
      fontSize: 13,
      letterSpacing: 0,
      lineHeight: 1.18,
      macOptionIsMeta: true,
      minimumContrastRatio: 1,
      rescaleOverlappingGlyphs: true,
      rightClickSelectsWord: true,
      scrollOnUserInput: true,
      scrollSensitivity: 1,
      scrollback: TERMINAL_SCROLLBACK_LINES,
      smoothScrollDuration: 0,
      theme: {
        background: '#050507',
        foreground: '#d8d8e8',
        cursor: '#5eead4',
        selectionBackground: '#3f3a87',
        black: '#050507',
        red: '#e25757',
        green: '#2eb672',
        yellow: '#d6a84c',
        blue: '#645df6',
        magenta: '#b584ff',
        cyan: '#5eead4',
        white: '#d8d8e8',
        brightBlack: '#57576a',
        brightRed: '#ff7b7b',
        brightGreen: '#63d797',
        brightYellow: '#f1ca73',
        brightBlue: '#8b87f8',
        brightMagenta: '#cfaaff',
        brightCyan: '#8ff7e8',
        brightWhite: '#ffffff',
      },
    });
    const unicode = window.Unicode11Addon ? new window.Unicode11Addon.Unicode11Addon() : null;
    if (unicode) {
      term.loadAddon(unicode);
      term.unicode.activeVersion = '11';
    }
    const fit = window.FitAddon ? new window.FitAddon.FitAddon() : null;
    if (fit) term.loadAddon(fit);
    term.open(host);
    term.focus();
    let wheelRemainder = 0;
    const terminalAppOwnsMouseWheel = () => {
      const mouseMode = term.modes?.mouseTrackingMode || 'none';
      return term.buffer?.active?.type === 'alternate' && mouseMode !== 'none';
    };
    const wheelLineHeight = () => {
      const cell = term._core?._renderService?.dimensions?.css?.cell;
      return cell?.height || Math.max(12, Math.round((term.options.fontSize || 13) * (term.options.lineHeight || 1.18))) || 16;
    };
    term.attachCustomWheelEventHandler((event) => {
      if (event.ctrlKey) return true;
      if (terminalAppOwnsMouseWheel()) return true;
      const scale = event.deltaMode === 1
        ? 1
        : event.deltaMode === 2
          ? Math.max(1, term.rows - 2)
          : 1 / wheelLineHeight();
      wheelRemainder += event.deltaY * scale;
      const lines = wheelRemainder > 0 ? Math.floor(wheelRemainder) : Math.ceil(wheelRemainder);
      if (lines !== 0) {
        term.scrollLines(lines);
        wheelRemainder -= lines;
      }
      event.preventDefault();
      event.stopPropagation();
      return false;
    });
    term.attachCustomKeyEventHandler((event) => {
      if (event.type !== 'keydown') return true;
      if (!event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return true;
      if (event.code === 'PageUp') {
        term.scrollPages(-1);
      } else if (event.code === 'PageDown') {
        term.scrollPages(1);
      } else if (event.code === 'Home') {
        term.scrollToTop();
      } else if (event.code === 'End') {
        term.scrollToBottom();
      } else {
        return true;
      }
      event.preventDefault();
      return false;
    });
    termRef.current = term;
    fitRef.current = fit;
    lastSizeRef.current = '';
    const fitTimers = [];

    const sendResize = () => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN && termRef.current) {
        const key = `${termRef.current.cols}x${termRef.current.rows}`;
        if (key !== lastSizeRef.current) {
          lastSizeRef.current = key;
          ws.send(JSON.stringify({type: 'resize', cols: termRef.current.cols, rows: termRef.current.rows}));
        }
      }
    };
    let resizeFrame = 0;
    const fitTerminal = () => {
      if (fitRef.current) fitRef.current.fit();
      if (termRef.current) termRef.current.refresh(0, Math.max(0, termRef.current.rows - 1));
      sendResize();
    };
    const resize = () => {
      if (resizeFrame) cancelAnimationFrame(resizeFrame);
      resizeFrame = requestAnimationFrame(() => {
        resizeFrame = 0;
        fitTerminal();
      });
    };
    const scheduleFits = () => {
      TERMINAL_FIT_DELAYS_MS.forEach(delay => {
        fitTimers.push(setTimeout(resize, delay));
      });
    };
    const observer = new ResizeObserver(resize);
    observer.observe(host);
    window.addEventListener('resize', resize);
    scheduleFits();
    document.fonts?.ready?.then(() => {
      if (termRef.current !== term) return;
      fitTerminal();
      scheduleFits();
    });

    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
    const ws = new WebSocket(`${scheme}://${window.location.host}/ws/terminal?slug=${encodeURIComponent(agent.slug)}&cols=${term.cols}&rows=${term.rows}`);
    wsRef.current = ws;
    ws.onopen = () => {
      setTermStatus('interactive');
      scheduleFits();
    };
    ws.onmessage = (event) => {
      let msg;
      try { msg = JSON.parse(event.data); } catch (_) { return; }
      if (msg.type === 'output' && msg.data) term.write(msg.data);
      if (msg.type === 'status') setTermStatus(msg.message || 'interactive');
      if (msg.type === 'error') {
        setTermStatus(msg.message || 'terminal error');
        term.writeln('');
        term.writeln(`error: ${msg.message || 'terminal error'}`);
      }
    };
    ws.onclose = () => setTermStatus('disconnected');
    ws.onerror = () => setTermStatus('connection error');
    const dataDisposable = term.onData((data) => {
      const active = wsRef.current;
      const input = stripTerminalGeneratedInput(data);
      if (input && active && active.readyState === WebSocket.OPEN) {
        active.send(JSON.stringify({type: 'input', data: input}));
      }
    });
    const resizeDisposable = term.onResize(({ cols, rows }) => {
      const active = wsRef.current;
      if (active && active.readyState === WebSocket.OPEN) {
        const key = `${cols}x${rows}`;
        if (key !== lastSizeRef.current) {
          lastSizeRef.current = key;
          active.send(JSON.stringify({type: 'resize', cols, rows}));
        }
      }
    });
    const focusTimer = setTimeout(() => term.focus(), 120);
    const focusHandler = () => term.focus();
    window.addEventListener('flow-terminal-focus', focusHandler);
    const sendTerminalText = (text) => {
      const input = stripTerminalGeneratedInput(text || '');
      const active = wsRef.current;
      if (input && active && active.readyState === WebSocket.OPEN) {
        active.send(JSON.stringify({type: 'input', data: input}));
      }
    };
    const uploadFiles = async (files, source) => {
      const list = Array.from(files || []).filter(Boolean);
      if (!list.length) return;
      const priorStatus = termStatus;
      setTermStatus(`uploading ${list.length} ${list.length === 1 ? 'file' : 'files'}…`);
      try {
        const form = new FormData();
        list.forEach((file, index) => form.append('files', file, terminalUploadName(file, index)));
        const resp = await fetch(`/api/tasks/${encodeURIComponent(agent.slug)}/attachments`, {
          method: 'POST',
          body: form,
        });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) throw new Error(data.message || data.error || `upload failed (${resp.status})`);
        if (data.insert_text) sendTerminalText(` ${data.insert_text}`);
        setTermStatus(source === 'paste' ? 'pasted file path' : 'dropped file path');
        setTimeout(() => setTermStatus(isTerminalLiveStatus(priorStatus) ? priorStatus : 'interactive'), 900);
        term.focus();
      } catch (err) {
        setTermStatus(err.message || 'file upload failed');
      }
    };
    const pasteHandler = (event) => {
      const files = terminalClipboardFiles(event.clipboardData);
      if (!files.length) return;
      event.preventDefault();
      event.stopPropagation();
      uploadFiles(files, 'paste');
    };
    const dragOverHandler = (event) => {
      if (!event.dataTransfer || !event.dataTransfer.types || !Array.from(event.dataTransfer.types).includes('Files')) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = 'copy';
    };
    const dropHandler = (event) => {
      const files = Array.from(event.dataTransfer?.files || []);
      if (!files.length) return;
      event.preventDefault();
      event.stopPropagation();
      uploadFiles(files, 'drop');
    };
    host.addEventListener('paste', pasteHandler, true);
    host.addEventListener('dragover', dragOverHandler);
    host.addEventListener('drop', dropHandler);

    return () => {
      clearTimeout(focusTimer);
      fitTimers.forEach(clearTimeout);
      if (resizeFrame) cancelAnimationFrame(resizeFrame);
      window.removeEventListener('flow-terminal-focus', focusHandler);
      host.removeEventListener('paste', pasteHandler, true);
      host.removeEventListener('dragover', dragOverHandler);
      host.removeEventListener('drop', dropHandler);
      observer.disconnect();
      window.removeEventListener('resize', resize);
      dataDisposable.dispose();
      resizeDisposable.dispose();
      if (wsRef.current) wsRef.current.close();
      wsRef.current = null;
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [agent.slug, agent.session_id]);

  useEffect(() => {
    const timer = setTimeout(() => {
      if (fitRef.current) fitRef.current.fit();
      if (termRef.current) termRef.current.focus();
    }, 80);
    return () => clearTimeout(timer);
  }, [fullscreen]);

  const statusKind = termStatus === 'connecting'
    ? 'waiting'
    : (termStatus === 'interactive' || termStatus.startsWith('connected'))
      ? 'running'
      : 'idle';
  const terminalKindLabel = agent.terminal?.mode === 'shared'
    ? 'shared terminal'
    : (agent.provider === 'codex' ? 'codex terminal' : 'terminal');

  return (
    <div className={`pane terminal-pane ${fullscreen ? 'pane-fullscreen' : ''}`}>
      <div className="pane-head">
        <Icon name="terminal" size={11}/>
        <ProviderMark provider={agent.provider || 'claude'} size={12}/>
        <span>{terminalKindLabel} · {agent.branch}</span>
        <div className="right">
          <Dot status={statusKind}/>
          <span className="terminal-status mono">{termStatus}</span>
          <button
            className="pane-icon-btn"
            onClick={() => setFullscreen(v => !v)}
            title={fullscreen ? 'Exit fullscreen (Esc)' : 'Fullscreen — focus mode'}
            aria-label={fullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
          >
            <Icon name={fullscreen ? 'minimize-2' : 'maximize-2'} size={12}/>
          </button>
        </div>
      </div>
      <div className="pane-body term xterm-host">
        <div className="xterm-container" ref={ref}/>
        {termStatus === 'connecting' && (
          <div className="terminal-loading">
            <div className="terminal-loading-card">
              <div className="terminal-loading-title">
                <FlowMark size={22} title=""/>
                <span>opening {agent.provider === 'codex' ? 'codex' : 'agent'} terminal</span>
              </div>
              <div className="loader-bar"></div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

const ContextDrawer = ({ agent }) => (
  <div className="pane">
    <div className="pane-head">
      <Icon name="layers" size={11}/>
      <span>Context</span>
    </div>
    <div className="pane-body" style={{padding: 10}}>
      <div className="meta-card">
        <h4>Diff · {(agent.diff_files || SAMPLE_DIFF_FILES).length} files</h4>
        {(agent.diff_files || SAMPLE_DIFF_FILES).map(f => (
          <div key={f.name} style={{display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0', borderBottom: '1px solid var(--border)', fontSize: 11.5, fontFamily: 'var(--mono)'}}>
            <span style={{flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--text)'}}>{f.name.split('/').pop()}</span>
            <span style={{color: 'var(--running)'}}>+{f.add}</span>
            <span style={{color: 'var(--error)'}}>-{f.rem}</span>
          </div>
        ))}
        <button className="btn sm ghost" style={{marginTop: 8, width: '100%'}}><Icon name="external-link" size={11}/>Open in VS Code</button>
      </div>
      <div className="meta-card">
        <h4>Recent tools</h4>
        {(agent.recent_tools || []).length ? agent.recent_tools.map((r, i) => (
          <div key={i} style={{display: 'flex', gap: 8, padding: '3px 0', fontSize: 11.5, fontFamily: 'var(--mono)'}}>
            <span style={{color: 'var(--accent)', width: 40}}>{r.name}</span>
            <span style={{color: 'var(--text-dim)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{r.s}</span>
          </div>
        )) : <div className="mono dim" style={{fontSize: 11.5}}>No tool calls recorded for this task.</div>}
      </div>
      <div className="meta-card">
        <h4>Brief</h4>
        <div className="md" style={{fontSize: 12}}>
          <p style={{margin: 0, color: 'var(--text-mid)'}}>{agent.brief || agent.summary || 'No brief text found.'}</p>
        </div>
      </div>
      <div className="meta-card">
        <h4>Metadata</h4>
        <dl className="kv">
          <dt>session</dt><dd>{shortUUID(agent.session_id)}</dd>
          <dt>started</dt><dd>{formatAge(agent.started_min)} ago</dd>
          <dt>context</dt><dd title="Provider-reported context usage from the session JSONL.">{fmtTokens(agent.tokens_used)} / {fmtTokens(agent.tokens_max)} ({Math.min(100, Math.round((agent.tokens_used / Math.max(1, agent.tokens_max)) * 100))}%)</dd>
          <dt>work_dir</dt><dd style={{fontSize: 10.5}}>{agent.work_dir}</dd>
        </dl>
      </div>
    </div>
  </div>
);

// ───────── Tasks list ───────────────────────────────────────────────────
const TasksList = ({ setFocus, action, goto }) => {
  const completed = ((window.MC && window.MC.DONE_TASKS) || DONE_TASKS || []);
  const tasks = [
    ...AGENTS.map(a => ({ ...a, kind: 'task', hasAgent: true, status_outer: 'in-progress' })),
    ...BACKLOG.map(b => ({ ...b, kind: 'task', hasAgent: false, status_outer: 'backlog' })),
    ...(completed.length ? completed : (DEAD_AGENT ? [DEAD_AGENT] : [])).map(t => ({ ...t, kind: 'task', hasAgent: false, status_outer: 'done' })),
  ];
  const openTask = (t) => {
    if (goto) goto(`task/${t.slug}`);
  };
  return (
    <div>
      <div className="action-bar">
        <span style={{fontSize: 11, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em'}}>Status</span>
        {['backlog','in-progress','done'].map(s => <button key={s} className="btn sm">{s}</button>)}
        <span style={{fontSize: 11, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginLeft: 12}}>Priority</span>
        {['high','medium','low'].map(p => <button key={p} className="btn sm">{p}</button>)}
        <span style={{marginLeft: 'auto', fontFamily:'var(--mono)', fontSize: 11, color: 'var(--text-dim)'}}>{tasks.length} tasks</span>
      </div>
      <table className="table">
        <thead>
          <tr>
            <th style={{width: 30}}></th>
            <th>Status</th><th>Priority</th><th>Slug</th><th>Name</th><th>Project</th>
            <th>Dependencies</th><th>Branch</th><th>Age</th><th>Tags</th><th></th>
          </tr>
        </thead>
        <tbody>
          {tasks.map(t => {
            const blockReason = taskStartBlocker(t);
            return (
            <tr key={t.slug} style={{cursor: 'pointer'}} onClick={() => openTask(t)}>
              <td>{t.hasAgent && <Dot status={t.status}/>}</td>
              <td><StatusPill status={t.status_outer}/></td>
              <td><PriorityPill priority={t.priority}/></td>
              <td className="mono" style={{fontSize: 12}}>{t.slug}</td>
              <td>{t.name}</td>
              <td className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>{t.project}</td>
              <td><DependencyBadges task={t} compact/></td>
              <td>{t.branch ? <BranchChip name={t.branch}/> : <span style={{color: 'var(--text-faint)'}}>—</span>}</td>
              <td className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>{t.started_min ? formatAge(t.started_min) : '—'}</td>
              <td>{(t.tags || []).slice(0,2).map(tg => <span key={tg} className="tag-chip" style={{marginRight: 4}}>{tg}</span>)}</td>
              <td>
                <div className="row-attach">
                  {t.hasAgent ? (
                    <button className="btn sm primary" disabled={!isCapabilityAvailable('providers', t.provider || 'claude')} title={isCapabilityAvailable('providers', t.provider || 'claude') ? '' : capabilityReason('providers', t.provider || 'claude')} onClick={(e) => { e.stopPropagation(); action('attach', t); }}><Icon name="external-link" size={10}/>Open</button>
                  ) : t.status_outer === 'backlog' ? (
                    <button className="btn sm green" disabled={!anyProviderAvailable() || !!blockReason} title={blockReason || (anyProviderAvailable() ? 'Choose Claude or Codex' : 'No supported agent binary found on PATH')} onClick={(e) => { e.stopPropagation(); action('spawn', t); }}><Icon name="play" size={10}/>Spawn</button>
                  ) : (
                    <button className="btn sm" onClick={(e) => { e.stopPropagation(); goto && goto(`task/${t.slug}`); }}><Icon name="check-circle" size={10}/>Details</button>
                  )}
                  <button className="btn sm" title="Archive task" onClick={(e) => { e.stopPropagation(); action('delete', t); }}><Icon name="archive" size={10}/>Archive</button>
                </div>
              </td>
            </tr>
          );})}
        </tbody>
      </table>
    </div>
  );
};

// ───────── Projects ─────────────────────────────────────────────────────
const ProjectsList = ({ goto, action }) => (
  <div>
    <div className="section-head">
      <h2>Projects</h2>
      <span className="count mono">{PROJECTS_MC.length} active</span>
      <button className="btn sm primary" style={{marginLeft: 'auto'}} onClick={() => action('create-project-open', {})}>
        <Icon name="plus" size={11}/>New project
      </button>
    </div>
    <div className="agent-grid" style={{gridTemplateColumns: 'repeat(3, 1fr)'}}>
      {PROJECTS_MC.map(p => {
        const t = p.tasks;
        return (
          <div key={p.slug} className="tile" style={{cursor: 'pointer'}} onClick={() => goto(`project/${p.slug}`)}>
            <div className="tile-stripe" style={{background: 'var(--accent)'}}></div>
            <div className="tile-body">
              <div className="tile-head">
                <span className="mono" style={{fontSize: 12, fontWeight: 600, color: 'var(--text)'}}>{p.slug}</span>
                <span style={{marginLeft: 'auto'}}><PriorityPill priority={p.priority}/></span>
                <button className="btn sm" title="Archive project" onClick={(e) => { e.stopPropagation(); action('delete', { ...p, kind: 'project' }); }}><Icon name="archive" size={10}/></button>
              </div>
              <div className="tile-name">{p.name}</div>
              <div className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>{t.total} tasks · {t.in_progress} in progress · {t.backlog} backlog · {t.done} done</div>
              <div style={{display: 'flex', height: 6, borderRadius: 3, overflow: 'hidden', background: 'var(--surface-3)', marginTop: 6}}>
                <span style={{flex: t.in_progress, background: 'var(--accent)'}}></span>
                <span style={{flex: t.backlog, background: 'var(--idle)', opacity: 0.4}}></span>
                <span style={{flex: t.done, background: 'var(--running)', opacity: 0.5}}></span>
              </div>
            </div>
          </div>
        );
      })}
    </div>
  </div>
);

// ───────── Playbooks ───────────────────────────────────────────────────
const PlaybooksList = ({ action, goto }) => (
  <div>
    <div className="section-head"><h2>Playbooks</h2><span className="count mono">{PLAYBOOKS_MC.length} active</span></div>
    <div className="agent-grid" style={{gridTemplateColumns: 'repeat(2, 1fr)'}}>
      {PLAYBOOKS_MC.map(p => (
        <div key={p.slug} className="tile" style={{cursor: 'pointer'}} onClick={() => goto(`playbook/${p.slug}`)}>
          <div className="tile-stripe" style={{background: 'var(--accent)'}}></div>
          <div className="tile-body">
            <div className="tile-head">
              <span className="mono" style={{fontSize: 12, fontWeight: 600, color: 'var(--text)'}}>{p.slug}</span>
              {p.project && <span className="tag-chip" style={{marginLeft: 4}}>{p.project}</span>}
              <button className="btn sm primary" style={{marginLeft: 'auto'}} disabled={!anyProviderAvailable()} title={anyProviderAvailable() ? '' : 'No supported agent binary found on PATH'} onClick={(e) => { e.stopPropagation(); action('spawn-run', { ...p, provider: defaultAvailableProvider() }); }}><Icon name="play" size={11}/>Spawn run</button>
              <button className="btn sm" title="Archive playbook" onClick={(e) => { e.stopPropagation(); action('delete', { ...p, kind: 'playbook' }); }}><Icon name="archive" size={10}/></button>
            </div>
            <div className="tile-name">{p.name}</div>
            <div className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>
              {p.runs_week} runs this week · {p.last_min == null ? 'never fired' : `last fired ${formatAge(p.last_min)} ago`}
            </div>
            <div className="mono" title={p.work_dir} style={{fontSize: 10.5, color: 'var(--text-faint)', marginTop: 5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{p.work_dir}</div>
            {(() => {
              const days = ['M','T','W','T','F','S','S'];
              const spark = p.spark.slice(-7);
              const max = Math.max(1, ...spark);
              return (
                <div className="pb-runchart" style={{marginTop: 10}}>
                  <div className="pb-runchart-bars" style={{display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: 4, alignItems: 'end', height: 38}}>
                    {spark.map((v, i) => {
                      const h = v === 0 ? 4 : Math.max(8, (v / max) * 100);
                      const isToday = i === spark.length - 1;
                      return (
                        <div key={i} title={`${v} run${v===1?'':'s'} · ${days[i]}`} style={{position: 'relative', height: '100%', display: 'flex', flexDirection: 'column', justifyContent: 'flex-end'}}>
                          <div style={{
                            height: `${h}%`,
                            background: v === 0 ? 'var(--surface-3)' : isToday ? 'var(--primary-hi)' : 'var(--accent)',
                            opacity: v === 0 ? 0.5 : isToday ? 1 : 0.55 + (v/max)*0.35,
                            borderRadius: '2px 2px 0 0',
                            boxShadow: isToday && v > 0 ? '0 0 10px var(--primary)' : 'none',
                            transition: 'height 220ms ease-out',
                          }}></div>
                          {v > 0 && <div className="mono" style={{position: 'absolute', top: -12, left: 0, right: 0, textAlign: 'center', fontSize: 9, color: 'var(--text-dim)'}}>{v}</div>}
                        </div>
                      );
                    })}
                  </div>
                  <div className="pb-runchart-axis" style={{display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: 4, marginTop: 4}}>
                    {days.map((d, i) => (
                      <div key={i} className="mono" style={{textAlign: 'center', fontSize: 9.5, color: i === days.length-1 ? 'var(--text-mid)' : 'var(--text-faint)', textTransform: 'uppercase', letterSpacing: '0.05em'}}>{d}</div>
                    ))}
                  </div>
                </div>
              );
            })()}
          </div>
        </div>
      ))}
    </div>
  </div>
);

const markdownInlineParts = (text) => {
  const parts = [];
  const source = String(text || '');
  const re = /(`[^`]+`|\*\*[^*]+\*\*|__[^_]+__|\*[^*\s][^*]*\*|_[^_\s][^_]*_)/g;
  let last = 0;
  let match;
  while ((match = re.exec(source))) {
    if (match.index > last) parts.push({ type: 'text', text: source.slice(last, match.index) });
    const token = match[0];
    if (token.startsWith('`')) parts.push({ type: 'code', text: token.slice(1, -1) });
    else if (token.startsWith('**') || token.startsWith('__')) parts.push({ type: 'strong', text: token.slice(2, -2) });
    else parts.push({ type: 'em', text: token.slice(1, -1) });
    last = match.index + token.length;
  }
  if (source.length > last) parts.push({ type: 'text', text: source.slice(last) });
  return parts.length ? parts : [{ type: 'text', text: source }];
};

const MarkdownInline = ({ text }) => (
  <>
    {markdownInlineParts(text).map((part, i) => {
      if (part.type === 'code') return <code key={i}>{part.text}</code>;
      if (part.type === 'strong') return <strong key={i}>{part.text}</strong>;
      if (part.type === 'em') return <em key={i}>{part.text}</em>;
      return <Fragment key={i}>{part.text}</Fragment>;
    })}
  </>
);

const MarkdownView = ({ source, empty = 'No brief text found.' }) => {
  const text = String(source || '').replace(/\r\n/g, '\n').trim();
  if (!text) return <div className="markdown-rendered empty">{empty}</div>;
  const blocks = [];
  const lines = text.split('\n');
  let paragraph = [];
  let list = null;
  let code = null;
  const flushParagraph = () => {
    if (!paragraph.length) return;
    blocks.push({ type: 'p', text: paragraph.join(' ') });
    paragraph = [];
  };
  const flushList = () => {
    if (!list) return;
    blocks.push(list);
    list = null;
  };
  const flushCode = () => {
    if (!code) return;
    blocks.push({ type: 'code', text: code.lines.join('\n') });
    code = null;
  };
  for (const raw of lines) {
    const line = raw.replace(/\s+$/, '');
    const trimmed = line.trim();
    const fence = trimmed.match(/^```/);
    if (code) {
      if (fence) flushCode();
      else code.lines.push(line);
      continue;
    }
    if (fence) {
      flushParagraph();
      flushList();
      code = { lines: [] };
      continue;
    }
    if (!trimmed) {
      flushParagraph();
      flushList();
      continue;
    }
    const heading = trimmed.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      flushList();
      blocks.push({ type: 'h', level: heading[1].length, text: heading[2] });
      continue;
    }
    if (/^[-*_]{3,}$/.test(trimmed)) {
      flushParagraph();
      flushList();
      blocks.push({ type: 'hr' });
      continue;
    }
    const quote = trimmed.match(/^>\s?(.*)$/);
    if (quote) {
      flushParagraph();
      flushList();
      blocks.push({ type: 'quote', text: quote[1] });
      continue;
    }
    const unordered = trimmed.match(/^[-*]\s+(.+)$/);
    const ordered = trimmed.match(/^(\d+)[.)]\s+(.+)$/);
    if (unordered || ordered) {
      flushParagraph();
      const type = ordered ? 'ol' : 'ul';
      if (list && list.type !== type) flushList();
      if (!list) list = { type, items: [] };
      list.items.push(ordered ? ordered[2] : unordered[1]);
      continue;
    }
    flushList();
    paragraph.push(trimmed);
  }
  flushParagraph();
  flushList();
  flushCode();
  return (
    <div className="markdown-rendered">
      {blocks.map((block, i) => {
        if (block.type === 'h') {
          const Tag = `h${Math.min(4, block.level)}`;
          return <Tag key={i}><MarkdownInline text={block.text}/></Tag>;
        }
        if (block.type === 'p') return <p key={i}><MarkdownInline text={block.text}/></p>;
        if (block.type === 'quote') return <blockquote key={i}><MarkdownInline text={block.text}/></blockquote>;
        if (block.type === 'hr') return <hr key={i}/>;
        if (block.type === 'code') return <pre key={i}><code>{block.text}</code></pre>;
        if (block.type === 'ul') return <ul key={i}>{block.items.map((item, j) => <li key={j}><MarkdownInline text={item}/></li>)}</ul>;
        if (block.type === 'ol') return <ol key={i}>{block.items.map((item, j) => <li key={j}><MarkdownInline text={item}/></li>)}</ol>;
        return null;
      })}
    </div>
  );
};

const EntityTabs = ({ tabs, active, onChange }) => (
  <div className="entity-tabs">
    {tabs.map(tab => (
      <button key={tab.id} className={active === tab.id ? 'active' : ''} onClick={() => onChange(tab.id)}>
        <Icon name={tab.icon} size={13}/>{tab.label}
      </button>
    ))}
  </div>
);

const ReadableFiles = ({ files, empty, fetchFile, minutesSinceISO, sourceKey = '' }) => {
  const [items, setItems] = useState([]);
  const [closedFiles, setClosedFiles] = useState(() => new Set());
  const toggleFile = (name) => {
    setClosedFiles(prev => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };
  useEffect(() => {
    let active = true;
    setItems((files || []).map(f => ({ ...f, loading: true, error: '', content: '' })));
    Promise.all((files || []).map(file =>
      fetchFile(file)
        .then(content => ({ ...file, loading: false, error: '', content }))
        .catch(err => ({ ...file, loading: false, error: err.message || String(err), content: '' }))
    )).then(next => { if (active) setItems(next); });
    return () => { active = false; };
  }, [sourceKey, (files || []).map(f => `${f.route || ''}:${f.filename}:${f.mtime || ''}`).join('|')]);
  if (!files || files.length === 0) return <BrandEmpty compact title={empty} body="Markdown files will appear here when they are added."/>;
  return (
    <div className="readable-files">
      {items.map(file => {
        const open = !closedFiles.has(file.filename);
        return (
          <article key={file.filename} className={`readable-file ${open ? 'open' : 'closed'}`}>
            <button
              type="button"
              className="readable-file-head"
              onClick={() => toggleFile(file.filename)}
              aria-expanded={open}
              aria-label={`${open ? 'Collapse' : 'Expand'} ${file.filename}`}
              title={`${open ? 'Collapse' : 'Expand'} ${file.filename}`}
            >
              <Icon name={open ? 'chevron-down' : 'chevron-right'} size={12}/>
              <Icon name="file-text" size={13}/>
              <span className="mono">{file.filename}</span>
              <span className="mono dim">{file.mtime ? `${formatAge(minutesSinceISO(file.mtime))} ago` : ''}</span>
            </button>
            {open && (file.loading ? <SkeletonRows rows={3}/> : file.error ? (
              <div className="mono" style={{fontSize: 11, color: 'var(--dead)'}}>{file.error}</div>
            ) : (
              <MarkdownView source={file.content} empty="No content found."/>
            ))}
          </article>
        );
      })}
    </div>
  );
};

// ───────── Playbook detail ──────────────────────────────────────────────
const PlaybookDetail = ({ slug, goto, action }) => {
  const summary = PLAYBOOKS_MC.find(p => p.slug === slug);
  const [detail, setDetail] = useState(null);
  const [brief, setBrief] = useState('');
  const [draftBrief, setDraftBrief] = useState('');
  const [editMode, setEditMode] = useState(false);
  const [activeTab, setActiveTab] = useState('brief');
  const [loadState, setLoadState] = useState({ loading: true, error: '' });
  const [saveState, setSaveState] = useState({ saving: false, error: '' });
  const [filePreview, setFilePreview] = useState(null);

  const loadDetail = () => {
    setLoadState({ loading: true, error: '' });
    Promise.all([
      fetch(`/api/playbooks/${encodeURIComponent(slug)}`).then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
      fetch(`/api/playbooks/${encodeURIComponent(slug)}/brief`).then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
    ])
      .then(([nextDetail, nextBrief]) => {
        setDetail(nextDetail);
        setBrief(nextBrief);
        setDraftBrief(nextBrief);
        setEditMode(false);
        setLoadState({ loading: false, error: '' });
      })
      .catch(err => setLoadState({ loading: false, error: err.message || String(err) }));
  };

  useEffect(() => {
    let active = true;
    setLoadState({ loading: true, error: '' });
    Promise.all([
      fetch(`/api/playbooks/${encodeURIComponent(slug)}`).then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
      fetch(`/api/playbooks/${encodeURIComponent(slug)}/brief`).then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
    ])
      .then(([nextDetail, nextBrief]) => {
        if (!active) return;
        setDetail(nextDetail);
        setBrief(nextBrief);
        setDraftBrief(nextBrief);
        setEditMode(false);
        setLoadState({ loading: false, error: '' });
      })
      .catch(err => {
        if (!active) return;
        setLoadState({ loading: false, error: err.message || String(err) });
      });
    return () => { active = false; };
  }, [slug]);

  const saveBrief = () => {
    setSaveState({ saving: true, error: '' });
    fetch(`/api/playbooks/${encodeURIComponent(slug)}/brief`, {
      method: 'PUT',
      headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
      body: draftBrief,
    })
      .then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))))
      .then(() => {
        setBrief(draftBrief);
        setEditMode(false);
        setSaveState({ saving: false, error: '' });
        loadDetail();
      })
      .catch(err => setSaveState({ saving: false, error: err.message || String(err) }));
  };

  const openRelatedFile = (file) => {
    setFilePreview({ ...file, loading: true, error: '', content: '' });
    fetch(`/api/playbooks/${encodeURIComponent(slug)}/${file.route}/${encodeURIComponent(file.filename)}`)
      .then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))))
      .then(content => setFilePreview({ ...file, loading: false, error: '', content }))
      .catch(err => setFilePreview({ ...file, loading: false, error: err.message || String(err), content: '' }));
  };

  const pb = detail || summary;
  if (!pb && loadState.loading) return <div className="pane" style={{padding: 18}}><SkeletonRows rows={5}/></div>;
  if (!pb) return <div><BrandEmpty title="Playbook not found" body={`No playbook matches ${slug}.`}/><button className="btn sm" style={{marginTop: 12}} onClick={() => goto('playbooks')}>Back to playbooks</button></div>;

  const days = ['M','T','W','T','F','S','S'];
  const runDays = detail ? (detail.run_days_30 || []) : (pb.spark || []);
  const spark = runDays.slice(-7);
  const max = Math.max(1, ...spark);
  const recentRuns = detail ? (detail.recent_runs || []) : [];
  const runsWeek = detail ? detail.run_count_7d : pb.runs_week;
  const totalRuns = runDays.reduce((a,b) => a+b, 0);
  const activeRuns = recentRuns.filter(r => r.status === 'in-progress').length;
  const lastRun = recentRuns[0];
  const project = detail ? detail.project_slug : pb.project;
  const workDir = detail ? detail.work_dir : pb.work_dir;
  const briefPath = detail ? detail.brief_path : '';
  const updates = detail ? (detail.updates || []) : [];
  const auxFiles = detail ? (detail.aux_files || []) : [];
  const relatedFiles = [
    ...updates.map(f => ({ ...f, type: 'update', route: 'updates' })),
    ...auxFiles.map(f => ({ ...f, type: 'sidecar', route: 'aux' })),
  ];
  const auxCount = auxFiles.length;
  const updateCount = updates.length;
  const minutesSinceISO = (iso) => {
    if (!iso) return null;
    const ts = Date.parse(iso);
    if (!Number.isFinite(ts)) return null;
    return Math.max(0, (Date.now() - ts) / 60000);
  };
  const lastRunLabel = lastRun ? `${formatAge(minutesSinceISO(lastRun.created_at))} ago` : 'never';
  const playbookTabs = [
    { id: 'brief', label: 'Brief', icon: 'file-text' },
    { id: 'runs', label: 'Run history', icon: 'list' },
    { id: 'updates', label: 'Updates', icon: 'activity' },
    { id: 'sidecars', label: 'Sidecar files', icon: 'folder' },
  ];
  const fetchPlaybookFile = (file) =>
    fetch(`/api/playbooks/${encodeURIComponent(slug)}/${file.route}/${encodeURIComponent(file.filename)}`)
      .then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))));

  return (
    <div className="entity-page">
      <div className="entity-hero">
        <div className="entity-hero-main">
          <div className="entity-kicker"><button className="btn sm" onClick={() => goto('playbooks')}><Icon name="arrow-left" size={11}/>Back</button>{project && <span className="tag-chip">{project}</span>}</div>
          <div className="entity-title-row">
            <h1>{pb.slug}</h1>
            <span className="tag-chip"><Icon name="play" size={11}/>playbook</span>
          </div>
          <p className="entity-subtitle">{pb.name}</p>
          <div className="entity-meta-row">
            <span title={workDir}><Icon name="folder" size={13}/>{workDir || 'no workdir'}</span>
            <span><Icon name="clock" size={13}/>last run {lastRunLabel}</span>
          </div>
        </div>
        <div className="entity-hero-actions">
          <button className="btn sm" onClick={() => { setDraftBrief(brief); setEditMode(true); }}><Icon name="edit-2" size={11}/>Edit</button>
          <button className="btn sm" onClick={() => action('delete', { ...pb, kind: 'playbook' })}><Icon name="archive" size={11}/>Archive</button>
          <button className="btn sm primary" disabled={!anyProviderAvailable()} title={anyProviderAvailable() ? '' : 'No supported agent binary found on PATH'} onClick={() => action('spawn-run', { ...pb, provider: defaultAvailableProvider() })}><Icon name="play" size={11}/>Spawn run</button>
        </div>
      </div>
      {loadState.error && <div className="pane" style={{padding: 12, marginTop: 12, borderColor: 'var(--dead)'}}><span className="mono" style={{fontSize: 11, color: 'var(--dead)'}}>{loadState.error}</span></div>}

      <div style={{display: 'grid', gridTemplateColumns: '1.4fr 1fr', gap: 16, marginTop: 12}}>
        <div className="pane" style={{padding: 16}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Activity · last 7 days</div>
          <div style={{display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: 8, alignItems: 'end', height: 110}}>
            {spark.map((v, i) => {
              const h = v === 0 ? 4 : Math.max(8, (v / max) * 100);
              const isToday = i === spark.length - 1;
              return (
                <div key={i} style={{position: 'relative', height: '100%', display: 'flex', flexDirection: 'column', justifyContent: 'flex-end'}}>
                  <div style={{ height: `${h}%`, background: v === 0 ? 'var(--surface-3)' : isToday ? 'var(--primary-hi)' : 'var(--accent)', opacity: v === 0 ? 0.5 : isToday ? 1 : 0.6 + (v/max)*0.3, borderRadius: '3px 3px 0 0', boxShadow: isToday && v > 0 ? '0 0 14px var(--primary)' : 'none' }}></div>
                  {v > 0 && <div className="mono" style={{position: 'absolute', top: -14, left: 0, right: 0, textAlign: 'center', fontSize: 10, color: 'var(--text)'}}>{v}</div>}
                </div>
              );
            })}
          </div>
          <div style={{display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: 8, marginTop: 6}}>
            {days.map((d, i) => <div key={i} className="mono" style={{textAlign: 'center', fontSize: 10, color: i === days.length-1 ? 'var(--text-mid)' : 'var(--text-faint)', textTransform: 'uppercase'}}>{d}</div>)}
          </div>
        </div>
        <div className="pane" style={{padding: 16}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Summary</div>
          <div style={{display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12}}>
            <div><div style={{fontSize: 22, fontWeight: 600, color: 'var(--text)'}}>{runsWeek}</div><div className="mono dim" style={{fontSize: 10.5}}>runs this week</div></div>
            <div><div style={{fontSize: 22, fontWeight: 600, color: 'var(--text)'}}>{totalRuns}</div><div className="mono dim" style={{fontSize: 10.5}}>total runs (30d)</div></div>
            <div><div style={{fontSize: 14, color: 'var(--text)', fontFamily: 'var(--mono)'}}>{lastRunLabel}</div><div className="mono dim" style={{fontSize: 10.5}}>last run</div></div>
            <div><div style={{fontSize: 14, color: activeRuns ? 'var(--running)' : 'var(--text)', fontFamily: 'var(--mono)'}}>{activeRuns}</div><div className="mono dim" style={{fontSize: 10.5}}>active runs</div></div>
          </div>
          <div style={{marginTop: 14, display: 'grid', gap: 7}}>
            <div className="mono" title={workDir} style={{fontSize: 11, color: 'var(--text-mid)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>work_dir: {workDir}</div>
            {briefPath && <div className="mono" title={briefPath} style={{fontSize: 11, color: 'var(--text-faint)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>brief: {briefPath}</div>}
            <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>{updateCount} updates · {auxCount} sidecar files</div>
          </div>
        </div>
      </div>

      <EntityTabs tabs={playbookTabs} active={activeTab} onChange={setActiveTab}/>

      {activeTab === 'brief' && <div className="entity-card">
        <div style={{display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em'}}>Brief</div>
          {editMode && <span className="tag-chip">editing</span>}
          <div style={{marginLeft: 'auto', display: 'flex', gap: 6}}>
            {editMode ? (
              <>
                <button className="btn sm" onClick={() => { setDraftBrief(brief); setEditMode(false); }} disabled={saveState.saving}>Cancel</button>
                <button className="btn sm primary" onClick={saveBrief} disabled={saveState.saving}><Icon name="save" size={11}/>{saveState.saving ? 'Saving' : 'Save'}</button>
              </>
            ) : (
              <button className="btn sm" onClick={() => { setDraftBrief(brief); setEditMode(true); }}><Icon name="edit-2" size={11}/>Edit</button>
            )}
          </div>
        </div>
        {saveState.error && <div className="mono" style={{fontSize: 11, color: 'var(--dead)', marginBottom: 10}}>{saveState.error}</div>}
        {editMode ? (
          <textarea className="form-input" value={draftBrief} onChange={e => setDraftBrief(e.target.value)} spellCheck={false} style={{minHeight: 360, width: '100%', fontFamily: 'var(--mono)', fontSize: 12.5, lineHeight: 1.55, resize: 'vertical'}}/>
        ) : (
          <MarkdownView source={brief} empty="No brief text found."/>
        )}
      </div>}

      {activeTab === 'updates' && <ReadableFiles
        files={updates.map(f => ({ ...f, route: 'updates' }))}
        empty="No playbook updates yet"
        fetchFile={fetchPlaybookFile}
        minutesSinceISO={minutesSinceISO}
      />}

      {activeTab === 'sidecars' && <ReadableFiles
        files={auxFiles.map(f => ({ ...f, route: 'aux' }))}
        empty="No sidecar files yet"
        fetchFile={fetchPlaybookFile}
        minutesSinceISO={minutesSinceISO}
      />}

      {activeTab === 'runs' && <div className="entity-card">
        <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Recent runs</div>
        {recentRuns.length ? (
          <div className="run-history-table">
            {recentRuns.map(r => (
              <div className="run-history-row" key={r.slug}>
                <div className="mono name">{r.slug}</div>
                <StatusPill status={r.status}/>
                <span className="mono dim">{formatAge(minutesSinceISO(r.created_at))} ago</span>
                <span className="mono dim">{formatAge(minutesSinceISO(r.updated_at))} ago</span>
                <button className="btn sm" onClick={() => goto(`session/${r.slug}`)}>View</button>
              </div>
            ))}
          </div>
        ) : (
          <div className="mono dim" style={{padding: 10}}>No runs yet.</div>
        )}
      </div>}
    </div>
  );
};

// ───────── Project detail ──────────────────────────────────────────────
const ProjectDetail = ({ slug, goto, action, onAddTask, refreshKey }) => {
  const summary = PROJECTS_MC.find(p => p.slug === slug);
  const [detail, setDetail] = useState(null);
  const [brief, setBrief] = useState('');
  const [draftBrief, setDraftBrief] = useState('');
  const [editMode, setEditMode] = useState(false);
  const [tasks, setTasks] = useState([]);
  const [loadState, setLoadState] = useState({ loading: true, error: '' });
  const [saveState, setSaveState] = useState({ saving: false, error: '' });

  useEffect(() => {
    let active = true;
    setLoadState({ loading: true, error: '' });
    Promise.all([
      fetch(`/api/projects/${encodeURIComponent(slug)}`).then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
      fetch(`/api/projects/${encodeURIComponent(slug)}/brief`).then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
      fetch(`/api/projects/${encodeURIComponent(slug)}/tasks?include_done=true`).then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
    ])
      .then(([nextDetail, nextBrief, nextTasks]) => {
        if (!active) return;
        setDetail(nextDetail);
        setBrief(nextBrief);
        setDraftBrief(nextBrief);
        setEditMode(false);
        setTasks(Array.isArray(nextTasks) ? nextTasks : []);
        setLoadState({ loading: false, error: '' });
      })
      .catch(err => {
        if (!active) return;
        setLoadState({ loading: false, error: err.message || String(err) });
      });
    return () => { active = false; };
  }, [slug, refreshKey]);

  const saveBrief = () => {
    setSaveState({ saving: true, error: '' });
    fetch(`/api/projects/${encodeURIComponent(slug)}/brief`, {
      method: 'PUT',
      headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
      body: draftBrief,
    })
      .then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))))
      .then(() => {
        setBrief(draftBrief);
        setEditMode(false);
        setSaveState({ saving: false, error: '' });
      })
      .catch(err => setSaveState({ saving: false, error: err.message || String(err) }));
  };

  const pr = detail || summary;
  if (!pr && loadState.loading) return <div className="pane" style={{padding: 18}}><SkeletonRows rows={5}/></div>;
  if (!pr) return <div><BrandEmpty title="Project not found" body={`No project matches ${slug}.`}/><button className="btn sm" style={{marginTop: 12}} onClick={() => goto('projects')}>Back to projects</button></div>;

  const counts = detail ? detail.task_counts : (summary ? summary.tasks : { total: 0, in_progress: 0, backlog: 0, done: 0 });
  const workDir = detail ? detail.work_dir : (summary ? summary.work_dir : '');
  const briefPath = detail ? detail.brief_path : '';
  const updates = detail ? (detail.updates || []) : [];
  const auxCount = detail ? (detail.aux_files || []).length : 0;
  const status = detail ? detail.status : 'active';

  const grouped = { 'in-progress': [], backlog: [], done: [] };
  tasks.forEach(t => {
    if (!grouped[t.status]) grouped[t.status] = [];
    grouped[t.status].push(t);
  });
  const statusOrder = ['in-progress', 'backlog', 'done'];

  const minutesSinceISO = (iso) => {
    if (!iso) return null;
    const ts = Date.parse(iso);
    if (!Number.isFinite(ts)) return null;
    return Math.max(0, (Date.now() - ts) / 60000);
  };
  const fetchProjectFile = (file) =>
    fetch(`/api/projects/${encodeURIComponent(slug)}/${file.route}/${encodeURIComponent(file.filename)}`)
      .then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))));

  return (
    <div className="entity-page">
      <div className="entity-hero">
        <div className="entity-hero-main">
          <div className="entity-kicker"><button className="btn sm" onClick={() => goto('projects')}><Icon name="arrow-left" size={11}/>Back</button></div>
          <div className="entity-title-row">
            <h1>{pr.slug}</h1>
            <StatusPill status={status}/>
            <PriorityPill priority={pr.priority}/>
          </div>
          <p className="entity-subtitle">{pr.name}</p>
          <div className="entity-meta-row">
            <span title={workDir}><Icon name="folder" size={13}/>{workDir || 'no workdir'}</span>
            {detail && <span><Icon name="clock" size={13}/>updated {formatAge(minutesSinceISO(detail.updated_at))} ago</span>}
          </div>
        </div>
        <div className="entity-hero-actions">
          <button className="btn sm" onClick={() => { setDraftBrief(brief); setEditMode(true); }}><Icon name="edit-2" size={11}/>Edit</button>
          <button className="btn sm primary" onClick={() => onAddTask && onAddTask(pr.slug)}><Icon name="plus" size={11}/>Add task</button>
          <button className="btn sm" onClick={() => action('delete', { ...pr, kind: 'project' })}><Icon name="archive" size={11}/>Archive</button>
        </div>
      </div>
      {loadState.error && <div className="pane" style={{padding: 12, marginTop: 12, borderColor: 'var(--dead)'}}><span className="mono" style={{fontSize: 11, color: 'var(--dead)'}}>{loadState.error}</span></div>}

      <div style={{display: 'grid', gridTemplateColumns: '1.4fr 1fr', gap: 16, marginTop: 12}}>
        <div className="pane" style={{padding: 16}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Tasks summary</div>
          <div style={{display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12}}>
            <div><div style={{fontSize: 22, fontWeight: 600, color: 'var(--text)'}}>{counts.total}</div><div className="mono dim" style={{fontSize: 10.5}}>total</div></div>
            <div><div style={{fontSize: 22, fontWeight: 600, color: 'var(--accent)'}}>{counts.in_progress}</div><div className="mono dim" style={{fontSize: 10.5}}>in progress</div></div>
            <div><div style={{fontSize: 22, fontWeight: 600, color: 'var(--text)'}}>{counts.backlog}</div><div className="mono dim" style={{fontSize: 10.5}}>backlog</div></div>
            <div><div style={{fontSize: 22, fontWeight: 600, color: 'var(--text)'}}>{counts.done}</div><div className="mono dim" style={{fontSize: 10.5}}>done</div></div>
          </div>
          <div style={{display: 'flex', height: 8, borderRadius: 4, overflow: 'hidden', background: 'var(--surface-3)', marginTop: 14}}>
            <span style={{flex: counts.in_progress, background: 'var(--accent)'}}></span>
            <span style={{flex: counts.backlog, background: 'var(--idle)', opacity: 0.4}}></span>
            <span style={{flex: counts.done, background: 'var(--running)', opacity: 0.5}}></span>
          </div>
        </div>
        <div className="pane" style={{padding: 16}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Details</div>
          <div style={{display: 'grid', gap: 7}}>
            <div className="mono" title={workDir} style={{fontSize: 11, color: 'var(--text-mid)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>work_dir: {workDir || '—'}</div>
            {briefPath && <div className="mono" title={briefPath} style={{fontSize: 11, color: 'var(--text-faint)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>brief: {briefPath}</div>}
            {detail && <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>created: {detail.created_at?.slice(0, 10)} · updated: {detail.updated_at?.slice(0, 10)}</div>}
            <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>{updates.length} updates · {auxCount} sidecar files</div>
          </div>
        </div>
      </div>

      <div className="entity-card" style={{marginTop: 16}}>
        <div style={{display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em'}}>Brief</div>
          {editMode && <span className="tag-chip">editing</span>}
          <div style={{marginLeft: 'auto', display: 'flex', gap: 6}}>
            {editMode ? (
              <>
                <button className="btn sm" onClick={() => { setDraftBrief(brief); setEditMode(false); }} disabled={saveState.saving}>Cancel</button>
                <button className="btn sm primary" onClick={saveBrief} disabled={saveState.saving}><Icon name="save" size={11}/>{saveState.saving ? 'Saving' : 'Save'}</button>
              </>
            ) : (
              <button className="btn sm" onClick={() => { setDraftBrief(brief); setEditMode(true); }}><Icon name="edit-2" size={11}/>Edit</button>
            )}
          </div>
        </div>
        {saveState.error && <div className="mono" style={{fontSize: 11, color: 'var(--dead)', marginBottom: 10}}>{saveState.error}</div>}
        {editMode ? (
          <textarea className="form-input" value={draftBrief} onChange={e => setDraftBrief(e.target.value)} spellCheck={false} style={{minHeight: 360, width: '100%', fontFamily: 'var(--mono)', fontSize: 12.5, lineHeight: 1.55, resize: 'vertical'}}/>
        ) : (
          <MarkdownView source={brief} empty="No brief text found."/>
        )}
      </div>

      <div className="pane" style={{padding: 16, marginTop: 16}}>
        <div style={{display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em'}}>Tasks</div>
          <span className="count mono">{tasks.length}</span>
          <button className="btn sm primary" style={{marginLeft: 'auto'}} onClick={() => onAddTask && onAddTask(pr.slug)}><Icon name="plus" size={11}/>Add task</button>
        </div>
        {tasks.length === 0 ? (
          <div className="mono dim" style={{padding: 10, fontSize: 12}}>No tasks under this project yet.</div>
        ) : (
          statusOrder.map(st => grouped[st] && grouped[st].length > 0 && (
            <div key={st} style={{marginBottom: 14}}>
              <div className="mono" style={{fontSize: 10, color: 'var(--text-faint)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 6}}>{st} <span style={{opacity: 0.5}}>({grouped[st].length})</span></div>
              <table className="tbl" style={{width: '100%'}}>
                <tbody>
                  {grouped[st].map(t => (
                    <tr key={t.slug}>
                      <td className="mono" style={{fontSize: 12, fontWeight: 600, color: 'var(--text)'}}>{t.slug}</td>
                      <td style={{color: 'var(--text-mid)'}}>{t.name}</td>
                      <td><PriorityPill priority={t.priority}/></td>
                      <td><DependencyBadges task={t} compact/></td>
                      <td className="mono dim" style={{fontSize: 11}}>{t.waiting_on ? `waiting: ${t.waiting_on}` : (t.temporal_summary || '')}</td>
                      <td style={{textAlign: 'right'}}><button className="btn sm" onClick={() => goto(`task/${t.slug}`)}>View</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))
        )}
      </div>

      <div style={{marginTop: 16}}>
        <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Recent updates</div>
        <ReadableFiles
          files={updates.map(f => ({ ...f, route: 'updates' }))}
          empty="No project updates yet"
          fetchFile={fetchProjectFile}
          minutesSinceISO={minutesSinceISO}
        />
      </div>
    </div>
  );
};

// ───────── Task detail ─────────────────────────────────────────────────
const TaskDetail = ({ slug, goto, action, refreshKey }) => {
  const [detail, setDetail] = useState(null);
  const [brief, setBrief] = useState('');
  const [loadState, setLoadState] = useState({ loading: true, error: '' });

  useEffect(() => {
    let active = true;
    setLoadState({ loading: true, error: '' });
    Promise.all([
      fetch(`/api/tasks/${encodeURIComponent(slug)}`).then(r => r.ok ? r.json() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))),
      fetch(`/api/tasks/${encodeURIComponent(slug)}/brief`).then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`)))).catch(() => ''),
    ])
      .then(([nextDetail, nextBrief]) => {
        if (!active) return;
        setDetail(nextDetail);
        setBrief(nextBrief || '');
        setLoadState({ loading: false, error: '' });
      })
      .catch(err => {
        if (!active) return;
        setLoadState({ loading: false, error: err.message || String(err) });
      });
    return () => { active = false; };
  }, [slug, refreshKey]);

  if (loadState.loading && !detail) return <div className="pane" style={{padding: 18}}><SkeletonRows rows={5}/></div>;
  if (loadState.error && !detail) return <div><BrandEmpty title="Task not found" body={loadState.error || `No task matches ${slug}.`}/><button className="btn sm" style={{marginTop: 12}} onClick={() => goto('tasks')}>Back to tasks</button></div>;
  if (!detail) return null;

  const minutesSinceISO = (iso) => {
    if (!iso) return null;
    const ts = Date.parse(iso);
    if (!Number.isFinite(ts)) return null;
    return Math.max(0, (Date.now() - ts) / 60000);
  };
  const fetchTaskFile = (file) =>
    fetch(`/api/tasks/${encodeURIComponent(slug)}/${file.route}/${encodeURIComponent(file.filename)}`)
      .then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(new Error(t || `HTTP ${r.status}`))));

  const live = !!detail.live;
  const status = detail.status;
  const provider = detail.session_provider || 'claude';
  const workDir = detail.work_dir || '';
  const updates = detail.updates || [];
  const aux = detail.aux_files || [];
  const tags = detail.tags || [];
  const stale = detail.stale_days && detail.stale_days > 0;

  const openSession = () => goto(`session/${slug}`);
  const sessionButton = (() => {
    if (live) {
      return <button className="btn sm primary" onClick={() => action('attach', { ...detail, slug, hasAgent: true, provider })}><Icon name="external-link" size={11}/>Resume terminal</button>;
    }
    if (status === 'backlog') {
      const blocked = !anyProviderAvailable();
      return <button className="btn sm green" disabled={blocked} title={blocked ? 'No supported agent binary found on PATH' : 'Spawn a fresh session'} onClick={() => action('spawn', { ...detail, slug, provider })}><Icon name="play" size={11}/>Spawn session</button>;
    }
    if (status === 'done') {
      return <button className="btn sm" onClick={openSession} disabled={!detail.transcript_available} title={detail.transcript_available ? 'Open transcript view' : 'No transcript on disk'}><Icon name="terminal" size={11}/>View transcript</button>;
    }
    // in-progress but not currently live — offer to resume the stored session
    return <button className="btn sm" onClick={openSession}><Icon name="terminal" size={11}/>Open session</button>;
  })();

  return (
    <div className="entity-page">
      <div className="entity-hero">
        <div className="entity-hero-main">
          <div className="entity-kicker"><button className="btn sm" onClick={() => goto('tasks')}><Icon name="arrow-left" size={11}/>Back</button>{detail.project_slug && <button className="tag-chip" style={{cursor: 'pointer'}} onClick={() => goto(`project/${detail.project_slug}`)} title="Open project">{detail.project_slug}</button>}</div>
          <div className="entity-title-row">
            <h1>{detail.slug}</h1>
            <StatusPill status={status}/>
            <PriorityPill priority={detail.priority}/>
            {live && <span className="tag-chip" title="Live session" style={{background: 'var(--running)', color: 'var(--bg)'}}>live</span>}
            {stale && <span className="tag-chip" title={`Stale ${detail.stale_days}d`} style={{background: 'var(--idle)'}}>⚠ stale</span>}
          </div>
          <p className="entity-subtitle">{detail.name}</p>
          <div className="entity-meta-row">
            <span title={workDir}><Icon name="folder" size={13}/>{workDir || 'no workdir'}</span>
            <span title={`Agent: ${provider}`}><ProviderMark provider={provider} size={13}/>{provider}</span>
            <span><Icon name="clock" size={13}/>updated {formatAge(minutesSinceISO(detail.updated_at))} ago</span>
            {detail.assignee && <span><Icon name="user" size={13}/>{detail.assignee}</span>}
            {detail.due_info && <span title={detail.due_date || ''}><Icon name="calendar" size={13}/>{detail.due_info}</span>}
            {detail.waiting_on && <span style={{color: 'var(--idle)'}}><Icon name="pause" size={13}/>waiting: {detail.waiting_on}</span>}
          </div>
        </div>
        <div className="entity-hero-actions">
          {sessionButton}
          <button className="btn sm" onClick={() => action('delete', { ...detail, kind: 'task' })} title="Archive task"><Icon name="archive" size={11}/>Archive</button>
        </div>
      </div>
      {loadState.error && <div className="pane" style={{padding: 12, marginTop: 12, borderColor: 'var(--dead)'}}><span className="mono" style={{fontSize: 11, color: 'var(--dead)'}}>{loadState.error}</span></div>}

      <div style={{display: 'grid', gridTemplateColumns: '1.4fr 1fr', gap: 16, marginTop: 12}}>
        <div className="pane" style={{padding: 16}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>State</div>
          <div style={{display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12}}>
            <div><div style={{fontSize: 20, fontWeight: 600, color: 'var(--text)'}}>{detail.days_in_status}d</div><div className="mono dim" style={{fontSize: 10.5}}>in {status}</div></div>
            <div><div style={{fontSize: 20, fontWeight: 600, color: 'var(--text)'}}>{updates.length}</div><div className="mono dim" style={{fontSize: 10.5}}>updates</div></div>
            <div><div style={{fontSize: 20, fontWeight: 600, color: 'var(--text)'}}>{aux.length}</div><div className="mono dim" style={{fontSize: 10.5}}>sidecar files</div></div>
          </div>
          {tags.length > 0 && (
            <div style={{marginTop: 14, display: 'flex', flexWrap: 'wrap', gap: 4}}>
              {tags.map(tg => <span key={tg} className="tag-chip">#{tg}</span>)}
            </div>
          )}
        </div>
        <div className="pane" style={{padding: 16}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Details</div>
          <div style={{display: 'grid', gap: 7}}>
            <div className="mono" title={workDir} style={{fontSize: 11, color: 'var(--text-mid)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>work_dir: {workDir || '—'}</div>
            <div className="mono" title={detail.brief_path} style={{fontSize: 11, color: 'var(--text-faint)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>brief: {detail.brief_path}</div>
            <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)', display: 'flex', alignItems: 'center', gap: 6}}>
              <ProviderMark provider={provider} size={11}/>
              <span>agent: {provider}</span>
            </div>
            <div style={{display: 'flex', alignItems: 'center', gap: 8, marginTop: 2}}>
              <span className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>permissions:</span>
              <div className="seg" style={{transform: 'scale(0.9)', transformOrigin: 'left center'}}>
                {['default', 'auto', 'bypass'].map(m => {
                  const current = (detail.permission_mode || 'default') === m;
                  return (
                    <button
                      key={m}
                      className={`seg-btn ${current ? 'on' : ''}`}
                      title={current ? `Current mode (${m})` : `Switch to ${m}` + (live ? ' (restarts running session)' : '')}
                      onClick={() => {
                        if (current) return;
                        const apply = () => {
                          action('update-permission-mode', { slug, permission_mode: m, _live: live, provider });
                        };
                        if (live) {
                          if (window.confirm(`Switch permissions to ${m}?\n\nThis terminates the running session; reattach to spawn a fresh one with the new mode.`)) apply();
                        } else {
                          apply();
                        }
                      }}
                    >{m}</button>
                  );
                })}
              </div>
            </div>
            {detail.session_id ? (
              <div className="mono" title="Click to copy" onClick={() => navigator.clipboard && navigator.clipboard.writeText(detail.session_id)} style={{fontSize: 11, color: 'var(--text-faint)', cursor: 'copy', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>session: {detail.session_id}</div>
            ) : (
              <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>session: <span style={{color: 'var(--text-faint)', opacity: 0.7}}>none yet</span></div>
            )}
            {detail.session_started && <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>session started: {detail.session_started.slice(0, 16).replace('T', ' ')}</div>}
            {detail.session_last_resumed && <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>last resumed: {detail.session_last_resumed.slice(0, 16).replace('T', ' ')}</div>}
            <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>created: {detail.created_at?.slice(0, 10)} · updated: {detail.updated_at?.slice(0, 10)}</div>
            {detail.parent_slug && <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>parent: <a onClick={() => goto(`task/${detail.parent_slug}`)} style={{cursor: 'pointer', color: 'var(--accent)'}}>{detail.parent_slug}</a></div>}
            {(detail.children || []).length > 0 && <div className="mono" style={{fontSize: 11, color: 'var(--text-faint)'}}>children: {(detail.children || []).map(c => c.slug).join(', ')}</div>}
          </div>
        </div>
      </div>

      <div className="entity-card" style={{marginTop: 16}}>
        <div style={{display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12}}>
          <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em'}}>Brief</div>
          <span className="mono dim" style={{fontSize: 10.5, marginLeft: 'auto'}}>edit via `flow edit {slug}` for now</span>
        </div>
        <MarkdownView source={brief} empty="No brief text found."/>
      </div>

      <div style={{marginTop: 16}}>
        <div className="mono" style={{fontSize: 10.5, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12}}>Recent updates</div>
        <ReadableFiles
          files={updates.map(f => ({ ...f, route: 'updates' }))}
          empty="No progress notes yet"
          fetchFile={fetchTaskFile}
          minutesSinceISO={minutesSinceISO}
        />
      </div>
    </div>
  );
};

// ───────── Trash ───────────────────────────────────────────────────────
const TrashView = ({ action }) => {
  const trash = TRASH || { tasks: [], projects: [], playbooks: [], total: 0 };
  const groups = [
    { key: 'tasks', label: 'Tasks', icon: 'list', items: trash.tasks || [] },
    { key: 'projects', label: 'Projects', icon: 'folder-tree', items: trash.projects || [] },
    { key: 'playbooks', label: 'Playbooks', icon: 'play', items: trash.playbooks || [] },
  ];
  const deletedAgo = (iso) => {
    const ts = Date.parse(iso || '');
    if (!Number.isFinite(ts)) return 'unknown';
    const min = Math.max(0, Math.round((Date.now() - ts) / 60000));
    return `${formatAge(min)} ago`;
  };
  return (
    <div>
      <div className="section-head">
        <h2>Trash</h2>
        <span className="count mono">{trash.total || 0} soft-deleted</span>
      </div>
      {groups.map(group => (
        <section key={group.key} className="pane" style={{marginBottom: 14}}>
          <div className="pane-head">
            <Icon name={group.icon} size={12}/>
            <span>{group.label}</span>
            <span className="count mono">{group.items.length}</span>
          </div>
          {group.items.length ? (
            <table className="table">
              <thead>
                <tr><th>Slug</th><th>Name</th><th>Status</th><th>Project</th><th>Deleted</th><th></th></tr>
              </thead>
              <tbody>
                {group.items.map(item => (
                  <tr key={`${item.kind}:${item.slug}`}>
                    <td className="mono" style={{fontSize: 12}}>{item.slug}</td>
                    <td>{item.name}</td>
                    <td>{item.status ? <StatusPill status={item.status}/> : <span className="mono dim">—</span>}{item.archived && <span className="tag-chip" style={{marginLeft: 6}}>archived</span>}</td>
                    <td className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>{item.project || '—'}</td>
                    <td className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>{deletedAgo(item.deleted_at)}</td>
                    <td style={{textAlign: 'right'}}>
                      <div className="row-attach" style={{justifyContent: 'flex-end'}}>
                        <button className="btn sm primary" onClick={() => action('restore', item)}><Icon name="rotate-ccw" size={11}/>Restore</button>
                        <button className="btn sm danger" onClick={() => action('destroy', item)}><Icon name="trash-2" size={11}/>Delete</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : <BrandEmpty compact title={`No deleted ${group.label.toLowerCase()}`} body="Soft-deleted records will appear here."/>}
        </section>
      ))}
    </div>
  );
};

// ───────── KB ──────────────────────────────────────────────────────────
const KBView = () => {
  const [sel, setSel] = useState(0);
  const [q, setQ] = useState('');
  const file = KB_FILES[sel];
  if (!file) {
    return (
      <div>
        <div className="section-head"><h2>KB</h2><span className="count mono">0 files</span></div>
        <div className="empty"><FlowMark size={32} title=""/><h3>No knowledge base files</h3><p>No knowledge base files found in this flow root.</p></div>
      </div>
    );
  }
  const entries = (file.entries || []).filter(e => !q || e.t.toLowerCase().includes(q.toLowerCase()));
  // group by month
  const groups = {};
  entries.forEach(e => {
    const m = e.d.slice(0, 7);
    (groups[m] = groups[m] || []).push(e);
  });
  return (
    <div className="kb-wrap">
      <div className="kb-list">
        {KB_FILES.map((f, i) => (
          <div key={f.name} className={`ki ${i===sel?'active':''}`} onClick={() => setSel(i)}>
            <div className="fn">{f.name}</div>
            <div className="pv">{f.preview}</div>
            <div className="ec">{f.count} entries</div>
          </div>
        ))}
      </div>
      <div className="kb-main">
        <div className="kb-head">
          <span className="mono" style={{fontSize: 13, color: 'var(--accent)'}}>{file.name}</span>
          <span className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>{entries.length} entries</span>
          <div style={{marginLeft: 'auto', position: 'relative'}}>
            <Icon name="search" size={11}/>
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="filter entries…" style={{background: 'var(--surface-2)', border: '1px solid var(--border)', color: 'var(--text)', padding: '4px 8px 4px 24px', borderRadius: 3, fontSize: 12, outline: 'none', width: 220}}/>
          </div>
        </div>
        <div className="kb-entries">
          {Object.entries(groups).map(([m, items]) => (
            <Fragment key={m}>
              <div className="kb-month">{m}</div>
              {items.map((e, i) => (
                <div key={i} className="kb-entry">
                  <span className="d">{e.d}</span>
                  <span>{e.t}</span>
                </div>
              ))}
            </Fragment>
          ))}
        </div>
      </div>
    </div>
  );
};

// ───────── Memories ────────────────────────────────────────────────────
const MemorySourcesView = () => {
  const [provider, setProvider] = useState('all');
  const [scope, setScope] = useState('all');
  const [q, setQ] = useState('');
  const [selectedID, setSelectedID] = useState('');
  const sources = Array.isArray(AGENT_MEMORY_SOURCES) ? AGENT_MEMORY_SOURCES : [];
  const providerCounts = sources.reduce((acc, src) => {
    const key = src.provider || 'other';
    acc[key] = (acc[key] || 0) + 1;
    return acc;
  }, {});
  const filtered = sources.filter(src => {
    if (provider !== 'all' && src.provider !== provider) return false;
    if (scope !== 'all' && src.scope !== scope) return false;
    if (!q.trim()) return true;
    const hay = [src.provider, src.scope, src.kind, src.label, src.path, src.status, src.content].join(' ').toLowerCase();
    return hay.includes(q.trim().toLowerCase());
  });
  useEffect(() => {
    if (!filtered.length) {
      setSelectedID('');
      return;
    }
    if (!filtered.some(src => src.id === selectedID)) {
      const next = filtered.find(src => src.available) || filtered[0];
      setSelectedID(next.id);
    }
  }, [filtered.map(src => src.id).join('|')]);
  const selected = filtered.find(src => src.id === selectedID) || filtered.find(src => src.available) || filtered[0];
  const available = sources.filter(src => src.available).length;
  const missing = sources.length - available;
  const scopeOptions = [
    ['all', 'All'],
    ['global', 'Global'],
    ['user', 'User'],
    ['project', 'Project'],
  ];
  const providerOptions = [
    ['all', 'All'],
    ['codex', 'Codex'],
    ['claude', 'Claude'],
  ].filter(([id]) => id === 'all' || providerCounts[id]);
  if (!sources.length) {
    return (
      <div>
        <div className="section-head"><h2>Memories</h2><span className="count mono">0 sources</span></div>
        <div className="empty"><FlowMark size={32} title=""/><h3>No memory sources</h3><p>No agent memory files found.</p></div>
      </div>
    );
  }
  return (
    <div>
      <div className="section-head">
        <h2>Memories</h2>
        <span className="count mono">{available} available · {missing} missing</span>
      </div>
      <div className="memory-toolbar">
        <div className="seg-row compact">
          {providerOptions.map(([id, label]) => (
            <button key={id} className={`seg-btn ${provider===id?'on':''}`} onClick={() => setProvider(id)}>{label}</button>
          ))}
        </div>
        <div className="seg-row compact">
          {scopeOptions.map(([id, label]) => (
            <button key={id} className={`seg-btn ${scope===id?'on':''}`} onClick={() => setScope(id)}>{label}</button>
          ))}
        </div>
        <div className="memory-search">
          <Icon name="search" size={12}/>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter sources"/>
        </div>
      </div>
      <div className="memory-wrap">
        <div className="memory-list">
          {filtered.map(src => (
            <button key={src.id} className={`memory-row ${src.id === selected?.id ? 'active' : ''} ${src.status}`} onClick={() => setSelectedID(src.id)}>
              <span className="memory-row-head">
                <span className={`memory-provider ${src.provider}`}>
                  <ProviderMark provider={src.provider} size={13}/>
                  <span>{src.provider}</span>
                </span>
                <span className={`memory-status ${src.status}`}>{src.status}</span>
              </span>
              <span className="memory-label">{src.label}</span>
              <span className="memory-kind mono">{src.kind}</span>
              <span className="memory-path mono">{src.path}</span>
            </button>
          ))}
          {!filtered.length && <div className="memory-empty mono">No matching sources</div>}
        </div>
        <div className="memory-main">
          {selected ? (
            <Fragment>
              <div className="memory-head">
                <div>
                  <div className="memory-title">
                    <span className={`memory-provider ${selected.provider}`}>
                      <ProviderMark provider={selected.provider} size={14}/>
                      <span>{selected.provider}</span>
                    </span>
                    <span>{selected.label}</span>
                  </div>
                  <div className="memory-meta mono">{selected.scope} · {selected.kind || 'source'} · {selected.size || 0} bytes{selected.mtime ? ` · ${selected.mtime}` : ''}</div>
                </div>
                <span className={`memory-status ${selected.status}`}>{selected.status}</span>
              </div>
              <div className="memory-pathline mono">{selected.path}</div>
              {selected.available ? (
                <details className="memory-content" open>
                  <summary><Icon name="file-text" size={12}/>{selected.format === 'markdown' ? 'Rendered Markdown' : 'Full content'}</summary>
                  {selected.format === 'markdown' ? (
                    <div className="memory-rendered"><MarkdownView source={selected.content || ''} empty="No content found."/></div>
                  ) : (
                    <pre>{selected.content || ''}</pre>
                  )}
                </details>
              ) : (
                <div className="memory-missing">
                  <Icon name="circle-off" size={18}/>
                  <span className="mono">{selected.error || 'File is not present on this machine.'}</span>
                </div>
              )}
            </Fragment>
          ) : (
            <div className="memory-missing"><Icon name="search-x" size={18}/><span className="mono">No source selected</span></div>
          )}
        </div>
      </div>
    </div>
  );
};

// ───────── Workdirs ────────────────────────────────────────────────────
const WorkdirsView = ({ action }) => {
  const [path, setPath] = useState('');
  const [pickerOpen, setPickerOpen] = useState(false);
  const submitAdd = (e) => {
    e.preventDefault();
    const cleanPath = path.trim();
    if (!cleanPath) return;
    action('workdir-add', { path: cleanPath });
    setPath('');
  };
  return (
    <div>
      <div className="section-head"><h2>Workdirs</h2><span className="count mono">{WORKDIRS.length} registered</span></div>
      <form className="pane" style={{padding: 12, marginBottom: 14}} onSubmit={submitAdd}>
        <div style={{display: 'grid', gridTemplateColumns: 'minmax(320px, 1fr) auto', gap: 8, alignItems: 'center'}}>
          <div className="path-picker" onClick={() => setPickerOpen(true)} title="Choose directory…">
            <Icon name="folder" size={13}/>
            <span className="path-picker-text mono">{path || 'Choose a directory…'}</span>
            <span className="path-picker-btn mono">Browse…</span>
          </div>
          <button className="btn sm primary" type="submit"><Icon name="plus" size={11}/>Add</button>
        </div>
      </form>
      {pickerOpen && <DirectoryPicker initial={path || WORKDIRS[0]?.path || ''} onPick={(p) => { setPath(p); setPickerOpen(false); }} onClose={() => setPickerOpen(false)}/>}
      <table className="table">
        <thead>
          <tr><th>Path</th><th>Name</th><th>Remote</th><th>Last used</th><th>Tasks</th><th></th></tr>
        </thead>
        <tbody>
          {WORKDIRS.map(w => (
              <tr key={w.path}>
                <td className="mono" style={{fontSize: 11.5}}>{w.path}</td>
                <td className="mono" style={{fontSize: 11.5}}>{w.name}</td>
                <td className="mono" style={{fontSize: 11, color: 'var(--text-dim)'}}>{w.remote || '—'}</td>
                <td className="mono" style={{fontSize: 11.5}}>{formatAge(w.used_min)} ago</td>
                <td className="mono" style={{fontSize: 11.5, color: 'var(--text-dim)'}}>{w.tasks}</td>
                <td>
                  <div className="row-attach" style={{justifyContent: 'flex-end'}}>
                    {w.untouched && <span className="pill stale">untouched 30d+</span>}
                    <button className="btn sm danger" type="button" onClick={() => action('workdir-remove', w)}><Icon name="trash-2" size={11}/>Remove</button>
                  </div>
                </td>
              </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

// ───────── Command Palette ─────────────────────────────────────────────
const CommandPalette = ({ onClose, goto, action }) => {
  const [q, setQ] = useState('');
  const [active, setActive] = useState(0);
  const [ftsState, setFtsState] = useState({ query: '', loading: false, results: [], error: '' });
  const normalize = (v) => String(v || '').toLowerCase();
  const searchText = (parts) => parts.flatMap(v => Array.isArray(v) ? v : [v]).filter(Boolean).join(' ');
  const taskStatusIcon = (status) => ({
    running: 'radio',
    waiting: 'alert-circle',
    idle: 'clock',
    stale: 'alert-triangle',
    backlog: 'list-plus',
    done: 'check-circle',
  }[status] || 'circle');
  const taskGroup = (status) => {
    if (status === 'backlog') return 'Backlog tasks';
    if (status === 'done') return 'Done tasks';
    return 'Active tasks';
  };
  const taskSubtitle = (task) => [task.project || 'floating', task.provider, task.branch, task.due ? `due ${task.due}` : ''].filter(Boolean).join(' · ');
  const activeTaskItems = AGENTS.map(a => ({
    group: taskGroup(a.status),
    kind: 'task',
    icon: taskStatusIcon(a.status),
    label: a.slug,
    title: a.name,
    subtitle: taskSubtitle(a),
    status: a.status,
    priority: a.priority,
    meta: a.status === 'running' ? 'live session' : a.status,
    search: searchText([a.slug, a.name, a.project, a.status, a.priority, a.provider, a.branch, a.tags]),
    onSel: () => goto(`session/${a.slug}`),
  }));
  const backlogTaskItems = BACKLOG.map(t => ({
    group: taskGroup('backlog'),
    kind: 'task',
    icon: taskStatusIcon('backlog'),
    label: t.slug,
    title: t.name,
    subtitle: [t.project || 'floating', t.due ? `due ${t.due}` : '', ...(t.tags || [])].filter(Boolean).join(' · '),
    status: 'backlog',
    priority: t.priority,
    meta: 'ready',
    search: searchText([t.slug, t.name, t.project, 'backlog', t.priority, t.due, t.tags]),
    onSel: () => goto(`session/${t.slug}`),
  }));
  const doneTaskItems = DONE_TASKS.map(t => ({
    group: taskGroup('done'),
    kind: 'task',
    icon: taskStatusIcon('done'),
    label: t.slug,
    title: t.name,
    subtitle: [t.project || 'floating', ...(t.tags || [])].filter(Boolean).join(' · '),
    status: 'done',
    priority: t.priority,
    meta: 'closed',
    search: searchText([t.slug, t.name, t.project, 'done', t.priority, t.tags]),
    onSel: () => goto(`session/${t.slug}`),
  }));
  const taskItems = [...activeTaskItems, ...backlogTaskItems, ...doneTaskItems];
  useEffect(() => {
    const raw = q.trim();
    if (raw.length < 2) {
      setFtsState({ query: raw, loading: false, results: [], error: '' });
      return;
    }
    let cancelled = false;
    setFtsState(prev => ({ ...prev, query: raw, loading: true, error: '' }));
    const timer = setTimeout(() => {
      fetch(`/api/search?q=${encodeURIComponent(raw)}&limit=8`)
        .then(r => r.ok ? r.json() : Promise.reject(new Error(`search ${r.status}`)))
        .then(data => {
          if (cancelled) return;
          const buckets = [
            ...(data.tasks || []),
            ...(data.projects || []),
            ...(data.playbooks || []),
            ...(data.updates || []),
            ...(data.transcripts || []),
          ];
          setFtsState({ query: raw, loading: false, results: buckets, error: '' });
        })
        .catch(err => {
          if (cancelled) return;
          setFtsState({ query: raw, loading: false, results: [], error: err.message || 'search failed' });
        });
    }, 160);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [q]);
  const ftsItems = useMemo(() => {
    if (q.trim().length < 2) return [];
    if (ftsState.loading) {
      return [{ group: 'Full-text search', kind: 'search', icon: 'search', label: 'Searching briefs and updates...', title: ftsState.query, meta: 'fts5', search: ftsState.query, onSel: () => {} }];
    }
    if (ftsState.error) {
      return [{ group: 'Full-text search', kind: 'search', icon: 'alert-triangle', label: 'Search unavailable', title: ftsState.error, meta: 'error', search: ftsState.query, onSel: () => {} }];
    }
    return (ftsState.results || []).map(r => ({
      group: 'Full-text search',
      kind: r.scope || 'search',
      icon: r.scope === 'update' ? 'file-text' : r.scope === 'transcript' ? 'messages-square' : r.type === 'project_brief' ? 'folder-tree' : r.type === 'playbook_brief' ? 'play' : 'search',
      label: r.slug,
      title: r.name,
      subtitle: r.snippet,
      meta: r.scope || r.type,
      search: searchText([r.slug, r.name, r.snippet, r.scope, r.type]),
      onSel: () => goto(String(r.url || '').replace(/^\/+/, '') || 'mc'),
    }));
  }, [q, ftsState.query, ftsState.loading, ftsState.error, ftsState.results]);
  const items = useMemo(() => {
    const all = [
      ...ftsItems,
      ...taskItems,
      ...PROJECTS_MC.map(p => ({
        group: 'Projects',
        kind: 'project',
        icon: 'folder-tree',
        label: p.slug,
        title: p.name,
        subtitle: p.work_dir,
        meta: `${p.tasks.in_progress} active · ${p.tasks.backlog} backlog · ${p.tasks.done} done`,
        search: searchText([p.slug, p.name, p.work_dir, p.priority, 'project']),
        onSel: () => goto(`project/${p.slug}`),
      })),
      ...PLAYBOOKS_MC.map(p => ({
        group: 'Playbooks',
        kind: 'playbook',
        icon: 'play',
        label: p.slug,
        title: p.name,
        subtitle: p.project || 'floating playbook',
        meta: `${p.runs_week} runs/wk`,
        search: searchText([p.slug, p.name, p.project, p.work_dir, 'playbook']),
        onSel: () => goto(`playbook/${p.slug}`),
      })),
      { group: 'Actions', icon: 'play', label: 'Spawn agent for backlog...', title: 'Create or start a flow session', meta: `${BACKLOG.length} backlog`, search: 'spawn agent backlog create task', onSel: () => action('spawn-prompt') },
      ...AGENTS.map(a => ({
        group: 'Actions',
        icon: 'terminal',
        label: `Attach ${a.slug}`,
        title: a.name,
        subtitle: a.project || 'floating',
        status: a.status,
        meta: 'terminal',
        search: searchText(['attach terminal browser', a.slug, a.name, a.project, a.status]),
        onSel: () => action('attach', a),
      })),
      { group: 'Navigation', icon: 'grid-3x3', label: 'Mission Control', meta: 'g m', onSel: () => goto('mc') },
      { group: 'Navigation', icon: 'box', label: 'Sessions', meta: 'g s', onSel: () => goto('sessions') },
      { group: 'Navigation', icon: 'list', label: 'Tasks', meta: 'g t', onSel: () => goto('tasks') },
      { group: 'Navigation', icon: 'folder-tree', label: 'Projects', meta: 'g p', onSel: () => goto('projects') },
      { group: 'Navigation', icon: 'play', label: 'Playbooks', meta: 'g b', onSel: () => goto('playbooks') },
      { group: 'Navigation', icon: 'folder', label: 'Workdirs', meta: 'g w', onSel: () => goto('workdirs') },
      { group: 'Navigation', icon: 'brain-circuit', label: 'Memories', meta: 'g c', onSel: () => goto('memories') },
      { group: 'Navigation', icon: 'book-open', label: 'KB', meta: 'g k', onSel: () => goto('kb') },
      { group: 'Navigation', icon: 'inbox', label: 'Inbox', meta: 'g i', onSel: () => goto('inbox') },
      { group: 'Navigation', icon: 'trash-2', label: 'Trash', meta: 'g x', onSel: () => goto('trash') },
    ].map((item, idx) => ({ ...item, _allIdx: idx, search: normalize(searchText([item.label, item.title, item.subtitle, item.meta, item.search, item.group])) }));
    const query = normalize(q.trim());
    if (!query) return all;
    return all.filter(i => i.search.includes(query));
  }, [q, ftsItems, AGENTS.length, BACKLOG.length, DONE_TASKS.length, PROJECTS_MC.length, PLAYBOOKS_MC.length]);
  const grouped = useMemo(() => {
    const g = {};
    items.forEach((it, i) => { (g[it.group] = g[it.group] || []).push({ ...it, _idx: i }); });
    return g;
  }, [items]);
  const groupOrder = ['Full-text search', 'Active tasks', 'Backlog tasks', 'Done tasks', 'Projects', 'Playbooks', 'Actions', 'Navigation'];
  const orderedGroups = groupOrder
    .filter(g => grouped[g] && grouped[g].length > 0)
    .concat(Object.keys(grouped).filter(g => !groupOrder.includes(g)))
    .map(g => [g, grouped[g]]);
  const totals = {
    running: AGENTS.filter(a => a.status === 'running').length,
    waiting: AGENTS.filter(a => a.status === 'waiting').length,
    tasks: taskItems.length,
    projects: PROJECTS_MC.length,
  };
  useEffect(() => {
    setActive(a => Math.min(Math.max(0, a), Math.max(0, items.length - 1)));
  }, [items.length]);
  useEffect(() => {
    document.querySelector('.palette-item.active')?.scrollIntoView({ block: 'nearest' });
  }, [active]);
  useEffect(() => {
    const handler = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); return; }
      if (e.key === 'Escape') onClose();
      else if (e.key === 'ArrowDown') { e.preventDefault(); setActive(a => Math.min(Math.max(0, items.length - 1), a + 1)); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); setActive(a => Math.max(0, a - 1)); }
      else if (e.key === 'Enter') { e.preventDefault(); items[active]?.onSel(); onClose(); }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [items, active, onClose]);
  return (
    <div className="modal-scrim" onClick={onClose}>
      <div className="palette" onClick={(e) => e.stopPropagation()}>
        <div className="palette-head">
          <span className="palette-head-icon"><Icon name="command" size={15}/></span>
          <div>
            <div className="palette-title">Switch project or task</div>
            <div className="palette-summary mono">{totals.running} running · {totals.waiting} waiting · {totals.tasks} tasks · {totals.projects} projects</div>
          </div>
          <span className="kbd">Cmd/Ctrl K</span>
        </div>
        <div className="palette-input-wrap">
          <Icon name="search" size={14}/>
          <input autoFocus className="palette-input" placeholder="Search briefs, updates, slugs, tags, or commands" value={q} onChange={(e) => { setQ(e.target.value); setActive(0); }}/>
        </div>
        <div className="palette-list" role="listbox" aria-label="Switcher results">
          {orderedGroups.map(([g, list]) => (
            <Fragment key={g}>
              <div className="palette-group"><span>{g}</span><span className="count mono">{list.length}</span></div>
              {list.map(it => (
                <div key={`${it.group}-${it.label}-${it._idx}`} role="option" aria-selected={active===it._idx} className={`palette-item ${active===it._idx?'active':''} ${it.kind || ''}`} onClick={() => { it.onSel(); onClose(); }} onMouseEnter={() => setActive(it._idx)}>
                  <span className={`palette-item-icon ${it.status || it.kind || ''}`}><Icon name={it.icon} size={14}/></span>
                  <span className="palette-item-main">
                    <span className="palette-item-title"><span className="label mono">{it.label}</span>{it.title && <span className="title">{it.title}</span>}</span>
                    {it.subtitle && <span className="subtitle mono">{it.subtitle}</span>}
                  </span>
                  <span className="palette-item-side">
                    {it.status && <span className={`pill ${it.status}`}>{it.status}</span>}
                    {it.priority && <PriorityPill priority={it.priority}/>}
                    <span className="meta mono">{it.meta}</span>
                  </span>
                </div>
              ))}
            </Fragment>
          ))}
          {items.length === 0 && (
            <div className="palette-empty">
              <Icon name="search-x" size={18}/>
              <div>No matches</div>
              <span className="mono">Try a task slug, project, status, or tag.</span>
            </div>
          )}
        </div>
        <div className="palette-foot">
          <span className="kbd">↑↓</span> navigate
          <span className="kbd">↵</span> select
          <span className="kbd">esc</span> close
          <span style={{marginLeft: 'auto'}}>{items.length} results</span>
        </div>
      </div>
    </div>
  );
};

// ───────── QR Remote ───────────────────────────────────────────────────
const QRModal = ({ onClose }) => {
  const [mode, setMode] = useState('tailscale');
  return (
    <div className="modal-scrim centered" onClick={onClose}>
      <div className="modal qr-modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head"><FlowLogo size={22}/> <span>Open flow on your phone</span></div>
        <div className="modal-body">Use the same local URL from a device that can reach this machine.</div>
        <div className="qr-box">
          <svg viewBox="0 0 100 100" width="160" height="160">
            {/* simple QR-ish pattern */}
            {(() => {
              const cells = [];
              const seed = 'fL0wMC-2026-river-spoon-cliff-mango-volt';
              for (let y = 0; y < 25; y++) {
                for (let x = 0; x < 25; x++) {
                  const h = (seed.charCodeAt((x * 13 + y * 7) % seed.length) + x * y) % 7;
                  if (h < 3) cells.push(<rect key={`${x}-${y}`} x={x*4} y={y*4} width="4" height="4" fill="#000"/>);
                }
              }
              return cells;
            })()}
            {/* corner positioning squares */}
            {[[0,0],[0,18],[18,0]].map(([x,y], i) => (
              <Fragment key={i}>
                <rect x={x*4} y={y*4} width="28" height="28" fill="#000"/>
                <rect x={x*4+4} y={y*4+4} width="20" height="20" fill="#fff"/>
                <rect x={x*4+8} y={y*4+8} width="12" height="12" fill="#000"/>
              </Fragment>
            ))}
          </svg>
        </div>
        <div className="passphrase">
          river-spoon-cliff-mango-volt
          <span className="rotate">rotates in 04:21</span>
        </div>
        <div className="seg-row">
          <div className={`seg-btn ${mode==='tailscale'?'on':''}`} onClick={() => setMode('tailscale')}>Tailscale Funnel</div>
          <div className={`seg-btn ${mode==='cloudflare'?'on':''}`} onClick={() => setMode('cloudflare')}>Cloudflare Tunnel</div>
          <div className={`seg-btn ${mode==='lan'?'on':''}`} onClick={() => setMode('lan')}>LAN only</div>
        </div>
        <div className="remote-clients">
          <div style={{fontSize: 10.5, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-dim)', padding: '0 0 6px'}}>Connected clients · 0</div>
          <div className="ck">
            <Icon name="smartphone" size={14}/>
            <span className="nm">No remote clients connected</span>
            <span className="tm">{window.location.host}</span>
          </div>
        </div>
        <div className="modal-foot"><button className="btn" onClick={onClose}>Close</button></div>
      </div>
    </div>
  );
};

// ───────── Confirm modal ───────────────────────────────────────────────
const ConfirmModal = ({ title, body, confirm, danger, onConfirm, onClose, checkLabel, requireText, requireLabel, requirePlaceholder }) => {
  const [checked, setChecked] = useState(false);
  const [typed, setTyped] = useState('');
  const inputRef = useRef(null);
  useEffect(() => { if (requireText && inputRef.current) inputRef.current.focus(); }, [requireText]);
  const matches = !requireText || typed.trim() === requireText;
  const canConfirm = matches && (!checkLabel || checked);
  const submit = () => { if (!canConfirm) return; onConfirm?.(checked); onClose(); };
  return (
    <div className="modal-scrim centered" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">{title}</div>
        <div className="modal-body">{body}</div>
        {requireText && (
          <div className="modal-confirm-type">
            <div className="modal-confirm-label">
              {requireLabel || <>Type <code className="modal-confirm-token">{requireText}</code> to confirm.</>}
            </div>
            <input
              ref={inputRef}
              type="text"
              className="form-input mono"
              autoComplete="off"
              spellCheck={false}
              placeholder={requirePlaceholder || requireText}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') submit(); }}
            />
          </div>
        )}
        {checkLabel && (
          <label className="modal-check">
            <input type="checkbox" checked={checked} onChange={(e) => setChecked(e.target.checked)}/>
            {checkLabel}
          </label>
        )}
        <div className="modal-foot">
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className={`btn ${danger ? 'danger' : 'primary'}`} disabled={!canConfirm} onClick={submit}>{confirm || 'Confirm'}</button>
        </div>
      </div>
    </div>
  );
};

// ───────── Shortcuts overlay ───────────────────────────────────────────
const ShortcutsOverlay = ({ onClose }) => (
  <div className="modal-scrim centered" onClick={onClose}>
    <div className="modal" style={{width: 540}} onClick={(e) => e.stopPropagation()}>
      <div className="modal-head">Keyboard shortcuts</div>
      <div className="modal-body" style={{display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '6px 24px'}}>
        {[
          ['Navigation', null],
          [<><span className="kbd">g</span> <span className="kbd">m</span></>, 'Mission Control'],
          [<><span className="kbd">g</span> <span className="kbd">s</span></>, 'Sessions'],
          [<><span className="kbd">g</span> <span className="kbd">t</span></>, 'Tasks'],
          [<><span className="kbd">g</span> <span className="kbd">p</span></>, 'Projects'],
          [<><span className="kbd">g</span> <span className="kbd">b</span></>, 'Playbooks'],
          [<><span className="kbd">g</span> <span className="kbd">w</span></>, 'Workdirs'],
          [<><span className="kbd">g</span> <span className="kbd">c</span></>, 'Memories'],
          [<><span className="kbd">g</span> <span className="kbd">k</span></>, 'KB'],
          [<><span className="kbd">g</span> <span className="kbd">i</span></>, 'Inbox'],
          [<><span className="kbd">g</span> <span className="kbd">x</span></>, 'Trash'],
          ['Actions', null],
          [<><span className="kbd">Cmd</span><span className="kbd">k</span></>, 'Project and task switcher'],
          [<span className="kbd">/</span>, 'Focus search'],
          [<><span className="kbd">j</span> / <span className="kbd">k</span></>, 'Next / prev'],
          [<span className="kbd">a</span>, 'Open focused agent'],
          [<span className="kbd">↵</span>, 'Open detail'],
          [<span className="kbd">y</span>, 'Approve (when waiting)'],
          [<span className="kbd">n</span>, 'Deny (when waiting)'],
          [<span className="kbd">?</span>, 'This help'],
        ].map(([k, v], i) => v === null ? (
          <div key={i} style={{gridColumn: '1/-1', fontSize: 10.5, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-faint)', paddingTop: 8, marginBottom: -2}}>{k}</div>
        ) : (
          <div key={i} style={{display: 'flex', gap: 8, alignItems: 'center', padding: '3px 0', fontSize: 12.5}}>
            <span style={{minWidth: 80}}>{k}</span>
            <span style={{color: 'var(--text-mid)'}}>{v}</span>
          </div>
        ))}
      </div>
      <div className="modal-foot"><button className="btn" onClick={onClose}>Close</button></div>
    </div>
  </div>
);

window.MC_SCREENS = {
  MissionControl, SessionsGrid, SessionDetail, CompletedSessionView, TasksList, TaskDetail, ProjectsList, ProjectDetail, PlaybooksList, PlaybookDetail,
  TrashView, KBView, MemorySourcesView, WorkdirsView,
  CommandPalette, QRModal, ConfirmModal, ShortcutsOverlay, CreateFlowModal, CreateProjectModal,
};
