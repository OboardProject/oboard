import React, { useEffect, useMemo, useRef, useState } from 'react'
import {
  ChevronLeft,
  ClipboardPaste,
  Copy,
  Eraser,
  Globe,
  Info,
  Maximize2,
  Minimize2,
  MoreHorizontal,
  Play,
  Plus,
  RefreshCw,
  Search,
  ShieldAlert,
  SquareTerminal,
  Terminal as TerminalIcon,
  X,
  Zap,
} from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import { Dropdown, DropdownContent, DropdownItem, DropdownTrigger } from '../ui/dropdown-menu'
import { StepUpAuth } from './StepUpAuth'
import {
  modeLabel,
  TerminalSessionPane,
  type RequestFn,
  type TerminalMode,
  type TerminalPaneHandle,
  type TerminalPaneState,
} from './TerminalSessionPane'

export type TerminalServer = {
  id: number
  name?: string
  status?: string
  agent_id?: string
  region_mode?: 'auto' | 'manual' | string
  region_code?: string
  detected_region_code?: string
  display_tags?: Array<{ text: string; tone?: string } | string>
  tags?: string[]
}

import {
  RegionFlag,
  serverRegionCode,
  regionLabel,
  normalizeRegionCode,
  regionFlagEmoji,
} from '../ui/RegionFlag'

export {
  RegionFlag,
  serverRegionCode,
  regionLabel,
  normalizeRegionCode,
  regionFlagEmoji,
}

// Matches the Controller per-user PTY ceiling. Batch runs are checked against it before any
// session is created so a run either starts whole or reports what has to be freed first.
export const maxTerminalSessions = 16

type SessionEntry = {
  key: string
  serverId: number
  serverName: string
  command: string
  batch: boolean
  state: TerminalPaneState
}

type AuthTask = {
  id: number
  serverIds: number[]
  resolve: (token: string) => void
  reject: (error: Error) => void
}

const idleState: TerminalPaneState = {
  phase: 'connecting', status: '正在连接', mode: 'login', loginEnv: false, info: null, commandSent: false,
}

export function serverDisplayName(server: TerminalServer) {
  return String(server.name || '').trim() || `server-${server.id}`
}

export function terminalServerConnectable(server: TerminalServer) {
  return Boolean(String(server.agent_id || '').trim()) && String(server.status || '').toLowerCase() === 'online'
}

export function terminalServerBlockReason(server: TerminalServer) {
  if (!String(server.agent_id || '').trim()) return '未接入 Agent'
  if (String(server.status || '').toLowerCase() !== 'online') return 'Agent 当前离线'
  return ''
}

function sessionPhaseLabel(entry: SessionEntry) {
  if (entry.state.phase === 'ready') return entry.batch && entry.state.commandSent ? '已执行' : '已连接'
  if (entry.state.phase === 'authorizing') return '等待认证'
  if (entry.state.phase === 'connecting') return '正在连接'
  if (entry.state.phase === 'waiting') return '正在等待节点'
  return entry.state.status
}

