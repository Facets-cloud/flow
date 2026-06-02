import { useEffect, useMemo, useRef, useState, type ClipboardEvent, type DragEvent } from 'react'
import { useLocation } from 'wouter'
import { AlertTriangle, FolderGit2, ImagePlus, Loader2, X } from 'lucide-react'
import { Modal } from './Modal'
import { Field } from './ui'
import { Select } from './Select'
import { AgentPicker, PermissionPicker, PriorityPicker } from './pickers'
import { WorkdirPicker } from './WorkdirPicker'
import { apiAction, apiActionForm, fileToRpcFile } from '../lib/api'
import { pushToast } from '../lib/toast'
import { queryClient, useProjects, useUiData } from '../lib/query'

function slugify(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60)
}

export function CreateTaskModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [, navigate] = useLocation()
  const { data: projects } = useProjects()
  const { data: ui } = useUiData()
  // Show ALL providers (so an uninstalled one greys out rather than vanishing);
  // availability drives the disabled state and the no-provider guard.
  const providers = useMemo(() => ui?.CAPABILITIES.providers ?? [], [ui])
  const availableProviders = useMemo(() => providers.filter((p) => p.available), [providers])
  const noProvider = providers.length > 0 && availableProviders.length === 0

  // Recent + most-used projects as one-click pills — fills the project AND its
  // workdir so you don't re-browse for a repo you already work in. Ranked by
  // recency (updated_at), then task volume; active projects only; capped at 10.
  const recentProjects = useMemo(() => {
    return (projects ?? [])
      .filter(
        (p) =>
          !p.archived_at &&
          !p.deleted_at &&
          p.work_dir &&
          // never surface flow-internal task scratch dirs (…/.flow/tasks/<x>/workspace)
          !p.work_dir.includes('/.flow/') &&
          !p.work_dir.endsWith('/workspace'),
      )
      .sort((a, b) => {
        const at = Date.parse(a.updated_at) || 0
        const bt = Date.parse(b.updated_at) || 0
        if (bt !== at) return bt - at
        return (b.task_counts?.total ?? 0) - (a.task_counts?.total ?? 0)
      })
      .slice(0, 10)
  }, [projects])

  const [name, setName] = useState('')
  const [prompt, setPrompt] = useState('')
  const [project, setProject] = useState('__adhoc')
  const [workDir, setWorkDir] = useState('')
  const [provider, setProvider] = useState('claude')

  // If the chosen provider isn't installed, fall back to whichever is.
  useEffect(() => {
    if (availableProviders.length && !availableProviders.some((p) => p.id === provider)) {
      setProvider(availableProviders[0].id)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [availableProviders])

  // Clicking a recent-project pill selects the project and fills its workdir.
  const pickProject = (slug: string, dir: string) => {
    setProject(slug)
    setWorkDir(dir)
  }
  const [permission, setPermission] = useState('default')
  const [priority, setPriority] = useState('medium')
  const [files, setFiles] = useState<File[]>([])
  const [busy, setBusy] = useState(false)
  const [dragging, setDragging] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)

  const projectWorkdir = projects?.find((p) => p.slug === project)?.work_dir
  const effectiveWorkdir = workDir.trim() || projectWorkdir || ''

  // Merge in images from any source (button, drag-drop, paste), de-duped by
  // name+size so the same paste/drop doesn't pile up.
  const addFiles = (incoming: File[]) => {
    const imgs = incoming.filter((f) => f.type.startsWith('image/'))
    if (!imgs.length) return
    setFiles((cur) => {
      const seen = new Set(cur.map((f) => `${f.name}:${f.size}`))
      const next = [...cur]
      for (const f of imgs) {
        const key = `${f.name}:${f.size}`
        if (!seen.has(key)) {
          seen.add(key)
          next.push(f)
        }
      }
      return next
    })
  }
  const onPaste = (e: ClipboardEvent) => {
    const imgs = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith('image/'))
    if (imgs.length) {
      e.preventDefault()
      addFiles(imgs)
    }
  }
  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setDragging(false)
    addFiles(Array.from(e.dataTransfer.files))
  }

  const reset = () => {
    setName('')
    setPrompt('')
    setProject('__adhoc')
    setWorkDir('')
    setPriority('medium')
    setFiles([])
    setBusy(false)
  }

  const submit = async () => {
    const slug = slugify(name)
    if (!name.trim() || !slug) {
      pushToast('error', 'A task name is required')
      return
    }
    if (!effectiveWorkdir) {
      pushToast('error', 'Pick a project or set a working directory')
      return
    }
    setBusy(true)
    try {
      let resp
      const base = {
        kind: 'create-flow',
        name: name.trim(),
        slug,
        project,
        work_dir: effectiveWorkdir,
        provider,
        permission_mode: permission,
        priority,
        prompt,
      }
      if (files.length) {
        const rpcFiles = await Promise.all(files.map((f) => fileToRpcFile(f, 'images')))
        resp = await apiActionForm(
          {
            kind: 'create-flow',
            name: base.name,
            slug,
            project,
            work_dir: effectiveWorkdir,
            provider,
            permission_mode: permission,
            priority,
            prompt,
          },
          rpcFiles,
        )
      } else {
        resp = await apiAction(base)
      }
      pushToast('ok', resp.message || `created ${slug}`)
      queryClient.invalidateQueries()
      onClose()
      reset()
      if (resp.bridge && resp.agent) navigate(`/session/${resp.agent.slug}`)
      else navigate(`/session/${slug}`)
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : 'create failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New task"
      width={620}
      footer={
        <>
          <span className="faint" style={{ fontSize: 12 }}>
            <span className="kbd">⌘</span> <span className="kbd">↵</span> to launch
          </span>
          <div className="spacer" />
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || noProvider} onClick={submit}>
            {busy ? <Loader2 size={15} className="spin" /> : null}
            Create & open session
          </button>
        </>
      }
    >
      <div
        className={`col${dragging ? ' dropping' : ''}`}
        style={{ gap: 14 }}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') submit()
        }}
        onPaste={onPaste}
        onDragOver={(e) => {
          if (Array.from(e.dataTransfer.types).includes('Files')) {
            e.preventDefault()
            setDragging(true)
          }
        }}
        onDragLeave={(e) => {
          if (e.currentTarget === e.target) setDragging(false)
        }}
        onDrop={onDrop}
      >
        {noProvider && (
          <div className="provider-guard">
            <AlertTriangle size={15} />
            <span>
              No agent runtime found. flow needs <b>Claude Code</b> or <b>Codex</b> installed on your PATH to run a
              session.
            </span>
          </div>
        )}
        <Field label="Task name">
          <input
            className="input"
            autoFocus
            placeholder="e.g. Wire up billing webhooks"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="Opening prompt / brief" hint="Becomes the task brief and the agent's first instruction.">
          <textarea
            className="textarea"
            placeholder="What should the agent do?"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
        </Field>
        <div className="row gap" style={{ gap: 14, alignItems: 'flex-start' }}>
          <Field label="Project">
            <Select
              value={project}
              onChange={setProject}
              options={[
                { value: '__adhoc', label: '— Ad-hoc (no project) —' },
                ...(projects ?? []).map((p) => ({ value: p.slug, label: p.name })),
              ]}
            />
          </Field>
          <Field label="Agent">
            <AgentPicker value={provider} onChange={setProvider} providers={providers} />
          </Field>
        </div>
        <Field
          label="Working directory"
          hint={projectWorkdir && !workDir ? `Inherits ${projectWorkdir}` : 'Absolute path the session runs in'}
        >
          {recentProjects.length > 0 && (
            <div className="wd-pills">
              {recentProjects.map((p) => (
                <button
                  key={p.slug}
                  type="button"
                  className={`wd-pill${project === p.slug ? ' active' : ''}`}
                  title={p.work_dir}
                  onClick={() => pickProject(p.slug, p.work_dir)}
                >
                  <FolderGit2 size={12} />
                  <span className="clip">{p.name}</span>
                </button>
              ))}
            </div>
          )}
          <WorkdirPicker
            value={workDir}
            onChange={setWorkDir}
            placeholder={projectWorkdir || '/Users/you/code/project'}
          />
        </Field>
        <div className="row gap" style={{ gap: 14, alignItems: 'flex-start' }}>
          <Field label="Permission mode">
            <PermissionPicker value={permission} onChange={setPermission} />
          </Field>
          <Field label="Priority">
            <PriorityPicker value={priority} onChange={setPriority} />
          </Field>
        </div>
        <div className="dropzone" onClick={() => fileInput.current?.click()}>
          <ImagePlus size={15} className="dim" />
          <span>
            <b>Attach images</b> — click, drag &amp; drop, or paste{' '}
            <span className="kbd">⌘V</span>
          </span>
          <input
            ref={fileInput}
            type="file"
            accept="image/*"
            multiple
            hidden
            onChange={(e) => {
              addFiles(Array.from(e.target.files ?? []))
              e.target.value = ''
            }}
          />
        </div>
        {files.length > 0 && (
          <div className="row gap wrap" style={{ gap: 6 }}>
            {files.map((f, i) => (
              <span key={`${f.name}-${i}`} className="filechip">
                <ImagePlus size={11} />
                <span className="clip" style={{ maxWidth: 160 }}>{f.name}</span>
                <button
                  type="button"
                  className="filechip-x"
                  aria-label={`Remove ${f.name}`}
                  onClick={() => setFiles((cur) => cur.filter((_, j) => j !== i))}
                >
                  <X size={11} />
                </button>
              </span>
            ))}
          </div>
        )}
      </div>
    </Modal>
  )
}

