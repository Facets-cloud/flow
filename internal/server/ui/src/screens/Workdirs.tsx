import { useState } from 'react'
import { Check, HardDrive, Loader2, Pencil, Plus, Trash2, X } from 'lucide-react'
import { queryClient, useWorkdirs } from '../lib/query'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { apiAction } from '../lib/api'
import { pushToast } from '../lib/toast'
import { confirmAction } from '../lib/confirm'
import { AddWorkdirModal } from '../components/modals'
import { EmptyState, ErrorNote, Loading } from '../components/ui'
import { ago } from '../lib/format'
import type { WorkdirView } from '../lib/types'

export function Workdirs() {
  useDocumentTitle('Workdirs')
  const { data, isLoading, error } = useWorkdirs()
  const [addOpen, setAddOpen] = useState(false)

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">registered repositories</div>
          <h1 className="h-xl">Workdirs</h1>
        </div>
        <div className="spacer" />
        <button className="btn" onClick={() => setAddOpen(true)}>
          <Plus size={15} /> Add workdir
        </button>
      </div>
      {isLoading ? (
        <Loading rows={4} />
      ) : error ? (
        <ErrorNote error={error} />
      ) : !data || data.length === 0 ? (
        <EmptyState
          icon={<HardDrive size={30} />}
          title="No workdirs"
          hint="Directories used by tasks register here automatically — or add one manually."
        />
      ) : (
        <div className="card" style={{ padding: '6px 14px 4px' }}>
          <table className="tbl fixed">
            <colgroup>
              <col />
              <col style={{ width: 280 }} />
              <col style={{ width: 70 }} />
              <col style={{ width: 96 }} />
              <col style={{ width: 64 }} />
            </colgroup>
            <thead>
              <tr>
                <th>Directory</th>
                <th>Remote</th>
                <th style={{ textAlign: 'right' }}>Tasks</th>
                <th style={{ textAlign: 'right' }}>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.map((w) => (
                <WorkdirRow key={w.path} w={w} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      <AddWorkdirModal open={addOpen} onClose={() => setAddOpen(false)} />
    </div>
  )
}

// One workdir row with inline rename + description editing. `workdir-rename`
// rewrites both name and description in one call (the backend passes whatever
// it's given straight to Register), so the editor submits both fields together
// to avoid clobbering the description with an empty value.
function WorkdirRow({ w }: { w: WorkdirView }) {
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [busy, setBusy] = useState(false)
  const [removing, setRemoving] = useState(false)

  const begin = () => {
    setName(w.name || (w.path.split('/').pop() ?? ''))
    setDesc(w.description || '')
    setEditing(true)
  }
  const save = async () => {
    const trimmed = name.trim()
    if (!trimmed) return // name is required by the backend
    setBusy(true)
    try {
      await apiAction({ kind: 'workdir-rename', path: w.path, name: trimmed, description: desc.trim() })
      pushToast('ok', 'workdir updated')
      queryClient.invalidateQueries()
      setEditing(false)
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : 'could not update workdir')
    } finally {
      setBusy(false)
    }
  }
  const remove = async () => {
    const ok = await confirmAction({
      title: 'Unregister workdir?',
      body: `${w.path}\n\nTasks already using it are unaffected — this only removes it from the registry.`,
      confirmLabel: 'Unregister',
      danger: true,
    })
    if (!ok) return
    setRemoving(true)
    try {
      await apiAction({ kind: 'workdir-remove', path: w.path })
      pushToast('ok', 'workdir unregistered')
      queryClient.invalidateQueries()
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : 'could not remove workdir')
    } finally {
      setRemoving(false)
    }
  }
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      save()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setEditing(false)
    }
  }

  return (
    <tr style={{ cursor: 'default' }}>
      <td>
        {editing ? (
          <div className="col" style={{ gap: 6 }}>
            <input
              className="input"
              autoFocus
              value={name}
              placeholder="name"
              onChange={(e) => setName(e.target.value)}
              onKeyDown={onKey}
            />
            <input
              className="input"
              value={desc}
              placeholder="description (optional)"
              onChange={(e) => setDesc(e.target.value)}
              onKeyDown={onKey}
            />
            <div className="mono faint clip" style={{ fontSize: 11 }}>{w.path}</div>
          </div>
        ) : (
          <>
            <div style={{ fontWeight: 500 }} className="clip">{w.name || w.path.split('/').pop()}</div>
            <div className="mono faint clip" style={{ fontSize: 11 }}>{w.path}</div>
            {w.description && (
              <div className="faint clip" style={{ fontSize: 11.5, marginTop: 2 }}>{w.description}</div>
            )}
          </>
        )}
      </td>
      <td className="dim mono clip" style={{ fontSize: 11.5 }}>{w.git_remote || '—'}</td>
      <td className="num" style={{ textAlign: 'right' }}>{w.tasks_using_this}</td>
      <td className="dim mono" style={{ textAlign: 'right', fontSize: 11.5 }}>
        {w.untouched_30d ? <span className="faint">untouched</span> : ago(w.last_used_at)}
      </td>
      <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
        {editing ? (
          <>
            <button className="btn icon ghost sm row-action" title="Save" aria-label="Save" disabled={busy} onClick={save}>
              {busy ? <Loader2 size={13} className="spin" /> : <Check size={14} />}
            </button>
            <button className="btn icon ghost sm row-action" title="Cancel" aria-label="Cancel" onClick={() => setEditing(false)}>
              <X size={14} />
            </button>
          </>
        ) : (
          <>
            <button className="btn icon ghost sm row-action" title="Rename / describe" aria-label="Edit workdir" onClick={begin}>
              <Pencil size={13} />
            </button>
            <button
              className="btn icon ghost sm row-action"
              title="Unregister workdir"
              aria-label="Unregister workdir"
              disabled={removing}
              onClick={remove}
            >
              <Trash2 size={13} />
            </button>
          </>
        )}
      </td>
    </tr>
  )
}
