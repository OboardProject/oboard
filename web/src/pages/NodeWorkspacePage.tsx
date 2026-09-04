import * as React from 'react'
import { ArrowDown, ArrowUp, Check, CloudDownload, Copy, Eye, Layers3, Pencil, Plus, RefreshCw, Search, Trash2, X } from 'lucide-react'
import { useDialogs } from '../components/ui/dialog-context'
import { NodeAssignmentsPage } from './NodeAssignmentsPage'
import { hasManagementAccess } from '../permissions'

type Client = { request<T = any>(path: string, init?: RequestInit): Promise<T> }
type Group = { id: number; kind: 'oboard' | 'remote' | 'manual'; system_key?: string; name: string; node_count: number }
type Source = { id: number; group_id: number; url_display: string; status: string; last_error?: string; last_success_at?: string }
type Output = { id: number; name: string; is_default: boolean; enabled: boolean; group_ids: number[]; filters?: Array<{ type: string; value: string }> }
type Node = { id: string; group_id: number; name: string; protocol: string; source: string; copyable: boolean; editable: boolean }
type ImportPreview = { nodes: Array<{ name: string; protocol: string; fingerprint: string }>; issues: Array<{ index: number; name?: string; message: string }> }
type Workspace = { subject: { id: number; username: string; nickname?: string }; node_groups: Group[]; node_sources: Source[]; subscription_outputs: Output[] }

const formats = ['auto', 'mihomo', 'stash', 'surge', 'surge-mac', 'loon', 'egern', 'shadowrocket', 'qx', 'surfboard', 'sing-box', 'v2ray', 'v2ray-uri']

