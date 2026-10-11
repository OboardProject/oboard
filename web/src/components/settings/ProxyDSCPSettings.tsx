import React, { useCallback, useEffect, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { Settings2, X } from 'lucide-react'
import { SettingsGroup, SettingsSwitchRow } from './SettingsLayout'
import { MotionDialogPanel } from '../ui/motion'
import { Switch } from '../ui/switch'
import { FieldHelp } from '../ui/form-field'

type Request = (path: string, init?: RequestInit) => Promise<any>
type Client = { request: Request; requestV2: Request }
type Props = { data: any; client: Client; load: () => Promise<void>; notify: (message: string, tone?: string) => void }
type Row = { id: number; name: string; enabled: boolean; effective_enabled: boolean; state: string; error?: string; report?: { error?: string; excluded?: string[] } }
const on = (value: unknown) => value === true || value === 'true'
const states: Record<string, string> = { pending: '等待同步', offline: '离线，等待同步', unsupported: '需要更新 Agent／内核或当前平台不支持', failed: '同步失败', partial: '部分支持', disabled: '' }
const coverage = 'EF（46）标记已支持的代理 TCP／原生 UDP socket。HY2（Hysteria2）暂不支持 DSCP 标记。SSH、realm、WARP 不在本功能范围内，直接路由动作也未覆盖。切换后新 TCP 连接生效，已有 TCP 自然结束；共享 UDP 立即更新。实际优先级由网络设备决定。'

export function ProxyDSCPSettings({ data, client, load, notify }: Props) {
  const [enabled, setEnabled] = useState(on(data.settings?.proxy_dscp_enabled))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [open, setOpen] = useState(false)
  useEffect(() => setEnabled(on(data.settings?.proxy_dscp_enabled)), [data.settings?.proxy_dscp_enabled])
  const save = async (value: boolean) => {
    if (saving) return
    setSaving(true); setError('')
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ proxy_dscp_enabled: value }) })
      setEnabled(value); await load()
      notify('DSCP 设置已保存，等待节点同步', 'success')
    } catch (e: any) { setError(e?.message || '保存失败，请重试') }
    finally { setSaving(false) }
  }
  return <>
    <SettingsGroup title="代理流量优先标记" actions={<><FieldHelp label="代理 DSCP 覆盖范围" hint={coverage} /><button type="button" className="ghost remote-access-server-button" onClick={() => setOpen(true)}><Settings2 size={14} aria-hidden="true" />管理服务器</button></>}>
      <SettingsSwitchRow label="全局启用 DSCP" description="开启覆盖所有服务器；关闭后按逐台设置生效。HY2（Hysteria2）暂不支持。" checked={enabled} disabled={saving} onChange={value => void save(value)} ariaLabel="全局启用代理 DSCP" />
      {error ? <p role="alert">{error}</p> : null}
    </SettingsGroup>
    <AnimatePresence>{open ? <ServerDialog client={client} global={enabled} notify={notify} onClose={() => setOpen(false)} /> : null}</AnimatePresence>
  </>
}

