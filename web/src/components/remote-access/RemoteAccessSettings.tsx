import React, { useEffect, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { Server as ServerIcon, Settings2, X, AlertTriangle } from 'lucide-react'
import { SettingsGroup, SettingsSwitchRow } from '../settings/SettingsLayout'
import { MotionDialogPanel } from '../ui/motion'
import { Switch } from '../ui/switch'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>
type ServerSummary = { id: number; name?: string; status?: string }
type ServerRemoteAccess = ServerSummary & {
  remote: boolean
  mcp: boolean
  effectiveRemote: boolean
  effectiveMcp: boolean
  error?: string
}

function settingEnabled(value: unknown, fallback = false) {
  if (value === undefined || value === null || value === '') return fallback
  return value === true || value === 'true' || value === 1 || value === '1'
}

function serverPolicy(view: any, server: ServerSummary, globalTerminal: boolean, globalMcp: boolean): ServerRemoteAccess {
  const remote = Boolean(view?.server?.remote_terminal_enabled)
  const mcp = Boolean(view?.server?.mcp_enabled)
  // Prefer backend effective if present, otherwise compute as global && server (per spec §2)
  const effectiveRemote = view?.effective?.remote_terminal !== undefined
    ? Boolean(view.effective.remote_terminal)
    : globalTerminal && remote
  const effectiveMcp = view?.effective?.mcp_enabled !== undefined
    ? Boolean(view.effective.mcp_enabled)
    : globalMcp && mcp
  return {
    ...server,
    remote,
    mcp,
    effectiveRemote,
    effectiveMcp,
  }
}

function snapshotServers(servers: unknown): ServerSummary[] {
  if (!Array.isArray(servers)) return []
  return servers.map(server => ({
    id: Number(server.id),
    name: typeof server.name === 'string' ? server.name : undefined,
    status: typeof server.status === 'string' ? server.status : undefined,
  })).filter(server => Number.isFinite(server.id) && server.id > 0)
}

function normalizeServersResponse(payload: unknown): ServerSummary[] {
  const items = Array.isArray(payload)
    ? payload
    : Array.isArray((payload as { servers?: unknown })?.servers)
      ? (payload as { servers: unknown[] }).servers
      : []
  return snapshotServers(items).sort((left, right) => {
    const byName = (left.name || '').localeCompare(right.name || '', 'zh-CN')
    return byName !== 0 ? byName : left.id - right.id
  })
}

export function RemoteAccessSettings({ data, client, load, notify }: { data: any; client: { request: RequestFn }; load: () => Promise<void>; notify: (message: string, tone?: string) => void }) {
  const [passwordConfirmation, setPasswordConfirmation] = useState(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
  const [saving, setSaving] = useState('')
  const [serversOpen, setServersOpen] = useState(false)

  useEffect(() => {
    setPasswordConfirmation(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
  }, [data.settings?.remote_terminal_password_confirmation_enabled])

  const save = async (key: string, body: Record<string, boolean>, success: string) => {
    if (saving) return
    setSaving(key)
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify(body) })
      await load()
      notify(success, 'success')
    } catch (error: any) {
      setPasswordConfirmation(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
      notify(error?.message || '保存失败', 'error')
    } finally {
      setSaving('')
    }
  }

  const openServers = () => setServersOpen(true)

  return (
    <>
      <SettingsGroup
        title="远程访问"
        description="在「管理服务器」中配置全局与逐台 Web 终端、MCP 远程控制。"
        actions={<button type="button" className="ghost remote-access-server-button" onClick={openServers}><Settings2 size={14} aria-hidden="true" />管理服务器</button>}
      >
        <SettingsSwitchRow
          label="WebSSH 密码确认"
          description="打开终端前再次确认管理员身份。"
          checked={passwordConfirmation}
          onChange={checked => {
            setPasswordConfirmation(checked)
            void save('password', { remote_terminal_password_confirmation_enabled: checked }, checked ? 'WebSSH 密码确认已开启' : 'WebSSH 密码确认已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="打开 WebSSH 前确认密码"
        />
      </SettingsGroup>
      <AnimatePresence>
        {serversOpen ? <RemoteAccessServerDialog
          globalTerminal={settingEnabled(data.settings?.remote_terminal_enabled, true)}
          globalMcp={settingEnabled(data.settings?.mcp_enabled, false)}
          client={client}
          load={load}
          notify={notify}
          onClose={() => setServersOpen(false)}
        /> : null}
      </AnimatePresence>
    </>
  )
}

function RemoteAccessServerDialog({
  globalTerminal: initialGlobalTerminal,
  globalMcp: initialGlobalMcp,
  client,
  load,
  notify,
  onClose,
}: {
  globalTerminal: boolean
  globalMcp: boolean
  client: { request: RequestFn }
  load: () => Promise<void>
  notify: (message: string, tone?: string) => void
  onClose: () => void
}) {
  const [rows, setRows] = useState<ServerRemoteAccess[]>([])
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [busy, setBusy] = useState(false)
  const [globalTerminal, setGlobalTerminal] = useState(initialGlobalTerminal)
  const [globalMcp, setGlobalMcp] = useState(initialGlobalMcp)
  const [savingGlobal, setSavingGlobal] = useState('')

  useEffect(() => {
    setGlobalTerminal(initialGlobalTerminal)
    setGlobalMcp(initialGlobalMcp)
  }, [initialGlobalTerminal, initialGlobalMcp])

  useEffect(() => {
    let cancelled = false
    const read = async () => {
      setLoading(true)
      setLoadError('')
      try {
        const listed = await client.request('/servers')
        const servers = normalizeServersResponse(listed)
        if (servers.length === 0) {
          if (!cancelled) {
            setRows([])
            setLoading(false)
          }
          return
        }
        const loaded = await Promise.all(servers.map(async server => {
          try {
            const result = await client.request(`/servers/${server.id}/remote-access`)
            return serverPolicy(result.remote_access, server, globalTerminal, globalMcp)
          } catch (error: any) {
            return { ...server, remote: true, mcp: false, effectiveRemote: false, effectiveMcp: false, error: error?.message || '读取失败' }
          }
        }))
        const recomputed = loaded
        if (!cancelled) {
          setRows(recomputed)
          setLoading(false)
        }
      } catch (error: any) {
        if (!cancelled) {
          setRows([])
          setLoadError(error?.message || '无法读取服务器列表')
          setLoading(false)
        }
      }
    }
    void read()
    return () => { cancelled = true }
  }, [client])

  // Keep effective in sync when global switches change locally, without refetch.
  useEffect(() => {
    setRows(current => current.map(row => ({
      ...row,
      effectiveRemote: globalTerminal && row.remote,
      effectiveMcp: globalMcp && row.mcp,
    })))
  }, [globalTerminal, globalMcp])

  const selectedRows = rows.filter(row => selected.has(row.id))
  const allSelected = rows.length > 0 && selected.size === rows.length

  const mcpConfiguredCount = rows.filter(row => row.mcp).length
  const remoteConfiguredCount = rows.filter(row => row.remote).length

  const saveGlobal = async (key: 'terminal' | 'mcp', body: Record<string, boolean>, checked: boolean, success: string) => {
    if (savingGlobal) return
    const previousTerminal = globalTerminal
    const previousMcp = globalMcp
    if (key === 'terminal') setGlobalTerminal(checked)
    else setGlobalMcp(checked)
    setSavingGlobal(key)
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify(body) })
      await load()
      notify(success, 'success')
    } catch (error: any) {
      setGlobalTerminal(previousTerminal)
      setGlobalMcp(previousMcp)
      notify(error?.message || '保存失败', 'error')
    } finally {
      setSavingGlobal('')
    }
  }

  const patchRows = async (targets: ServerRemoteAccess[], body: Record<string, boolean>, success: string) => {
    if (busy || targets.length === 0) return
    setBusy(true)
    let failures = 0
    const updated = new Map<number, ServerRemoteAccess>()
    await Promise.all(targets.map(async row => {
      try {
        const result = await client.request(`/servers/${row.id}/remote-access`, {
          method: 'PATCH',
          body: JSON.stringify(body),
        })
        updated.set(row.id, serverPolicy(result.remote_access, row, globalTerminal, globalMcp))
      } catch (error: any) {
        failures += 1
        updated.set(row.id, { ...row, error: error?.message || '保存失败' })
      }
    }))
    setRows(current => current.map(row => {
      const next = updated.get(row.id)
      if (!next) return row
      // Preserve computed effective with latest global switches
      return { ...next, effectiveRemote: globalTerminal && next.remote, effectiveMcp: globalMcp && next.mcp }
    }))
    if (failures > 0) notify(`${failures} 台服务器保存失败`, 'error')
    else notify(success, 'success')
    setBusy(false)
  }

  const toggleSelected = (id: number) => setSelected(current => {
    const next = new Set(current)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  })

  const controlsLocked = Boolean(busy || savingGlobal)

  const renderMcpHint = (row: ServerRemoteAccess) => {
    if (row.error) return null
    if (row.mcp && globalMcp) {
      if (row.status === 'offline' || row.status === 'unknown' || row.status === '' || row.status === undefined) {
        // Keep policy distinction: offline does not flip switch; show authorized but offline
        // Only show offline hint when status is explicitly offline; unknown still counts as not online but we treat as authorized
        if (row.status === 'offline') return <span className="muted remote-access-hint">已授权 · Agent 离线</span>
        return <span className="muted remote-access-hint" style={{ color: 'var(--success, #16a34a)' }}>已生效</span>
      }
      if (row.status === 'offline') return <span className="muted remote-access-hint">已授权 · Agent 离线</span>
      return <span className="muted remote-access-hint" style={{ color: 'var(--success, #16a34a)' }}>已生效</span>
    }
    if (row.mcp && !globalMcp) return <span className="muted remote-access-hint" style={{ color: '#d97706' }}>未生效 · 全局已关闭</span>
    if (!row.mcp && globalMcp) return <span className="muted remote-access-hint">此服务器未授权</span>
    return <span className="muted remote-access-hint">未授权</span>
  }

  const renderRemoteHint = (row: ServerRemoteAccess) => {
    if (row.error) return null
    if (row.remote && globalTerminal) {
      if (row.status === 'offline') return <span className="muted remote-access-hint">已授权 · Agent 离线</span>
      return <span className="muted remote-access-hint" style={{ color: 'var(--success, #16a34a)' }}>已生效</span>
    }
    if (row.remote && !globalTerminal) return <span className="muted remote-access-hint" style={{ color: '#d97706' }}>未生效 · 全局已关闭</span>
    if (!row.remote && globalTerminal) return <span className="muted remote-access-hint">此服务器未授权</span>
    return <span className="muted remote-access-hint">未授权</span>
  }

  return (
    <MotionDialogPanel onCancel={onClose} className="remote-access-server-dialog" aria-labelledby="remote-access-server-title">
      <header className="dialog-head">
        <div><h2 id="remote-access-server-title">服务器远程控制</h2><p className="muted">全局开关为总闸，仅在全局与服务器均开启时对应远程能力才真正生效；关闭全局不会改写单台服务器的已配置策略。</p></div>
        <button type="button" className="ghost dialog-close icon-button" onClick={onClose} disabled={controlsLocked} aria-label="关闭" title="关闭"><X size={16} /></button>
      </header>
      <div className="dialog-body remote-access-server-body">
        <div className="remote-access-global-bar">
          <div className="remote-access-global-item">
            <div>
              <strong>Web 远程终端</strong>
              <span className="muted">全局远程终端总开关。只有同时开启全局开关和服务器自身的 Web 远程终端权限，Web 终端才能连接该服务器。</span>
            </div>
            <Switch
              checked={globalTerminal}
              disabled={controlsLocked}
              onChange={checked => void saveGlobal('terminal', { remote_terminal_enabled: checked }, checked, checked ? 'Web 远程终端已全局开启' : 'Web 远程终端已全局关闭')}
              ariaLabel="全局启用 Web 远程终端"
            />
          </div>
          <div className="remote-access-global-item">
            <div>
              <strong>MCP 远程控制</strong>
              <span className="muted">全局远程控制总开关。只有同时开启全局开关和服务器自身的 MCP 远程控制权限，MCP 才能对该服务器执行远程操作。</span>
            </div>
            <Switch
              checked={globalMcp}
              disabled={controlsLocked}
              onChange={checked => void saveGlobal('mcp', { mcp_enabled: checked }, checked, checked ? 'MCP 远程控制已全局开启' : 'MCP 远程控制已全局关闭')}
              ariaLabel="全局启用 MCP 远程控制"
            />
          </div>
        </div>
        {/* Global MCP status banners per spec §7 */}
        {!loading && !loadError && rows.length > 0 && globalMcp && mcpConfiguredCount === 0 ? (
          <div className="remote-access-global-warning" style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 12px', borderRadius: 8, background: 'var(--warning-bg, #fef3c7)', border: '1px solid #f59e0b', color: '#92400e', fontSize: 13 }}>
            <AlertTriangle size={16} aria-hidden="true" />
            <span>MCP 远程控制总开关已开启，但当前没有任何服务器授权 MCP 远程控制。</span>
          </div>
        ) : null}
        {!loading && !loadError && rows.length > 0 && globalMcp && mcpConfiguredCount > 0 ? (
          <div className="muted" style={{ fontSize: 12, padding: '4px 2px' }}>
            MCP 远程控制 已启用 · {mcpConfiguredCount} / {rows.length} 台服务器可被 MCP 远程控制
          </div>
        ) : null}
        {!loading && !loadError && rows.length > 0 && !globalMcp && mcpConfiguredCount > 0 ? (
          <div className="muted" style={{ fontSize: 12, padding: '4px 2px' }}>
            MCP 远程控制 全局已关闭 · 已配置 {mcpConfiguredCount} / {rows.length} 台（开启全局后立即生效）
          </div>
        ) : null}
        {loading ? <p className="muted remote-access-server-loading">正在读取服务器设置…</p> : loadError ? <div className="remote-access-server-empty"><ServerIcon size={20} aria-hidden="true" /><p>{loadError}</p></div> : rows.length === 0 ? <div className="remote-access-server-empty"><ServerIcon size={20} aria-hidden="true" /><p>暂无服务器</p></div> : <>
          <div className="remote-access-bulk-bar">
            <label><input type="checkbox" checked={allSelected} onChange={() => setSelected(allSelected ? new Set() : new Set(rows.map(row => row.id)))} disabled={controlsLocked} />全选</label>
            <span>{selected.size > 0 ? `已选 ${selected.size} / ${rows.length} 台` : `共 ${rows.length} 台服务器`}</span>
            <div>
              <button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: true }, '已批量开启远程控制')}>开启远程</button>
              <button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: false }, '已批量关闭远程控制')}>关闭远程</button>
              <button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_enabled: true }, '已批量开启 MCP')}>开启 MCP</button>
              <button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_enabled: false }, '已批量关闭 MCP')}>关闭 MCP</button>
            </div>
          </div>
          <div className="remote-access-server-list">
            <div className="remote-access-server-row remote-access-server-row-head" aria-hidden="true"><span /><span>服务器</span><span>远程</span><span>MCP 远程控制</span></div>
            {rows.map(row => <div className="remote-access-server-row" key={row.id}>
              <input type="checkbox" checked={selected.has(row.id)} onChange={() => toggleSelected(row.id)} disabled={controlsLocked} aria-label={`选择 ${row.name || `服务器 ${row.id}`}`} />
              <div className="remote-access-server-name"><strong>{row.name || `服务器 ${row.id}`}</strong><span>{row.status === 'online' ? '在线' : row.status === 'offline' ? '离线' : '未连接'}{row.error ? ` · ${row.error}` : ''}</span></div>
              <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2 }}>
                <Switch checked={row.remote} disabled={controlsLocked} onChange={checked => void patchRows([row], { remote_terminal_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}远程`} />
                {renderRemoteHint(row)}
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2 }}>
                <Switch checked={row.mcp} disabled={controlsLocked} onChange={checked => void patchRows([row], { mcp_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}MCP`} />
                {renderMcpHint(row)}
              </div>
            </div>)}
          </div>
        </>}
      </div>
      <footer className="dialog-actions"><button type="button" onClick={onClose} disabled={controlsLocked}>{busy ? '保存中…' : '完成'}</button></footer>
    </MotionDialogPanel>
  )
}
