// Mission Control — primitives, tiles, transcript, focus drawer
const {
  AGENTS, BACKLOG, KB_FILES, WORKDIRS, PLAYBOOKS_MC, PROJECTS_MC,
  SAMPLE_TRANSCRIPT, formatAge, formatActivity, fmtTokens, shortUUID,
  ClockCtx,
} = window.MC;

function lucideIconKey(name) {
  return String(name || '')
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map(part => part.charAt(0).toUpperCase() + part.slice(1))
    .join('');
}

function reactSvgAttrs(attrs = {}) {
  return Object.fromEntries(Object.entries(attrs).map(([key, value]) => {
    if (key === 'class') return ['className', value];
    if (key.startsWith('aria-') || key.startsWith('data-')) return [key, value];
    return [key.replace(/-([a-z])/g, (_, c) => c.toUpperCase()), value];
  }));
}

function renderIconNode(node, key) {
  const [tag, attrs = {}, children = []] = node;
  return React.createElement(
    tag,
    { ...reactSvgAttrs(attrs), key },
    children.map((child, i) => renderIconNode(child, i))
  );
}

const Icon = ({ name, size = 14, className = '', style = {} }) => {
  const icon = window.lucide?.icons?.[lucideIconKey(name)];
  const cls = ['lucide', `lucide-${name}`, className].filter(Boolean).join(' ');
  if (!icon) {
    return <span className={className} style={{ display: 'inline-block', width: size, height: size, ...style }} aria-hidden="true"></span>;
  }
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cls}
      style={style}
      aria-hidden="true"
      focusable="false"
    >
      {icon.map((child, i) => renderIconNode(child, i))}
    </svg>
  );
};

const FlowMark = ({ size = 36, className = '', title = 'flow' }) => {
  const gid = useMemo(() => `flow-mark-${Math.random().toString(36).slice(2)}`, []);
  return (
    <svg
      className={`flow-mark ${className}`.trim()}
      viewBox="0 0 36 36"
      width={size}
      height={size}
      xmlns="http://www.w3.org/2000/svg"
      role={title ? 'img' : undefined}
      aria-label={title || undefined}
      aria-hidden={title ? undefined : 'true'}
      focusable="false"
    >
      <defs>
        <linearGradient id={gid} x1="0" y1="0" x2="36" y2="36" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#645df6"/>
          <stop offset="1" stopColor="#8b87f8"/>
        </linearGradient>
      </defs>
      <rect width="36" height="36" rx="8" fill={`url(#${gid})`}/>
      <path d="M9 23 Q14 23 14 18 Q14 13 19 13 Q24 13 24 18 Q24 23 27 23" fill="none" stroke="white" strokeWidth="2.6" strokeLinecap="round"/>
      <circle cx="9" cy="23" r="2" fill="white"/>
      <circle cx="27" cy="23" r="2" fill="white"/>
    </svg>
  );
};

const FlowLogo = ({ size = 32, wordmark = true, className = '' }) => (
  <span className={`flow-logo ${className}`.trim()}>
    <FlowMark size={size} title=""/>
    {wordmark && <span className="flow-wordmark">flow<span className="wordmark-dot">.</span></span>}
  </span>
);

const FlowLoader = ({ label = 'loading', kind = 'orbit' }) => (
  <span className={`flow-loader ${kind}`} role="status" aria-live="polite">
    {kind === 'flow' ? (
      <svg className="loader-flow" viewBox="0 0 54 54" aria-hidden="true">
        <circle className="arc" cx="27" cy="27" r="22"></circle>
      </svg>
    ) : kind === 'stream' ? (
      <span className="loader-stream" aria-hidden="true"><span></span><span></span><span></span><span></span><span></span></span>
    ) : (
      <span className="loader-orbit" aria-hidden="true"><span className="core"></span></span>
    )}
    <span>{label}</span>
  </span>
);

const SkeletonRows = ({ rows = 6 }) => (
  <div className="skeleton-stack" aria-hidden="true">
    {Array.from({ length: rows }).map((_, i) => (
      <div key={i} className="skeleton-row">
        <span className="skeleton-dot"></span>
        <span className="skeleton-line" style={{width: `${72 - (i % 3) * 12}%`}}></span>
        <span className="skeleton-pill"></span>
      </div>
    ))}
  </div>
);

const Dot = ({ status }) => <span className={`dot ${status}`}></span>;