export function CreateProjectModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState('')
  const [workDir, setWorkDir] = useState('')
  const [priority, setPriority] = useState('medium')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    const slug = slugify(name)
    if (!slug) {
      pushToast('error', 'A project name is required')
      return
    }
    if (!workDir.trim()) {
      pushToast('error', 'A working directory is required')
      return
    }
    setBusy(true)
    try {
      const resp = await apiAction({
        kind: 'create-project',
        name: name.trim(),
        slug,
        work_dir: workDir.trim(),
        priority,
        description,
      })
      pushToast('ok', resp.message || `created project ${slug}`)
      queryClient.invalidateQueries()
      onClose()
      setName('')
      setWorkDir('')
      setDescription('')
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : 'create failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New project"
      footer={
        <>
          <div className="spacer" />
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy} onClick={submit}>
            {busy ? <Loader2 size={15} className="spin" /> : null}
            Create project
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 14 }}>
        <Field label="Project name">
          <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Working directory" hint="Browse to a folder, or create a new one inline.">
          <WorkdirPicker value={workDir} onChange={setWorkDir} />
        </Field>
        <Field label="Priority">
          <Select
            value={priority}
            onChange={setPriority}
            options={[
              { value: 'high', label: 'High' },
              { value: 'medium', label: 'Medium' },
              { value: 'low', label: 'Low' },
            ]}
          />
        </Field>
        <Field label="Brief" hint="Optional — describes the project for agents.">
          <textarea className="textarea" value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
      </div>
    </Modal>
  )
}