function ServerDialog({ client, global, notify, onClose }: { client: Client; global: boolean; notify: Props['notify']; onClose: () => void }) {
  const [rows, setRows] = useState<Row[]>([])
  const [mutationErrors, setMutationErrors] = useState<Record<number, string>>({})
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      const listed = await client.request('/servers', { signal })
      const servers: { id: number; name: string }[] = Array.isArray(listed) ? listed : listed.servers || []
      const next: Row[] = new Array(servers.length)
      let cursor = 0
      await Promise.all(Array.from({ length: Math.min(4, servers.length) }, async () => {
        while (cursor < servers.length && !signal?.aborted) {
          const index = cursor++, server = servers[index]
          try { next[index] = { ...server, ...await client.requestV2('/servers/' + server.id + '/proxy-dscp', { signal }) } }
          catch (e: any) { next[index] = { ...server, enabled: false, effective_enabled: false, state: 'failed', error: e?.message || '读取失败' } }
        }
      }))
      if (!signal?.aborted) { setRows(next); setError('') }
    } catch (e: any) { if (!signal?.aborted) setError(e?.message || '无法读取服务器设置') }
    finally { if (!signal?.aborted) setLoading(false) }
  }, [client])
  useEffect(() => {
    if (busy) return
    const abort = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const poll = async () => { await refresh(abort.signal); if (!abort.signal.aborted) timer = setTimeout(poll, 10000) }
    void poll()
    return () => { abort.abort(); clearTimeout(timer) }
  }, [refresh, busy])
  const patch = async (targets: Row[], enabled?: boolean) => {
    if (busy || targets.length === 0) return
    setBusy(true)
    let cursor = 0, failures = 0
    const errors = new Map<number, string>()
    await Promise.all(Array.from({ length: Math.min(4, targets.length) }, async () => {
      while (cursor < targets.length) {
        const row = targets[cursor++]
        try { await client.requestV2('/servers/' + row.id + '/proxy-dscp', { method: 'POST', body: JSON.stringify({ ...(enabled === undefined ? { retry: true } : { enabled }), request_id: crypto.randomUUID() }) }) }
        catch (e: any) { failures++; errors.set(row.id, e?.message || '保存失败，请重试') }
      }
    }))
    await refresh()
    setMutationErrors(current => { const next = { ...current }; for (const row of targets) { if (errors.has(row.id)) next[row.id] = errors.get(row.id)!; else delete next[row.id] } return next })
    notify(failures ? failures + ' 台服务器保存失败' : 'DSCP 设置已保存，等待节点同步', failures ? 'error' : 'success')
    setBusy(false)
  }
  const selectable = rows.filter(row => !row.error)
  const toggle = (id: number) => setSelected(current => { const next = new Set(current); next.has(id) ? next.delete(id) : next.add(id); return next })
  return <MotionDialogPanel onCancel={() => { if (!busy) onClose() }} className="remote-access-server-dialog" aria-labelledby="proxy-dscp-title">
    <header className="dialog-head"><div className="settings-heading"><h2 id="proxy-dscp-title">代理流量优先标记</h2><FieldHelp label="代理 DSCP 覆盖范围" hint={coverage} /></div><button type="button" className="ghost icon-button dialog-close" autoFocus disabled={busy} onClick={onClose} aria-label="关闭"><X size={16} aria-hidden="true" /></button></header>
    <div className="dialog-body" aria-busy={loading || busy}>
      {global ? <p className="muted">全局设置已覆盖逐台设置；关闭全局后可编辑。</p> : null}
      {error ? <p role="alert">{error}</p> : null}
      {loading ? <p role="status">正在读取服务器设置…</p> : rows.length === 0 ? <p>暂无服务器</p> : <>
        {!global ? <div className="remote-access-bulk-bar"><label><input type="checkbox" disabled={busy || selectable.length === 0} checked={selectable.length > 0 && selectable.every(row => selected.has(row.id))} onChange={e => setSelected(e.target.checked ? new Set(selectable.map(row => row.id)) : new Set())} />全选</label><div><button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patch(selectable.filter(row => selected.has(row.id)), true)}>开启</button><button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void patch(selectable.filter(row => selected.has(row.id)), false)}>关闭</button></div></div> : null}
        <ul className="proxy-dscp-list">{rows.map(row => <li key={row.id}>
          {!global ? <input type="checkbox" aria-label={'选择 ' + row.name} disabled={busy || !!row.error} checked={selected.has(row.id)} onChange={() => toggle(row.id)} /> : null}
          <div className="proxy-dscp-name"><strong>{row.name}</strong><span className="muted" aria-live="polite">{mutationErrors[row.id] || row.error || (row.report?.error === 'policy_state_unreadable' ? 'DSCP 策略读取失败，可重试' : row.report?.error ? '部分 Socket 标记失败，可重试' : row.report?.excluded?.includes('ipv6-mptcp') ? '部分支持：IPv6 MPTCP 的 DSCP 受内核限制' : states[row.state]) || ''}</span></div>
          {mutationErrors[row.id] || row.state === 'failed' || row.report?.error ? <button type="button" className="ghost" disabled={busy} onClick={() => void patch([row])}>重试</button> : null}
          <Switch ariaLabel={row.name + '代理 DSCP'} checked={global || row.enabled} disabled={busy || global || !!row.error} onChange={value => void patch([row], value)} />
        </li>)}</ul>
      </>}
    </div>
    <footer className="dialog-actions"><button type="button" disabled={busy} onClick={onClose}>完成</button></footer>
  </MotionDialogPanel>
}