const StatusPill = ({ status }) => {
  const label = { running: 'agent running', waiting: 'needs input', idle: 'agent idle', stale: 'stale', dead: 'crashed', 'in-progress': 'in-progress', backlog: 'backlog', done: 'done' }[status] || status;
  return <span className={`pill ${status}`}>{status === 'waiting' && <span style={{display:'inline-block',width:6,height:6,borderRadius:'50%',background:'currentColor',marginRight:5,animation:'pulse 1.6s ease-in-out infinite'}}></span>}{label}</span>;
};

const TaskStatePill = ({ status }) => {
  if (!status) return null;
  const label = { 'in-progress': 'task in progress', backlog: 'task backlog', done: 'task done' }[status] || `task ${status}`;
  return <span className={`pill ${status}`}>{label}</span>;
};

const PriorityPill = ({ priority }) => {
  const glyph = { high: '▲', low: '▽', medium: '◆' }[priority];
  return <span className={`pill prio-${priority}`}>{glyph} {priority}</span>;
};

const ProviderMark = ({ provider, size = 14 }) => {
  if (provider === 'claude') {
    return (
      <img
        className="provider-mark provider-brand-mark"
        src="/assets/claudecode-color.svg"
        width={size}
        height={size}
        alt=""
        aria-hidden="true"
        style={{ width: size, height: size }}
      />
    );
  }
  if (provider === 'codex') {
    return (
      <img
        className="provider-mark provider-brand-mark"
        src="/assets/codex-color.svg"
        width={size}
        height={size}
        alt=""
        aria-hidden="true"
        style={{ width: size, height: size }}
      />
    );
  }
  return null;
};

const AgentChip = ({ provider, compact }) => {
  const label = provider === 'codex' ? 'codex' : provider;
  const title = provider === 'claude' ? 'provider' : label;
  return (
    <span className={`agent-chip ${provider}`} title={title} aria-label={title}>
      <ProviderMark provider={provider} size={12}/>
      {!compact && provider !== 'claude' && <span className="lbl">{label}</span>}
    </span>
  );
};

const BranchChip = ({ name }) => (
  <span className="branch-chip"><Icon name="git-branch" size={10}/>{name}</span>
);

const DependencyBadges = ({ task, compact = false }) => {
  if (!task) return null;
  // Prefer the multi-parent array; fall back to the legacy single parent /
  // parent_slug for any payload that hasn't been re-fetched since the
  // server upgrade.
  let parents = Array.isArray(task.parents) && task.parents.length
    ? task.parents.slice()
    : (task.parent ? [task.parent] : (task.parent_slug ? [{ slug: task.parent_slug }] : []));
  const children = Array.isArray(task.children) ? task.children : [];
  if (parents.length === 0 && children.length === 0) return null;
  const childPreview = children.slice(0, compact ? 1 : 3).map(c => c.slug).join(', ');
  const hiddenChildren = Math.max(0, children.length - (compact ? 1 : 3));
  const maxParents = compact ? 1 : 3;
  const shownParents = parents.slice(0, maxParents);
  const hiddenParents = Math.max(0, parents.length - maxParents);
  return (
    <div className={`dependency-strip ${compact ? 'compact' : ''}`}>
      {shownParents.map((p, i) => {
        const status = p.status || 'unknown';
        const isOpen = status !== 'done';
        return (
          <span key={p.slug || i} className={`dependency-chip parent ${isOpen ? 'open' : 'done'}`} title={`${task.slug} depends on ${p.slug}${p.status ? ` (${p.status})` : ''}`}>
            <Icon name="corner-down-right" size={10}/>
            <span>{i === 0 ? 'depends on' : 'and'}</span>
            <strong className="mono">{p.slug}</strong>
            {p.status && <em>{p.status}</em>}
          </span>
        );
      })}
      {hiddenParents > 0 && (
        <span className="dependency-chip parent open" title={parents.slice(maxParents).map(p => `${p.slug}${p.status ? ` (${p.status})` : ''}`).join(', ')}>
          <em>+{hiddenParents} more</em>
        </span>
      )}
      {children.length > 0 && (
        <span className="dependency-chip children" title={`${children.length} task${children.length === 1 ? '' : 's'} depend on ${task.slug}${childPreview ? `: ${childPreview}` : ''}`}>
          <Icon name="git-fork" size={10}/>
          <span>blocks</span>
          <strong className="mono">{children.length}</strong>
          {!compact && childPreview && <em>{childPreview}{hiddenChildren ? ` +${hiddenChildren}` : ''}</em>}
        </span>
      )}
    </div>
  );
};

