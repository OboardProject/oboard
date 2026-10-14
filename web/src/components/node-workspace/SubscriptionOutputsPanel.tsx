import * as React from 'react'
import { ArrowDown, ArrowUp, Check, Copy, Eye, Pencil, Plus, Trash2 } from 'lucide-react'
import { useDialogs } from '../ui/dialog-context'
import { Dialog } from '../ui/dialog'
import { errorText, filterProtocols, filterTypeLabels, subscriptionFormats, type Client, type FilterRule, type NodeGroup, type Notify, type Workspace } from './types'

type PreviewResult = {
  preview?: {
    nodes?: unknown[]
    content?: string
    filtered_count?: number
    filter_dropped?: number
    invalid_reasons?: unknown[]
    filter_stats?: Array<{ type: string; value: string; matched: number; dropped: number; remaining: number; skip_reason?: string }>
  }
  deduplicated_count?: number
}

function draftOf(name: string, groupIDs: number[], filters: FilterRule[]) {
  return JSON.stringify({ name, group_ids: groupIDs, filters })
}

export function SubscriptionOutputsPanel({ workspace, data, client, subjectQuery, busy, refresh, message }: {
  workspace: Workspace
  data: any
  client: Client
  subjectQuery: string
  busy: boolean
  refresh: () => Promise<void>
  message: Notify
}) {
  const dialogs = useDialogs()
  const [selectedID, setSelectedID] = React.useState(workspace.subscription_outputs[0]?.id || 0)
  const selected = workspace.subscription_outputs.find(item => item.id === selectedID) || workspace.subscription_outputs[0]
  const [name, setName] = React.useState(selected?.name || '')
  const [groupIDs, setGroupIDs] = React.useState<number[]>(selected?.group_ids || [])
  const [filters, setFilters] = React.useState<FilterRule[]>(selected?.filters || [])
  const [format, setFormat] = React.useState('mihomo')
  const [preview, setPreview] = React.useState<PreviewResult | null>(null)
  const [previewBusy, setPreviewBusy] = React.useState(false)
  const [editorOpen, setEditorOpen] = React.useState(false)
  const [saving, setSaving] = React.useState(false)
  const [saveStatus, setSaveStatus] = React.useState('')
  const saveInFlight = React.useRef(false)
  const session = React.useRef(0)
  const draftValue = draftOf(name, groupIDs, filters)
  const [savedValue, setSavedValue] = React.useState(draftValue)
  const dirty = draftValue !== savedValue
  const currentDraft = React.useRef(draftValue)
  currentDraft.current = draftValue
  const groupByID = new Map(workspace.node_groups.map(group => [group.id, group]))

  const resetEditor = () => {
    setName(selected?.name || ''); setGroupIDs(selected?.group_ids || []); setFilters(selected?.filters || []); setSaveStatus('')
    setSavedValue(draftOf(selected?.name || '', selected?.group_ids || [], selected?.filters || []))
  }
  React.useEffect(() => {
    session.current++
    resetEditor()
    setPreview(null)
  }, [selected?.id])

  const openEditor = () => { session.current++; resetEditor(); setEditorOpen(true) }
  const closeEditor = async () => {
    const currentSession = session.current
    if (dirty && !await dialogs.confirm({ title: '关闭编辑？', message: '当前未保存的输入会丢失；已经提交的修改会继续处理。', confirmText: '放弃并关闭', tone: 'danger' })) return
    if (currentSession === session.current) { session.current++; resetEditor(); setEditorOpen(false) }
  }
  const selectOutput = async (id: number) => {
    if (id === selected?.id) return
    const currentSession = session.current
    if (dirty && !await dialogs.confirm({ title: '切换组合订阅？', message: '当前未保存的输入会丢失；已经提交的修改会继续处理。', confirmText: '放弃并切换', tone: 'danger' })) return
    if (currentSession === session.current) setSelectedID(id)
  }
  const createOutput = async () => {
    const next = await dialogs.prompt({ title: '新建组合订阅', message: '请输入组合订阅名称。', placeholder: '组合订阅名称', confirmText: '创建' })
    if (!next) return
    try {
      await client.request(`/subscription-outputs${subjectQuery}`, { method: 'POST', body: JSON.stringify({ name: next, group_ids: [workspace.node_groups.find(group => group.kind === 'oboard')?.id].filter(Boolean) }) })
      await refresh()
    } catch (error) {
      message(errorText(error), 'error')
    }
  }

  const listHead = <div className="nw-output-list-head"><strong>组合订阅</strong><button type="button" className="ghost icon-button" title="新建组合" aria-label="新建组合" disabled={busy} onClick={() => void createOutput()}><Plus size={15} /></button></div>
  if (!selected) return <section className="nw-section" aria-label="组合订阅"><div className="nw-empty"><strong>暂无组合订阅</strong><span>组合订阅把多个节点组按顺序合并为一条订阅链接。</span><button type="button" onClick={() => void createOutput()} disabled={busy}><Plus size={15} />新建组合</button></div></section>

  const orderedGroups = [...groupIDs.map(id => groupByID.get(id)).filter((group): group is NodeGroup => Boolean(group)), ...workspace.node_groups.filter(group => !groupIDs.includes(group.id))]
  const moveGroup = (groupID: number, offset: -1 | 1) => setGroupIDs(current => {
    const from = current.indexOf(groupID)
    const to = from + offset
    if (from < 0 || to < 0 || to >= current.length) return current
    const next = [...current]
    ;[next[from], next[to]] = [next[to], next[from]]
    return next
  })
  const filtersComplete = filters.every(rule => rule.value.trim() !== '')
  const updateFilter = (index: number, patch: Partial<FilterRule>) => setFilters(current => current.map((rule, position) => position === index ? { ...rule, ...patch } : rule))
  const moveFilter = (index: number, offset: -1 | 1) => setFilters(current => {
    const to = index + offset
    if (to < 0 || to >= current.length) return current
    const next = [...current]
    ;[next[index], next[to]] = [next[to], next[index]]
    return next
  })
  const ruleValueLabel = (rule: FilterRule) => rule.type.endsWith('_group') ? groupByID.get(Number(rule.value))?.name || `节点组 #${rule.value}` : rule.value
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
  const save = async () => {
    if (saveInFlight.current) return
    const savingSession = session.current
    const snapshot = draftValue
    saveInFlight.current = true
    setSaving(true)
    setSaveStatus('')
    try {
      await client.request(`/subscription-outputs/${selected.id}${subjectQuery}`, { method: 'PATCH', body: snapshot })
      if (savingSession === session.current) {
        setSavedValue(snapshot)
        setPreview(null)
        setSaveStatus(currentDraft.current === snapshot ? '已保存' : '已保存提交的内容；当前还有未保存的修改。')
        if (currentDraft.current === snapshot) setEditorOpen(false)
      }
      void refresh()
    } catch (error) {
      if (savingSession === session.current) setSaveStatus(errorText(error))
    } finally {
      saveInFlight.current = false
      setSaving(false)
    }
  }
  const runPreview = async () => {
    const previewSession = session.current
    setPreviewBusy(true)
    try {
      const result = await client.request<PreviewResult>(`/subscription-outputs/${selected.id}/preview${subjectQuery}`, { method: 'POST', body: JSON.stringify({ format }) })
      if (previewSession === session.current) setPreview(result)
    } catch (error) { if (previewSession === session.current) message(errorText(error), 'error') } finally { setPreviewBusy(false) }
  }
  const deleteOutput = async () => {
    if (!await dialogs.confirm({ title: `删除组合订阅“${selected.name}”？`, message: '使用该组合订阅链接的客户端将无法继续更新。删除后无法恢复。', confirmText: '删除', tone: 'danger' })) return
    try {
      await client.request(`/subscription-outputs/${selected.id}${subjectQuery}`, { method: 'DELETE' })
      message('组合订阅已删除')
      setSelectedID(0)
      await refresh()
    } catch (error) {
      message(errorText(error), 'error')
    }
  }
  const copyURL = async () => {
    const users = data.users || []
    const user = users.find((item: any) => item.id === workspace.subject.id) || data.account_user || data.current_user
    if (!user?.subscription_token) { message('该用户暂时没有可用的订阅链接', 'warning'); return }
    const activeRelay = (data.subscription_relays || []).find((relay: any) => relay.active)
    const basePath = new URL(document.querySelector('base')?.getAttribute('href') || '/', window.location.origin).pathname.replace(/\/$/, '')
    const base = String(data.subscription_public_base_url || data.settings?.subscription_relay_url || activeRelay?.public_url || `${window.location.origin}${basePath}`).replace(/\/$/, '')
    const query = new URLSearchParams({ format, profile_id: String(selected.id) })
    try {
      await navigator.clipboard.writeText(`${base}/api/v1/subscriptions/${user.subscription_token}?${query}`)
      message('组合订阅链接已复制')
    } catch { message('未能复制订阅链接，请检查浏览器剪贴板权限后重试。', 'warning') }
  }

  const savedFilters = selected.filters || []
  const stats = preview?.preview
  return <section className="nw-section nw-outputs" aria-label="组合订阅">
    <aside className="nw-output-list" aria-label="组合订阅列表">
      {listHead}
      {workspace.subscription_outputs.map(output => <button key={output.id} type="button" className={selected.id === output.id ? 'active' : ''} aria-current={selected.id === output.id ? 'true' : undefined} onClick={() => void selectOutput(output.id)}>
        <span className="nw-output-list-name">{output.name}{output.is_default && <small className="nw-tag">默认</small>}</span>
        <span className="nw-output-list-meta">{output.group_ids.length} 个节点组 · {output.filters?.length || 0} 条规则</span>
      </button>)}
    </aside>

    <div className="nw-output-detail">
      <header className="nw-output-head">
        <div><h3>{selected.name}</h3>{selected.is_default && <span className="nw-tag">默认</span>}</div>
        <div className="nw-output-head-actions">
          {!selected.is_default && <button type="button" className="ghost danger-text" disabled={busy} onClick={() => void deleteOutput()}><Trash2 size={15} />删除</button>}
          <button type="button" className="ghost" onClick={openEditor}><Pencil size={15} />编辑组合</button>
        </div>
      </header>

      <div className="nw-output-summary">
        <section aria-label="输出节点组">
          <h4>节点组<small>按顺序输出</small></h4>
          {selected.group_ids.length === 0 ? <p className="nw-muted">未选择节点组</p> : <ol className="nw-order-list">{selected.group_ids.map(id => <li key={id}><span>{groupByID.get(id)?.name || `节点组 #${id}`}</span><small>{groupByID.get(id)?.node_count ?? 0} 个节点</small></li>)}</ol>}
        </section>
        <section aria-label="过滤规则">
          <h4>过滤规则<small>{savedFilters.length ? `${savedFilters.length} 条，依次执行` : '未设置'}</small></h4>
          {savedFilters.length === 0 ? <p className="nw-muted">输出所选节点组中的全部节点。</p> : <ol className="nw-order-list">{savedFilters.map((rule, index) => <li key={index}><span>{filterTypeLabels[rule.type] || rule.type}</span><code>{ruleValueLabel(rule)}</code></li>)}</ol>}
        </section>
      </div>

      <section className="nw-output-deliver" aria-label="获取订阅">
        <h4>获取订阅</h4>
        <div className="nw-output-deliver-row">
          <label className="nw-inline-field"><span>客户端格式</span><select value={format} onChange={event => { setFormat(event.target.value); setPreview(null) }} aria-label="客户端格式">{subscriptionFormats.map(item => <option key={item}>{item}</option>)}</select></label>
          <div className="nw-output-deliver-actions">
            <button type="button" className="ghost" onClick={() => void runPreview()} disabled={busy || saving || dirty || previewBusy} title={dirty ? '请先保存修改，再预览订阅实际输出' : '预览已保存配置的实际输出'}><Eye size={15} />{previewBusy ? '生成中…' : '预览输出'}</button>
            <button type="button" onClick={() => void copyURL()}><Copy size={15} />复制订阅链接</button>
          </div>
        </div>
        {preview && <div className="nw-preview">
          <dl className="nw-preview-stats">
            <div><dt>输出节点</dt><dd>{stats?.nodes?.length || 0}</dd></div>
            <div><dt>格式不兼容</dt><dd>{stats?.filtered_count || 0}</dd></div>
            <div><dt>规则过滤</dt><dd>{stats?.filter_dropped || 0}</dd></div>
            <div><dt>去重</dt><dd>{preview.deduplicated_count || 0}</dd></div>
            <div><dt>错误</dt><dd>{stats?.invalid_reasons?.length || 0}</dd></div>
          </dl>
          {Array.isArray(stats?.filter_stats) && stats.filter_stats.length > 0 && <div className="nw-filter-stats">{stats.filter_stats.map((stat, index) => <span key={index} className={`nw-filter-stat${stat.dropped > 0 ? ' is-dropped' : ''}`}>{filterTypeLabels[stat.type] || stat.type} · {stat.value}{stat.skip_reason ? ` · ${stat.skip_reason}` : ` · 命中 ${stat.matched} / 丢弃 ${stat.dropped} / 剩余 ${stat.remaining}`}</span>)}</div>}
          <pre>{stats?.content || '此格式没有兼容节点'}</pre>
        </div>}
      </section>
    </div>

    <Dialog isOpen={editorOpen} onClose={() => void closeEditor()} title={`编辑组合 · ${selected.name}`} size="xl" className="nw-dialog" footer={<><button type="button" className="ghost" onClick={() => void closeEditor()}>关闭</button><button type="button" onClick={() => void save()} disabled={busy || saving || !name.trim() || groupIDs.length === 0 || !filtersComplete}><Check size={15} />{saving ? '保存中…' : '保存'}</button></>}>
      <div className="nw-form nw-output-editor">
        {saveStatus && <p className="nw-form-status" role="status">{saveStatus}</p>}
        <label><span>组合名称</span><input value={name} onChange={event => setName(event.target.value)} maxLength={80} /></label>
        <fieldset className="nw-fieldset">
          <legend>节点组</legend>
          <p className="nw-form-hint">勾选要输出的节点组，并用箭头调整输出顺序。</p>
          {orderedGroups.map(group => {
            const position = groupIDs.indexOf(group.id)
            const selectedGroup = position >= 0
            return <div className={`nw-output-group-row${selectedGroup ? ' is-selected' : ''}`} key={group.id}>
              <input type="checkbox" checked={selectedGroup} aria-label={`${selectedGroup ? '移除' : '添加'}节点组 ${group.name}`} onChange={event => setGroupIDs(current => event.target.checked ? [...current, group.id] : current.filter(id => id !== group.id))} />
              <span>{selectedGroup ? `${position + 1}. ${group.name}` : group.name}</span>
              <small>{group.node_count} 个节点</small>
              <div className="nw-order-buttons" aria-label={`${group.name} 顺序`}>
                <button type="button" className="ghost icon-button" title="上移" aria-label={`上移 ${group.name}`} disabled={!selectedGroup || position === 0} onClick={() => moveGroup(group.id, -1)}><ArrowUp size={14} /></button>
                <button type="button" className="ghost icon-button" title="下移" aria-label={`下移 ${group.name}`} disabled={!selectedGroup || position === groupIDs.length - 1} onClick={() => moveGroup(group.id, 1)}><ArrowDown size={14} /></button>
              </div>
            </div>
          })}
        </fieldset>
        <fieldset className="nw-fieldset">
          <legend>过滤规则</legend>
          <p className="nw-form-hint">规则按顺序执行，作用于去除国旗前缀后的节点名；被排除的节点不会被后续规则恢复。</p>
          {filters.length === 0 && <p className="nw-muted">未设置过滤规则，输出全部节点。</p>}
          {filters.map((rule, index) => <div className="nw-output-filter-row" key={index}>
            <span className="nw-filter-position">{index + 1}</span>
            <select value={rule.type} aria-label={`第 ${index + 1} 条规则类型`} onChange={event => updateFilter(index, { type: event.target.value, value: event.target.value === rule.type ? rule.value : '' })}>{Object.entries(filterTypeLabels).map(([type, label]) => <option key={type} value={type}>{label}</option>)}</select>
            {filterValueControl(rule, index)}
            <div className="nw-order-buttons" aria-label={`第 ${index + 1} 条规则顺序`}>
              <button type="button" className="ghost icon-button" title="上移" aria-label={`上移规则 ${index + 1}`} disabled={index === 0} onClick={() => moveFilter(index, -1)}><ArrowUp size={14} /></button>
              <button type="button" className="ghost icon-button" title="下移" aria-label={`下移规则 ${index + 1}`} disabled={index === filters.length - 1} onClick={() => moveFilter(index, 1)}><ArrowDown size={14} /></button>
              <button type="button" className="ghost icon-button danger-text" title="删除" aria-label={`删除规则 ${index + 1}`} onClick={() => setFilters(current => current.filter((_, position) => position !== index))}><Trash2 size={14} /></button>
            </div>
          </div>)}
          <button type="button" className="ghost nw-add-rule" disabled={filters.length >= 32} onClick={() => setFilters(current => [...current, { type: 'keep_name', value: '' }])}><Plus size={14} />添加规则</button>
        </fieldset>
      </div>
    </Dialog>
  </section>
}