export function NodeWorkspacePage({ data, client, load, notify, legacySubscriptions, sessionUser }: { data: any; client: Client; load: () => Promise<void>; notify?: (message: string, tone?: 'success' | 'error' | 'warning') => void; legacySubscriptions?: React.ReactNode; sessionUser?: { role?: string } | null }) {
  // The role decides which workspace to mount, so it must be resolved before the
  // first paint. Page data arrives asynchronously; fall back to the restored
  // session so an administrator never sees the platform-user layout first.
  const role: string | undefined = data.session?.role || data.current_user?.role || sessionUser?.role || undefined
  const roleResolved = Boolean(role)
  const isAdmin = hasManagementAccess(role)
  const [modeOverride, setModeOverride] = React.useState<'global' | 'user' | null>(null)
  const mode: 'global' | 'user' = isAdmin ? modeOverride || 'global' : 'user'
  const setMode = (next: 'global' | 'user') => setModeOverride(next)
  const [userID, setUserID] = React.useState<number>(data.current_user?.id || data.account_user?.id || 0)
  const [tab, setTab] = React.useState<'library' | 'groups' | 'outputs'>('library')
  const [workspace, setWorkspace] = React.useState<Workspace | null>(null)
  const [nodes, setNodes] = React.useState<Node[]>([])
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')
  const subjectQuery = isAdmin && userID ? `?user_id=${userID}` : ''
  const users = (data.users || []).filter((user: any) => user.status === 'active')

  React.useEffect(() => {
    if (!userID) setUserID(Number(data.current_user?.id || data.account_user?.id || 0))
  }, [data.current_user?.id, data.account_user?.id, userID])

  const refresh = React.useCallback(async () => {
    if (!roleResolved || mode === 'global' || (isAdmin && !userID)) return
    setBusy(true)
    setError('')
    try {
      const [workspaceResult, libraryResult] = await Promise.all([
        client.request<Workspace>(`/node-workspace${subjectQuery}`),
        client.request<{ nodes: Node[] }>(`/node-library${subjectQuery}`),
      ])
      setWorkspace(workspaceResult)
      setNodes(libraryResult.nodes || [])
    } catch (requestError: any) {
      setError(requestError?.message || String(requestError))
    } finally {
      setBusy(false)
    }
  }, [client, mode, subjectQuery, isAdmin, userID, roleResolved])

  React.useEffect(() => { void refresh() }, [refresh])

  const message = (value: string, tone: 'success' | 'error' | 'warning' = 'success') => notify?.(value, tone)
  const mutate = async (path: string, init: RequestInit, success: string): Promise<boolean> => {
    setBusy(true)
    try {
      await client.request(path + subjectQuery, init)
      message(success)
      await refresh()
      return true
    } catch (requestError: any) {
      setError(requestError?.message || String(requestError))
      message(requestError?.message || String(requestError), 'error')
      return false
    } finally {
      setBusy(false)
    }
  }

  if (!roleResolved) return <div className="node-workspace"><div className="node-workspace-state">正在加载节点…</div></div>

  return <div className="node-workspace">
    {isAdmin && <div className="node-perspective-bar" aria-label="节点视角">
      <div className="node-mode-switch" role="group" aria-label="节点管理模式">
        <button type="button" className={mode === 'global' ? 'active' : ''} aria-pressed={mode === 'global'} onClick={() => setMode('global')}>全部节点</button>
        <button type="button" className={mode === 'user' ? 'active' : ''} aria-pressed={mode === 'user'} onClick={() => setMode('user')}>用户视角</button>
      </div>
      {mode === 'user' && <label><span>查看用户</span><select value={userID} onChange={event => setUserID(Number(event.target.value))} aria-label="选择用户视角">
        <option value={0}>选择用户</option>
        {users.map((user: any) => <option key={user.id} value={user.id}>{user.nickname || user.username} ({user.username})</option>)}
      </select></label>}
      {mode === 'user' && workspace && <strong className="node-impersonation-notice">正在代管 {workspace.subject.nickname || workspace.subject.username} 的节点</strong>}
    </div>}

    {mode === 'global' && isAdmin ? <NodeAssignmentsPage data={data} client={client} load={load} /> : <>
      <div className="node-workspace-tabs" role="tablist" aria-label="节点工作台">
        <button type="button" role="tab" aria-selected={tab === 'library'} className={tab === 'library' ? 'active' : ''} onClick={() => setTab('library')}><Search size={16} />节点库</button>
        <button type="button" role="tab" aria-selected={tab === 'groups'} className={tab === 'groups' ? 'active' : ''} onClick={() => setTab('groups')}><Layers3 size={16} />节点组</button>
        <button type="button" role="tab" aria-selected={tab === 'outputs'} className={tab === 'outputs' ? 'active' : ''} onClick={() => setTab('outputs')}><CloudDownload size={16} />组合订阅</button>
      </div>
      {error && <div className="node-workspace-error" role="alert">{error}</div>}
      {busy && !workspace ? <div className="node-workspace-state">正在加载节点…</div> : !workspace ? <div className="node-workspace-state">选择一个用户以查看节点</div> : <>
        {tab === 'library' && <NodeLibrary nodes={nodes} groups={workspace.node_groups} busy={busy} onCopy={async node => {
          try {
            const result = await client.request<{ url: string }>(`/node-library/share${subjectQuery}`, { method: 'POST', body: JSON.stringify({ node_id: node.id }) })
            await navigator.clipboard.writeText(result.url)
            message('节点链接已复制')
          } catch (requestError: any) { message(requestError?.message || String(requestError), 'error') }
        }} onEdit={async (node, patch) => {
          try {
            await client.request(`/node-library/${node.id}${subjectQuery}`, { method: 'PATCH', body: JSON.stringify(patch) })
            message('节点已更新')
            await refresh()
          } catch (requestError: any) { message(requestError?.message || String(requestError), 'error') }
        }} />}
        {tab === 'groups' && <NodeGroups workspace={workspace} busy={busy} mutate={mutate} subjectQuery={subjectQuery} client={client} refresh={refresh} message={message} />}
        {tab === 'outputs' && <SubscriptionOutputs workspace={workspace} data={data} client={client} subjectQuery={subjectQuery} busy={busy} refresh={refresh} message={message} legacySubscriptions={legacySubscriptions} />}
      </>}
    </>}
  </div>
}