const Sparkline = ({ data, color = 'var(--accent)', height = 24, width = 120 }) => {
  const max = Math.max(1, ...data);
  return (
    <svg width={width} height={height} className="spark">
      {data.map((v, i) => {
        const x = (i / (data.length - 1 || 1)) * width;
        const h = Math.max(1, (v / max) * (height - 2));
        return <rect key={i} x={x - 1} y={height - h} width="2" height={h} fill={color} opacity={v > 0 ? 0.85 : 0.2}/>;
      })}
    </svg>
  );
};

const ACTIVITY_ESTIMATE_TITLE = 'Session activity from provider transcript and terminal state, with flow task timestamps as fallback.';
const TOKEN_ESTIMATE_TITLE = 'Provider-reported context usage from the session JSONL (input + cache_creation + cache_read + output for Claude; total_tokens for Codex).';

// Pixel-indicator: 60 cells, each cell is 1 minute of estimated activity
const PixelIndicator = ({ data, status, height = 18 }) => {
  const max = Math.max(1, ...data);
  const cellColor = (v) => {
    if (v === 0) return 'rgba(255,255,255,0.04)';
    const intensity = Math.min(1, v / max);
    if (status === 'waiting') return `hsla(36, 90%, 60%, ${0.3 + intensity * 0.6})`;
    if (status === 'stale') return `hsla(36, 70%, 55%, ${0.2 + intensity * 0.4})`;
    if (status === 'idle') return `hsla(220, 10%, 60%, ${0.2 + intensity * 0.4})`;
    if (status === 'dead') return `hsla(0, 50%, 40%, ${0.3 + intensity * 0.3})`;
    return `hsla(245, 70%, 70%, ${0.3 + intensity * 0.7})`;
  };
  return (
    <div className="pixel-indicator" style={{height}} title={ACTIVITY_ESTIMATE_TITLE} aria-label="Estimated session activity over the last 60 minutes">
      {data.slice(-60).map((v, i) => (
        <div key={i} className="cell" style={{background: cellColor(v)}}></div>
      ))}
    </div>
  );
};

