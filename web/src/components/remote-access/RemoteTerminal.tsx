import React, { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { Copy, ClipboardPaste, Maximize2, Minimize2, RefreshCw, Trash2, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import { StepUpAuth } from './StepUpAuth'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

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
}

const waitingStatuses = new Set(['正在连接', '正在等待节点', '已连接'])

function terminalStatusLabel(reason: string, detail?: string) {
  const trimmed = reason.trim()
  const label = closedReasonLabels[trimmed] || trimmed
  const extra = String(detail || '').trim()
  if (!extra || extra === label) return label
  return `${label}：${extra.slice(0, 200)}`
}

export function RemoteTerminal({
  serverId,
  serverName,
  client,
  websocketURL,
  passwordConfirmationRequired,
  onClose,
}: {
  serverId: number
  serverName: string
  client: { request: RequestFn }
  websocketURL: (sessionId: string) => string
  passwordConfirmationRequired: boolean
  onClose: () => void
}) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const dataDisposableRef = useRef<{ dispose: () => void } | null>(null)
  const sessionRef = useRef('')
  const resizeTimer = useRef<number>(0)
  const connectGen = useRef(0)
  const openedRef = useRef(false)
  const readyRef = useRef(false)
  const [termReady, setTermReady] = useState(false)
  const [stepUp, setStepUp] = useState(passwordConfirmationRequired)
  const [fullscreen, setFullscreen] = useState(false)
  const [status, setStatus] = useState(passwordConfirmationRequired ? '等待认证' : '正在连接')

  const fitAndResize = () => {
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

  const connect = async (stepUpToken: string) => {
    const gen = ++connectGen.current
    const previousSession = sessionRef.current
    sessionRef.current = ''
    detachSocket()
    if (previousSession) void closeControllerSession(previousSession)
    setStepUp(false)
    setStatus('正在连接')
    openedRef.current = false
    readyRef.current = false
    try {
      const term = termRef.current
      const cols = term?.cols || 120
      const rows = term?.rows || 32
      const created = await client.request(`/servers/${serverId}/terminal/sessions`, {
        method: 'POST',
        body: JSON.stringify({ step_up_token: stepUpToken, cols, rows }),
      }) as { session_id?: string }
      if (gen !== connectGen.current) {
        if (created?.session_id) void closeControllerSession(created.session_id)
        return
      }
      if (!created?.session_id) {
        setStatus('创建会话失败')
        return
      }
      sessionRef.current = created.session_id
      const socket = new WebSocket(websocketURL(created.session_id))
      socket.binaryType = 'arraybuffer'
      socketRef.current = socket
      socket.onopen = () => {
        if (gen !== connectGen.current) return
        openedRef.current = true
        setStatus('正在等待节点')
      }
      socket.onmessage = event => {
        if (gen !== connectGen.current) return
        if (typeof event.data === 'string') {
          try {
            const message = JSON.parse(event.data)
            if (message.type === 'ready') {
              readyRef.current = true
              setStatus('已连接')
              fitAndResize()
              return
            }
            if (message.type === 'closed' || message.type === 'error') {
              setStatus(terminalStatusLabel(String(message.reason || message.type), message.detail))
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
        setStatus(current => {
          if (!waitingStatuses.has(current)) return current
          if (readyRef.current) return '已断开'
          if (openedRef.current) return '节点未就绪'
          return '连接失败'
        })
      }
      dataDisposableRef.current?.dispose()
      if (term) {
        dataDisposableRef.current = term.onData(data => {
          if (socket.readyState === WebSocket.OPEN) socket.send(new TextEncoder().encode(data))
        })
      }
    } catch (error: any) {
      if (gen !== connectGen.current) return
      setStatus(String(error?.message || error || '连接失败'))
    }
  }

  useEffect(() => {
    if (passwordConfirmationRequired || !termReady) return
    void connect('')
    return () => {
      connectGen.current += 1
    }
  }, [passwordConfirmationRequired, termReady])

  useEffect(() => {
    if (!hostRef.current || termRef.current) return
    const host = hostRef.current
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      fontSize: 13,
      theme: { background: '#0b1220', foreground: '#e5eefc' },
      allowProposedApi: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    termRef.current = term
    fitRef.current = fit
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
      observer?.disconnect()
      window.removeEventListener('resize', scheduleFit)
      window.clearTimeout(resizeTimer.current)
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
    const frame = window.requestAnimationFrame(() => fitAndResize())
    return () => window.cancelAnimationFrame(frame)
  }, [fullscreen])

  const copySelection = async () => {
    const text = termRef.current?.getSelection() || ''
    if (text) await navigator.clipboard.writeText(text)
  }

  const pasteClipboard = async () => {
    const text = await navigator.clipboard.readText()
    if (text && socketRef.current?.readyState === WebSocket.OPEN) {
      socketRef.current.send(new TextEncoder().encode(text))
    }
  }

  const sendInterrupt = () => {
    if (socketRef.current?.readyState === WebSocket.OPEN) {
      socketRef.current.send(new Uint8Array([3]))
    }
  }

  const clearScreen = () => termRef.current?.clear()

  const reconnect = () => {
    if (passwordConfirmationRequired) {
      connectGen.current += 1
      detachSocket()
      setStepUp(true)
      setStatus('等待认证')
      return
    }
    void connect('')
  }

  const closeSession = async () => {
    connectGen.current += 1
    const sessionId = sessionRef.current
    sessionRef.current = ''
    detachSocket()
    await closeControllerSession(sessionId)
    onClose()
  }

  return (
    <>
      <MotionDialogPanel onCancel={() => void closeSession()} className={`remote-terminal-dialog${fullscreen ? ' remote-terminal-fullscreen' : ''}`}>
        <header className="dialog-head">
          <div className="remote-terminal-title">
            <h2>远程终端 · {serverName}</h2>
            <p className="muted">{status}</p>
          </div>
          <div className="dialog-head-actions">
            <button type="button" className="ghost icon-button" onClick={() => void copySelection()} aria-label="复制" title="复制"><Copy size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={() => void pasteClipboard()} aria-label="粘贴" title="粘贴"><ClipboardPaste size={15} /></button>
            <button type="button" className="ghost remote-terminal-shortcut" onClick={sendInterrupt} aria-label="发送 Ctrl+C" title="发送 Ctrl+C">Ctrl+C</button>
            <button type="button" className="ghost icon-button" onClick={clearScreen} aria-label="清屏" title="清屏"><Trash2 size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={reconnect} aria-label="重连" title="重连"><RefreshCw size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={() => setFullscreen(current => !current)} aria-label={fullscreen ? '退出全屏' : '全屏'} title={fullscreen ? '退出全屏' : '全屏'}>
              {fullscreen ? <Minimize2 size={15} /> : <Maximize2 size={15} />}
            </button>
            <button type="button" className="ghost icon-button" onClick={() => void closeSession()} aria-label="关闭" title="关闭"><X size={15} /></button>
          </div>
        </header>
        <div className="dialog-body remote-terminal-body">
          <div ref={hostRef} className="remote-terminal-host" />
        </div>
      </MotionDialogPanel>
      {stepUp ? (
        <StepUpAuth
          request={client.request}
          purpose="remote_terminal"
          resourceType="server"
          resourceId={serverId}
          autoStartPasskey
          title="打开远程终端"
          warning="确认后即可打开这台服务器的 WebSSH。"
          onComplete={token => { void connect(token) }}
          onCancel={onClose}
        />
      ) : null}
    </>
  )
}