function NodeLibrary({ nodes, groups, busy, onCopy, onEdit }: { nodes: Node[]; groups: Group[]; busy: boolean; onCopy: (node: Node) => Promise<void>; onEdit: (node: Node, patch: { name?: string; content?: string }) => Promise<void> }) {
  const [query, setQuery] = React.useState('')
  const [groupID, setGroupID] = React.useState(0)
  const [protocol, setProtocol] = React.useState('')
  const [editing, setEditing] = React.useState<Node | null>(null)
  const [editName, setEditName] = React.useState('')
  const [editContent, setEditContent] = React.useState('')
  const [editBusy, setEditBusy] = React.useState(false)
  const visible = nodes.filter(node => (!query || node.name.toLowerCase().includes(query.toLowerCase())) && (!groupID || node.group_id === groupID) && (!protocol || node.protocol === protocol))
  const protocols = Array.from(new Set(nodes.map(node => node.protocol))).sort()
  const openEdit = (node: Node) => { setEditing(node); setEditName(node.name); setEditContent('') }
  const saveEdit = async () => {
    if (!editing || (!editName.trim() && !editContent.trim())) return
    setEditBusy(true)
    try {
      await onEdit(editing, { name: editName.trim(), content: editContent.trim() })
      setEditing(null)
    } finally {
      setEditBusy(false)
    }
  }
  return <section className="node-workspace-panel" role="tabpanel">
    <div className="node-library-toolbar">
      <label className="node-search"><Search size={15} /><span className="sr-only">搜索节点</span><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索节点" /></label>
      <select value={groupID} onChange={event => setGroupID(Number(event.target.value))} aria-label="按节点组筛选"><option value={0}>全部节点组</option>{groups.map(group => <option key={group.id} value={group.id}>{group.name}</option>)}</select>
      <select value={protocol} onChange={event => setProtocol(event.target.value)} aria-label="按协议筛选"><option value="">全部协议</option>{protocols.map(value => <option key={value}>{value}</option>)}</select>
      <span className="node-result-count">{visible.length} 个节点</span>
    </div>
    {editing && <div className="node-edit-form" aria-label={`编辑 ${editing.name}`}>
      <strong>编辑 {editing.name}</strong>
      <label><span>节点名称</span><input value={editName} onChange={event => setEditName(event.target.value)} maxLength={80} placeholder="留空时使用新配置中的名称" /></label>
      <label><span>节点链接或配置</span><textarea value={editContent} onChange={event => setEditContent(event.target.value)} rows={3} placeholder="粘贴新的节点链接或配置，可留空" /></label>
      <div className="node-import-actions"><button type="button" className="ghost" disabled={editBusy} onClick={() => setEditing(null)}><X size={15} />取消</button><button type="button" disabled={editBusy || (!editName.trim() && !editContent.trim())} onClick={() => void saveEdit()}><Check size={15} />保存</button></div>
    </div>}
    {visible.length === 0 ? <div className="node-workspace-state">没有符合条件的节点</div> : <div className="node-library-table-wrap"><table className="node-library-table"><thead><tr><th>节点</th><th>协议</th><th>节点组</th><th>来源</th><th aria-label="操作" /></tr></thead><tbody>
      {visible.map(node => <tr key={node.id}><td><strong>{node.name}</strong></td><td><span className="node-protocol-chip">{node.protocol}</span></td><td>{groups.find(group => group.id === node.group_id)?.name || 'OBoard'}</td><td>{node.source === 'oboard' ? 'OBoard' : '第三方'}</td><td>{node.editable && <button type="button" className="icon-button" title="编辑节点" aria-label={`编辑 ${node.name}`} disabled={busy} onClick={() => openEdit(node)}><Pencil size={15} /></button>}<button type="button" className="icon-button" title={node.copyable ? '复制节点链接' : '此节点无法无损复制'} aria-label={`复制 ${node.name} 的节点链接`} disabled={busy || !node.copyable} onClick={() => void onCopy(node)}><Copy size={15} /></button></td></tr>)}
    </tbody></table></div>}
  </section>
}