// ───────── Agent tile (Mission Control) ────────────────────────────────
const AgentTile = ({ agent, onOpen, onAction, big }) => {
  const tick = useContext(ClockCtx);
  const liveSec = agent.status === 'running' ? Math.max(0, agent.last_activity_sec - (tick % 30)) : agent.last_activity_sec + (tick > 0 ? tick : 0);
  const tokens_pct = Math.max(0, Math.min(100, (agent.tokens_used / Math.max(1, agent.tokens_max)) * 100));
  const tokens_tier = tokens_pct >= 90 ? 'danger' : tokens_pct >= 70 ? 'warn' : 'ok';
  const waitingKind = agent.waiting_for?.kind || '';
  const permissionWaiting = agent.status === 'waiting' && waitingKind === 'permission';
  const flowWaiting = agent.status === 'waiting' && waitingKind === 'flow';
  return (
    <div className={`tile ${agent.status}`} onClick={() => onOpen(agent)}>
      <div className="tile-stripe"></div>
      <div className="tile-body">
        <div className="tile-head">
          <Dot status={agent.status}/>
          <StatusPill status={agent.status}/>
          <TaskStatePill status={agent.task_status}/>
          <PriorityPill priority={agent.priority}/>
          <span className="tile-spacer"></span>
          <AgentChip provider={agent.provider}/>
        </div>
        <div className="tile-title">{agent.name || agent.slug}</div>
        <div className="tile-ref mono">{agent.slug}</div>
        <div className="tile-meta">
          <BranchChip name={agent.branch}/>
          <span className="m-sep">·</span>
          <span className="mono">{formatAge(agent.started_min)} old</span>
          {agent.project && <><span className="m-sep">·</span><span className="mono dim">{agent.project}</span></>}
        </div>
        <DependencyBadges task={agent} compact/>
        {(agent.pr_links || []).length > 0 && (
          <div className="pr-link-row" onClick={(e) => e.stopPropagation()}>
            {agent.pr_links.map(pr => (
              <a key={`${pr.repo}-${pr.number}`} className={`pr-chip ${pr.state}`} href={pr.url} target="_blank" rel="noreferrer" title={`${pr.repo} #${pr.number}`}>
                <Icon name="git-pull-request" size={10}/>
                <span className="mono">{pr.repo} #{pr.number}</span>
                <span className="mono dim">{pr.state}</span>
              </a>
            ))}
          </div>
        )}
        {agent.hook_health && (
          <div className="hook-health" onClick={(e) => e.stopPropagation()}>
            <Icon name="shield-alert" size={14}/>
            <div>
              <strong>Codex hooks need attention</strong>
              <p>{agent.hook_health.message}</p>
              {agent.hook_health.action && <div className="mono">{agent.hook_health.action}</div>}
            </div>
          </div>
        )}
        {agent.status === 'waiting' && agent.waiting_for && (
          <div className="tile-wait">
            <div className="wait-head"><Icon name="hand" size={11}/> {permissionWaiting ? 'Awaiting your approval' : 'Awaiting your input'}</div>
            <div className="wait-cmd mono">$ {agent.waiting_for.cmd}</div>
            <div className="wait-why">{agent.waiting_for.why}</div>
          </div>
        )}
        {agent.status === 'dead' && (
          <div className="tile-wait err">
            <div className="wait-head"><Icon name="alert-octagon" size={11}/> Crashed</div>
            <div className="wait-cmd mono err">{agent.exit_reason || 'unknown'}</div>
          </div>
        )}
        <div className="tile-pixel">
          <PixelIndicator data={agent.activity} status={agent.status}/>
          <div className="pixel-foot">
            <span className="mono">last action: {formatActivity(liveSec)}</span>
            <span className={`mono ctx-text ctx-${tokens_tier}`} style={{marginLeft: 'auto'}} title={TOKEN_ESTIMATE_TITLE}>{fmtTokens(agent.tokens_used)} / {fmtTokens(agent.tokens_max)} ctx · {Math.round(tokens_pct)}%</span>
          </div>
          <div className={`tokens-bar ctx-${tokens_tier}`}><span style={{width: `${tokens_pct}%`}}></span></div>
        </div>
        <div className="tile-action-row" onClick={(e) => e.stopPropagation()}>
          {agent.last_action && <span className="tile-last mono">{agent.last_action}</span>}
          <div className="tile-actions">
            {agent.status === 'waiting' && (
              <>
                {permissionWaiting ? (
                  <>
                    <button className="btn sm green" onClick={() => onAction('approve', agent)}><Icon name="check" size={11}/>Approve</button>
                    <button className="btn sm danger" onClick={() => onAction('deny', agent)}><Icon name="x" size={11}/>Deny</button>
                  </>
                ) : (
                  <>
                    {flowWaiting && <button className="btn sm green" onClick={() => onAction('clear-waiting', agent)}><Icon name="unlock" size={11}/>Unblock</button>}
                    <button className="btn sm" onClick={() => onAction('pause', agent)}><Icon name="pause" size={11}/>Pause</button>
                    <button className="btn sm primary" onClick={() => onAction('attach', agent)}><Icon name="external-link" size={11}/>Open</button>
                  </>
                )}
              </>
            )}
            {agent.status === 'running' && (
              <>
                <button className="btn sm" onClick={() => onAction('pause', agent)}><Icon name="pause" size={11}/>Pause</button>
                <button className="btn sm primary" onClick={() => onAction('attach', agent)}><Icon name="external-link" size={11}/>Open</button>
              </>
            )}
            {agent.status === 'idle' && (
              <>
                <button className="btn sm" onClick={() => onAction('resume', agent)}><Icon name="play" size={11}/>Resume</button>
                <button className="btn sm primary" onClick={() => onAction('attach', agent)}><Icon name="external-link" size={11}/>Open</button>
              </>
            )}
            {agent.status === 'stale' && (
              <>
                <button className="btn sm" onClick={() => onAction('archive', agent)}><Icon name="archive" size={11}/>Archive</button>
                <button className="btn sm primary" onClick={() => onAction('attach', agent)}><Icon name="external-link" size={11}/>Open</button>
              </>
            )}
            {agent.status === 'dead' && (
              <>
                <button className="btn sm" onClick={() => onAction('investigate', agent)}><Icon name="search" size={11}/>Investigate</button>
                <button className="btn sm primary" onClick={() => onAction('restart', agent)}><Icon name="refresh-cw" size={11}/>Restart</button>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};

// ───────── Activity heatmap (top of MC) ────────────────────────────────
const ActivityHeatmap = ({ data = [] }) => {
  const tick = useContext(ClockCtx);
  const parseDate = (key) => {
    const [y, m, d] = String(key || '').split('-').map(Number);
    return Number.isFinite(y) && Number.isFinite(m) && Number.isFinite(d) ? new Date(y, m - 1, d) : new Date();
  };
  const dateKey = (date) => {
    const y = date.getFullYear();
    const m = String(date.getMonth() + 1).padStart(2, '0');
    const d = String(date.getDate()).padStart(2, '0');
    return `${y}-${m}-${d}`;
  };
  const days = useMemo(() => {
    if (data && data.length) return data.slice(-84);
    const today = new Date();
    return Array.from({ length: 84 }).map((_, i) => {
      const day = new Date(today);
      day.setDate(today.getDate() - (83 - i));
      return { date: dateKey(day), count: 0, tasks: [] };
    });
  }, [data, tick]);
  const weeks = Array.from({ length: Math.ceil(days.length / 7) }, (_, i) => days.slice(i * 7, i * 7 + 7));
  const values = days.map(day => day.count || 0);
  const totalActions = values.reduce((sum, count) => sum + count, 0);
  const max = Math.max(1, ...values);
  const level = (count) => {
    if (!count) return 0;
    const ratio = count / max;
    if (ratio < 0.25) return 1;
    if (ratio < 0.5) return 2;
    if (ratio < 0.75) return 3;
    return 4;
  };
  const colors = [
    'rgba(255,255,255,0.045)',
    'rgba(46, 182, 114, 0.24)',
    'rgba(46, 182, 114, 0.46)',
    'rgba(46, 182, 114, 0.72)',
    'rgba(83, 230, 145, 0.95)',
  ];
  const monthLabels = weeks.map((week, i) => {
    const date = parseDate(week[0]?.date);
    const prev = i > 0 ? parseDate(weeks[i - 1][0]?.date) : null;
    return !prev || prev.getMonth() !== date.getMonth()
      ? date.toLocaleString('en-US', { month: 'short' })
      : '';
  });
  const dayTip = (day) => {
    const count = day.count || 0;
    const label = parseDate(day.date).toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' });
    const tasks = (day.tasks || []).slice(0, 3).join(', ');
    const taskSuffix = tasks ? ` · ${tasks}` : '';
    if (!count) return `No activity on ${label}`;
    return `${count} action${count === 1 ? '' : 's'} on ${label}${taskSuffix}`;
  };

  return (
    <div className="heatmap gh">
      <div className="heatmap-head">
        <Icon name="git-commit" size={11}/>
        <span>Agent activity · last 12 weeks</span>
        <span className="mono dim gh-action-count">{totalActions} actions</span>
      </div>
      <div className="gh-grid">
        <div className="gh-days mono">
          {['', 'Mon', '', 'Wed', '', 'Fri', ''].map((label, i) => <div key={i} className="gh-day-label">{label}</div>)}
        </div>
        <div className="gh-chart">
          <div className="gh-months">
            {monthLabels.map((label, i) => <div key={i} className="gh-month-cell mono">{label}</div>)}
          </div>
          <div className="gh-weeks">
            {weeks.map((week, w) => (
              <div key={w} className="gh-week">
                {week.map(day => {
                  const count = day.count || 0;
                  const tasks = (day.tasks || []).join(', ');
                  const tip = dayTip(day);
                  return (
                    <div
                      key={day.date}
                      className="gh-cell"
                      title={`${count} action${count === 1 ? '' : 's'} · ${day.date}${tasks ? ` · ${tasks}` : ''}`}
                      data-tip={tip}
                      aria-label={tip}
                      tabIndex={0}
                      style={{background: colors[level(count)]}}
                    ></div>
                  );
                })}
              </div>
            ))}
          </div>
          <div className="gh-legend mono">
            <span>Less</span>
            {colors.map((color, i) => <div key={i} className="gh-cell" style={{background: color}}></div>)}
            <span>More</span>
          </div>
        </div>
      </div>
    </div>
  );
};

// ───────── Transcript view ─────────────────────────────────────────────
const firstTextLine = (value) => String(value || '').trim().split(/\r?\n/).find(Boolean) || '';
const lineCount = (value) => String(value || '').split(/\r?\n/).length;
const renderInlineMarkdown = (text, keyPrefix = 'in') => {
  const source = String(text || '');
  const parts = [];
  const inlineRe = /(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^)]+\)|https?:\/\/[^\s)]+)/g;
  let last = 0;
  let idx = 0;
  let match;
  while ((match = inlineRe.exec(source)) !== null) {
    if (match.index > last) parts.push(source.slice(last, match.index));
    const token = match[0];
    const key = `${keyPrefix}-${idx++}`;
    if (token.startsWith('`') && token.endsWith('`')) {
      parts.push(<code key={key} className="md-inline-code">{token.slice(1, -1)}</code>);
    } else if (token.startsWith('**') && token.endsWith('**')) {
      parts.push(<strong key={key}>{renderInlineMarkdown(token.slice(2, -2), `${key}-b`)}</strong>);
    } else {
      const link = token.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
      const href = link ? link[2] : token;
      const label = link ? link[1] : token;
      parts.push(<a key={key} className="md-link" href={href} target="_blank" rel="noreferrer">{label}</a>);
    }
    last = inlineRe.lastIndex;
  }
  if (last < source.length) parts.push(source.slice(last));
  return parts;
};

