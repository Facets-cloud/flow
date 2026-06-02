import { useEffect, useMemo, useState } from 'react'
import { useLocation } from 'wouter'
import { Inbox as InboxIcon, CheckCheck, ExternalLink, Play } from 'lucide-react'
import { useAction, useInbox, useInboxConversation } from '../lib/query'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { NudgeComposer } from '../components/NudgeComposer'
import { EmptyState, ErrorNote, Loading, ProviderIcon, SourceIcon, StatusDot } from '../components/ui'
import { Md } from '../components/Markdown'
import { ago, dateTime } from '../lib/format'
import type { InboxFeedEntry } from '../lib/types'

// Slack/GitHub use :claude:/:codex: shortcodes to trigger an agent. In rendered
// markdown we swap them for the real brand logos (inline images, sized by the
// .md img[alt] CSS); in plain-text snippets we fall back to the readable word.
function emojifyMd(text: string): string {
  return (text || '')
    .replace(/:claude:/gi, '![claude](/claudecode-color.svg)')
    .replace(/:codex:/gi, '![codex](/codex-color.svg)')
}
function emojifyText(text: string): string {
  return (text || '').replace(/:claude:/gi, 'Claude').replace(/:codex:/gi, 'Codex')
}

// Inline text with :claude:/:codex: shortcodes swapped for the brand logo —
// used for short labels (badges/titles) where markdown images aren't available.
function ProviderEmojiText({ text }: { text: string }) {
  const parts = (text || '').split(/(:claude:|:codex:)/gi)
  return (
    <>
      {parts.map((p, i) => {
        const id = p.replace(/:/g, '').toLowerCase()
        if (id === 'claude' || id === 'codex') return <ProviderIcon key={i} provider={id} size={13} />
        return p ? <span key={i}>{p}</span> : null
      })}
    </>
  )
}

// A reaction chip: the brand logo + label for claude/codex, else the raw tag.
function ReactionChip({ reaction }: { reaction: string }) {
  const id = reaction.replace(/:/g, '').trim().toLowerCase()
  if (id === 'claude' || id === 'codex') {
    return (
      <span className="tag reaction-chip">
        <ProviderIcon provider={id} size={13} />
        {id === 'codex' ? 'Codex' : 'Claude'}
      </span>
    )
  }
  return <span className="tag">{reaction}</span>
}

interface Convo {
  slug: string
  name: string
  project?: string
  status: string
  source?: string
  live: boolean
  unread: number
  latest: InboxFeedEntry
}

export function InboxScreen() {
  useDocumentTitle('Inbox')
  const { data, isLoading, error } = useInbox()
  const [selected, setSelected] = useState<string | null>(null)

  const convos = useMemo<Convo[]>(() => {
    const map = new Map<string, Convo>()
    for (const e of data?.entries ?? []) {
      const c = map.get(e.task_slug)
      if (!c) {
        map.set(e.task_slug, {
          slug: e.task_slug,
          name: e.task_name,
          project: e.project_slug ?? undefined,
          status: e.status,
          source: e.source,
          live: e.live,
          unread: e.unread ? 1 : 0,
          latest: e,
        })
      } else {
        if (e.unread) c.unread += 1
        if (Date.parse(e.timestamp) > Date.parse(c.latest.timestamp)) c.latest = e
      }
    }
    return [...map.values()].sort((a, b) => Date.parse(b.latest.timestamp) - Date.parse(a.latest.timestamp))
  }, [data])

  useEffect(() => {
    if (!selected && convos.length) setSelected(convos[0].slug)
  }, [convos, selected])

  if (isLoading) return <div className="page"><Loading rows={5} /></div>
  if (error) return <div className="page"><ErrorNote error={error} /></div>
  if (!data || convos.length === 0) {
    return (
      <div className="page">
        <EmptyState
          icon={<InboxIcon size={30} />}
          title="Inbox is empty"
          hint="Slack reactions and GitHub mentions routed to flow tasks land here."
        />
      </div>
    )
  }

  return (
    <div className="page flush">
      <div className="twopane">
        <div className="pane-list">
          <div className="pane-list-head">
            <div className="eyebrow">inbox</div>
            <div className="h-lg">{data.unread_count} unread · {data.task_count} threads</div>
          </div>
          {convos.map((c) => (
            <div key={c.slug} className={`pli${selected === c.slug ? ' active' : ''}`} onClick={() => setSelected(c.slug)}>
              <div className="pli-top">
                {c.unread > 0 && <span className="unread-dot" />}
                <SourceIcon source={c.source} />
                <span className="pli-title clip">{c.name}</span>
                <span className="faint mono" style={{ fontSize: 10.5 }}>{ago(c.latest.timestamp)}</span>
              </div>
              <div className="pli-snippet">{emojifyText(c.latest.body_snippet || c.latest.body)}</div>
              <div className="row gap" style={{ gap: 6 }}>
                {c.live && <span className="badge ok"><StatusDot status="running" />live</span>}
                {c.project && <span className="tag">{c.project}</span>}
                {c.unread > 1 && <span className="tag">{c.unread} new</span>}
              </div>
            </div>
          ))}
        </div>
        <div className="pane-detail">
          {selected && <Conversation slug={selected} unread={convos.find((c) => c.slug === selected)?.unread ?? 0} />}
        </div>
      </div>
    </div>
  )
}

