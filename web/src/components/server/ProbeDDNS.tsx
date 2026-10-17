import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { ChevronRight, Pencil, Plus, RefreshCw, Search, Trash2 } from 'lucide-react'
import { RegionFlag, serverRegionCode } from '../ui/RegionFlag'
import { Dialog } from '../ui/dialog'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
import type { Server } from '../proxy-path/types'
import { ddnsAddressChoices, ddnsPrefixFromAddress, type DDNSInterface } from './ddns-addresses'
import { ServerPickerSelect } from './ServerPickerSelect'

type Client = { request: (path: string, init?: RequestInit) => Promise<any>; requestV2?: (path: string, init?: RequestInit) => Promise<any> }
type Credential = { id: number; name: string; provider: string; enabled: boolean; verified_at?: string; zones?: { zone_name: string }[] }
export type ProbeDDNSRecord = {
  id: number
  server_id: number
  revision: number
  name: string
  dns_credential_id?: number
  domain: string
  record_types: 'a' | 'aaaa' | 'both'
  proxy_enabled: boolean
  interval_seconds: number
  enabled: boolean
  interface_name?: string
  interface_prefix?: string
  interface_address?: string
  address_family?: '' | 'ipv4' | 'ipv6'
  exclude_private: boolean
  status?: string
  error?: string
  current_address?: string
  last_synced_at?: string
}

const emptyRecord = (serverID = 0): ProbeDDNSRecord => ({
  id: 0, server_id: serverID, revision: 0, name: '', domain: '', record_types: 'a', proxy_enabled: false,
  interval_seconds: 300, enabled: true, address_family: '', exclude_private: false,
})

const statusText: Record<string, string> = { pending: '等待更新', running: '更新中', ok: '已更新', failed: '更新失败' }

async function v2(client: Client, path: string, init?: RequestInit) {
  if (client.requestV2) return client.requestV2(path, init)
  const result = await client.request(path, init)
  return result.data ?? result
}