const MarkdownBlock = ({ text }) => {
  const lines = String(text || '').replace(/\n{4,}/g, '\n\n\n').split(/\r?\n/);
  const nodes = [];
  let para = [];
  let list = null;
  let code = null;

  const flushPara = () => {
    if (!para.length) return;
    const body = para.join(' ').trim();
    if (body) nodes.push(<p key={`p-${nodes.length}`}>{renderInlineMarkdown(body, `p-${nodes.length}`)}</p>);
    para = [];
  };
  const flushList = () => {
    if (!list) return;
    const ListTag = list.type;
    nodes.push(
      <ListTag key={`l-${nodes.length}`} className="md-list">
        {list.items.map((item, i) => <li key={i}>{renderInlineMarkdown(item, `li-${nodes.length}-${i}`)}</li>)}
      </ListTag>
    );
    list = null;
  };
  const flushCode = () => {
    if (!code) return;
    nodes.push(<pre key={`c-${nodes.length}`} className="md-code mono"><code>{code.lines.join('\n')}</code></pre>);
    code = null;
  };

  lines.forEach((line) => {
    if (line.trim().startsWith('```')) {
      if (code) flushCode();
      else {
        flushPara();
        flushList();
        code = { lines: [] };
      }
      return;
    }
    if (code) {
      code.lines.push(line);
      return;
    }
    if (!line.trim()) {
      flushPara();
      flushList();
      return;
    }
    const heading = line.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      flushPara();
      flushList();
      nodes.push(<div key={`h-${nodes.length}`} className={`md-heading level-${heading[1].length}`}>{renderInlineMarkdown(heading[2], `h-${nodes.length}`)}</div>);
      return;
    }
    const bullet = line.match(/^\s*[-*]\s+(.+)$/);
    const numbered = line.match(/^\s*\d+\.\s+(.+)$/);
    if (bullet || numbered) {
      flushPara();
      const type = numbered ? 'ol' : 'ul';
      if (!list || list.type !== type) {
        flushList();
        list = { type, items: [] };
      }
      list.items.push((bullet || numbered)[1]);
      return;
    }
    const quote = line.match(/^\s*>\s?(.+)$/);
    if (quote) {
      flushPara();
      flushList();
      nodes.push(<blockquote key={`q-${nodes.length}`}>{renderInlineMarkdown(quote[1], `q-${nodes.length}`)}</blockquote>);
      return;
    }
    para.push(line.trim());
  });
  flushCode();
  flushPara();
  flushList();
  return <div className="md-body">{nodes.length ? nodes : <p>{text}</p>}</div>;
};