function Conversation({ slug, unread }: { slug: string; unread: number }) {
  const [, navigate] = useLocation()
  const { data, isLoading, error } = useInboxConversation(slug)
  const action = useAction()
  const markRead = () => {
    if (unread <= 0 || action.isPending) return
    action.mutate({ kind: 'mark-read', target: slug })
  }
  if (isLoading) return <div style={{ padding: 24 }}><Loading rows={4} /></div>
  if (error) return <div style={{ padding: 24 }}><ErrorNote error={error} /></div>
  if (!data) return null
  return (
    <div style={{ padding: '22px 26px', maxWidth: 820 }}>
      <div className="detail-head">
        <div style={{ flex: 1 }}>
          <div className="eyebrow row gap" style={{ gap: 7 }}>
            <SourceIcon source={data.source} />
            {data.channel_name || data.source || 'conversation'}
          </div>
          <div className="detail-title">{data.name}</div>
          <div className="detail-ref">{data.slug}</div>
        </div>
        {unread > 0 && (
          <button type="button" className="btn ghost sm" disabled={action.isPending} onClick={markRead}>
            <CheckCheck size={13} /> Mark read
          </button>
        )}
        <button type="button" className="btn primary sm" onClick={() => navigate(`/session/${slug}`)}>
          <Play size={13} /> Open session
        </button>
      </div>

      <div className="inbox-reply">
        <div className="eyebrow row gap" style={{ gap: 7 }}>
          <ProviderIcon provider={data.provider} size={13} />
          Respond
        </div>
        <NudgeComposer slug={slug} />
      </div>

      <div className="col" style={{ gap: 14, marginTop: 8 }}>
        {data.messages.map((m, i) => (
          <div key={i} className="card" style={{ padding: '13px 15px' }}>
            <div className="row gap" style={{ gap: 8, marginBottom: 7 }}>
              <SourceIcon source={m.source} />
              <span style={{ fontWeight: 600, fontSize: 13 }}>{m.sender_name}</span>
              {m.title && <span className="badge"><ProviderEmojiText text={m.title} /></span>}
              {m.reaction && <ReactionChip reaction={m.reaction} />}
              <div className="spacer" />
              <span className="faint mono" style={{ fontSize: 11 }}>{dateTime(m.timestamp)}</span>
            </div>
            <Md source={emojifyMd(m.body)} />
            {m.permalink && (
              <a className="btn ghost sm" style={{ marginTop: 8 }} href={m.permalink} target="_blank" rel="noreferrer">
                <ExternalLink size={13} /> Open in {m.source}
              </a>
            )}
          </div>
        ))}
        {data.messages.length === 0 && <div className="faint">No messages in this thread.</div>}
      </div>
    </div>
  )
}