const formatInterval = (seconds: number) => {
  if (!seconds) return '未设置'
  if (seconds % 3600 === 0) return `每 ${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `每 ${seconds / 60} 分钟`
  return `每 ${seconds} 秒`
}

const addressParts = (text?: string) => (text || '').split(/[，,]/).map(item => item.trim()).filter(Boolean).map(item => {
  const [type, ...rest] = item.split(/\s+/)
  return rest.length && /^(A|AAAA)$/i.test(type) ? { type: type.toUpperCase(), value: rest.join(' ') } : { type: '', value: item }
})

type ServerGroup = { id: number; server?: Server; name: string; records: ProbeDDNSRecord[] }

export function ProbeDDNS({ client, servers, credentials, canManage }: { client: Client; servers: Server[]; credentials: Credential[]; canManage: boolean }) {
  const [records, setRecords] = useState<ProbeDDNSRecord[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<{ record: ProbeDDNSRecord; serverLocked: boolean } | null>(null)
  const [deleting, setDeleting] = useState<ProbeDDNSRecord | null>(null)
  const [query, setQuery] = useState('')
  const [scope, setScope] = useState<'configured' | 'all'>('configured')
  const [collapsed, setCollapsed] = useState<Set<number>>(() => new Set())
  const refresh = useCallback(async () => {
    try {
      const result = await v2(client, '/probe-ddns')
      setRecords(result.records || [])
      setError('')
    } catch (cause: any) {
      setError(cause?.message || 'DDNS 记录加载失败')
    } finally {
      setLoading(false)
    }
  }, [client])
  useEffect(() => { void refresh() }, [refresh])
  const save = async (record: ProbeDDNSRecord) => {
    setBusy(true)
    setError('')
    try {
      await v2(client, '/probe-ddns', { method: 'POST', body: JSON.stringify({ action: 'save', request_id: crypto.randomUUID(), input: inputOf(record) }) })
      setEditing(null)
      setNotice(record.id ? 'DDNS 记录已保存。' : 'DDNS 记录已创建。')
      setCollapsed(current => { const next = new Set(current); next.delete(record.server_id); return next })
      await refresh()
    } catch (cause: any) {
      setError(cause?.message || '保存失败，请重试')
      throw cause
    } finally {
      setBusy(false)
    }
  }
  const mutate = async (action: 'run' | 'delete', record: ProbeDDNSRecord) => {
    setBusy(true)
    setError('')
    try {
      await v2(client, '/probe-ddns', { method: 'POST', body: JSON.stringify({ action, request_id: crypto.randomUUID(), input: { id: record.id, server_id: record.server_id, revision: record.revision } }) })
      setDeleting(null)
      setNotice(action === 'run' ? `已安排更新「${record.name}」。` : 'DDNS 记录已删除。')
      await refresh()
    } catch (cause: any) {
      setError(cause?.message || '操作失败，请重试')
    } finally {
      setBusy(false)
    }
  }
  const groups = useMemo<ServerGroup[]>(() => {
    const byServer = new Map<number, ProbeDDNSRecord[]>()
    for (const record of records) byServer.set(record.server_id, [...(byServer.get(record.server_id) || []), record])
    const known = servers.map(server => ({ id: server.id, server, name: server.name, records: byServer.get(server.id) || [] }))
    const orphaned = [...byServer.keys()].filter(id => !servers.some(server => server.id === id)).map(id => ({ id, name: `服务器 #${id}`, records: byServer.get(id) || [] }))
    return [...known, ...orphaned]
  }, [records, servers])
  const needle = query.trim().toLowerCase()
  const visibleGroups = groups.flatMap(group => {
    if (scope === 'configured' && !group.records.length) return []
    if (!needle || group.name.toLowerCase().includes(needle)) return [group]
    const matched = group.records.filter(record => `${record.name} ${record.domain} ${record.current_address || ''}`.toLowerCase().includes(needle))
    return matched.length ? [{ ...group, records: matched }] : []
  })
  const configuredServers = groups.filter(group => group.records.length).length
  const failedCount = records.filter(record => record.enabled && record.status === 'failed').length
  const toggle = (id: number) => setCollapsed(current => {
    const next = new Set(current)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  })
  const create = (serverID: number, serverLocked: boolean) => setEditing({ record: emptyRecord(serverID), serverLocked })
  return <div className="return-latency-tasks probe-ddns">
    <div className="return-latency-section-head">
      <div><h3>DDNS</h3><p className="muted">按服务器维护动态解析。入口可以选用这里的域名作为连接地址。</p></div>
      <div className="return-latency-toolbar">
        <button type="button" className="primary" disabled={!canManage || !servers.length} onClick={() => create(servers[0]?.id || 0, false)}><Plus size={15} aria-hidden="true" />新建 DDNS</button>
        <button type="button" className="ghost" disabled={busy || loading} onClick={() => { setLoading(true); void refresh() }}><RefreshCw size={15} aria-hidden="true" />刷新</button>
      </div>
    </div>
    {notice && <p className="muted" role="status">{notice}</p>}
    {error && <p className="danger-text" role="alert">{error}</p>}
    {records.length > 0 && <p className="muted probe-ddns-summary">{configuredServers} 台服务器 · {records.length} 条记录{failedCount ? <> · <span className="danger-text">{failedCount} 条更新失败</span></> : null}</p>}
    <div className="return-latency-filters">
      <div className="return-latency-search"><Search size={15} aria-hidden="true" /><input type="search" aria-label="搜索 DDNS" placeholder="搜索服务器、名称或域名" value={query} onChange={event => setQuery(event.target.value)} /></div>
      <Select aria-label="服务器范围" value={scope} onChange={event => setScope(event.target.value as 'configured' | 'all')}>
        <option value="configured">已配置 DDNS 的服务器</option>
        <option value="all">全部服务器</option>
      </Select>
    </div>
    {loading && !records.length ? <p className="muted" role="status">正在加载 DDNS 记录…</p>
      : !groups.length ? <p className="muted">暂无服务器，请先在服务器管理中添加。</p>
      : !records.length && scope === 'configured' && !needle ? <div className="latency-empty-state">
        <p>还没有 DDNS 记录</p>
        <p className="muted">选择服务器、域名账号和检查间隔。需要固定网卡地址时，先读取该服务器的网卡。</p>
        <button type="button" className="ghost" onClick={() => setScope('all')}>查看全部服务器</button>
      </div>
      : !visibleGroups.length ? <div className="latency-empty-state"><p>没有匹配的服务器或记录</p><button type="button" className="ghost" onClick={() => { setQuery(''); setScope('configured') }}>清除筛选</button></div>
      : <div className="probe-ddns-groups">
        <div className="probe-ddns-columns" aria-hidden="true"><span>名称 / 域名</span><span>当前解析 / 地址来源</span><span>检查间隔</span><span>状态</span><span className="is-actions">操作</span></div>
        {visibleGroups.map(group => {
        const open = !collapsed.has(group.id) || Boolean(needle)
        const failed = group.records.filter(record => record.enabled && record.status === 'failed').length
        const state = !group.server ? 'unregistered' : !group.server.agent_id ? 'unregistered' : group.server.status === 'online' ? 'online' : 'offline'
        const panelID = `probe-ddns-server-${group.id}`
        return <section key={group.id} className="probe-ddns-group" aria-label={group.name}>
          <header className="probe-ddns-group-head">
            <button type="button" className="probe-ddns-group-toggle" aria-expanded={open} aria-controls={panelID} onClick={() => toggle(group.id)}>
              <ChevronRight size={16} aria-hidden="true" className="probe-ddns-chevron" />
              {group.server ? <RegionFlag code={serverRegionCode(group.server)} size={18} /> : null}
              <strong>{group.name}</strong>
            </button>
            <span className={`probe-server-status ${state}`}>{!group.server ? '已移除' : state === 'online' ? '在线' : state === 'offline' ? '离线' : '未接入'}</span>
            <span className="muted probe-ddns-group-count">{group.records.length} 条</span>
            {failed ? <span className="probe-server-status offline">{failed} 条失败</span> : null}
            {group.server && <button type="button" className="ghost probe-ddns-group-add" disabled={!canManage || busy} aria-label={`为 ${group.name} 添加 DDNS`} onClick={() => create(group.id, true)}><Plus size={14} aria-hidden="true" />添加</button>}
          </header>
          {open && <div id={panelID} className="probe-ddns-group-body">
            {!group.records.length ? <p className="muted probe-ddns-group-empty">这台服务器还没有 DDNS 记录。</p> : <ul className="probe-ddns-rows" aria-label={`${group.name} 的 DDNS 记录`}>{group.records.map(record => <li key={record.id} className="probe-ddns-row">
              <div className="probe-ddns-cell is-name"><strong>{record.name} <small className="muted">#{record.id}</small></strong><span className="probe-task-target">{record.domain}</span></div>
              <div className="probe-ddns-cell">
                <span className="probe-ddns-label">当前解析</span>
                {addressParts(record.current_address).length ? <span className="probe-ddns-addresses">{addressParts(record.current_address).map(part => <span key={part.value}><span className="probe-ddns-type">{part.type}</span>{part.value}</span>)}</span> : <span className="muted">尚未解析</span>}
                <small className="muted">{record.interface_prefix ? record.interface_name + ' · 前缀 ' + record.interface_prefix : record.interface_address ? `${record.interface_name} · ${record.interface_address}` : '服务器公网地址'}{record.exclude_private ? ' · 排除内网' : ''}</small>
              </div>
              <div className="probe-ddns-cell"><span className="probe-ddns-label">检查间隔</span>{formatInterval(record.interval_seconds)}</div>
              <div className="probe-ddns-cell"><span className="probe-ddns-label">状态</span><span className={`probe-task-badge${record.enabled && record.status === 'ok' ? ' is-on' : ''}`}>{record.enabled ? (statusText[record.status || 'pending'] || record.status) : '已停用'}</span>{record.error ? <small className="danger-text">{record.error}</small> : null}</div>
              <div className="probe-ddns-cell is-actions"><div className="probe-task-card-actions">
                <button type="button" className="ghost" aria-label={`编辑 ${record.name}`} disabled={!canManage || busy} onClick={() => setEditing({ record, serverLocked: false })}><Pencil size={14} aria-hidden="true" />编辑</button>
                <button type="button" className="ghost" disabled={!canManage || busy || !record.enabled} onClick={() => void mutate('run', record)}>立即更新</button>
                <button type="button" className="ghost danger" aria-label={`删除 ${record.name}`} disabled={!canManage || busy} onClick={() => setDeleting(record)}><Trash2 size={14} aria-hidden="true" />删除</button>
              </div></div>
            </li>)}</ul>}
          </div>}
        </section>
      })}</div>}
    <Dialog isOpen={Boolean(editing)} onClose={() => { if (!busy) setEditing(null) }} title={editing?.record.id ? '编辑 DDNS' : editing?.serverLocked ? `为 ${servers.find(server => server.id === editing.record.server_id)?.name || '服务器'} 添加 DDNS` : '新建 DDNS'} size="lg">
      {editing && <ProbeDDNSForm record={editing.record} servers={servers} credentials={credentials} client={client} busy={busy} canManage={canManage} serverLocked={editing.serverLocked} onCancel={() => setEditing(null)} onSubmit={save} />}
    </Dialog>
    <Dialog isOpen={Boolean(deleting)} onClose={() => { if (!busy) setDeleting(null) }} title="删除 DDNS 记录" footer={<>
      <button type="button" className="ghost" disabled={busy} onClick={() => setDeleting(null)}>取消</button>
      <button type="button" className="danger" disabled={busy} onClick={() => { if (deleting) void mutate('delete', deleting) }}>{busy ? '删除中…' : '确认删除'}</button>
    </>}>
      <p>删除「{deleting?.name}」会同时删除域名服务商上的对应解析，并取消入口对它的引用。</p>
    </Dialog>
  </div>
}