const ToolCard = ({ entry, index, open, setOpen, kind }) => {
  const key = `${kind}-${index}`;
  const isOpen = !!open[key];
  const payload = kind === 'call' ? entry.input : (entry.preview || entry.summary || '');
  const summary = firstTextLine(kind === 'call' ? entry.input : (entry.summary || entry.preview)) || (kind === 'call' ? 'tool call' : 'tool output');
  const name = entry.tool || (kind === 'call' ? 'tool' : 'result');

  if (kind === 'result') {
    return (
      <div className={`tool-card result${isOpen ? ' open' : ''}${entry._new ? ' slide-in' : ''}`}>
        <span className="tool-elbow mono" aria-hidden="true">└─</span>
        <div className="tool-result-card">
          <button type="button" className="tool-result-head" onClick={() => setOpen(o => ({ ...o, [key]: !o[key] }))} aria-expanded={isOpen}>
            <Icon name={isOpen ? 'chevron-down' : 'chevron-right'} size={11}/>
            <span className="tool-result-name mono">{name}</span>
            <span className="tool-result-summary mono">{summary}</span>
            {payload && <span className="tool-result-lines mono">{lineCount(payload)} lines</span>}
          </button>
          {isOpen && payload && <pre className="tool-result-body mono">{payload}</pre>}
        </div>
      </div>
    );
  }

  return (
    <div className={`tool-card call${isOpen ? ' open' : ''}${entry._new ? ' slide-in' : ''}`}>
      <button type="button" className="tool-call-head" onClick={() => setOpen(o => ({ ...o, [key]: !o[key] }))} aria-expanded={isOpen}>
        <Icon name={isOpen ? 'chevron-down' : 'chevron-right'} size={11}/>
        <Icon name="wrench" size={11}/>
        <span className="tool-call-name mono">{name}</span>
        <span className="tool-call-summary mono">{summary}</span>
        {payload && <span className="tool-call-lines mono">{lineCount(payload)} lines</span>}
      </button>
      {isOpen && payload && <pre className="tool-call-body mono">{payload}</pre>}
    </div>
  );
};

