import { useEffect, useRef } from 'react'
import { Terminal as XTerm, type IDisposable, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import '@xterm/xterm/css/xterm.css'
import { pushToast } from '../lib/toast'

// Live PTY terminal powered by xterm.js, bound to flow's /ws/terminal JSON
// protocol: server pushes {type:"output"|"status"|"error"}, client sends
// {type:"input"|"resize"}. Sessions run inside tmux, so on (re)attach the
// server resizes the pane to our URL cols/rows and replays a fresh
// capture-pane of the full history — which is why we FIT BEFORE CONNECTING:
// connecting at the real measured size makes that replay render at the right
// width (no rewrap/overlap), and xterm's effectively-unlimited scrollback
// (TERMINAL_SCROLLBACK_LINES) keeps every replayed line so you can scroll to
// the very start of the session.
//
// This is a faithful port of flow's long-standing xterm integration (the one
// the user described as "worked solid"): scrollback cap, FitAddon +
// Unicode11Addon, OSC 52 → system clipboard, auto-copy on selection, a copy
// scroll-guard, custom wheel/key scrolling, and DA-response stripping on input.
// No wterm-era workarounds (sanitizeSGR / split-VT reassembly) are needed —
// xterm's own VT parser buffers partial escape sequences across writes.

// xterm caps scrollback at this many lines. The max 32-bit unsigned value is
// effectively "unlimited" for an interactive session and is what lets the
// browser scroll back to the first line of the replayed tmux history.
const TERMINAL_SCROLLBACK_LINES = 4294967295

// Re-fit at these millisecond offsets after mount / socket-open / font load.
// Layout (flex, fullscreen toggle, font swap) settles asynchronously; a single
// fit at t=0 measures a not-yet-final box. Fitting again at increasing delays
// converges on the correct grid without spamming resizes forever.
const TERMINAL_FIT_DELAYS_MS = [0, 40, 160, 420, 900]

// Device-Attributes responses the terminal *emits* in reply to DA queries
// (ESC [ ? … c / ESC [ > … c). If the host echoes them back to us as input we
// must drop them, or they get sent to the PTY as bogus keystrokes.
// eslint-disable-next-line no-control-regex
const TERMINAL_GENERATED_INPUT_RE = /\x1b\[(?:\?[0-9;]*|>[0-9;]*)c/g
const stripTerminalGeneratedInput = (data = ''): string => data.replace(TERMINAL_GENERATED_INPUT_RE, '')

const TERMINAL_FONT =
  "'JetBrains Mono', 'JetBrainsMono Nerd Font', 'FiraCode Nerd Font', ui-monospace, 'SF Mono', Menlo, Monaco, monospace"

// Pinned dark palette — the terminal stays dark in every app theme (IDE
// convention; a dark panel reads as intentional in light mode). Background
// matches .flow-term so there's no seam between chrome and grid.
const TERMINAL_THEME: ITheme = {
  background: '#0a0b0e',
  foreground: '#d7d4cd',
  cursor: '#5eead4',
  cursorAccent: '#0a0b0e',
  selectionBackground: '#3f3a87',
  black: '#0a0b0e',
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
}

interface Props {
  slug: string
  restartKey?: number
  onStatus?: (kind: 'status' | 'error' | 'closed' | 'open', message: string) => void
}

function termWsURL(slug: string, cols: number, rows: number): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}/ws/terminal?slug=${encodeURIComponent(
    slug,
  )}&cols=${cols}&rows=${rows}`
}

export function TaskTerminal({ slug, restartKey = 0, onStatus }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    host.innerHTML = ''

    const term = new XTerm({
      cols: 120,
      rows: 32,
      allowProposedApi: true, // required for unicode11 + parser.registerOscHandler
      allowTransparency: true,
      altClickMovesCursor: true,
      customGlyphs: true,
      cursorBlink: true,
      convertEol: false,
      drawBoldTextInBrightColors: true,
      fontFamily: TERMINAL_FONT,
      fontSize: 12.5,
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
      theme: TERMINAL_THEME,
    })

    const unicode = new Unicode11Addon()
    term.loadAddon(unicode)
    term.unicode.activeVersion = '11'
    const fit = new FitAddon()
    term.loadAddon(fit)

    let destroyed = false
    let ws: WebSocket | null = null
    let wsOpened = false
    let lastSize = ''

    const send = (obj: unknown) => {
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj))
    }

    // ---- OSC 52 → system clipboard -------------------------------------
    // tmux (set-clipboard on) emits OSC 52 with the selected text base64-encoded
    // when the user drag-selects inside a mouse-mode session. xterm doesn't
    // surface OSC 52 to the OS clipboard by default (security), so plug it in.
    // Payload is "<Pc>;<Pd>"; Pd="?" is a read query we can't honor without a
    // user gesture, so ignore it.
    const osc52: IDisposable | null = term.parser.registerOscHandler(52, (data) => {
      const semi = data.indexOf(';')
      if (semi < 0) return false
      const payload = data.slice(semi + 1)
      if (!payload || payload === '?') return false
      if (!navigator.clipboard?.writeText) return true
      let text: string
      try {
        text = atob(payload)
      } catch {
        return false
      }
      if (!text) return true
      navigator.clipboard
        .writeText(text)
        .then(() => pushToast('ok', 'copied to clipboard'))
        .catch(() => pushToast('error', 'clipboard copy failed'))
      return true
    })

    term.open(host)
    term.focus()

    // ---- custom wheel scrolling ----------------------------------------
    // When the inner TUI has mouse tracking on it owns the wheel (pass through);
    // otherwise we scroll the scrollback ourselves, handling line/page/pixel
    // delta modes and accumulating sub-line remainders for smooth scrolling.
    let wheelRemainder = 0
    const wheelLineHeight = (): number => {
      // _core is private; the rendered cell height is the most accurate value.
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const cell = (term as any)._core?._renderService?.dimensions?.css?.cell
      return (
        cell?.height ||
        Math.max(12, Math.round((term.options.fontSize || 13) * (term.options.lineHeight || 1.18))) ||
        16
      )
    }
    term.attachCustomWheelEventHandler((event) => {
      if (event.ctrlKey) return true
      const mouseMode = term.modes?.mouseTrackingMode ?? 'none'
      if (mouseMode !== 'none') return true
      const scale =
        event.deltaMode === 1
          ? 1
          : event.deltaMode === 2
            ? Math.max(1, term.rows - 2)
            : 1 / wheelLineHeight()
      wheelRemainder += event.deltaY * scale
      const lines = wheelRemainder > 0 ? Math.floor(wheelRemainder) : Math.ceil(wheelRemainder)
      if (lines !== 0) {
        term.scrollLines(lines)
        wheelRemainder -= lines
      }
      event.preventDefault()
      event.stopPropagation()
      return false
    })

    // ---- Shift + PageUp/PageDown/Home/End → scrollback nav -------------
    term.attachCustomKeyEventHandler((event) => {
      if (event.type !== 'keydown') return true
      if (!event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return true
      if (event.code === 'PageUp') term.scrollPages(-1)
      else if (event.code === 'PageDown') term.scrollPages(1)
      else if (event.code === 'Home') term.scrollToTop()
      else if (event.code === 'End') term.scrollToBottom()
      else return true
      event.preventDefault()
      return false
    })

    // ---- copy scroll-guard ---------------------------------------------
    // When scrolled up reading old output, starting a drag-select snaps the
    // viewport to the bottom (xterm's SelectionService resets _userScrolling on
    // mousedown). Snapshot the logical viewport on mousedown and, if it snaps to
    // the buffer base during the drag, restore it via scrollToLine (keeps
    // xterm's internal scroll state consistent). Poll on rAF rather than DOM
    // scroll events so it works regardless of the renderer's scroll path.
    let copyGuardCleanup: (() => void) | null = null
    const armCopyScrollGuard = () => {
      copyGuardCleanup?.()
      const buf = term.buffer.active
      const savedViewportY = buf.viewportY
      if (savedViewportY >= buf.baseY) return // already at the bottom — nothing to protect
      let restored = false
      let frameId = 0
      let disposeTimer = 0 as unknown as ReturnType<typeof setTimeout> | 0
      const tick = () => {
        if (restored || destroyed) return
        const b = term.buffer.active
        if (b.viewportY >= b.baseY && b.viewportY > savedViewportY + 1) {
          term.scrollToLine(savedViewportY)
          restored = true
          return
        }
        frameId = requestAnimationFrame(tick)
      }
      frameId = requestAnimationFrame(tick)
      const stop = () => {
        if (frameId) cancelAnimationFrame(frameId)
        if (disposeTimer) clearTimeout(disposeTimer as ReturnType<typeof setTimeout>)
        window.removeEventListener('mouseup', onMouseUp, true)
        copyGuardCleanup = null
      }
      // Keep the guard alive briefly past mouseup so post-drag clipboard / OSC 52
      // effects are still covered.
      const onMouseUp = () => {
        disposeTimer = setTimeout(stop, 400)
      }
      window.addEventListener('mouseup', onMouseUp, true)
      copyGuardCleanup = stop
    }
    const onHostMouseDown = (e: MouseEvent) => {
      if (e.button !== 0 && e.button !== 2) return
      armCopyScrollGuard()
    }
    host.addEventListener('mousedown', onHostMouseDown, true)

    // ---- auto-copy selection to clipboard ------------------------------
    let selectionCopyTimer = 0 as unknown as ReturnType<typeof setTimeout> | 0
    const flushSelectionCopy = () => {
      selectionCopyTimer = 0
      if (!term.hasSelection()) return
      const text = term.getSelection()
      if (!text || !text.trim()) return
      if (!navigator.clipboard?.writeText) return
      navigator.clipboard
        .writeText(text)
        .then(() => pushToast('ok', 'copied to clipboard'))
        .catch(() => pushToast('error', 'clipboard copy failed'))
    }
    const selectionDisposable = term.onSelectionChange(() => {
      if (selectionCopyTimer) clearTimeout(selectionCopyTimer as ReturnType<typeof setTimeout>)
      selectionCopyTimer = setTimeout(flushSelectionCopy, 120)
    })

    // ---- input → PTY ---------------------------------------------------
    const dataDisposable = term.onData((data) => {
      const input = stripTerminalGeneratedInput(data)
      if (input) send({ type: 'input', data: input })
    })

    // ---- fit + resize --------------------------------------------------
    const sendResize = () => {
      if (!ws || ws.readyState !== WebSocket.OPEN) return
      const key = `${term.cols}x${term.rows}`
      if (key === lastSize) return
      lastSize = key
      send({ type: 'resize', cols: term.cols, rows: term.rows })
    }
    const fitNow = () => {
      try {
        fit.fit()
      } catch {
        /* host not measurable yet */
      }
    }
    // term.onResize is the authoritative "grid dimensions changed" signal — fire
    // a resize to the PTY (deduped) whenever fit (or anything) changes the grid.
    const resizeDisposable = term.onResize(() => sendResize())

    let resizeFrame = 0
    const resize = () => {
      if (resizeFrame) cancelAnimationFrame(resizeFrame)
      resizeFrame = requestAnimationFrame(() => {
        resizeFrame = 0
        fitNow()
        term.refresh(0, Math.max(0, term.rows - 1))
        if (!wsOpened) openWS()
      })
    }

    // Connect the socket once, at the real fitted size. If the host isn't laid
    // out yet (fit yields a degenerate grid), bail — the ResizeObserver's first
    // real callback re-enters here with a valid size.
    const openWS = () => {
      if (wsOpened || destroyed) return
      fitNow()
      if (term.cols < 2 || term.rows < 2) return
      wsOpened = true
      lastSize = `${term.cols}x${term.rows}`
      const sock = new WebSocket(termWsURL(slug, term.cols, term.rows))
      ws = sock
      sock.onopen = () => {
        onStatus?.('open', 'connected')
        scheduleFits()
      }
      sock.onmessage = (ev) => {
        let m: { type: string; data?: string; message?: string }
        try {
          m = JSON.parse(ev.data as string)
        } catch {
          return
        }
        if (m.type === 'output' && m.data != null) term.write(m.data)
        else if (m.type === 'status') onStatus?.('status', m.message ?? '')
        else if (m.type === 'error') onStatus?.('error', m.message ?? 'terminal error')
      }
      sock.onclose = () => {
        if (!destroyed) onStatus?.('closed', 'terminal disconnected')
      }
      sock.onerror = () => {
        if (!destroyed) onStatus?.('error', 'connection error')
      }
    }

    const fitTimers: ReturnType<typeof setTimeout>[] = []
    const scheduleFits = () => {
      for (const delay of TERMINAL_FIT_DELAYS_MS) fitTimers.push(setTimeout(resize, delay))
    }

    const observer = new ResizeObserver(resize)
    observer.observe(host)
    window.addEventListener('resize', resize)
    scheduleFits()
    document.fonts?.ready?.then(() => {
      if (destroyed) return
      fitNow()
      scheduleFits()
    })

    // Initial connect at the fitted size.
    openWS()

    const focusTimer = setTimeout(() => {
      if (!destroyed) term.focus()
    }, 120)

    return () => {
      destroyed = true
      clearTimeout(focusTimer)
      fitTimers.forEach(clearTimeout)
      if (resizeFrame) cancelAnimationFrame(resizeFrame)
      if (selectionCopyTimer) clearTimeout(selectionCopyTimer as ReturnType<typeof setTimeout>)
      copyGuardCleanup?.()
      observer.disconnect()
      window.removeEventListener('resize', resize)
      host.removeEventListener('mousedown', onHostMouseDown, true)
      dataDisposable.dispose()
      resizeDisposable.dispose()
      selectionDisposable.dispose()
      osc52?.dispose()
      try {
        ws?.close()
      } catch {
        /* noop */
      }
      ws = null
      term.dispose()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug, restartKey])

  return (
    <div className="flow-term">
      <div className="xterm-host" ref={hostRef} />
    </div>
  )
}
