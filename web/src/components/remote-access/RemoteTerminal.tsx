import React, { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { Copy, ClipboardPaste, Maximize2, Minimize2, RefreshCw, Trash2, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import { StepUpAuth } from './StepUpAuth'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

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
  const sessionRef = useRef('')
  const resizeTimer = useRef<number>(0)
  const autoConnectStarted = useRef(false)
  const [stepUp, setStepUp] = useState(passwordConfirmationRequired)
  const [fullscreen, setFullscreen] = useState(false)
  const [status, setStatus] = useState(passwordConfirmationRequired ? '等待认证' : '正在连接')

  const sendResize = () => {
    const term = termRef.current
    const socket = socketRef.current
    if (!term || !socket || socket.readyState !== WebSocket.OPEN) return
    socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
  }

  const connect = async (stepUpToken: string) => {
    setStepUp(false)
    setStatus('正在连接')
    const term = termRef.current
    const cols = term?.cols || 120
    const rows = term?.rows || 32
    const created = await client.request(`/servers/${serverId}/terminal/sessions`, {
      method: 'POST',
      body: JSON.stringify({ step_up_token: stepUpToken, cols, rows }),
    }) as { session_id: string }
    sessionRef.current = created.session_id
    const socket = new WebSocket(websocketURL(created.session_id))
    socket.binaryType = 'arraybuffer'
    socketRef.current = socket
    socket.onopen = () => setStatus('已连接')
    socket.onmessage = event => {
      if (typeof event.data === 'string') {
        try {
          const message = JSON.parse(event.data)
          if (message.type === 'closed' || message.type === 'error') {
            setStatus(message.reason || message.type)
          }
        } catch {
          termRef.current?.write(event.data)
        }
        return
      }
      const bytes = event.data instanceof ArrayBuffer ? new Uint8Array(event.data) : event.data
      termRef.current?.write(bytes)
    }
    socket.onclose = () => setStatus('已断开')
    if (term) {
      term.onData(data => {
        if (socket.readyState === WebSocket.OPEN) socket.send(new TextEncoder().encode(data))
      })
    }
  }

  useEffect(() => {
    if (passwordConfirmationRequired || autoConnectStarted.current) return
    autoConnectStarted.current = true
    void connect('')
  }, [passwordConfirmationRequired])

  useEffect(() => {
    if (!hostRef.current || termRef.current) return
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      fontSize: 13,
      theme: { background: '#0b1220', foreground: '#e5eefc' },
      allowProposedApi: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(hostRef.current)
    fit.fit()
    termRef.current = term
    fitRef.current = fit
    const onResize = () => {
      window.clearTimeout(resizeTimer.current)
      resizeTimer.current = window.setTimeout(() => {
        fit.fit()
        sendResize()
      }, 100)
    }
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
      window.clearTimeout(resizeTimer.current)
      socketRef.current?.close()
      term.dispose()
      termRef.current = null
    }
  }, [])

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
    socketRef.current?.close()
    if (passwordConfirmationRequired) {
      setStepUp(true)
      setStatus('等待认证')
      return
    }
    void connect('')
  }

  const closeSession = async () => {
    if (sessionRef.current) {
      try {
        await client.request(`/servers/${serverId}/terminal/sessions/${sessionRef.current}`, { method: 'DELETE' })
      } catch {
        // session may already be gone
      }
    }
    socketRef.current?.close()
    onClose()
  }

  return (
    <>
      <MotionDialogPanel onCancel={() => void closeSession()} className={`server-detail-dialog${fullscreen ? ' remote-terminal-fullscreen' : ''}`}>
        <header className="dialog-head">
          <div>
            <h2>远程终端 · {serverName}</h2>
            <p className="muted">{status}</p>
          </div>
          <div className="dialog-head-actions">
            <button type="button" className="ghost icon-button" onClick={() => void copySelection()} aria-label="复制" title="复制"><Copy size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={() => void pasteClipboard()} aria-label="粘贴" title="粘贴"><ClipboardPaste size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={sendInterrupt} aria-label="发送 Ctrl+C" title="Ctrl+C">Ctrl+C</button>
            <button type="button" className="ghost icon-button" onClick={clearScreen} aria-label="清屏" title="清屏"><Trash2 size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={reconnect} aria-label="重连" title="重连"><RefreshCw size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={() => setFullscreen(current => !current)} aria-label={fullscreen ? '退出全屏' : '全屏'} title={fullscreen ? '退出全屏' : '全屏'}>
              {fullscreen ? <Minimize2 size={15} /> : <Maximize2 size={15} />}
            </button>
            <button type="button" className="ghost icon-button" onClick={() => void closeSession()} aria-label="关闭" title="关闭"><X size={15} /></button>
          </div>
        </header>
        <div className="dialog-body" style={{ padding: 0 }}>
          <div ref={hostRef} style={{ height: fullscreen ? 'min(80dvh, 720px)' : '420px', width: '100%' }} />
        </div>
      </MotionDialogPanel>
      {stepUp ? (
        <StepUpAuth
          request={client.request}
          purpose="remote_terminal"
          resourceType="server"
          resourceId={serverId}
          title="打开远程终端"
          warning="确认后即可打开这台服务器的 WebSSH。"
          onComplete={token => { void connect(token) }}
          onCancel={onClose}
        />
      ) : null}
    </>
  )
}