const ChatCopyBtn = ({ text }) => {
  const [copied, setCopied] = useState(false);
  const onClick = () => {
    if (!navigator.clipboard) return;
    navigator.clipboard.writeText(String(text || '')).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }).catch(() => {});
  };
  return (
    <button type="button" className="chat-copy-btn" onClick={onClick} title={copied ? 'Copied' : 'Copy response'} aria-label={copied ? 'Copied' : 'Copy response'}>
      <Icon name={copied ? 'check' : 'copy'} size={11}/>
    </button>
  );
};

const ChatMessage = ({ entry, provider }) => {
  const isUser = entry.type === 'user';
  const isThinking = entry.type === 'thinking';
  const text = entry.text || '';
  const label = isUser ? 'you' : isThinking ? 'thinking' : (provider === 'codex' ? 'codex' : 'claude');

  if (isUser && /^\s*\[Request interrupted.*\]\s*$/i.test(text)) {
    return (
      <div className="chat-interrupted">
        <span className="chat-interrupted-line" aria-hidden="true"/>
        <span className="mono">request interrupted</span>
        <span className="chat-interrupted-line" aria-hidden="true"/>
      </div>
    );
  }

  if (isUser) {
    return (
      <article className={`chat-message user${entry._new ? ' slide-in' : ''}`}>
        <div className="chat-user-bubble">
          <div className="chat-user-body"><MarkdownBlock text={text}/></div>
          <Icon name="check-check" size={12} className="chat-user-tick"/>
        </div>
      </article>
    );
  }

  if (isThinking) {
    return (
      <article className={`chat-message thinking${entry._new ? ' slide-in' : ''}`}>
        <div className="chat-bot">
          <div className="chat-bot-head">
            <Icon name="brain" size={12}/>
            <span className="chat-bot-name mono">thinking</span>
          </div>
          <div className="chat-bot-body thinking"><MarkdownBlock text={text}/></div>
        </div>
      </article>
    );
  }

  return (
    <article className={`chat-message assistant${entry._new ? ' slide-in' : ''}`}>
      <div className="chat-bot">
        <div className="chat-bot-head">
          <span className="chat-bot-name mono">{label}</span>
        </div>
        <div className="chat-bot-body"><MarkdownBlock text={text}/></div>
        <ChatCopyBtn text={text}/>
      </div>
    </article>
  );
};