export function TerminalWorkspace({
  servers,
  initialServerId,
  client,
  websocketURL,
  passwordConfirmationRequired,
  onClose,
}: {
  servers: TerminalServer[]
  initialServerId?: number | null
  client: { request: RequestFn }
  websocketURL: (serverId: number, sessionId: string) => string
  passwordConfirmationRequired: boolean
  onClose: () => void
}) {
  const [sessions, setSessions] = useState<SessionEntry[]>([])
  const [activeKey, setActiveKey] = useState('')
  const [view, setView] = useState<'sessions' | 'picker' | 'batch'>('picker')
  const [fullscreen, setFullscreen] = useState(false)
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [infoOpen, setInfoOpen] = useState(false)
  const [notice, setNotice] = useState('')
  const [pickerQuery, setPickerQuery] = useState('')
  const [pickerStatusFilter, setPickerStatusFilter] = useState<'all' | 'online' | 'offline'>('all')
  const [pickerRegionFilter, setPickerRegionFilter] = useState('')
  const [pickerSelection, setPickerSelection] = useState<number[]>([])
  const [batchCommand, setBatchCommand] = useState('')
  const [batchQuery, setBatchQuery] = useState('')
  const [batchSelection, setBatchSelection] = useState<number[]>([])

  const handlesRef = useRef(new Map<string, TerminalPaneHandle>())
  const sessionCounter = useRef(0)
  const authCounter = useRef(0)
  const authQueueRef = useRef<AuthTask[]>([])
  const [authTask, setAuthTask] = useState<AuthTask | null>(null)
  const batchTokenRef = useRef<{ token: string; remaining: Set<number>; expiresAt: number } | null>(null)
  const bootstrappedRef = useRef(false)

  const serverById = useMemo(() => new Map(servers.map(server => [Number(server.id), server])), [servers])
  const activeSession = sessions.find(entry => entry.key === activeKey) || null

  const enqueueAuth = (serverIds: number[]) => new Promise<string>((resolve, reject) => {
    const task: AuthTask = { id: ++authCounter.current, serverIds, resolve, reject }
    authQueueRef.current.push(task)
    setAuthTask(current => current || authQueueRef.current[0] || null)
  })

  const finishAuth = () => {
    authQueueRef.current.shift()
    setAuthTask(authQueueRef.current[0] || null)
  }

  // One authentication per server for ad-hoc terminals; one `server_set` authentication for a
  // batch run, consumed once per server it lists.
  const acquireToken = React.useCallback(async (serverId: number) => {
    if (!passwordConfirmationRequired) return ''
    const batch = batchTokenRef.current
    if (batch && batch.remaining.has(serverId) && Date.now() < batch.expiresAt) {
      batch.remaining.delete(serverId)
      return batch.token
    }
    return enqueueAuth([serverId])
  }, [passwordConfirmationRequired])

  const registerHandle = (key: string, handle: TerminalPaneHandle | null) => {
    if (handle) handlesRef.current.set(key, handle)
    else handlesRef.current.delete(key)
  }

  const updateSessionState = (key: string, state: TerminalPaneState) => {
    setSessions(current => current.map(entry => (entry.key === key ? { ...entry, state } : entry)))
  }

  const openSession = (server: TerminalServer, options?: { command?: string; batch?: boolean; focus?: boolean }) => {
    const key = `pane-${server.id}-${++sessionCounter.current}`
    setSessions(current => [...current, {
      key,
      serverId: Number(server.id),
      serverName: serverDisplayName(server),
      command: String(options?.command || ''),
      batch: Boolean(options?.batch),
      state: idleState,
    }])
    if (options?.focus !== false) {
      setActiveKey(key)
      setView('sessions')
    }
    return key
  }

  const connectServer = (server: TerminalServer) => {
    setNotice('')
    const existing = sessions.find(entry => entry.serverId === Number(server.id))
    if (existing) {
      setActiveKey(existing.key)
      setView('sessions')
      setSidebarOpen(false)
      return
    }
    if (sessions.length >= maxTerminalSessions) {
      setNotice(`最多同时保持 ${maxTerminalSessions} 个终端会话，请先关闭一些会话`)
      return
    }
    openSession(server)
    setSidebarOpen(false)
  }

  const closeSession = async (key: string) => {
    const handle = handlesRef.current.get(key)
    handlesRef.current.delete(key)
    const remaining = sessions.filter(entry => entry.key !== key)
    setSessions(remaining)
    if (activeKey === key) setActiveKey(remaining.length ? remaining[remaining.length - 1].key : '')
    if (!remaining.length) setView('picker')
    await handle?.release()
  }

  const closeAllSessions = async () => {
    const handles = Array.from(handlesRef.current.values())
    handlesRef.current.clear()
    setSessions([])
    setActiveKey('')
    setView('picker')
    await Promise.all(handles.map(handle => handle.release()))
  }

  const closeWorkspace = async () => {
    await closeAllSessions()
    onClose()
  }

  useEffect(() => {
    if (bootstrappedRef.current) return
    bootstrappedRef.current = true
    const target = initialServerId ? serverById.get(Number(initialServerId)) : null
    if (target && terminalServerConnectable(target)) openSession(target)
  }, [initialServerId, serverById])

  useEffect(() => {
    if (view === 'sessions' && !activeSession && sessions.length) setActiveKey(sessions[0].key)
  }, [view, activeSession, sessions])

  const connectableServers = useMemo(() => servers.filter(terminalServerConnectable), [servers])
  const onlineCount = useMemo(() => servers.filter(terminalServerConnectable).length, [servers])
  const offlineCount = useMemo(() => servers.length - onlineCount, [servers, onlineCount])

  const regionOptionsList = useMemo(() => {
    const map = new Map<string, number>()
    for (const server of servers) {
      const code = serverRegionCode(server)
      if (code) {
        map.set(code, (map.get(code) || 0) + 1)
      }
    }
    return Array.from(map.entries())
      .map(([code, count]) => ({ code, label: regionLabel(code), count }))
      .sort((a, b) => a.label.localeCompare(b.label, 'zh-CN'))
  }, [servers])

  const pickerServers = useMemo(() => {
    const query = pickerQuery.trim().toLowerCase()
    return servers.filter(server => {
      const isConnectable = terminalServerConnectable(server)
      if (pickerStatusFilter === 'online' && !isConnectable) return false
      if (pickerStatusFilter === 'offline' && isConnectable) return false

      const regCode = serverRegionCode(server)
      if (pickerRegionFilter && regCode !== pickerRegionFilter) return false

      if (query) {
        const name = serverDisplayName(server).toLowerCase()
        const idStr = String(server.id)
        const regLabel = regionLabel(regCode).toLowerCase()
        const tags = [
          ...(server.tags || []),
          ...(server.display_tags || []).map(t => (typeof t === 'string' ? t : t?.text || '')),
        ].map(t => String(t).toLowerCase())
        const matchName = name.includes(query)
        const matchId = idStr === query
        const matchRegCode = regCode.toLowerCase().includes(query)
        const matchRegLabel = regLabel.includes(query)
        const matchTag = tags.some(t => t.includes(query))
        if (!matchName && !matchId && !matchRegCode && !matchRegLabel && !matchTag) {
          return false
        }
      }
      return true
    })
  }, [servers, pickerQuery, pickerStatusFilter, pickerRegionFilter])

  const connectableInPicker = useMemo(() => {
    return pickerServers.filter(terminalServerConnectable)
  }, [pickerServers])

  const allConnectableSelected = connectableInPicker.length > 0
    && connectableInPicker.every(s => pickerSelection.includes(Number(s.id)))

  const togglePickerSelect = (serverId: number) => {
    const server = serverById.get(serverId)
    if (!server || !terminalServerConnectable(server)) return
    setPickerSelection(current => (current.includes(serverId)
      ? current.filter(id => id !== serverId)
      : [...current, serverId]))
  }

  const selectAllConnectableInPicker = () => {
    const connectableIds = connectableInPicker.map(s => Number(s.id))
    setPickerSelection(current => {
      const allSelected = connectableIds.length > 0 && connectableIds.every(id => current.includes(id))
      if (allSelected) {
        return current.filter(id => !connectableIds.includes(id))
      }
      return Array.from(new Set([...current, ...connectableIds]))
    })
  }

  const connectSelectedServers = async () => {
    const targets = pickerSelection
      .map(id => serverById.get(id))
      .filter((server): server is TerminalServer => Boolean(server && terminalServerConnectable(server)))

    if (!targets.length) {
      setNotice('请至少选择一台在线服务器')
      return
    }

    const reuse: Array<{ entry: SessionEntry; server: TerminalServer }> = []
    const fresh: TerminalServer[] = []
    for (const server of targets) {
      const existing = sessions.find(entry => entry.serverId === Number(server.id))
      if (existing) reuse.push({ entry: existing, server })
      else fresh.push(server)
    }

    if (sessions.length + fresh.length > maxTerminalSessions) {
      setNotice(`最多同时保持 ${maxTerminalSessions} 个终端会话，当前已有 ${sessions.length} 个，无法同时启动 ${fresh.length} 个新会话`)
      return
    }
    setNotice('')

    if (passwordConfirmationRequired && fresh.length) {
      batchTokenRef.current = null
      let token = ''
      try {
        token = await enqueueAuth(fresh.map(server => Number(server.id)))
      } catch {
        setNotice('未完成认证，批量启动已取消')
        return
      }
      batchTokenRef.current = {
        token,
        remaining: new Set(fresh.map(server => Number(server.id))),
        expiresAt: Date.now() + 100_000,
      }
    }

    let firstKey = ''
    for (const server of fresh) {
      const key = openSession(server, { focus: false })
      if (!firstKey) firstKey = key
    }
    if (!firstKey && reuse.length > 0) {
      firstKey = reuse[0].entry.key
    }

    if (firstKey) {
      setActiveKey(firstKey)
      setView('sessions')
      setSidebarOpen(false)
      setPickerSelection([])
    }
  }

  const batchServers = useMemo(() => {
    const query = batchQuery.trim().toLowerCase()
    if (!query) return connectableServers
    return connectableServers.filter(server => serverDisplayName(server).toLowerCase().includes(query) || String(server.id) === query)
  }, [connectableServers, batchQuery])

  const toggleBatchServer = (serverId: number) => {
    setBatchSelection(current => (current.includes(serverId)
      ? current.filter(id => id !== serverId)
      : [...current, serverId]))
  }

  const runBatch = async () => {
    const command = batchCommand.trim()
    if (!command) {
      setNotice('请先输入要执行的命令')
      return
    }
    const targets = batchSelection
      .map(id => serverById.get(id))
      .filter((server): server is TerminalServer => Boolean(server && terminalServerConnectable(server)))
    if (!targets.length) {
      setNotice('请至少选择一台在线服务器')
      return
    }
    const reuse: Array<{ entry: SessionEntry; server: TerminalServer }> = []
    const fresh: TerminalServer[] = []
    for (const server of targets) {
      const existing = sessions.find(entry => entry.serverId === Number(server.id))
      if (existing) reuse.push({ entry: existing, server })
      else fresh.push(server)
    }
    if (sessions.length + fresh.length > maxTerminalSessions) {
      setNotice(`最多同时保持 ${maxTerminalSessions} 个终端会话，当前已有 ${sessions.length} 个，请减少选择或先关闭一些会话`)
      return
    }
    setNotice('')

    if (passwordConfirmationRequired && fresh.length) {
      batchTokenRef.current = null
      let token = ''
      try {
        token = await enqueueAuth(fresh.map(server => Number(server.id)))
      } catch {
        setNotice('未完成认证，批量执行已取消')
        return
      }
      batchTokenRef.current = {
        token,
        remaining: new Set(fresh.map(server => Number(server.id))),
        // Controller step-up tokens live 120s; stop reusing this one well before that.
        expiresAt: Date.now() + 100_000,
      }
    }

    let firstKey = ''
    for (const server of fresh) {
      const key = openSession(server, { command, batch: true, focus: false })
      if (!firstKey) firstKey = key
    }
    for (const item of reuse) {
      const handle = handlesRef.current.get(item.entry.key)
      if (handle?.runCommand(command)) {
        setSessions(current => current.map(entry => (entry.key === item.entry.key ? { ...entry, batch: true, command } : entry)))
        if (!firstKey) firstKey = item.entry.key
      }
    }
    if (firstKey) {
      setActiveKey(firstKey)
      setView('sessions')
      setSidebarOpen(false)
    }
  }

  const activeHandle = activeKey ? handlesRef.current.get(activeKey) : undefined
  const info = activeSession?.state.info || null
  const identity = info?.username && info?.shell
    ? `${info.username} · ${info.shell} · ${modeLabel(info.mode || activeSession?.state.mode)}`
    : ''
  const showLoginActions = Boolean(activeSession?.state.loginEnv || info)
  const shellExited = Boolean(activeSession?.state.status.startsWith('Shell 已退出'))
  const headerTitle = view === 'sessions' && activeSession ? `远程终端 · ${activeSession.serverName}` : '远程终端'

  const reconnectActive = (mode?: TerminalMode) => activeHandle?.reconnect(mode)

  return (
    <>
      <MotionDialogPanel
        onCancel={() => void closeWorkspace()}
        className={`remote-terminal-dialog terminal-workspace-dialog${fullscreen ? ' remote-terminal-fullscreen' : ''}`}
        ariaLabel="远程终端工作台"
      >
        <header className="dialog-head">
          <div className="remote-terminal-title">
            <div className="remote-terminal-heading">
              <button
                type="button"
                className="ghost icon-button terminal-sidebar-toggle"
                onClick={() => setSidebarOpen(open => !open)}
                aria-label={sidebarOpen ? '收起会话列表' : '展开会话列表'}
                aria-expanded={sidebarOpen}
              >
                {sidebarOpen ? <ChevronLeft size={15} /> : <SquareTerminal size={15} />}
              </button>
              <h2>{headerTitle}</h2>
            </div>
            <div className="remote-terminal-meta">
              {identity ? <span className="remote-terminal-identity">{identity}</span> : null}
              {identity ? <span className="remote-terminal-dot">·</span> : null}
              <span className="muted">
                {view === 'picker' ? '选择要连接的服务器'
                  : view === 'batch' ? '批量执行'
                  : activeSession ? activeSession.state.status : '暂无会话'}
              </span>
            </div>
          </div>
          <div className="dialog-head-actions">
            <button
              type="button"
              className="ghost remote-terminal-shortcut"
              onClick={() => activeHandle?.interrupt()}
              disabled={!activeSession}
              aria-label="发送 Ctrl+C"
              title="发送 Ctrl+C"
            >Ctrl+C</button>
            <button
              type="button"
              className="ghost icon-button"
              onClick={() => reconnectActive()}
              disabled={!activeSession}
              aria-label="重连"
              title="重新连接"
            ><RefreshCw size={14} /></button>
            <Dropdown>
              <DropdownTrigger>
                <button type="button" className="ghost icon-button" aria-label="终端菜单" title="更多操作"><MoreHorizontal size={14} /></button>
              </DropdownTrigger>
              <DropdownContent align="right" className="remote-terminal-menu">
                <DropdownItem onClick={() => { setView('picker'); setSidebarOpen(false) }}>
                  <Plus size={14} className="remote-terminal-menu-icon" />
                  <span>新建连接</span>
                </DropdownItem>
                <DropdownItem onClick={() => { setView('batch'); setSidebarOpen(false) }}>
                  <Zap size={14} className="remote-terminal-menu-icon" />
                  <span>批量执行</span>
                </DropdownItem>
                {activeSession ? (
                  <>
                    <DropdownItem onClick={() => reconnectActive()}>
                      <RefreshCw size={14} className="remote-terminal-menu-icon" />
                      <span>重新连接</span>
                    </DropdownItem>
                    {showLoginActions ? (
                      <DropdownItem onClick={() => reconnectActive('minimal')} aria-label="最小环境打开">
                        <ShieldAlert size={14} className="remote-terminal-menu-icon" />
                        <span>以最小环境打开</span>
                      </DropdownItem>
                    ) : null}
                    <DropdownItem onClick={() => activeHandle?.clear()}>
                      <Eraser size={14} className="remote-terminal-menu-icon" />
                      <span>清屏</span>
                    </DropdownItem>
                    <DropdownItem onClick={() => void activeHandle?.copySelection()}>
                      <Copy size={14} className="remote-terminal-menu-icon" />
                      <span>复制选中内容</span>
                    </DropdownItem>
                    <DropdownItem onClick={() => void activeHandle?.paste()}>
                      <ClipboardPaste size={14} className="remote-terminal-menu-icon" />
                      <span>粘贴到终端</span>
                    </DropdownItem>
                    {info ? (
                      <DropdownItem onClick={() => setInfoOpen(current => !current)}>
                        <Info size={14} className="remote-terminal-menu-icon" />
                        <span>{infoOpen ? '隐藏终端信息' : '显示终端信息'}</span>
                      </DropdownItem>
                    ) : null}
                  </>
                ) : null}
                {sessions.length ? (
                  <DropdownItem onClick={() => void closeAllSessions()}>
                    <X size={14} className="remote-terminal-menu-icon" />
                    <span>关闭全部会话</span>
                  </DropdownItem>
                ) : null}
              </DropdownContent>
            </Dropdown>
            <button
              type="button"
              className="ghost icon-button"
              onClick={() => setFullscreen(current => !current)}
              aria-label={fullscreen ? '退出全屏' : '全屏'}
              title={fullscreen ? '退出全屏' : '全屏'}
            >{fullscreen ? <Minimize2 size={14} /> : <Maximize2 size={14} />}</button>
            <button type="button" className="remote-terminal-close-dot" onClick={() => void closeWorkspace()} aria-label="关闭" title="关闭">
              <span className="macos-dot macos-dot-close"><X size={8} strokeWidth={2.6} /></span>
            </button>
          </div>
        </header>
        <div className={`dialog-body remote-terminal-body terminal-workspace-body${sidebarOpen ? ' is-sidebar-open' : ''}`}>
          <aside className="terminal-workspace-sidebar" aria-label="终端会话">
            <div className="terminal-sidebar-actions">
              <button
                type="button"
                className={view === 'picker' ? 'ghost is-active' : 'ghost'}
                onClick={() => { setView('picker'); setSidebarOpen(false); setNotice('') }}
              ><Plus size={14} aria-hidden="true" /><span>新建连接</span></button>
              <button
                type="button"
                className={view === 'batch' ? 'ghost is-active' : 'ghost'}
                onClick={() => { setView('batch'); setSidebarOpen(false); setNotice('') }}
              ><Zap size={14} aria-hidden="true" /><span>批量执行</span></button>
            </div>
            <div className="terminal-sidebar-heading">
              <span>已连接</span>
              <span className="muted">{sessions.length} / {maxTerminalSessions}</span>
            </div>
            {sessions.length ? (
              <ul className="terminal-session-list">
                {sessions.map(entry => (
                  <li key={entry.key}>
                    <button
                      type="button"
                      className={`terminal-session-item${entry.key === activeKey && view === 'sessions' ? ' is-active' : ''}`}
                      onClick={() => { setActiveKey(entry.key); setView('sessions'); setSidebarOpen(false); setNotice('') }}
                      aria-current={entry.key === activeKey && view === 'sessions'}
                    >
                      <span className={`terminal-session-dot is-${entry.state.phase}`} aria-hidden="true" />
                      <span className="terminal-session-text">
                        <span className="terminal-session-name">{entry.serverName}</span>
                        <span className="terminal-session-status">{sessionPhaseLabel(entry)}</span>
                      </span>
                    </button>
                    <button
                      type="button"
                      className="terminal-session-close"
                      onClick={event => {
                        event.stopPropagation()
                        void closeSession(entry.key)
                      }}
                      aria-label={`关闭 ${entry.serverName} 的会话`}
                      title="关闭会话"
                    ><X size={12} /></button>
                  </li>
                ))}
              </ul>
            ) : <p className="muted terminal-sidebar-empty">还没有终端会话</p>}
          </aside>
          <div className="terminal-workspace-main">
            {notice ? <div className="remote-terminal-banner" role="status"><span>{notice}</span></div> : null}
            {shellExited && showLoginActions && view === 'sessions' ? (
              <div className="remote-terminal-banner">
                <span>登录配置可能导致 Shell 退出。</span>
                <button type="button" className="ghost" onClick={() => reconnectActive('minimal')}>以最小环境打开</button>
              </div>
            ) : null}
            {infoOpen && info && view === 'sessions' ? (
              <div className="remote-terminal-info" role="region" aria-label="终端信息">
                <dl>
                  <div><dt>用户</dt><dd>{info.username || '-'}</dd></div>
                  <div><dt>UID / GID</dt><dd>{info.uid ?? '-'} / {info.gid ?? '-'}</dd></div>
                  <div><dt>HOME</dt><dd>{info.home || '-'}</dd></div>
                  <div><dt>Shell</dt><dd>{info.shell || '-'}</dd></div>
                  <div><dt>模式</dt><dd>{modeLabel(info.mode)}</dd></div>
                  <div><dt>目录</dt><dd>{info.cwd || '-'}</dd></div>
                  <div><dt>TERM</dt><dd>{info.term || '-'}</dd></div>
                  <div><dt>系统环境</dt><dd>{info.system_environment_loaded ? '已加载' : '未加载'}</dd></div>
                  <div><dt>本机 terminal.env</dt><dd>{info.terminal_environment_loaded ? '已加载' : '未加载'}</dd></div>
                </dl>
              </div>
            ) : null}

            <div className="terminal-workspace-stage">
              <div className="terminal-pane-stack" hidden={view !== 'sessions'}>
                {sessions.map(entry => (
                  <div key={entry.key} className="terminal-pane-slot" hidden={entry.key !== activeKey}>
                    <TerminalSessionPane
                      ref={handle => registerHandle(entry.key, handle)}
                      serverId={entry.serverId}
                      client={client}
                      websocketURL={websocketURL}
                      acquireToken={acquireToken}
                      active={entry.key === activeKey && view === 'sessions'}
                      autoCommand={entry.command}
                      onState={state => updateSessionState(entry.key, state)}
                    />
                  </div>
                ))}
                {!sessions.length ? <p className="muted terminal-stage-empty">还没有终端会话，先在左侧新建连接。</p> : null}
              </div>

              {view === 'picker' ? (
                <div className="terminal-workspace-form" role="region" aria-label="选择服务器">
                  <div className="terminal-picker-toolbar">
                    <div className="terminal-picker-toolbar-top">
                      <div className="terminal-form-search">
                        <Search size={15} aria-hidden="true" />
                        <input
                          type="search"
                          value={pickerQuery}
                          onChange={event => setPickerQuery(event.target.value)}
                          placeholder="搜索服务器名称、编号或地区"
                          aria-label="搜索服务器"
                        />
                      </div>
                      {connectableInPicker.length > 0 ? (
                        <div className="terminal-picker-quick-actions">
                          <button
                            type="button"
                            className="ghost"
                            onClick={selectAllConnectableInPicker}
                          >
                            {allConnectableSelected ? '取消全选' : `全选在线 (${connectableInPicker.length})`}
                          </button>
                        </div>
                      ) : null}
                    </div>

                    <div className="terminal-picker-filters">
                      <div className="terminal-filter-pills" role="radiogroup" aria-label="状态筛选">
                        <button
                          type="button"
                          className={`terminal-filter-pill${pickerStatusFilter === 'all' ? ' is-active' : ''}`}
                          onClick={() => setPickerStatusFilter('all')}
                        >全部 <span className="pill-count">{servers.length}</span></button>
                        <button
                          type="button"
                          className={`terminal-filter-pill${pickerStatusFilter === 'online' ? ' is-active' : ''}`}
                          onClick={() => setPickerStatusFilter('online')}
                        >在线 <span className="pill-count">{onlineCount}</span></button>
                        <button
                          type="button"
                          className={`terminal-filter-pill${pickerStatusFilter === 'offline' ? ' is-active' : ''}`}
                          onClick={() => setPickerStatusFilter('offline')}
                        >离线 <span className="pill-count">{offlineCount}</span></button>
                      </div>

                      {regionOptionsList.length > 0 ? (
                        <div className="terminal-region-filter">
                          <Globe size={13} aria-hidden="true" />
                          <select
                            value={pickerRegionFilter}
                            onChange={e => setPickerRegionFilter(e.target.value)}
                            aria-label="按地区筛选"
                          >
                            <option value="">全部地区 ({servers.length})</option>
                            {regionOptionsList.map(item => (
                              <option key={item.code} value={item.code}>{item.label} ({item.count})</option>
                            ))}
                          </select>
                        </div>
                      ) : null}
                    </div>
                  </div>

                  {pickerServers.length ? (
                    <ul className="terminal-server-list">
                      {pickerServers.map(server => {
                        const blocked = terminalServerBlockReason(server)
                        const connectable = terminalServerConnectable(server)
                        const selected = pickerSelection.includes(Number(server.id))
                        const isConnected = sessions.some(s => s.serverId === Number(server.id))
                        return (
                          <li key={server.id} className={`terminal-picker-item${selected ? ' is-selected' : ''}`}>
                            {connectable ? (
                              <input
                                type="checkbox"
                                className="terminal-picker-checkbox"
                                checked={selected}
                                onChange={() => togglePickerSelect(Number(server.id))}
                                aria-label={`选择 ${serverDisplayName(server)}`}
                              />
                            ) : (
                              <span className="terminal-picker-checkbox-empty" aria-hidden="true" />
                            )}
                            <button
                              type="button"
                              className={`terminal-server-option${selected ? ' is-selected' : ''}`}
                              disabled={Boolean(blocked)}
                              title={blocked || `连接 ${serverDisplayName(server)}`}
                              onClick={() => {
                                if (pickerSelection.length > 0) {
                                  togglePickerSelect(Number(server.id))
                                } else {
                                  connectServer(server)
                                }
                              }}
                            >
                              <span className="terminal-server-flag">
                                <RegionFlag code={serverRegionCode(server)} size={18} />
                              </span>
                              <span className="terminal-server-name">{serverDisplayName(server)}</span>
                              <span className="muted terminal-server-hint">{blocked || (isConnected ? '已连接' : '可连接')}</span>
                            </button>
                          </li>
                        )
                      })}
                    </ul>
                  ) : <p className="muted">没有匹配的服务器</p>}

                  {pickerSelection.length > 0 ? (
                    <div className="terminal-picker-batch-bar">
                      <div className="terminal-batch-bar-info">
                        <span className="batch-bar-count">已选择 <strong>{pickerSelection.length}</strong> 台服务器</span>
                        {sessions.length + pickerSelection.length > maxTerminalSessions ? (
                          <span className="batch-bar-warning">
                            （超出上限，最多保持 {maxTerminalSessions} 个会话）
                          </span>
                        ) : null}
                      </div>
                      <div className="terminal-batch-bar-actions">
                        <button
                          type="button"
                          className="ghost"
                          onClick={() => setPickerSelection([])}
                        >取消选择</button>
                        <button
                          type="button"
                          className="primary"
                          onClick={() => void connectSelectedServers()}
                        >
                          <Play size={13} aria-hidden="true" />
                          <span>一键启动所选终端 ({pickerSelection.length})</span>
                        </button>
                      </div>
                    </div>
                  ) : null}
                </div>
              ) : null}

              {view === 'batch' ? (
                <div className="terminal-workspace-form" role="region" aria-label="批量执行">
                  <label className="terminal-batch-label" htmlFor="terminal-batch-command">要执行的命令</label>
                  <textarea
                    id="terminal-batch-command"
                    rows={3}
                    value={batchCommand}
                    onChange={event => setBatchCommand(event.target.value)}
                    placeholder="例如：uptime"
                    spellCheck={false}
                  />
                  <p className="muted terminal-batch-hint">命令会在每台所选服务器的终端里以当前登录账号执行，请自行确认命令安全。</p>
                  <div className="terminal-batch-toolbar">
                    <div className="terminal-form-search">
                      <Search size={15} aria-hidden="true" />
                      <input
                        type="search"
                        value={batchQuery}
                        onChange={event => setBatchQuery(event.target.value)}
                        placeholder="搜索在线服务器"
                        aria-label="搜索在线服务器"
                      />
                    </div>
                    <button
                      type="button"
                      className="ghost"
                      onClick={() => setBatchSelection(batchServers.map(server => Number(server.id)))}
                      disabled={!batchServers.length}
                    >全选筛选结果</button>
                    <button type="button" className="ghost" onClick={() => setBatchSelection([])} disabled={!batchSelection.length}>清空</button>
                  </div>
                  {batchServers.length ? (
                    <ul className="terminal-server-list is-checkable">
                      {batchServers.map(server => {
                        const id = Number(server.id)
                        return (
                          <li key={id}>
                            <label className="terminal-server-check">
                              <input type="checkbox" checked={batchSelection.includes(id)} onChange={() => toggleBatchServer(id)} />
                              <span className="terminal-server-flag">
                                <RegionFlag code={serverRegionCode(server)} size={18} />
                              </span>
                              <span className="terminal-server-name">{serverDisplayName(server)}</span>
                              {sessions.some(entry => entry.serverId === id) ? <span className="muted terminal-server-hint">复用已有会话</span> : null}
                            </label>
                          </li>
                        )
                      })}
                    </ul>
                  ) : <p className="muted">没有可连接的在线服务器</p>}
                  <div className="terminal-batch-actions">
                    <span className="muted">已选 {batchSelection.length} 台 · 会话上限 {maxTerminalSessions}</span>
                    <button type="button" onClick={() => void runBatch()} disabled={!batchSelection.length || !batchCommand.trim()}>
                      <Play size={14} aria-hidden="true" />连接并执行
                    </button>
                  </div>
                </div>
              ) : null}
            </div>
          </div>
        </div>
      </MotionDialogPanel>
      {authTask ? (
        <StepUpAuth
          key={authTask.id}
          request={client.request}
          purpose="remote_terminal"
          resourceType={authTask.serverIds.length > 1 ? 'server_set' : 'server'}
          resourceId={authTask.serverIds.length > 1 ? authTask.serverIds.join(',') : authTask.serverIds[0]}
          autoStartPasskey
          title={authTask.serverIds.length > 1 ? `打开 ${authTask.serverIds.length} 台服务器的远程终端` : '打开远程终端'}
          warning={authTask.serverIds.length > 1
            ? '确认后将为所选服务器分别打开 WebSSH 并执行命令。'
            : '确认后即可打开这台服务器的 WebSSH。'}
          onComplete={token => { authTask.resolve(token); finishAuth() }}
          onCancel={() => { authTask.reject(new Error('未完成认证')); finishAuth() }}
        />
      ) : null}
    </>
  )
}