export function CreatePlaybookModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [, navigate] = useLocation()
  const { data: projects } = useProjects()
  const [name, setName] = useState('')
  const [project, setProject] = useState('__none')
  const [workDir, setWorkDir] = useState('')
  const [definition, setDefinition] = useState('')
  const [busy, setBusy] = useState(false)

  const projectWorkdir = projects?.find((p) => p.slug === project)?.work_dir
  const effectiveWorkdir = workDir.trim() || projectWorkdir || ''

  const reset = () => {
    setName('')
    setProject('__none')
    setWorkDir('')
    setDefinition('')
    setBusy(false)
  }

  const submit = async () => {
    const slug = slugify(name)
    if (!slug) {
      pushToast('error', 'A playbook name is required')
      return
    }
    if (!effectiveWorkdir) {
      pushToast('error', 'Pick a project or set a working directory')
      return
    }
    setBusy(true)
    try {
      const resp = await apiAction({
        kind: 'create-playbook',
        name: name.trim(),
        slug,
        project: project === '__none' ? '' : project,
        work_dir: effectiveWorkdir,
        description: definition,
      })
      pushToast('ok', resp.message || `created playbook ${slug}`)
      queryClient.invalidateQueries()
      onClose()
      reset()
      navigate(`/playbook/${slug}`)
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : 'create failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New playbook"
      width={620}
      footer={
        <>
          <div className="spacer" />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy} onClick={submit}>
            {busy ? <Loader2 size={15} className="spin" /> : null}
            Create playbook
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 14 }}>
        <Field label="Playbook name">
          <input
            className="input"
            aria-label="Playbook name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="Project">
          <Select
            value={project}
            onChange={setProject}
            options={[
              { value: '__none', label: 'No project' },
              ...(projects ?? []).map((p) => ({ value: p.slug, label: p.name })),
            ]}
          />
        </Field>
        <Field label="Working directory" hint="Defaults to the selected project's directory when available.">
          <WorkdirPicker value={workDir || projectWorkdir || ''} onChange={setWorkDir} />
        </Field>
        <Field label="Definition">
          <textarea
            className="textarea"
            aria-label="Playbook definition"
            value={definition}
            placeholder={'## Each run does\n- Inspect the queue\n- Capture decisions back into the playbook'}
            onChange={(e) => setDefinition(e.target.value)}
          />
        </Field>
      </div>
    </Modal>
  )
}