function NodeGroups({ workspace, busy, mutate, subjectQuery, client, refresh, message }: { workspace: Workspace; busy: boolean; mutate: (path: string, init: RequestInit, success: string) => Promise<boolean>; subjectQuery: string; client: Client; refresh: () => Promise<void>; message: (value: string, tone?: 'success' | 'error' | 'warning') => void }) {
  const dialogs = useDialogs()
  const [kind, setKind] = React.useState<'manual' | 'remote'>('remote')
  const [name, setName] = React.useState('')
  const [value, setValue] = React.useState('')
  const [importPreview, setImportPreview] = React.useState<ImportPreview | null>(null)
  const [editingID, setEditingID] = React.useState(0)
  const [editName, setEditName] = React.useState('')
  const [editURL, setEditURL] = React.useState('')
  const [editContent, setEditContent] = React.useState('')
  const create = async () => {
    if (!name.trim() || !value.trim()) return
    if (await mutate('/node-groups', { method: 'POST', body: JSON.stringify({ name, kind, ...(kind === 'remote' ? { url: value } : { content: value }) }) }, '节点组已创建')) {
      setName(''); setValue(''); setImportPreview(null)
    }
  }
  const previewImport = async () => {
    try {
      const result = await client.request<ImportPreview>(`/node-import-preview${subjectQuery}`, { method: 'POST', body: JSON.stringify(kind === 'remote' ? { url: value } : { content: value }) })
      setImportPreview(result)
    } catch (error: any) {
      setImportPreview(null)
      message(error?.message || String(error), 'error')
    }
  }
  const openEdit = (group: Group) => { setEditingID(group.id); setEditName(group.name); setEditURL(''); setEditContent('') }
  const closeEdit = () => { setEditingID(0); setEditName(''); setEditURL(''); setEditContent('') }
  const saveEdit = async (group: Group) => {
    const nextName = editName.trim()
    const url = editURL.trim()
    const content = editContent.trim()
    if (!nextName) return
    const body: Record<string, unknown> = { name: nextName }
    if (group.kind === 'remote' && url) body.url = url
    if (group.kind === 'manual' && content) body.content = content
    if (await mutate(`/node-groups/${group.id}`, { method: 'PATCH', body: JSON.stringify(body) }, '节点组已更新')) closeEdit()
  }
  const deleteGroup = async (group: Group) => {
    if (!await dialogs.confirm({
      title: `删除节点组“${group.name}”？`,
      message: '该节点组及其中节点都会被删除。',
      confirmText: '删除',
      tone: 'danger',
    })) return
    await mutate(`/node-groups/${group.id}`, { method: 'DELETE' }, '节点组已删除')
  }
  return <section className="node-workspace-panel" role="tabpanel">
    <div className="node-group-create">
      <div className="node-mode-switch" role="group" aria-label="来源类型"><button type="button" className={kind === 'remote' ? 'active' : ''} aria-pressed={kind === 'remote'} onClick={() => { setKind('remote'); setImportPreview(null) }}>远程订阅</button><button type="button" className={kind === 'manual' ? 'active' : ''} aria-pressed={kind === 'manual'} onClick={() => { setKind('manual'); setImportPreview(null) }}>手动节点</button></div>
      <label><span>节点组名称</span><input value={name} onChange={event => setName(event.target.value)} maxLength={80} /></label>
      <label className="node-source-value"><span>{kind === 'remote' ? 'HTTPS 订阅 URL' : '节点链接或配置'}</span>{kind === 'remote' ? <input type="url" value={value} onChange={event => setValue(event.target.value)} placeholder="https://" /> : <textarea value={value} onChange={event => setValue(event.target.value)} rows={3} />}</label>
      <div className="node-import-actions"><button type="button" className="ghost" onClick={() => void previewImport()} disabled={busy || !value.trim()}><Eye size={15} />预览</button><button type="button" onClick={() => void create()} disabled={busy || !name.trim() || !value.trim()}><Plus size={15} />添加</button></div>
    </div>
    {importPreview && <div className="node-import-preview" aria-live="polite"><strong>可导入 {importPreview.nodes.length} 个节点</strong><div>{importPreview.nodes.slice(0, 8).map(node => <span key={node.fingerprint}>{node.name} · {node.protocol}</span>)}</div>{importPreview.nodes.length > 8 && <small>另有 {importPreview.nodes.length - 8} 个节点</small>}{importPreview.issues.length > 0 && <details><summary>{importPreview.issues.length} 项未导入</summary>{importPreview.issues.map(issue => <span key={`${issue.index}-${issue.message}`}>第 {issue.index} 项：{issue.message}</span>)}</details>}</div>}
    <div className="node-group-grid">{workspace.node_groups.map(group => {
      const source = workspace.node_sources.find(item => item.group_id === group.id)
      const editing = editingID === group.id
      const editUnchanged = !editURL.trim() && !editContent.trim() && editName.trim() === group.name
      return <article className="node-group-item" key={group.id}>{editing ? <form className="node-group-edit" aria-label={`编辑 ${group.name}`} onSubmit={event => { event.preventDefault(); void saveEdit(group) }}>
        <label><span>节点组名称</span><input value={editName} onChange={event => setEditName(event.target.value)} maxLength={80} /></label>
        {group.kind === 'remote' && <label><span>HTTPS 订阅 URL{source && <small>当前 {source.url_display}</small>}</span><input type="url" value={editURL} placeholder={source?.url_display || 'https://'} onChange={event => setEditURL(event.target.value)} /></label>}
        {group.kind === 'manual' && <label><span>新增节点链接</span><textarea value={editContent} rows={3} placeholder="粘贴新的节点链接或配置，留空则保持现有节点不变" onChange={event => setEditContent(event.target.value)} /></label>}
        <div className="node-import-actions"><button type="button" className="ghost" disabled={busy} onClick={closeEdit}><X size={15} />取消</button><button type="submit" disabled={busy || !editName.trim() || editUnchanged}><Check size={15} />保存</button></div>
      </form> : <>
        <div><strong>{group.name}</strong><span>{group.kind === 'oboard' ? '系统组' : group.kind === 'remote' ? '远程来源' : '手动组'} · {group.node_count} 个节点</span></div>
        {source && <div className="node-source-status"><span>{source.url_display}</span><span className={`status-${source.status}`}>{source.status === 'ready' ? '已同步' : source.status === 'error' ? '同步失败' : '等待同步'}{source.last_success_at ? ` · ${new Date(source.last_success_at).toLocaleString()}` : ''}</span>{source.last_error && <small>{source.last_error}</small>}</div>}
        <div className="node-group-actions">
          {source && <button type="button" className="icon-button" title="刷新来源" aria-label={`刷新 ${group.name}`} disabled={busy} onClick={async () => { try { await client.request(`/node-sources/${source.id}/refresh${subjectQuery}`, { method: 'POST', body: '{}' }); message('来源已刷新'); await refresh() } catch (error: any) { message(error?.message || String(error), 'error') } }}><RefreshCw size={15} /></button>}
          <button type="button" className="icon-button" title="编辑节点组" aria-label={`编辑 ${group.name}`} disabled={busy} onClick={() => openEdit(group)}><Pencil size={15} /></button>
          {group.kind !== 'oboard' && <button type="button" className="icon-button danger" title="删除" aria-label={`删除 ${group.name}`} disabled={busy} onClick={() => void deleteGroup(group)}><Trash2 size={15} /></button>}
        </div>
      </>}</article>
    })}</div>
  </section>
}