const TranscriptView = ({ entries, live, provider = 'claude' }) => {
  const [live_entries, setLive] = useState([]);
  const [open, setOpen] = useState({});
  const all = entries.concat(live_entries);
  const ref = useRef(null);

  useEffect(() => {
    if (!live) return;
    let extras = [
      { type: 'assistant', text: 'Committing now.' },
      { type: 'tool_use', tool: 'Bash', input: '$ git add -A && git commit -m "fix(scan): treat null Results as zero findings"' },
      { type: 'tool_result', tool: 'Bash', summary: '1 file changed, 5 insertions(+), 2 deletions(-)' },
      { type: 'tool_use', tool: 'Bash', input: '$ git push origin image-vuln/scanner-v2' },
      { type: 'tool_result', tool: 'Bash', summary: 'pushed to origin' },
      { type: 'assistant', text: 'Pushed. PR is at github.com/Facets-cloud/starboard/pull/142.' },
    ];
    let i = 0;
    const id = setInterval(() => {
      if (i >= extras.length) { clearInterval(id); return; }
      setLive(le => [...le, { ...extras[i], _new: true }]);
      i += 1;
      setTimeout(() => { if (ref.current) ref.current.scrollTop = ref.current.scrollHeight; }, 50);
    }, 4500);
    return () => clearInterval(id);
  }, [live]);

  useEffect(() => {
    if (live && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [live, all.length]);

  return (
    <div className={`transcript chat-transcript ${provider}`} ref={ref}>
      {all.length === 0 && <div className="transcript-empty mono">No transcript entries yet.</div>}
      {all.map((e, i) => {
        if (e.type === 'user' || e.type === 'assistant' || e.type === 'thinking') return <ChatMessage key={i} entry={e} provider={provider}/>;
        if (e.type === 'tool_use') {
          return <ToolCard key={i} entry={e} index={i} open={open} setOpen={setOpen} kind="call"/>;
        }
        if (e.type === 'tool_result') {
          return <ToolCard key={i} entry={e} index={i} open={open} setOpen={setOpen} kind="result"/>;
        }
        return null;
      })}
      {live && <div className="live-pill"><span className="dot running"></span>streaming</div>}
    </div>
  );
};

// ───────── Focus drawer (Mission Control click on tile) ────────────────
const FocusDrawer = ({ agent, onClose, goto, action }) => {
  useEffect(() => {
    const h = (e) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [onClose]);
  if (!agent) return null;
  return (
    <div className="drawer-scrim" onClick={onClose}>
      <div className="drawer" onClick={(e) => e.stopPropagation()}>
        <div className="drawer-head">
          <Dot status={agent.status}/>
          <span className="mono" style={{fontSize: 14, fontWeight: 500}}>{agent.slug}</span>
          <StatusPill status={agent.status}/>
          <TaskStatePill status={agent.task_status}/>
          <button className="btn sm" style={{marginLeft: 'auto'}} onClick={onClose}><Icon name="x" size={11}/></button>
        </div>
        <div className="drawer-body">
          <div className="drawer-meta">
            <span>{agent.name}</span>
            <div className="mono dim" style={{fontSize: 12, marginTop: 4}}>{agent.project || '(floating)'} · {agent.branch}</div>
            <DependencyBadges task={agent}/>
          </div>
          <div className="drawer-summary">
            <h4>Last 5 minutes</h4>
            <div className="dim">{agent.summary}</div>
          </div>
          {agent.hook_health && (
            <div className="hook-health">
              <Icon name="shield-alert" size={14}/>
              <div>
                <strong>Codex hooks need attention</strong>
                <p>{agent.hook_health.message}</p>
                {agent.hook_health.action && <div className="mono">{agent.hook_health.action}</div>}
              </div>
            </div>
          )}
          <div className="drawer-summary">
            <h4>Next step (per the agent)</h4>
            <div>{agent.next_step}</div>
          </div>
          <div className="drawer-actions">
            <button className="btn primary" onClick={() => { action('attach', agent); onClose(); }}><Icon name="maximize-2" size={12}/>Open full view</button>
            <button className="btn" onClick={() => action('iterm', agent)}><Icon name="external-link" size={12}/>iTerm</button>
          </div>
        </div>
      </div>
    </div>
  );
};

window.MC.Icon = Icon;
window.MC.FlowMark = FlowMark;
window.MC.FlowLogo = FlowLogo;
window.MC.FlowLoader = FlowLoader;
window.MC.SkeletonRows = SkeletonRows;
window.MC.Dot = Dot;
window.MC.StatusPill = StatusPill;
window.MC.TaskStatePill = TaskStatePill;
window.MC.PriorityPill = PriorityPill;
window.MC.AgentChip = AgentChip;
window.MC.ProviderMark = ProviderMark;
window.MC.BranchChip = BranchChip;
window.MC.DependencyBadges = DependencyBadges;
window.MC.PixelIndicator = PixelIndicator;
window.MC.Sparkline = Sparkline;
window.MC.AgentTile = AgentTile;
window.MC.TranscriptView = TranscriptView;
window.MC.ActivityHeatmap = ActivityHeatmap;
window.MC.FocusDrawer = FocusDrawer;