export function CreateKBModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated?: (filename: string) => void
}) {
  const [name, setName] = useState('')
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)

  const reset = () => {
    setName('')
    setBody('')
    setBusy(false)
  }

  const submit = async () => {
    const slug = slugify(name)
    if (!slug) {
      pushToast('error', 'A document name is required')
      return
    }
    const filename = `${slug}.md`
    setBusy(true)
    try {
      const resp = await apiAction({
        kind: 'create-kb',
        name: name.trim(),
        slug: filename,
        description: body,
      })
      pushToast('ok', resp.message || `created ${filename}`)
      await queryClient.invalidateQueries({ queryKey: ['kb'] })
      onClose()
      reset()
      onCreated?.(filename)
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : 'create failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New KB document"
      width={560}
      footer={
        <>
          <div className="spacer" />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy} onClick={submit}>
            {busy ? <Loader2 size={15} className="spin" /> : null}
            Create document
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 14 }}>
        <Field label="Document name">
          <input
            className="input"
            aria-label="Document name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="Body">
          <textarea
            className="textarea"
            aria-label="Document body"
            value={body}
            onChange={(e) => setBody(e.target.value)}
          />
        </Field>
      </div>
    </Modal>
  )
}

export function AddWorkdirModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [path, setPath] = useState('')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (!path.trim()) {
      pushToast('error', 'A directory is required')
      return
    }
    setBusy(true)
    try {
      const resp = await apiAction({ kind: 'workdir-add', path: path.trim(), name: name.trim() })
      pushToast('ok', resp.message || 'workdir registered')
      queryClient.invalidateQueries()
      onClose()
      setPath('')
      setName('')
    } catch (e) {
      pushToast('error', e instanceof Error ? e.message : 'could not register workdir')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Register workdir"
      footer={
        <>
          <div className="spacer" />
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy} onClick={submit}>
            {busy ? <Loader2 size={15} className="spin" /> : null}
            Register
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 14 }}>
        <Field label="Directory" hint="Browse to a folder, or create a new one inline.">
          <WorkdirPicker value={path} onChange={setPath} />
        </Field>
        <Field label="Name" hint="Optional — defaults to the folder name.">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
      </div>
    </Modal>
  )
}
