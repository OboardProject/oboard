import React, { useEffect, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { Server as ServerIcon, Settings2, X, ChevronDown } from 'lucide-react'
import { SettingsGroup, SettingsSwitchRow } from '../settings/SettingsLayout'
import { MotionDialogPanel } from '../ui/motion'
import { Switch } from '../ui/switch'
import { FieldHelp } from '../ui/form-field'
import { StepUpAuth } from './StepUpAuth'

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
  const effectiveRemote = globalTerminal || remote
  const effectiveMcp = globalMcp || mcp
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
  const [pendingPasswordConfirmation, setPendingPasswordConfirmation] = useState<boolean | null>(null)
  const [serversOpen, setServersOpen] = useState(false)

  useEffect(() => {
    setPasswordConfirmation(settingEnabled(data.settings?.remote_terminal_password_confirmation_enabled, true))
  }, [data.settings?.remote_terminal_password_confirmation_enabled])

  const save = async (checked: boolean, token: string) => {
    if (saving) return
    setSaving('password')
    setPendingPasswordConfirmation(null)
    try {
      const body = { remote_terminal_password_confirmation_enabled: checked, step_up_token: token }
      await client.request('/settings', { method: 'POST', body: JSON.stringify(body) })
      setPasswordConfirmation(checked)
      await load()
      notify(checked ? 'WebSSH 密码确认已开启' : 'WebSSH 密码确认已关闭', 'success')
    } catch (error: any) {
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
        description="管理终端和 MCP 客户端可以访问的服务器。"
        actions={<button type="button" className="ghost remote-access-server-button" onClick={openServers}><Settings2 size={14} aria-hidden="true" />管理服务器</button>}
      >
        <SettingsSwitchRow
          label="WebSSH 密码确认"
          description="打开终端前验证管理员身份。修改此项也需要验证。"
          checked={passwordConfirmation}
          onChange={checked => setPendingPasswordConfirmation(checked)}
          disabled={Boolean(saving) || pendingPasswordConfirmation !== null}
          ariaLabel="打开 WebSSH 前确认密码"
        />
      </SettingsGroup>
      {pendingPasswordConfirmation !== null ? <StepUpAuth
        request={client.request}
        purpose="remote_terminal_settings"
        resourceType="setting"
        resourceId={`remote_terminal_password_confirmation_enabled:${pendingPasswordConfirmation}`}
        title={pendingPasswordConfirmation ? '开启 WebSSH 密码确认' : '关闭 WebSSH 密码确认'}
        warning="更改此设置前，请再次验证管理员身份。"
        onComplete={token => void save(pendingPasswordConfirmation, token)}
        onCancel={() => setPendingPasswordConfirmation(null)}
      /> : null}
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
  const [listOpen, setListOpen] = useState(false)

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
      effectiveRemote: globalTerminal || row.remote,
      effectiveMcp: globalMcp || row.mcp,
    })))
  }, [globalTerminal, globalMcp])

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
      return { ...next, effectiveRemote: globalTerminal || next.remote, effectiveMcp: globalMcp || next.mcp }
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

  return (
    <MotionDialogPanel onCancel={onClose} className="remote-access-server-dialog" aria-labelledby="remote-access-server-title">
      <header className="dialog-head">
        <div className="settings-heading"><h2 id="remote-access-server-title">服务器远程控制</h2><FieldHelp label="服务器远程控制" hint="全局开启时覆盖现有和新接入服务器；关闭全局后按各服务器的设置生效。" /></div>
        <button type="button" className="ghost dialog-close icon-button" onClick={onClose} disabled={controlsLocked} aria-label="关闭" title="关闭"><X size={16} /></button>
      </header>
      <div className="dialog-body remote-access-server-body">
        <div className="remote-access-global-bar">
          <div className="remote-access-global-item">
            <div>
              <strong>Web 远程终端</strong>
              <FieldHelp label="Web 远程终端" hint="开启后对所有现有和新接入服务器生效。" />
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
              <FieldHelp label="MCP 远程控制" hint="开启后对所有现有和新接入服务器生效，仍需 MCP 授权。" />
            </div>
            <Switch
              checked={globalMcp}
              disabled={controlsLocked}
              onChange={checked => void saveGlobal('mcp', { mcp_enabled: checked }, checked, checked ? 'MCP 远程控制已全局开启' : 'MCP 远程控制已全局关闭')}
              ariaLabel="全局启用 MCP 远程控制"
            />
          </div>
        </div>
        <p className="remote-access-policy-note">全局开启覆盖所有现有和新接入服务器；关闭后按逐台设置生效。</p>
        {loading ? <p className="muted remote-access-server-loading">正在读取服务器设置…</p> : loadError ? <div className="remote-access-server-empty"><ServerIcon size={20} aria-hidden="true" /><p>{loadError}</p></div> : rows.length === 0 ? <div className="remote-access-server-empty"><ServerIcon size={20} aria-hidden="true" /><p>暂无服务器</p></div> : <>
          <button type="button" className="remote-access-list-toggle" aria-expanded={listOpen} aria-controls="remote-access-server-controls" onClick={() => setListOpen(open => !open)}>
            <span>逐台设置 <span className="muted">{rows.length} 台服务器</span></span>
            <ChevronDown size={18} aria-hidden="true" />
          </button>
          {listOpen ? <div id="remote-access-server-controls" className="remote-access-list-content">
          {globalTerminal && globalMcp ? <p className="remote-access-list-note">两项全局设置已覆盖逐台设置。关闭对应全局开关后可编辑。</p> : null}
          {!globalTerminal || !globalMcp ? <div className="remote-access-bulk-bar">
            <label><input type="checkbox" checked={allSelected} onChange={() => setSelected(allSelected ? new Set() : new Set(rows.map(row => row.id)))} disabled={controlsLocked} />全选</label>
            <span>{selected.size > 0 ? `已选 ${selected.size} / ${rows.length} 台` : `共 ${rows.length} 台服务器`}</span>
            <div>
              {!globalTerminal ? <><button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: true }, '已批量开启远程控制')}>开启远程</button>
              <button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { remote_terminal_enabled: false }, '已批量关闭远程控制')}>关闭远程</button></> : null}
              {!globalMcp ? <><button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_enabled: true }, '已批量开启 MCP')}>开启 MCP</button>
              <button type="button" className="ghost" disabled={controlsLocked || selected.size === 0} onClick={() => void patchRows(selectedRows, { mcp_enabled: false }, '已批量关闭 MCP')}>关闭 MCP</button></> : null}
            </div>
          </div> : null}
          <div className={globalTerminal && globalMcp ? 'remote-access-server-list is-global' : 'remote-access-server-list'}>
            <div className="remote-access-server-row remote-access-server-row-head" aria-hidden="true"><span /><span>服务器</span><span>远程</span><span>MCP 远程控制</span></div>
            {rows.map(row => <div className="remote-access-server-row" key={row.id}>
              <input type="checkbox" checked={selected.has(row.id)} onChange={() => toggleSelected(row.id)} disabled={controlsLocked || (globalTerminal && globalMcp)} aria-label={`选择 ${row.name || `服务器 ${row.id}`}`} />
              <div className="remote-access-server-name"><strong>{row.name || `服务器 ${row.id}`}</strong><span>{row.status === 'online' ? '在线' : row.status === 'offline' ? '离线' : '未连接'}{row.error ? ` · ${row.error}` : ''}</span></div>
              <div className="remote-access-server-control">
                <span className="remote-access-mobile-label">Web 终端</span>
                <Switch checked={row.effectiveRemote} disabled={controlsLocked || globalTerminal} onChange={checked => void patchRows([row], { remote_terminal_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}远程`} />
              </div>
              <div className="remote-access-server-control">
                <span className="remote-access-mobile-label">MCP 控制</span>
                <Switch checked={row.effectiveMcp} disabled={controlsLocked || globalMcp} onChange={checked => void patchRows([row], { mcp_enabled: checked }, `${row.name || `服务器 ${row.id}`} 已更新`)} ariaLabel={`${row.name || `服务器 ${row.id}`}MCP`} />
              </div>
            </div>)}
          </div>
          </div> : null}
        </>}
      </div>
      <footer className="dialog-actions"><button type="button" onClick={onClose} disabled={controlsLocked}>{busy ? '保存中…' : '完成'}</button></footer>
    </MotionDialogPanel>
  )
}