type FilterRule = { type: string; value: string }
const filterTypeLabels: Record<string, string> = {
  keep_name: '名字正则保留', drop_name: '名字正则排除',
  keep_protocol: '协议保留', drop_protocol: '协议排除',
  keep_region: '地区保留', drop_region: '地区排除',
  keep_group: '分组保留', drop_group: '分组排除',
}
const filterProtocols = ['vless', 'vmess', 'trojan', 'tuic', 'hysteria2', 'anytls', 'shadowsocks', 'socks5', 'socks', 'snell', 'mieru', 'ssh']

function SubscriptionOutputs({ workspace, data, client, subjectQuery, busy, refresh, message, legacySubscriptions }: { workspace: Workspace; data: any; client: Client; subjectQuery: string; busy: boolean; refresh: () => Promise<void>; message: (value: string, tone?: 'success' | 'error' | 'warning') => void; legacySubscriptions?: React.ReactNode }) {
  const dialogs = useDialogs()
  const [selectedID, setSelectedID] = React.useState(workspace.subscription_outputs[0]?.id || 0)
  const selected = workspace.subscription_outputs.find(item => item.id === selectedID) || workspace.subscription_outputs[0]
  const [name, setName] = React.useState(selected?.name || '')
  const [groupIDs, setGroupIDs] = React.useState<number[]>(selected?.group_ids || [])
  const [filters, setFilters] = React.useState<FilterRule[]>(selected?.filters || [])
  const [format, setFormat] = React.useState('mihomo')
  const [preview, setPreview] = React.useState<any>(null)
  React.useEffect(() => { setName(selected?.name || ''); setGroupIDs(selected?.group_ids || []); setFilters(selected?.filters || []); setPreview(null) }, [selected?.id])
  if (!selected) return <section className="node-workspace-panel"><div className="node-workspace-state">暂无组合订阅</div></section>
  const orderedGroups = [...groupIDs.map(id => workspace.node_groups.find(group => group.id === id)).filter((group): group is Group => Boolean(group)), ...workspace.node_groups.filter(group => !groupIDs.includes(group.id))]
  const moveGroup = (groupID: number, offset: -1 | 1) => setGroupIDs(current => {
    const from = current.indexOf(groupID)
    const to = from + offset
    if (from < 0 || to < 0 || to >= current.length) return current
    const next = [...current]
    ;[next[from], next[to]] = [next[to], next[from]]
    return next
  })
  const filteredHasValue = filters.every(rule => rule.value.trim() !== '')
  const updateFilter = (index: number, patch: Partial<FilterRule>) => setFilters(current => current.map((rule, position) => position === index ? { ...rule, ...patch } : rule))
  const moveFilter = (index: number, offset: -1 | 1) => setFilters(current => {
    const to = index + offset
    if (to < 0 || to >= current.length) return current
    const next = [...current]
    ;[next[index], next[to]] = [next[to], next[index]]
    return next
  })
  const filterValueControl = (rule: FilterRule, index: number) => {
    if (rule.type === 'keep_protocol' || rule.type === 'drop_protocol') {
      return <select value={rule.value} aria-label={`第 ${index + 1} 条规则协议`} onChange={event => updateFilter(index, { value: event.target.value })}>{filterProtocols.map(protocol => <option key={protocol} value={protocol}>{protocol}</option>)}</select>
    }
    if (rule.type === 'keep_group' || rule.type === 'drop_group') {
      return <select value={rule.value} aria-label={`第 ${index + 1} 条规则分组`} onChange={event => updateFilter(index, { value: event.target.value })}><option value="">选择节点组</option>{workspace.node_groups.map(group => <option key={group.id} value={String(group.id)}>{group.name}</option>)}</select>
    }
    const placeholder = rule.type.endsWith('region') ? '如 JP、HK、US' : '正则表达式，如 香港|东京'
    return <input value={rule.value} aria-label={`第 ${index + 1} 条规则值`} placeholder={placeholder} maxLength={256} onChange={event => updateFilter(index, { value: event.target.value })} />
  }
  const save = async () => { try { await client.request(`/subscription-outputs/${selected.id}${subjectQuery}`, { method: 'PATCH', body: JSON.stringify({ name, group_ids: groupIDs, filters, enabled: true }) }); message('组合订阅已保存'); await refresh() } catch (error: any) { message(error?.message || String(error), 'error') } }
  const runPreview = async () => { try { const result = await client.request<any>(`/subscription-outputs/${selected.id}/preview${subjectQuery}`, { method: 'POST', body: JSON.stringify({ format }) }); setPreview(result) } catch (error: any) { message(error?.message || String(error), 'error') } }
  const createOutput = async () => {
    const next = await dialogs.prompt({ title: '新建组合订阅', message: '请输入组合订阅名称。', placeholder: '组合订阅名称', confirmText: '创建' })
    if (!next) return
    try {
      await client.request(`/subscription-outputs${subjectQuery}`, { method: 'POST', body: JSON.stringify({ name: next, group_ids: [workspace.node_groups.find(group => group.kind === 'oboard')?.id].filter(Boolean) }) })
      await refresh()
    } catch (error: any) {
      message(error?.message || String(error), 'error')
    }
  }
  const deleteOutput = async () => {
    if (!await dialogs.confirm({ title: `删除组合订阅“${selected.name}”？`, message: '删除后无法恢复。', confirmText: '删除', tone: 'danger' })) return
    try {
      await client.request(`/subscription-outputs/${selected.id}${subjectQuery}`, { method: 'DELETE' })
      message('组合订阅已删除')
      setSelectedID(0)
      await refresh()
    } catch (error: any) {
      message(error?.message || String(error), 'error')
    }
  }
  const copyURL = async () => {
    const users = data.users || []
    const user = users.find((item: any) => item.id === workspace.subject.id) || data.account_user || data.current_user
    if (!user?.subscription_token) { message('该用户没有可复制的旧式订阅凭证', 'warning'); return }
    const activeRelay = (data.subscription_relays || []).find((relay: any) => relay.active)
    const base = String(data.subscription_public_base_url || data.settings?.subscription_relay_url || activeRelay?.public_url || window.location.origin).replace(/\/$/, '')
    const query = new URLSearchParams({ format, profile_id: String(selected.id) })
    await navigator.clipboard.writeText(`${base}/api/v1/subscriptions/${user.subscription_token}?${query}`)
    message('组合订阅链接已复制')
  }
  return <section className="node-workspace-panel" role="tabpanel"><div className="subscription-output-layout"><aside aria-label="组合订阅列表"><div className="output-list-head"><strong>组合</strong><button type="button" className="icon-button" title="新建组合" aria-label="新建组合" disabled={busy} onClick={() => void createOutput()}><Plus size={15} /></button></div>{workspace.subscription_outputs.map(output => <button key={output.id} type="button" className={selected.id === output.id ? 'active' : ''} onClick={() => setSelectedID(output.id)}><span>{output.name}</span>{output.is_default && <small>默认</small>}</button>)}</aside><div className="output-editor">
    <label><span>组合名称</span><input value={name} onChange={event => setName(event.target.value)} maxLength={80} /></label>
    <fieldset><legend>按顺序选择节点组</legend>{orderedGroups.map(group => {
      const position = groupIDs.indexOf(group.id)
      const selectedGroup = position >= 0
      return <div className="output-group-row" key={group.id}><input type="checkbox" checked={selectedGroup} aria-label={`${selectedGroup ? '移除' : '添加'}节点组 ${group.name}`} onChange={event => setGroupIDs(current => event.target.checked ? [...current, group.id] : current.filter(id => id !== group.id))} /><span>{selectedGroup ? `${position + 1}. ${group.name}` : group.name}</span><small>{group.node_count} 个节点</small><div className="output-group-order" aria-label={`${group.name} 顺序`}>
        <button type="button" className="icon-button" title="上移" aria-label={`上移 ${group.name}`} disabled={!selectedGroup || position === 0} onClick={() => moveGroup(group.id, -1)}><ArrowUp size={14} /></button>
        <button type="button" className="icon-button" title="下移" aria-label={`下移 ${group.name}`} disabled={!selectedGroup || position === groupIDs.length - 1} onClick={() => moveGroup(group.id, 1)}><ArrowDown size={14} /></button>
      </div></div>
    })}</fieldset>
    <fieldset><legend>过滤规则（按顺序执行，被排除的节点不会被后续规则恢复）</legend>
      {filters.length === 0 && <p className="muted" style={{ margin: '0 0 6px', fontSize: 12 }}>未设置过滤规则，输出全部节点。规则作用于去除国旗前缀后的节点名。</p>}
      {filters.map((rule, index) => <div className="output-filter-row" key={index}>
        <span className="filter-position">{index + 1}</span>
        <select value={rule.type} aria-label={`第 ${index + 1} 条规则类型`} onChange={event => updateFilter(index, { type: event.target.value, value: event.target.value === rule.type ? rule.value : '' })}>{Object.entries(filterTypeLabels).map(([type, label]) => <option key={type} value={type}>{label}</option>)}</select>
        {filterValueControl(rule, index)}
        <div className="output-group-order" aria-label={`第 ${index + 1} 条规则顺序`}>
          <button type="button" className="icon-button" title="上移" aria-label={`上移规则 ${index + 1}`} disabled={index === 0} onClick={() => moveFilter(index, -1)}><ArrowUp size={14} /></button>
          <button type="button" className="icon-button" title="下移" aria-label={`下移规则 ${index + 1}`} disabled={index === filters.length - 1} onClick={() => moveFilter(index, 1)}><ArrowDown size={14} /></button>
        </div>
        <button type="button" className="icon-button danger-text" title="删除" aria-label={`删除规则 ${index + 1}`} onClick={() => setFilters(current => current.filter((_, position) => position !== index))}><Trash2 size={14} /></button>
      </div>)}
      <div className="output-add-rule"><button type="button" className="ghost" disabled={filters.length >= 32} onClick={() => setFilters(current => [...current, { type: 'keep_name', value: '' }])}><Plus size={14} />添加规则</button></div>
    </fieldset>
    <div className="output-actions"><select value={format} onChange={event => setFormat(event.target.value)} aria-label="客户端格式">{formats.map(item => <option key={item}>{item}</option>)}</select><div className="output-action-buttons"><button type="button" onClick={() => void save()} disabled={busy || !name.trim() || groupIDs.length === 0 || !filteredHasValue}><Check size={15} />保存</button><button type="button" className="ghost" onClick={() => void runPreview()} disabled={busy}><Eye size={15} />预览</button><button type="button" className="ghost" onClick={() => void copyURL()}><Copy size={15} />复制订阅</button>{!selected.is_default && <button type="button" className="ghost danger" disabled={busy} onClick={() => void deleteOutput()}><Trash2 size={15} />删除组合</button>}</div></div>
    {preview && <div className="output-preview"><div><span>输出 {preview.preview?.nodes?.length || 0} 个</span><span>格式过滤 {preview.preview?.filtered_count || 0} 个</span><span>规则过滤 {preview.preview?.filter_dropped || 0} 个</span><span>去重 {preview.deduplicated_count || 0} 个</span><span>错误 {preview.preview?.invalid_reasons?.length || 0} 个</span></div>{Array.isArray(preview.preview?.filter_stats) && preview.preview.filter_stats.length > 0 && <div className="filter-stats">{preview.preview.filter_stats.map((stat: any, index: number) => <span key={index} className={`filter-stat ${stat.dropped > 0 ? 'filter-stat-dropped' : ''}`}>{filterTypeLabels[stat.type] || stat.type} · {stat.value}{stat.skip_reason ? ` · ${stat.skip_reason}` : ` · 命中 ${stat.matched} / 丢弃 ${stat.dropped} / 剩余 ${stat.remaining}`}</span>)}</div>}<pre>{preview.preview?.content || '此格式没有兼容节点'}</pre></div>}
  </div></div>{legacySubscriptions && <div className="legacy-subscriptions-embedded">{legacySubscriptions}</div>}</section>
}
