import { useLocation } from 'wouter'
import { GitBranch, Clock3, Radar, Coins, AlertTriangle } from 'lucide-react'
import type { UiAgent } from '../lib/types'
import { fromMinutes, fromSeconds, compact, compactTokens } from '../lib/format'
import { ProviderIcon, Sparkline, StatusDot, TokenBar } from './ui'
import { NudgeComposer } from './NudgeComposer'

const BADGE_TONE: Record<string, string> = {
  waiting: 'warn',
  running: 'ok',
  dead: 'danger',
  stale: 'danger',
  released: '',
}
const STATUS_LABEL: Record<string, string> = {
  dead: 'crashed',
  stale: 'stalled',
}

export function AgentCard({ agent }: { agent: UiAgent }) {
  const [, navigate] = useLocation()
  const waiting = agent.status === 'waiting'
  // A finished task should read as "done", not as the residual runtime state
  // (its session is merely idle/released). task_status is the source of truth
  // for completion; the runtime status drives everything else.
  const isDone = agent.task_status === 'done'
  const badgeStatus = isDone ? 'done' : agent.status
  return (
    <article className="card acard" onClick={() => navigate(`/session/${agent.slug}`)}>
      <div className="acard-top">
        <ProviderIcon provider={agent.provider} size={17} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="acard-title clip">{agent.name}</div>
          <div className="acard-ref clip">{agent.slug}</div>
        </div>
        {agent.hook_health && (
          <span
            className="acard-mon"
            style={{ color: 'var(--warn)' }}
            title={`${agent.hook_health.message}${agent.hook_health.action ? ' — ' + agent.hook_health.action : ''}`}
          >
            <AlertTriangle size={14} />
          </span>
        )}
        {agent.monitored && (
          <span className="acard-mon" title="Background monitor active">
            <Radar size={14} />
          </span>
        )}
        <span className={`badge ${isDone ? 'info' : BADGE_TONE[agent.status] ?? ''}`}>
          <StatusDot status={badgeStatus} />
          {isDone ? 'done' : STATUS_LABEL[agent.status] ?? agent.status}
        </span>
      </div>

      {waiting && agent.waiting_for?.why && (
        <div className="badge warn" style={{ height: 'auto', padding: '5px 9px', whiteSpace: 'normal', textAlign: 'left' }}>
          {agent.waiting_for.why}
        </div>
      )}

      <div className="acard-summary">{agent.last_action || agent.summary}</div>

      <div className="acard-meta">
        {agent.project && <span className="tag">{agent.project}</span>}
        <span className="row" style={{ gap: 5 }}>
          <GitBranch size={12} /> <span className="mono clip" style={{ maxWidth: 150 }}>{agent.branch}</span>
        </span>
        <span className="row" style={{ gap: 5 }}>
          <Clock3 size={12} /> {fromMinutes(agent.started_min)}
        </span>
        {agent.tokens_session > 0 && (
          <span
            className="tag tok-pill"
            title={`${agent.tokens_session.toLocaleString()} tokens used this session · context ${agent.tokens_used.toLocaleString()} / ${agent.tokens_max.toLocaleString()}`}
          >
            <Coins size={11} /> {compactTokens(agent.tokens_session)} tok
          </span>
        )}
      </div>

      <div className="acard-foot">
        <span className="acard-idle mono">
          {isDone
            ? `done ${fromSeconds(agent.last_activity_sec)} ago`
            : agent.status === 'running' && agent.last_activity_sec < 120
            ? 'active'
            : `idle ${fromSeconds(agent.last_activity_sec)}`}
        </span>
        <div className="spacer" />
        {(agent.diff.add > 0 || agent.diff.rem > 0) && (
          <span className="diffstat">
            <span className="add">+{agent.diff.add}</span>
            <span className="rem">−{agent.diff.rem}</span>
          </span>
        )}
      </div>

      <div className="acard-spark">
        <Sparkline data={agent.activity ?? []} flex />
      </div>

      <div className="row gap" style={{ gap: 9 }}>
        <TokenBar used={agent.tokens_used} max={agent.tokens_max} />
        <span className="faint mono" style={{ fontSize: 10.5 }}>
          {compact(agent.tokens_used)}/{compact(agent.tokens_max)}
        </span>
      </div>

      {!isDone && <NudgeComposer slug={agent.slug} compact />}
    </article>
  )
}
