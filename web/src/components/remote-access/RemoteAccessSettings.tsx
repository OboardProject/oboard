import React, { useEffect, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { Server as ServerIcon, Settings2, X } from 'lucide-react'
import { SettingsGroup, SettingsSwitchRow } from '../settings/SettingsLayout'
import { MotionDialogPanel } from '../ui/motion'
import { Switch } from '../ui/switch'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>
type ServerSummary = { id: number; name?: string; status?: string }
type ServerRemoteAccess = ServerSummary & { remote: boolean; mcp: boolean; error?: string }

function settingEnabled(value: unknown, fallback = false) {
  if (value === undefined || value === null || value === '') return fallback
  return value === true || value === 'true' || value === 1 || value === '1'
}

function serverPolicy(view: any, server: ServerSummary): ServerRemoteAccess {
  return {
    ...server,
    remote: Boolean(view?.server?.remote_terminal_enabled),
    mcp: Boolean(view?.server?.mcp_enabled),
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
            return serverPolicy(result.remote_access, server)
          } catch (error: any) {
            return { ...server, remote: true, mcp: false, error: error?.message || '读取失败' }
          }
        }))
        if (!cancelled) {
          setRows(loaded)
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

  const selectedRows = rows.filter(row => selected.has(row.id))
  const allSelected = rows.length > 0 && selected.size === rows.length

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
        updated.set(row.id, serverPolicy(result.remote_access, row))
      } catch (error: any) {
        failures += 1
        updated.set(row.id, { ...row, error: error?.message || '保存失败' })
      }
    }))
    setRows(current => current.map(row => updated.get(row.id) || row))
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

  return (
    <MotionDialogPanel onCancel={onClose} className="remote-access-server-dialog" aria-labelledby="remote-access-server-title">
      <header className="dialog-head">
        <div><h2 id="remote-access-server-title">服务器远程控制</h2><p className="muted">全局开启后，逐台开关会保留原状态但暂时不可编辑；关闭全局后可继续逐台调整。</p></div>
        <button type="button" className="ghost dialog-close icon-button" onClick={onClose} disabled={controlsLocked} aria-label="关闭" title="关闭"><X size={16} /></button>
      </header>
      <div className="dialog-body remote-access-server-body">
        <div className="remote-access-global-bar">
          <div className="remote-access-global-item">
            <div>
              <strong>Web 远程终端</strong>
              <span className="muted">全局开启后，所有服务器统一允许浏览器终端。</span>
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
              <span className="muted">全局开启后，MCP 连接允许主机级远程控制（仍需 Privileged Grant）。</span>
            </div>
            <Switch
              checked={globalMcp}
              disabled={controlsLocked}
              onChange={checked => void saveGlobal('mcp', { mcp_enabled: checked }, checked, checked ? 'MCP 远程控制已全局开启' : 'MCP 远程控制已全局关闭')}
              ariaLabel="全局启用 MCP 远程控制"
            />
          </div>
        </div>
        {loading ? <p className="muted remote-access-server-loading">正在读取服务器设置…</p> : loadError ? <div className="remote-access-server-empty"><ServerIcon size={20} aria-hidden="true" /><p>{loadError}</p></div> : rows.length === 0 ? <div className="remote-access-server-empty"><ServerIcon size={20} aria-hidden="true" /><p>暂无服务器</p></div> : <>
          <div className="remote-access-bulk-bar">
            <label><input type="checkbox" checked={allSelected} onChange={() => setSelected(allSelected ? new Set() : new Set(rows.map(row => row.id)))} disabled={controlsLocked} />全选</label>
            <span>{selected.size > 0 ? `已选 ${selected.size} / ${rows.length} 台` : `共 ${rows.length} 台服务器`}</span>
            <div>
              <button type="button" className="ghost" disabled={controlsLocked || globalTerminal || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: true }, '已批量开启远程控制')}>开启远程</button>
              <button type="button" className="ghost" disabled={controlsLocked || globalTerminal || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: false }, '已批量关闭远程控制')}>关闭远程</button>
              <button type="button" className="ghost" disabled={controlsLocked || globalMcp || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_enabled: true }, '已批量开启 MCP')}>开启 MCP</button>
              <button type="button" className="ghost" disabled={controlsLocked || globalMcp || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_enabled: false }, '已批量关闭 MCP')}>关闭 MCP</button>
            </div>
          </div>
          <div className="remote-access-server-list">
            <div className="remote-access-server-row remote-access-server-row-head" aria-hidden="true"><span /><span>服务器</span><span>远程</span><span>MCP 远程控制</span></div>
            {rows.map(row => <div className="remote-access-server-row" key={row.id}>
              <input type="checkbox" checked={selected.has(row.id)} onChange={() => toggleSelected(row.id)} disabled={controlsLocked} aria-label={`选择 ${row.name || `服务器 ${row.id}`}`} />
              <div className="remote-access-server-name"><strong>{row.name || `服务器 ${row.id}`}</strong><span>{row.status === 'online' ? '在线' : row.status === 'offline' ? '离线' : '未连接'}{row.error ? ` · ${row.error}` : ''}</span></div>
              <Switch checked={row.remote} disabled={controlsLocked || globalTerminal} onChange={checked => void patchRows([row], { remote_terminal_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}远程`} />
              <Switch checked={row.mcp} disabled={controlsLocked || globalMcp} onChange={checked => void patchRows([row], { mcp_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}MCP`} />
            </div>)}
          </div>
        </>}
      </div>
      <footer className="dialog-actions"><button type="button" onClick={onClose} disabled={controlsLocked}>{busy ? '保存中…' : '完成'}</button></footer>
    </MotionDialogPanel>
  )
}