export function InboundDDNSField({ serverID, recordID, domain, client, credentials, onChange }: {
  serverID: number
  recordID?: number
  domain?: string
  client: Client
  credentials: Credential[]
  onChange: (record: ProbeDDNSRecord | null) => void
}) {
  const [records, setRecords] = useState<ProbeDDNSRecord[]>([])
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const load = useCallback(async () => {
    if (!serverID) { setRecords([]); return }
    try {
      const result = await v2(client, '/probe-ddns')
      setRecords((result.records || []).filter((record: ProbeDDNSRecord) => record.server_id === serverID))
      setError('')
    } catch (cause: any) {
      setError(cause?.message || 'DDNS 记录加载失败')
    }
  }, [client, serverID])
  useEffect(() => { void load() }, [load])
  const selected = records.find(record => record.id === recordID)
  const create = async (record: ProbeDDNSRecord) => {
    setBusy(true)
    setError('')
    try {
      await v2(client, '/probe-ddns', { method: 'POST', body: JSON.stringify({ action: 'save', request_id: crypto.randomUUID(), input: inputOf(record) }) })
      const result = await v2(client, '/probe-ddns')
      const next = ((result.records || []) as ProbeDDNSRecord[]).filter(item => item.server_id === serverID)
      setRecords(next)
      const created = next.find(item => !records.some(previous => previous.id === item.id) && item.name === record.name.trim() && item.domain === record.domain.trim().toLowerCase().replace(/\.$/, ''))
      if (created) onChange(created)
      setCreating(false)
    } catch (cause: any) {
      setError(cause?.message || '创建失败，请重试')
      throw cause
    } finally {
      setBusy(false)
    }
  }
  return <div className="inbound-editor-grid">
    <FormField label="DDNS 连接地址" hint="选用后，订阅 Host 使用这条记录的域名。证书 SNI 不变，也不会因此开启入口解析。">
      <Select aria-label="选择 DDNS 记录" value={recordID || 0} onChange={event => {
        const id = Number(event.target.value)
        onChange(records.find(record => record.id === id) || null)
      }}>
        <option value={0}>不使用 DDNS</option>
        {records.map(record => <option key={record.id} value={record.id}>{record.name} · {record.domain}</option>)}
      </Select>
    </FormField>
    <div className="inline-field-action">
      <button type="button" className="ghost" disabled={!serverID} onClick={() => setCreating(true)}>新建 DDNS</button>
    </div>
    {selected && <p className="field-hint">当前 Host 将使用 {selected.domain}。{selected.error ? ` 最近一次更新失败：${selected.error}` : ''}</p>}
    {!selected && domain && <p className="field-hint">已选记录暂时不在这台服务器的列表中。</p>}
    {error && <p className="danger-text" role="alert">{error}</p>}
    <Dialog isOpen={creating} onClose={() => { if (!busy) setCreating(false) }} title="新建 DDNS" size="lg">
      <ProbeDDNSForm record={emptyRecord(serverID)} servers={[]} credentials={credentials} client={client} busy={busy} canManage serverLocked onCancel={() => setCreating(false)} onSubmit={create} />
    </Dialog>
  </div>
}

