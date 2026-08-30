import React, { useEffect, useMemo, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { Server as ServerIcon, Settings2, X } from 'lucide-react'
import { SettingsGroup, SettingsSwitchRow } from '../settings/SettingsLayout'
import { MotionDialogPanel } from '../ui/motion'
import { Switch } from '../ui/switch'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>
type ServerSummary = { id: number; name?: string; status?: string }
type ServerRemoteAccess = ServerSummary & { remote: boolean; operations: boolean; exec: boolean; shell: boolean; interactive: boolean; error?: string }
const noServers: ServerSummary[] = []

function settingEnabled(value: unknown, fallback = false) {
  if (value === undefined || value === null || value === '') return fallback
  return value === true || value === 'true' || value === 1 || value === '1'
}

function serverPolicy(view: any, server: ServerSummary): ServerRemoteAccess {
  return {
    ...server,
    remote: Boolean(view?.server?.remote_terminal_enabled),
    operations: Boolean(view?.server?.mcp_remote_operations_enabled),
    exec: Boolean(view?.server?.mcp_structured_exec_enabled),
    shell: Boolean(view?.server?.mcp_raw_shell_enabled),
    interactive: Boolean(view?.server?.mcp_interactive_terminal_enabled),
  }
}

export function RemoteAccessSettings({ data, client, load, notify }: { data: any; client: { request: RequestFn }; load: () => Promise<void>; notify: (message: string, tone?: string) => void }) {
  const [terminal, setTerminal] = useState(settingEnabled(data.settings?.remote_terminal_enabled, true))
  const [operations, setOperations] = useState(settingEnabled(data.settings?.mcp_remote_operations_enabled, false))
  const [exec, setExec] = useState(settingEnabled(data.settings?.mcp_structured_exec_enabled, false))
  const [shell, setShell] = useState(settingEnabled(data.settings?.mcp_raw_shell_enabled, false))
  const [interactive, setInteractive] = useState(settingEnabled(data.settings?.mcp_interactive_terminal_enabled, false))
  const [passwordConfirmation, setPasswordConfirmation] = useState(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
  const [saving, setSaving] = useState('')
  const [serversOpen, setServersOpen] = useState(false)

  useEffect(() => {
    setTerminal(settingEnabled(data.settings?.remote_terminal_enabled, true))
    setOperations(settingEnabled(data.settings?.mcp_remote_operations_enabled, false))
    setExec(settingEnabled(data.settings?.mcp_structured_exec_enabled, false))
    setShell(settingEnabled(data.settings?.mcp_raw_shell_enabled, false))
    setInteractive(settingEnabled(data.settings?.mcp_interactive_terminal_enabled, false))
    setPasswordConfirmation(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
  }, [data.settings?.remote_terminal_enabled, data.settings?.mcp_remote_operations_enabled, data.settings?.mcp_structured_exec_enabled, data.settings?.mcp_raw_shell_enabled, data.settings?.mcp_interactive_terminal_enabled, data.settings?.remote_terminal_password_confirmation_enabled])

  const save = async (key: string, body: Record<string, boolean>, success: string) => {
    if (saving) return
    setSaving(key)
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify(body) })
      await load()
      notify(success, 'success')
    } catch (error: any) {
      setTerminal(settingEnabled(data.settings?.remote_terminal_enabled, true))
      setOperations(settingEnabled(data.settings?.mcp_remote_operations_enabled, false))
      setExec(settingEnabled(data.settings?.mcp_structured_exec_enabled, false))
      setShell(settingEnabled(data.settings?.mcp_raw_shell_enabled, false))
      setInteractive(settingEnabled(data.settings?.mcp_interactive_terminal_enabled, false))
      setPasswordConfirmation(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
      notify(error?.message || '保存失败', 'error')
    } finally {
      setSaving('')
    }
  }

  return (
    <>
      <SettingsGroup
        title="远程访问"
        description="独立管理 Web 终端与 MCP 主机控制。"
        actions={<button type="button" className="ghost remote-access-server-button" onClick={() => setServersOpen(true)}><Settings2 size={14} aria-hidden="true" />管理服务器</button>}
      >
        <SettingsSwitchRow
          label="Web 远程终端"
          description="允许管理员通过浏览器打开 Agent PTY。"
          checked={terminal}
          onChange={checked => {
            setTerminal(checked)
            void save('terminal', { remote_terminal_enabled: checked }, checked ? '远程控制已开启' : '远程控制已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 Web 远程终端"
        />
        <SettingsSwitchRow
          label="MCP 结构化主机操作"
          description="允许授权 MCP 客户端执行受控的服务器查询与运维操作。"
          checked={operations}
          onChange={checked => {
            setOperations(checked)
            void save('mcp_operations', { mcp_remote_operations_enabled: checked }, checked ? 'MCP 结构化操作已开启' : 'MCP 结构化操作已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 MCP 结构化操作"
        />
        <SettingsSwitchRow
          label="MCP 结构化命令执行"
          description="允许授权 MCP 以 argv 方式执行命令（不经过 shell）。"
          checked={exec}
          onChange={checked => {
            setExec(checked)
            void save('mcp_exec', { mcp_structured_exec_enabled: checked }, checked ? 'MCP 结构化执行已开启' : 'MCP 结构化执行已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 MCP 结构化执行"
        />
        <SettingsSwitchRow
          label="MCP 原始 Shell"
          description="允许授权 MCP 以 /bin/sh -c 执行 shell 表达式。"
          checked={shell}
          onChange={checked => {
            setShell(checked)
            void save('mcp_shell', { mcp_raw_shell_enabled: checked }, checked ? 'MCP 原始 Shell 已开启' : 'MCP 原始 Shell 已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 MCP 原始 Shell"
        />
        <SettingsSwitchRow
          label="MCP 交互式终端"
          description="允许授权 MCP 打开交互式 PTY（最高风险）。"
          checked={interactive}
          onChange={checked => {
            setInteractive(checked)
            void save('mcp_interactive', { mcp_interactive_terminal_enabled: checked }, checked ? 'MCP 交互式终端已开启' : 'MCP 交互式终端已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 MCP 交互式终端"
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
          return { ...server, remote: true, operations: false, exec: false, shell: false, interactive: false, error: error?.message || '读取失败' }
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
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: true }, '已批量开启远程控制')}>开启远程</button>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: false }, '已批量关闭远程控制')}>关闭远程</button>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_remote_operations_enabled: true }, '已批量开启 MCP 操作')}>开启操作</button>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_structured_exec_enabled: true }, '已批量开启 MCP 执行')}>开启执行</button>
              <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_interactive_terminal_enabled: true }, '已批量开启 MCP 终端')}>开启终端</button>
            </div>
          </div>
          <div className="remote-access-server-list">
            <div className="remote-access-server-row remote-access-server-row-head" aria-hidden="true"><span /><span>服务器</span><span>远程</span><span>操作</span><span>执行</span><span>Shell</span><span>终端</span></div>
            {rows.map(row => <div className="remote-access-server-row" key={row.id}>
              <input type="checkbox" checked={selected.has(row.id)} onChange={() => toggleSelected(row.id)} disabled={busy} aria-label={`选择 ${row.name || `服务器 ${row.id}`}`} />
              <div className="remote-access-server-name"><strong>{row.name || `服务器 ${row.id}`}</strong><span>{row.status === 'online' ? '在线' : row.status === 'offline' ? '离线' : '未连接'}{row.error ? ` · ${row.error}` : ''}</span></div>
              <Switch checked={row.remote} disabled={busy} onChange={checked => void patchRows([row], { remote_terminal_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}远程`} />
              <Switch checked={row.operations} disabled={busy} onChange={checked => void patchRows([row], { mcp_remote_operations_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}操作`} />
              <Switch checked={row.exec} disabled={busy} onChange={checked => void patchRows([row], { mcp_structured_exec_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}执行`} />
              <Switch checked={row.shell} disabled={busy} onChange={checked => void patchRows([row], { mcp_raw_shell_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}Shell`} />
              <Switch checked={row.interactive} disabled={busy} onChange={checked => void patchRows([row], { mcp_interactive_terminal_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}终端`} />
            </div>)}
          </div>
        </>}
      </div>
      <footer className="dialog-actions"><button type="button" onClick={onClose} disabled={busy}>{busy ? '保存中…' : '完成'}</button></footer>
    </MotionDialogPanel>
  )
}
