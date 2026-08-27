import React, { useEffect, useMemo, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { Server as ServerIcon, Settings2, X } from 'lucide-react'
import { SettingsGroup, SettingsSwitchRow } from '../settings/SettingsLayout'
import { MotionDialogPanel } from '../ui/motion'
import { Switch } from '../ui/switch'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>
type ServerSummary = { id: number; name?: string; status?: string }
type ServerRemoteAccess = ServerSummary & { remote: boolean; mcp: boolean; error?: string }
const noServers: ServerSummary[] = []

function settingEnabled(value: unknown, fallback = false) {
  if (value === undefined || value === null || value === '') return fallback
  return value === true || value === 'true' || value === 1 || value === '1'
}

function mcpEnabled(settings: any) {
  return settingEnabled(settings?.mcp_remote_operations_enabled)
    && settingEnabled(settings?.mcp_structured_exec_enabled)
    && settingEnabled(settings?.mcp_raw_shell_enabled)
}

function serverPolicy(view: any, server: ServerSummary): ServerRemoteAccess {
  return {
    ...server,
    remote: Boolean(view?.server?.remote_terminal_enabled),
    mcp: Boolean(view?.server?.mcp_remote_operations_enabled)
      && Boolean(view?.server?.mcp_structured_exec_enabled)
      && Boolean(view?.server?.mcp_raw_shell_enabled),
  }
}

export function RemoteAccessSettings({ data, client, load, notify }: { data: any; client: { request: RequestFn }; load: () => Promise<void>; notify: (message: string, tone?: string) => void }) {
  const [terminal, setTerminal] = useState(settingEnabled(data.settings?.remote_terminal_enabled, true))
  const [mcp, setMCP] = useState(mcpEnabled(data.settings))
  const [passwordConfirmation, setPasswordConfirmation] = useState(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
  const [saving, setSaving] = useState('')
  const [serversOpen, setServersOpen] = useState(false)

  useEffect(() => {
    setTerminal(settingEnabled(data.settings?.remote_terminal_enabled, true))
    setMCP(mcpEnabled(data.settings))
    setPasswordConfirmation(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
  }, [data.settings?.remote_terminal_enabled, data.settings?.mcp_remote_operations_enabled, data.settings?.mcp_structured_exec_enabled, data.settings?.mcp_raw_shell_enabled, data.settings?.remote_terminal_password_confirmation_enabled])

  const save = async (key: string, body: Record<string, boolean>, success: string) => {
    if (saving) return
    setSaving(key)
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify(body) })
      await load()
      notify(success, 'success')
    } catch (error: any) {
      setTerminal(settingEnabled(data.settings?.remote_terminal_enabled, true))
      setMCP(mcpEnabled(data.settings))
      setPasswordConfirmation(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
      notify(error?.message || '保存失败', 'error')
    } finally {
      setSaving('')
    }
  }

  return (
    <>
      <SettingsGroup
        title="远程控制"
        description="统一管理 WebSSH 和 MCP 控制。"
        actions={<button type="button" className="ghost remote-access-server-button" onClick={() => setServersOpen(true)}><Settings2 size={14} aria-hidden="true" />管理服务器</button>}
      >
        <SettingsSwitchRow
          label="启用远程控制"
          description="允许管理员使用 WebSSH。"
          checked={terminal}
          onChange={checked => {
            setTerminal(checked)
            if (!checked) setMCP(false)
            void save('terminal', { remote_terminal_enabled: checked }, checked ? '远程控制已开启' : '远程控制已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用远程控制"
        />
        <SettingsSwitchRow
          label="启用 MCP 控制"
          description="允许授权的 MCP 客户端管理服务器。"
          checked={mcp}
          onChange={checked => {
            setMCP(checked)
            if (checked) setTerminal(true)
            void save('mcp', { mcp_remote_operations_enabled: checked }, checked ? 'MCP 控制已开启' : 'MCP 控制已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 MCP 控制"
        />
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
        {serversOpen ? <RemoteAccessServerDialog servers={Array.isArray(data.servers) ? data.servers : noServers} client={client} notify={notify} onClose={() => setServersOpen(false)} /> : null}
      </AnimatePresence>
    </>
  )
}

function RemoteAccessServerDialog({ servers, client, notify, onClose }: { servers: ServerSummary[]; client: { request: RequestFn }; notify: (message: string, tone?: string) => void; onClose: () => void }) {
  const [rows, setRows] = useState<ServerRemoteAccess[]>([])
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    const read = async () => {
      setLoading(true)
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
    }
    void read()
    return () => { cancelled = true }
  }, [servers, client])

  const selectedRows = useMemo(() => rows.filter(row => selected.has(row.id)), [rows, selected])
  const allSelected = rows.length > 0 && selected.size === rows.length

  const patchRows = async (targets: ServerRemoteAccess[], change: { remote?: boolean; mcp?: boolean }, success: string) => {
    if (busy || targets.length === 0) return
    setBusy(true)
    let failures = 0
    const updated = new Map<number, ServerRemoteAccess>()
    await Promise.all(targets.map(async row => {
      const nextRemote = change.remote ?? (change.mcp === true ? true : row.remote)
      const nextMCP = change.remote === false ? false : (change.mcp ?? row.mcp)
      try {
        const result = await client.request(`/servers/${row.id}/remote-access`, {
          method: 'PATCH',
          body: JSON.stringify({ remote_terminal_enabled: nextRemote, mcp_remote_operations_enabled: nextMCP }),
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

  return (
    <MotionDialogPanel onCancel={onClose} className="remote-access-server-dialog" aria-labelledby="remote-access-server-title">
      <header className="dialog-head">
        <div><h2 id="remote-access-server-title">服务器远程控制</h2><p className="muted">逐台修改，或勾选后批量设置。</p></div>
        <button type="button" className="ghost dialog-close icon-button" onClick={onClose} disabled={busy} aria-label="关闭" title="关闭"><X size={16} /></button>
      </header>
      <div className="dialog-body remote-access-server-body">
        {loading ? <p className="muted remote-access-server-loading">正在读取服务器设置…</p> : rows.length === 0 ? <div className="remote-access-server-empty"><ServerIcon size={20} aria-hidden="true" /><p>暂无服务器</p></div> : <>
          <div className="remote-access-bulk-bar">
            <label><input type="checkbox" checked={allSelected} onChange={() => setSelected(allSelected ? new Set() : new Set(rows.map(row => row.id)))} disabled={busy} />全选</label>
            <span>{selected.size > 0 ? `已选 ${selected.size} 台` : '选择服务器后批量设置'}</span>
            <div>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote: true }, '已批量开启远程控制')}>开启远程</button>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote: false }, '已批量关闭远程控制')}>关闭远程</button>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp: true }, '已批量开启 MCP 控制')}>开启 MCP</button>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp: false }, '已批量关闭 MCP 控制')}>关闭 MCP</button>
            </div>
          </div>
          <div className="remote-access-server-list">
            <div className="remote-access-server-row remote-access-server-row-head" aria-hidden="true"><span /><span>服务器</span><span>远程控制</span><span>MCP 控制</span></div>
            {rows.map(row => <div className="remote-access-server-row" key={row.id}>
              <input type="checkbox" checked={selected.has(row.id)} onChange={() => toggleSelected(row.id)} disabled={busy} aria-label={`选择 ${row.name || `服务器 ${row.id}`}`} />
              <div className="remote-access-server-name"><strong>{row.name || `服务器 ${row.id}`}</strong><span>{row.status === 'online' ? '在线' : row.status === 'offline' ? '离线' : '未连接'}{row.error ? ` · ${row.error}` : ''}</span></div>
              <Switch checked={row.remote} disabled={busy} onChange={checked => void patchRows([row], { remote: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}远程控制`} />
              <Switch checked={row.mcp} disabled={busy} onChange={checked => void patchRows([row], { mcp: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`} MCP 控制`} />
            </div>)}
          </div>
        </>}
      </div>
      <footer className="dialog-actions"><button type="button" onClick={onClose} disabled={busy}>{busy ? '保存中…' : '完成'}</button></footer>
    </MotionDialogPanel>
  )
}