function inputOf(record: ProbeDDNSRecord) {
  return {
    id: record.id || undefined,
    server_id: record.server_id,
    revision: record.revision || undefined,
    name: record.name.trim(),
    dns_credential_id: record.dns_credential_id,
    domain: record.domain.trim(),
    record_types: record.record_types,
    proxy_enabled: record.proxy_enabled,
    interval_seconds: record.interval_seconds,
    enabled: record.enabled,
    interface_name: record.interface_name || '',
    interface_address: record.interface_address || '',
    interface_prefix: record.interface_prefix || '',
    address_family: record.address_family || '',
    exclude_private: record.exclude_private,
  }
}

export function ProbeDDNSForm({ record, servers, credentials, client, busy, canManage, serverLocked, onCancel, onSubmit }: {
  record: ProbeDDNSRecord
  servers: Server[]
  credentials: Credential[]
  client: Client
  busy: boolean
  canManage: boolean
  serverLocked?: boolean
  onCancel: () => void
  onSubmit: (record: ProbeDDNSRecord) => Promise<void>
}) {
  const [draft, setDraft] = useState(record)
  const [bindInterface, setBindInterface] = useState(Boolean(record.interface_name || record.interface_address || record.interface_prefix))
  const [interfaces, setInterfaces] = useState<DDNSInterface[]>([])
  const [readStatus, setReadStatus] = useState('')
  const [reading, setReading] = useState(false)
  const [formError, setFormError] = useState('')
  const [prefixMode, setPrefixMode] = useState(Boolean(record.interface_prefix))
  const bound = bindInterface
  const usable = credentials.filter(item => item.enabled && item.verified_at)
  const provider = usable.find(item => item.id === Number(draft.dns_credential_id || 0))?.provider
  const choices = useMemo(() => ddnsAddressChoices(interfaces, draft.address_family || '', draft.exclude_private), [interfaces, draft.address_family, draft.exclude_private])
  const prefixChoices = choices.flatMap(choice => {
    const prefix = ddnsPrefixFromAddress(choice.address)
    return prefix ? [{ ...choice, prefix }] : []
  })
  const selectedChoice = choices.find(choice => choice.interfaceName === draft.interface_name && choice.address === draft.interface_address)
  const readInterfaces = async () => {
    if (!draft.server_id || reading) return
    setReading(true)
    setReadStatus('')
    try {
      const created = await client.request(`/servers/${draft.server_id}/network-interfaces`, { method: 'POST', body: '{}' })
      let task = created?.task || {}
      if (!task.id) throw new Error('主控未返回网卡读取任务')
      for (let attempt = 0; attempt < 30; attempt += 1) {
        if (['succeeded', 'failed', 'rollback_failed'].includes(String(task.status || ''))) break
        await new Promise(resolve => window.setTimeout(resolve, 1000))
        const response = await client.request(`/agent-tasks/${task.id}`)
        task = response?.task || task
      }
      if (task.status !== 'succeeded') throw new Error('Agent 读取网卡失败')
      const result = JSON.parse(String(task.result_json || '{}'))
      const next = Array.isArray(result.interfaces) ? result.interfaces : []
      setInterfaces(next)
      setReadStatus(next.length ? `已读取 ${next.length} 个网卡` : 'Agent 未返回网卡')
    } catch (cause: any) {
      setReadStatus(cause?.message || '读取网卡失败')
    } finally {
      setReading(false)
    }
  }
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (bound && (!draft.interface_name || (prefixMode ? !draft.interface_prefix : (!draft.interface_address || (interfaces.length > 0 && !selectedChoice))))) {
      setFormError(draft.exclude_private && interfaces.length ? '当前过滤条件下没有可保存的网卡地址。' : '请先读取网卡，并从 Agent 返回的地址中选择。')
      return
    }
    setFormError('')
    const next = { ...draft }
    if (!bound) {
      next.interface_name = ''
      next.interface_address = ''
      next.interface_prefix = ''
    } else if (prefixMode) {
      next.interface_address = ''
      next.record_types = next.interface_prefix?.includes(':') ? 'aaaa' : 'a'
      next.address_family = next.record_types === 'a' ? 'ipv4' : 'ipv6'
    } else if (selectedChoice) {
      next.interface_prefix = ''
      next.record_types = selectedChoice.family === 'ipv4' ? 'a' : 'aaaa'
      next.address_family = selectedChoice.family
    }
    void onSubmit(next).catch((cause: { message?: string }) => setFormError(cause?.message || '保存失败，请重试'))
  }
  return <form onSubmit={submit}>
    <fieldset disabled={!canManage || busy} className="return-latency-fields">
      <div className="inbound-editor-grid">
        {!serverLocked && <FormField label="服务器" required>
          <ServerPickerSelect ariaLabel="DDNS 服务器" required servers={servers} value={draft.server_id || 0} onChange={serverID => {
            if (serverID !== draft.server_id) { setInterfaces([]); setReadStatus('') }
            setDraft({ ...draft, server_id: serverID, interface_name: '', interface_address: '', interface_prefix: '' })
          }} />
        </FormField>}
        <FormField label="名称" required>
          <input aria-label="DDNS 名称" required maxLength={60} value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} />
        </FormField>
        <FormField label="域名服务账号" required>
          <Select aria-label="域名服务账号" required value={draft.dns_credential_id || 0} onChange={event => setDraft({ ...draft, dns_credential_id: Number(event.target.value) || undefined, proxy_enabled: false })}>
            <option value={0}>选择账号</option>
            {usable.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
          </Select>
          {!usable.length && <p className="field-hint">没有可用账号。请先在“域名解析”中添加并验证账号。</p>}
        </FormField>
        <FormField label="解析域名" required hint="每个任务只维护自己的解析，同一域名可创建多个任务。">
          <input aria-label="解析域名" required value={draft.domain} onChange={event => setDraft({ ...draft, domain: event.target.value })} autoCapitalize="none" autoCorrect="off" spellCheck={false} />
        </FormField>
        <FormField label="记录类型">
          <Select aria-label="记录类型" value={draft.record_types} disabled={bound} onChange={event => setDraft({ ...draft, record_types: event.target.value as ProbeDDNSRecord['record_types'] })}>
            <option value="a">A</option>
            <option value="aaaa">AAAA</option>
            <option value="both">A + AAAA</option>
          </Select>
        </FormField>
        <FormField label="检查间隔">
          <Select aria-label="DDNS 检查间隔" value={draft.interval_seconds} onChange={event => setDraft({ ...draft, interval_seconds: Number(event.target.value) })}>
            <option value={300}>5 分钟</option>
            <option value={900}>15 分钟</option>
            <option value={3600}>1 小时</option>
            <option value={21600}>6 小时</option>
          </Select>
        </FormField>
        {provider === 'cloudflare' && <FormField label="Cloudflare 代理" hint="代理入口应保持 DNS only。">
          <Select aria-label="Cloudflare 代理" value={String(draft.proxy_enabled)} onChange={event => setDraft({ ...draft, proxy_enabled: event.target.value === 'true' })}>
            <option value="false">DNS only</option>
            <option value="true">开启代理</option>
          </Select>
        </FormField>}
      </div>
      <div className="inbound-editor-switch">
        <span><strong>绑定网卡地址</strong><small>不绑定则使用服务器当前公网地址。可固定一个地址，或按前缀跟随网卡地址变化。</small></span>
        <Switch checked={bound} ariaLabel="绑定网卡地址" onChange={checked => { setBindInterface(checked); setDraft({ ...draft, interface_name: checked ? draft.interface_name : '', interface_address: checked ? draft.interface_address : '', exclude_private: checked ? true : draft.exclude_private }) }} />
      </div>
      {bound && <>
        <div className="inbound-editor-grid">
          <FormField label="地址族">
            <Select aria-label="地址族" value={draft.address_family || ''} onChange={event => setDraft({ ...draft, address_family: event.target.value as ProbeDDNSRecord['address_family'], interface_address: '' })}>
              <option value="">IPv4 和 IPv6</option>
              <option value="ipv4">仅 IPv4</option>
              <option value="ipv6">仅 IPv6</option>
            </Select>
          </FormField>
          <div className="inbound-editor-switch">
            <span><strong>排除内网地址</strong><small>开启后不会发布私网、链路本地或运营商级 NAT 地址。</small></span>
            <Switch checked={draft.exclude_private} ariaLabel="排除内网地址" onChange={checked => setDraft({ ...draft, exclude_private: checked, interface_address: '' })} />
          </div>
        </div>
        <div className="inline-field-action">
          <button type="button" className="ghost" disabled={!draft.server_id || reading} onClick={() => void readInterfaces()}><RefreshCw size={15} aria-hidden="true" className={reading ? 'spin' : ''} />{reading ? '读取中…' : '读取网卡'}</button>
        </div>
        {readStatus && <p className={choices.length ? 'field-hint' : 'danger-text'} role={choices.length ? 'status' : 'alert'}>{readStatus}</p>}
        <FormField label="取址方式">
          <Select aria-label="取址方式" value={prefixMode ? 'prefix' : 'exact'} onChange={event => { setPrefixMode(event.target.value === 'prefix'); setDraft({ ...draft, interface_address: '', interface_prefix: event.target.value === 'prefix' && selectedChoice ? ddnsPrefixFromAddress(selectedChoice.address) || '' : '' }) }}>
            <option value="exact">固定地址</option><option value="prefix">匹配地址前缀</option>
          </Select>
        </FormField>
        {prefixMode ? <>
          <FormField label="从现有 IP 生成前缀" hint="使用网卡上报的掩码生成，可在下方调整。">
            <Select aria-label="从现有 IP 生成前缀" value="" disabled={!prefixChoices.length} onChange={event => {
              const choice = prefixChoices[Number(event.target.value)]
              if (!choice || event.target.value === '') return
              setDraft({ ...draft, interface_name: choice.interfaceName, interface_address: '', interface_prefix: choice.prefix, address_family: choice.family, record_types: choice.family === 'ipv4' ? 'a' : 'aaaa' })
            }}>
              <option value="">{prefixChoices.length ? '选择现有 IP' : '请先读取网卡或调整地址过滤条件'}</option>
              {prefixChoices.map((choice, index) => <option key={choice.interfaceName + choice.address} value={index}>{choice.interfaceName} · {choice.address} → {choice.prefix}</option>)}
            </Select>
          </FormField>
          <div className="inbound-editor-grid">
          <FormField label="网卡" required>
            <Select aria-label="前缀匹配网卡" required value={draft.interface_name || ''} onChange={event => setDraft({ ...draft, interface_name: event.target.value })}>
              <option value="">请先读取网卡</option>
              {draft.interface_name && !interfaces.some(item => item.name === draft.interface_name) && <option value={draft.interface_name}>{draft.interface_name}（已保存）</option>}
              {interfaces.filter(item => item.up && item.running && !item.loopback).map(item => <option key={item.name} value={item.name}>{item.name}</option>)}
            </Select>
          </FormField>
          <FormField label="地址前缀" required hint="填写 CIDR，例如 203.0.113.0/24 或 2001:db8::/48。只有一个地址匹配时才更新；无匹配或多个匹配会保留原解析。">
            <input aria-label="地址前缀" required maxLength={80} value={draft.interface_prefix || ''} placeholder="203.0.113.0/24" onChange={event => setDraft({ ...draft, interface_prefix: event.target.value, record_types: event.target.value.includes(':') ? 'aaaa' : 'a' })} />
          </FormField>
        </div></> : <>
        <FormField label="网卡地址" required={interfaces.length > 0} hint={draft.interface_address ? `已保存 ${draft.interface_name} ${draft.interface_address}` : '读取后从列表选择，不能手工填写。'}>
          <Select aria-label="网卡地址" value={selectedChoice ? `${selectedChoice.interfaceName}\u001f${selectedChoice.address}` : ''} onChange={event => {
            const choice = choices.find(item => `${item.interfaceName}\u001f${item.address}` === event.target.value)
            if (!choice) return
            setDraft({ ...draft, interface_name: choice.interfaceName, interface_address: choice.address, address_family: choice.family, record_types: choice.family === 'ipv4' ? 'a' : 'aaaa' })
          }}>
            <option value="">{choices.length ? '选择地址' : '请先读取网卡'}</option>
            {choices.map(choice => <option key={`${choice.interfaceName}-${choice.address}`} value={`${choice.interfaceName}\u001f${choice.address}`}>{choice.interfaceName} · {choice.address}</option>)}
          </Select>
        </FormField>
        {interfaces.length > 0 && draft.interface_address && !selectedChoice && <p className="danger-text" role="alert">{draft.exclude_private ? '该地址不在当前过滤结果中。若它是内网地址，本次不会发布。' : '该地址已不在最近一次读取结果中，请重新选择。'}</p>}
        </>}
      </>}
      <div className="inbound-editor-switch">
        <span><strong>启用</strong><small>停用后保留记录，但不再更新解析。</small></span>
        <Switch checked={draft.enabled} ariaLabel="启用 DDNS" onChange={checked => setDraft({ ...draft, enabled: checked })} />
      </div>
      {formError && <p className="danger-text" role="alert">{formError}</p>}
      <div className="return-latency-form-actions">
        <button type="button" className="ghost" onClick={onCancel}>取消</button>
        <button type="submit" className="primary" disabled={busy || !draft.server_id}>{busy ? '保存中…' : '保存'}</button>
      </div>
    </fieldset>
  </form>
}

