import { useEffect, useRef, useState, type CSSProperties, type PointerEvent } from 'react'
import { Minus, X } from 'lucide-react'
import { TaskTerminal } from './Terminal'
import { ProviderIcon } from './ui'

export interface FloatingTerminalDescriptor {
  id: string
  provider: string
  title: string
}

interface Props {
  terminal: FloatingTerminalDescriptor
  onClose: () => void
}

function initialPosition(): { x: number; y: number } {
  if (typeof window === 'undefined') return { x: 48, y: 96 }
  const width = Math.min(720, window.innerWidth - 24)
  return {
    x: Math.max(12, window.innerWidth - width - 24),
    y: Math.max(72, Math.min(112, window.innerHeight - 480)),
  }
}

function clampPosition(x: number, y: number): { x: number; y: number } {
  if (typeof window === 'undefined') return { x, y }
  const maxX = Math.max(12, window.innerWidth - 140)
  const maxY = Math.max(12, window.innerHeight - 72)
  return {
    x: Math.min(Math.max(12, x), maxX),
    y: Math.min(Math.max(12, y), maxY),
  }
}

export function FloatingTerminalWindow({ terminal, onClose }: Props) {
  const [pos, setPos] = useState(initialPosition)
  const [minimized, setMinimized] = useState(false)
  const [status, setStatus] = useState('connecting')
  const dragRef = useRef<{ dx: number; dy: number } | null>(null)

  useEffect(() => {
    setPos(initialPosition())
    setMinimized(false)
    setStatus('connecting')
  }, [terminal.id])

  useEffect(() => {
    const onResize = () => setPos((p) => clampPosition(p.x, p.y))
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const onDragStart = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    dragRef.current = { dx: e.clientX - pos.x, dy: e.clientY - pos.y }
    e.currentTarget.setPointerCapture(e.pointerId)
  }

  const onDragMove = (e: PointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current
    if (!drag) return
    setPos(clampPosition(e.clientX - drag.dx, e.clientY - drag.dy))
  }

  const onDragEnd = (e: PointerEvent<HTMLDivElement>) => {
    dragRef.current = null
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId)
    }
  }

  const style = { left: pos.x, top: pos.y } as CSSProperties

  return (
    <div className={`floating-terminal${minimized ? ' minimized' : ''}`} style={style}>
      <div
        className="floating-terminal-head"
        onPointerDown={onDragStart}
        onPointerMove={onDragMove}
        onPointerUp={onDragEnd}
        onPointerCancel={onDragEnd}
      >
        <div className="floating-terminal-title">
          <span className={`dot ${status === 'connected' ? 'running' : 'idle'}`} />
          <span className="clip">{terminal.title || 'Ask Flow'}</span>
        </div>
        <div className="floating-terminal-meta">
          <span className="provider-chip">
            <ProviderIcon provider={terminal.provider} size={13} />
            {terminal.provider}
          </span>
          <span className="mono">{status}</span>
        </div>
        <button
          type="button"
          className="btn icon sm"
          title={minimized ? 'Restore terminal' : 'Minimize terminal'}
          onClick={(e) => {
            e.stopPropagation()
            setMinimized((v) => !v)
          }}
        >
          <Minus size={15} />
        </button>
        <button
          type="button"
          className="btn icon sm"
          title="Close terminal"
          onClick={(e) => {
            e.stopPropagation()
            onClose()
          }}
        >
          <X size={15} />
        </button>
      </div>
      {!minimized && (
        <div className="floating-terminal-body">
          <TaskTerminal
            slug={terminal.id}
            kind="floating"
            onStatus={(kind, message) => setStatus(kind === 'open' ? 'connected' : message || kind)}
          />
        </div>
      )}
    </div>
  )
}
