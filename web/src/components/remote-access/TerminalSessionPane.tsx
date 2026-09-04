import React, { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { ClipboardPaste, Copy, CornerDownLeft, Square } from 'lucide-react'
import {
  createCompositionGuard,
  exceedsLongPressMove,
  longPressDelayMS,
  noteCompositionEnd,
  noteCompositionStart,
  shouldDropComposedDuplicate,
} from './terminal-input'

export type RequestFn = (path: string, init?: RequestInit) => Promise<any>
export type TerminalMode = 'login' | 'minimal'

export type TerminalInfo = {
  username?: string
  uid?: number
  gid?: number
  home?: string
  shell?: string
  mode?: string
  cwd?: string
  term?: string
  system_environment_loaded?: boolean
  terminal_environment_loaded?: boolean
}

export type TerminalPhase = 'authorizing' | 'connecting' | 'waiting' | 'ready' | 'closed' | 'failed'

export type TerminalPaneState = {
  phase: TerminalPhase
  status: string
  mode: TerminalMode
  loginEnv: boolean
  info: TerminalInfo | null
  commandSent: boolean
}

export type TerminalPaneHandle = {
  focus: () => void
  fit: () => void
  clear: () => void
  interrupt: () => void
  copySelection: () => Promise<void>
  paste: () => Promise<void>
  reconnect: (mode?: TerminalMode) => void
  release: () => Promise<void>
  // Runs a command in an already connected session. Returns false when the socket is not open.
  runCommand: (command: string) => boolean
}

const closedReasonLabels: Record<string, string> = {
  peer_closed: '对端已关闭',
  agent_cleanup: '节点已结束会话',
  user_close: '已关闭',
  oversized_frame: '数据过大，连接已断开',
  slow_consumer: '发送过慢，连接已断开',
  pty_start_failed: '节点无法启动终端',
  interactive_url_failed: '节点终端地址无效',
  websocket_dial_failed: '节点无法连接终端通道',
  session_limit: '节点终端数量已达上限',
  prepare_timeout: '等待节点启动超时',
  prepare_invalid: '终端会话无效',
  agent_local_gate_denied: '节点本地安全锁拒绝终端',
  controller_close: '会话已关闭',
  controller_disconnect: '节点连接已断开',
  shell_exited: 'Shell 已退出',
  login_shell_disabled: '该账号已禁用终端登录',
  login_shell_missing: '登录 Shell 不存在',
}

export function terminalStatusLabel(reason: string, detail?: string) {
  const trimmed = reason.trim()
  const label = closedReasonLabels[trimmed] || trimmed
  const extra = String(detail || '').trim()
  if (!extra || extra === label) return label
  return `${label}：${extra.slice(0, 200)}`
}

export function modeLabel(mode?: string) {
  return mode === 'minimal' ? 'Minimal' : 'Login'
}

const TERMINAL_THEME_DARK = {
  background: '#0b1220',
  foreground: '#e5eefc',
  cursor: '#e5eefc',
  cursorAccent: '#0b1220',
  selectionBackground: '#386ea8',
  selectionInactiveBackground: '#243044',
  black: '#0b1220',
  red: '#f87171',
  green: '#34d399',
  yellow: '#fbbf24',
  blue: '#60a5fa',
  magenta: '#c084fc',
  cyan: '#38bdf8',
  white: '#e5eefc',
  brightBlack: '#64748b',
  brightRed: '#fca5a5',
  brightGreen: '#6ee7b7',
  brightYellow: '#fde047',
  brightBlue: '#93c5fd',
  brightMagenta: '#d8b4fe',
  brightCyan: '#7dd3fc',
  brightWhite: '#ffffff',
}

const TERMINAL_THEME_LIGHT = {
  background: '#ffffff',
  foreground: '#0f172a',
  cursor: '#0f172a',
  cursorAccent: '#ffffff',
  selectionBackground: '#b4d5fe',
  selectionInactiveBackground: '#e2e8f0',
  black: '#0f172a',
  red: '#dc2626',
  green: '#16a34a',
  yellow: '#d97706',
  blue: '#2563eb',
  magenta: '#9333ea',
  cyan: '#0891b2',
  white: '#f8fafc',
  brightBlack: '#64748b',
  brightRed: '#ef4444',
  brightGreen: '#22c55e',
  brightYellow: '#eab308',
  brightBlue: '#3b82f6',
  brightMagenta: '#a855f7',
  brightCyan: '#06b6d4',
  brightWhite: '#ffffff',
}

function isDocumentDark(): boolean {
  if (typeof document === 'undefined') return true
  return document.documentElement.dataset.theme === 'dark' || document.documentElement.classList.contains('dark')
}

type PaneProps = {
  serverId: number
  client: { request: RequestFn }
  websocketURL: (serverId: number, sessionId: string) => string
  // Resolves a step-up token for this server, or '' when confirmation is not required.
  // Rejecting cancels the connection attempt without opening a session.
  acquireToken: (serverId: number) => Promise<string>
  active: boolean
  autoCommand?: string
  onState: (state: TerminalPaneState) => void
}

export const TerminalSessionPane = forwardRef<TerminalPaneHandle, PaneProps>(function TerminalSessionPane(
  { serverId, client, websocketURL, acquireToken, active, autoCommand, onState },
  ref,
) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const dataDisposableRef = useRef<{ dispose: () => void } | null>(null)
  const sessionRef = useRef('')
  const modeRef = useRef<TerminalMode>('login')
  const resizeTimer = useRef<number>(0)
  const connectGen = useRef(0)
  const openedRef = useRef(false)
  const readyRef = useRef(false)
  const commandSentRef = useRef(false)
  const compositionRef = useRef(createCompositionGuard())
  const longPressRef = useRef({ timer: 0, start: { x: 0, y: 0 } })
  const stateRef = useRef<TerminalPaneState>({
    phase: 'connecting', status: '正在连接', mode: 'login', loginEnv: false, info: null, commandSent: false,
  })
  const onStateRef = useRef(onState)
  onStateRef.current = onState

  const [termReady, setTermReady] = useState(false)
  const [touchMenu, setTouchMenu] = useState<{ x: number; y: number } | null>(null)
  const [manualPaste, setManualPaste] = useState<string | null>(null)

  const publish = (patch: Partial<TerminalPaneState>) => {
    stateRef.current = { ...stateRef.current, ...patch }
    onStateRef.current(stateRef.current)
  }

  const send = (data: string) => {
    const socket = socketRef.current
    if (socket?.readyState === WebSocket.OPEN) socket.send(new TextEncoder().encode(data))
  }

  // Background sessions are hidden, so their host has no layout box. FitAddon would happily
  // propose its 2x1 floor for a zero-size parent and resize the remote PTY down to nothing, so
  // a pane only ever fits while it is actually laid out.
  const fitAndResize = () => {
    const host = hostRef.current
    if (!host || !host.clientWidth || !host.clientHeight) return
    fitRef.current?.fit()
    const term = termRef.current
    const socket = socketRef.current
    if (!term || !socket || socket.readyState !== WebSocket.OPEN) return
    socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
  }

  const detachSocket = () => {
    dataDisposableRef.current?.dispose()
    dataDisposableRef.current = null
    const socket = socketRef.current
    socketRef.current = null
    if (!socket) return
    socket.onopen = null
    socket.onmessage = null
    socket.onclose = null
    socket.onerror = null
    if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) socket.close()
  }

  const closeControllerSession = async (sessionId: string) => {
    if (!sessionId) return
    try {
      await client.request(`/servers/${serverId}/terminal/sessions/${sessionId}`, { method: 'DELETE' })
    } catch {
      // session may already be gone
    }
  }

  const connect = async (nextMode: TerminalMode = modeRef.current) => {
    const gen = ++connectGen.current
    const previousSession = sessionRef.current
    sessionRef.current = ''
    modeRef.current = nextMode
    detachSocket()
    if (previousSession) void closeControllerSession(previousSession)
    openedRef.current = false
    readyRef.current = false
    commandSentRef.current = false
    publish({ phase: 'authorizing', status: '等待认证', mode: nextMode, info: null, commandSent: false })

    let stepUpToken = ''
    try {
      stepUpToken = await acquireToken(serverId)
    } catch (error: any) {
      if (gen !== connectGen.current) return
      publish({ phase: 'failed', status: String(error?.message || '未完成认证') })
      return
    }
    if (gen !== connectGen.current) return
    publish({ phase: 'connecting', status: '正在连接' })

    try {
      const term = termRef.current
      const created = await client.request(`/servers/${serverId}/terminal/sessions`, {
        method: 'POST',
        body: JSON.stringify({
          step_up_token: stepUpToken,
          cols: term?.cols || 120,
          rows: term?.rows || 32,
          mode: nextMode,
        }),
      }) as { session_id?: string; login_env?: boolean; mode?: string }
      if (gen !== connectGen.current) {
        if (created?.session_id) void closeControllerSession(created.session_id)
        return
      }
      if (!created?.session_id) {
        publish({ phase: 'failed', status: '创建会话失败' })
        return
      }
      publish({ loginEnv: Boolean(created.login_env) })
      sessionRef.current = created.session_id
      const socket = new WebSocket(websocketURL(serverId, created.session_id))
      socket.binaryType = 'arraybuffer'
      socketRef.current = socket
      socket.onopen = () => {
        if (gen !== connectGen.current) return
        openedRef.current = true
        publish({ phase: 'waiting', status: '正在等待节点' })
      }
      socket.onmessage = event => {
        if (gen !== connectGen.current) return
        if (typeof event.data === 'string') {
          try {
            const message = JSON.parse(event.data)
            if (message.type === 'ready') {
              readyRef.current = true
              publish({
                phase: 'ready',
                status: '已连接',
                info: message.info && typeof message.info === 'object' ? message.info : null,
              })
              fitAndResize()
              const command = String(autoCommand || '').trim()
              if (command && !commandSentRef.current) {
                commandSentRef.current = true
                send(`${command}\r`)
                publish({ commandSent: true })
              }
              return
            }
            if (message.type === 'closed' || message.type === 'error') {
              publish({
                phase: message.type === 'error' ? 'failed' : 'closed',
                status: terminalStatusLabel(String(message.reason || message.type), message.detail),
              })
            }
          } catch {
            termRef.current?.write(event.data)
          }
          return
        }
        const bytes = event.data instanceof ArrayBuffer ? new Uint8Array(event.data) : event.data
        termRef.current?.write(bytes)
      }
      socket.onclose = () => {
        if (gen !== connectGen.current) return
        const phase = stateRef.current.phase
        if (phase !== 'connecting' && phase !== 'waiting' && phase !== 'ready') return
        if (readyRef.current) publish({ phase: 'closed', status: '已断开' })
        else if (openedRef.current) publish({ phase: 'failed', status: '节点未就绪' })
        else publish({ phase: 'failed', status: '连接失败' })
      }
      dataDisposableRef.current?.dispose()
      if (term) {
        dataDisposableRef.current = term.onData(data => {
          if (shouldDropComposedDuplicate(compositionRef.current, data, Date.now())) return
          send(data)
        })
      }
    } catch (error: any) {
      if (gen !== connectGen.current) return
      publish({ phase: 'failed', status: String(error?.message || error || '连接失败') })
    }
  }

  useEffect(() => {
    if (!hostRef.current || termRef.current) return
    const host = hostRef.current
    const initialDark = isDocumentDark()
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      fontSize: 13,
      theme: initialDark ? TERMINAL_THEME_DARK : TERMINAL_THEME_LIGHT,
      allowProposedApi: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    termRef.current = term
    fitRef.current = fit

    const themeObserver = typeof MutationObserver !== 'undefined' && typeof document !== 'undefined'
      ? new MutationObserver(() => {
          const dark = isDocumentDark()
          if ((term as any).options) {
            ;(term as any).options.theme = dark ? TERMINAL_THEME_DARK : TERMINAL_THEME_LIGHT
          }
        })
      : null
    if (themeObserver && typeof document !== 'undefined') {
      themeObserver.observe(document.documentElement, {
        attributes: true,
        attributeFilter: ['data-theme', 'class'],
      })
    }

    const textarea = term.textarea
    const guard = compositionRef.current
    const handleCompositionStart = () => noteCompositionStart(guard)
    const handleCompositionEnd = (event: Event) => {
      noteCompositionEnd(guard, String((event as CompositionEvent).data || ''), Date.now())
    }
    textarea?.addEventListener('compositionstart', handleCompositionStart)
    textarea?.addEventListener('compositionend', handleCompositionEnd)

    const scheduleFit = () => {
      window.clearTimeout(resizeTimer.current)
      resizeTimer.current = window.setTimeout(() => fitAndResize(), 50)
    }
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(scheduleFit)
    observer?.observe(host)
    window.addEventListener('resize', scheduleFit)
    scheduleFit()
    setTermReady(true)
    return () => {
      themeObserver?.disconnect()
      textarea?.removeEventListener('compositionstart', handleCompositionStart)
      textarea?.removeEventListener('compositionend', handleCompositionEnd)
      observer?.disconnect()
      window.removeEventListener('resize', scheduleFit)
      window.clearTimeout(resizeTimer.current)
      window.clearTimeout(longPressRef.current.timer)
      dataDisposableRef.current?.dispose()
      dataDisposableRef.current = null
      detachSocket()
      term.dispose()
      termRef.current = null
      fitRef.current = null
      setTermReady(false)
    }
  }, [])

  useEffect(() => {
    if (!termReady) return
    void connect()
    return () => {
      connectGen.current += 1
    }
  }, [termReady])

  useEffect(() => {
    if (!active) return
    const frame = window.requestAnimationFrame(() => {
      fitAndResize()
      termRef.current?.focus()
    })
    return () => window.cancelAnimationFrame(frame)
  }, [active])

  const copySelection = async () => {
    const text = termRef.current?.getSelection() || ''
    if (text && navigator.clipboard?.writeText) await navigator.clipboard.writeText(text)
  }

  // Reading the clipboard needs a secure context. Panels served over plain HTTP fall back to a
  // small field the operator can paste into with the native long-press menu.
  const paste = async () => {
    try {
      if (!navigator.clipboard?.readText) throw new Error('clipboard unavailable')
      const text = await navigator.clipboard.readText()
      if (text) send(text)
    } catch {
      setManualPaste('')
    }
  }

  const reconnect = (nextMode: TerminalMode = modeRef.current) => {
    void connect(nextMode)
  }

  const release = async () => {
    connectGen.current += 1
    const sessionId = sessionRef.current
    sessionRef.current = ''
    detachSocket()
    await closeControllerSession(sessionId)
  }

  useImperativeHandle(ref, () => ({
    focus: () => termRef.current?.focus(),
    fit: () => fitAndResize(),
    clear: () => termRef.current?.clear(),
    interrupt: () => {
      const socket = socketRef.current
      if (socket?.readyState === WebSocket.OPEN) socket.send(new Uint8Array([3]))
    },
    copySelection,
    paste,
    reconnect,
    release,
    runCommand: (command: string) => {
      const trimmed = String(command || '').trim()
      if (!trimmed || socketRef.current?.readyState !== WebSocket.OPEN) return false
      send(`${trimmed}\r`)
      commandSentRef.current = true
      publish({ commandSent: true })
      return true
    },
  }))

  const cancelLongPress = () => {
    window.clearTimeout(longPressRef.current.timer)
    longPressRef.current.timer = 0
  }

  const startLongPress = (event: React.TouchEvent<HTMLDivElement>) => {
    if (event.touches.length !== 1) {
      cancelLongPress()
      return
    }
    const touch = event.touches[0]
    const host = hostRef.current?.getBoundingClientRect()
    longPressRef.current.start = { x: touch.clientX, y: touch.clientY }
    cancelLongPress()
    longPressRef.current.timer = window.setTimeout(() => {
      setTouchMenu({
        x: Math.max(8, touch.clientX - (host?.left || 0)),
        y: Math.max(8, touch.clientY - (host?.top || 0)),
      })
    }, longPressDelayMS)
  }

  const trackLongPress = (event: React.TouchEvent<HTMLDivElement>) => {
    const touch = event.touches[0]
    if (!touch) return
    if (exceedsLongPressMove(longPressRef.current.start, { x: touch.clientX, y: touch.clientY })) cancelLongPress()
  }

  return (
    <div className="terminal-pane" data-active={active ? 'true' : 'false'}>
      <div
        ref={hostRef}
        className="remote-terminal-host terminal-pane-host"
        onTouchStart={startLongPress}
        onTouchMove={trackLongPress}
        onTouchEnd={cancelLongPress}
        onTouchCancel={cancelLongPress}
      />
      {touchMenu ? (
        <>
          <div className="terminal-touch-backdrop" onClick={() => setTouchMenu(null)} onTouchStart={() => setTouchMenu(null)} />
          <div className="terminal-touch-menu" role="menu" style={{ left: touchMenu.x, top: touchMenu.y }}>
            <button type="button" role="menuitem" onClick={() => { setTouchMenu(null); void paste() }}>
              <ClipboardPaste size={14} aria-hidden="true" /><span>粘贴</span>
            </button>
            <button type="button" role="menuitem" onClick={() => { setTouchMenu(null); void copySelection() }}>
              <Copy size={14} aria-hidden="true" /><span>复制选中</span>
            </button>
            <button type="button" role="menuitem" onClick={() => { setTouchMenu(null); send('\r') }}>
              <CornerDownLeft size={14} aria-hidden="true" /><span>回车</span>
            </button>
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setTouchMenu(null)
                const socket = socketRef.current
                if (socket?.readyState === WebSocket.OPEN) socket.send(new Uint8Array([3]))
              }}
            >
              <Square size={14} aria-hidden="true" /><span>发送 Ctrl+C</span>
            </button>
          </div>
        </>
      ) : null}
      {manualPaste !== null ? (
        <div className="terminal-manual-paste" role="group" aria-label="手动粘贴">
          <label htmlFor={`terminal-manual-paste-${serverId}`}>无法读取剪贴板，请在此长按粘贴后发送</label>
          <textarea
            id={`terminal-manual-paste-${serverId}`}
            autoFocus
            rows={2}
            value={manualPaste}
            onChange={event => setManualPaste(event.target.value)}
          />
          <div className="terminal-manual-paste-actions">
            <button type="button" className="ghost" onClick={() => setManualPaste(null)}>取消</button>
            <button
              type="button"
              onClick={() => {
                if (manualPaste) send(manualPaste)
                setManualPaste(null)
                termRef.current?.focus()
              }}
            >发送</button>
          </div>
        </div>
      ) : null}
    </div>
  )
})
