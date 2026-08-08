import * as React from 'react'
import { Plus, RefreshCw, RotateCcw, Save, Trash2 } from 'lucide-react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Select } from '../ui/select'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }
type Rule = { rule_id: number; kind: string; scope_key: string }
type Exclusion = { node_type: string; node_id: number }
type CatalogPage = { nodes: any[]; total: number; page: number; page_size: number }
type Props = { plan: { id: number; latest_revision_id: number; lock_version: number; pending_revision_id?: number }; client: AnyClient; onSaved: () => void; notify?: (message: string, tone?: any) => void }

const ruleLabels: Record<string, string> = {
  entry_inbound: '入口', entry_server: '入口服务器', path_server: '路径服务器', exit_server: '出口服务器', exit_region: '出口地区', external_outbound: '导入节点',
}

export function PlanMembershipRulesPanel({ plan, client, onSaved, notify }: Props) {
  const [rules, setRules] = React.useState<Rule[]>([])
  const [exclusions, setExclusions] = React.useState<Exclusion[]>([])
  const [catalog, setCatalog] = React.useState<any[]>([])
  const [kind, setKind] = React.useState('exit_region')
  const [scope, setScope] = React.useState('')
  const [preview, setPreview] = React.useState<any>(null)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  const load = React.useCallback(async () => {
    setError('')
    try {
      const [policy, firstPage] = await Promise.all([
        client.request<any>(`/subscription-plans/${plan.id}/membership-rules`),
        client.request<CatalogPage>('/assignable-nodes?page=1&page_size=200'),
      ])
      const pageCount = Math.ceil((firstPage.total || 0) / 200)
      const remainingPages = pageCount > 1
        ? await Promise.all(Array.from({ length: pageCount - 1 }, (_, index) => client.request<CatalogPage>(`/assignable-nodes?page=${index + 2}&page_size=200`)))
        : []
      setRules(policy.rules || [])
      setExclusions(policy.exclusions || [])
      setCatalog([...(firstPage.nodes || []), ...remainingPages.flatMap(page => page.nodes || [])])
      setPreview(null)
    } catch (reason: any) { setError(reason?.message || String(reason)) }
  }, [client, plan.id])

  React.useEffect(() => { void load() }, [load])

  const options = React.useMemo(() => {
    const values = new Map<string, string>()
    for (const node of catalog) {
      if (kind === 'entry_inbound' && node.entry_key) values.set(node.entry_key, `${node.entry_server_name || node.name} · ${node.entry_key}`)
      if (kind === 'entry_server' && node.entry_server_id) values.set(String(node.entry_server_id), node.entry_server_name || `服务器 #${node.entry_server_id}`)
      if (kind === 'path_server') for (const item of node.path_servers || []) values.set(String(item.server_id), item.server_name || `服务器 #${item.server_id}`)
      if (kind === 'exit_server' && node.exit_server_id) values.set(String(node.exit_server_id), node.exit_server_name || `服务器 #${node.exit_server_id}`)
      if (kind === 'exit_region' && node.exit_region) values.set(node.exit_region, node.exit_region)
      if (kind === 'external_outbound' && node.type === 'external_outbound') values.set(String(node.id), node.name)
    }
    return Array.from(values.entries()).sort((a, b) => a[1].localeCompare(b[1]))
  }, [catalog, kind])

  React.useEffect(() => { setScope(options[0]?.[0] || '') }, [kind, options.length])

  const addRule = () => {
    if (!scope) return
    const nextID = rules.reduce((max, item) => Math.max(max, item.rule_id), 0) + 1
    if (rules.some(item => item.kind === kind && item.scope_key === scope)) return
    setRules(list => [...list, { rule_id: nextID, kind, scope_key: scope }])
    setPreview(null)
  }

  const requestBody = () => ({ base_revision_id: plan.latest_revision_id, expected_lock_version: plan.lock_version, rules, exclusions })
  const matchCount = (ruleID: number) => Object.values(preview?.matched_by || {}).filter((ids: any) => Array.isArray(ids) && ids.includes(ruleID)).length
  const runPreview = async () => {
    setBusy(true); setError('')
    try { setPreview(await client.request(`/subscription-plans/${plan.id}/membership-rules/preview`, { method: 'POST', body: JSON.stringify(requestBody()) })) }
    catch (reason: any) { setError(reason?.message || String(reason)) } finally { setBusy(false) }
  }
  const save = async () => {
    setBusy(true); setError('')
    try {
      const result = await client.request<any>(`/subscription-plans/${plan.id}/membership-rules/versions`, { method: 'POST', body: JSON.stringify({ ...requestBody(), change_summary: '修改方案自动节点规则' }) })
      notify?.(result.no_change ? '自动规则没有变化' : result.access_change_id ? `规则已保存，变更 #${result.access_change_id} 正在应用` : '规则已保存', result.no_change ? 'warning' : 'success')
      await load(); onSaved()
    } catch (reason: any) { const message = reason?.message || String(reason); setError(message.includes('409') ? '方案已发生变化，请重新加载后重试。' : message) } finally { setBusy(false) }
  }

  return <section className="card-custom" style={{ padding: 12, display: 'grid', gap: 10 }} aria-label="自动节点规则">
    <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}><div><h3 style={{ margin: 0 }}>自动节点规则</h3><p className="muted" style={{ margin: '3px 0 0' }}>多条规则取并集；手动节点保留，排除项持续生效。</p></div><Button variant="ghost" size="icon" onClick={() => void load()} aria-label="重新加载自动规则"><RefreshCw size={14} /></Button></div>
    {error && <p style={{ color: 'var(--color-danger)', margin: 0 }}>{error}</p>}
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 180px), 1fr))', gap: 8 }}>
      <Select value={kind} onChange={event => setKind(event.target.value)} aria-label="规则范围类型">{Object.entries(ruleLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</Select>
      <Select value={scope} onChange={event => setScope(event.target.value)} aria-label="规则范围"><option value="">当前没有可选范围</option>{options.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</Select>
      <Button variant="outline" disabled={!scope} onClick={addRule}><Plus size={14} /> 添加</Button>
    </div>
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>{rules.length === 0 ? <span className="muted">未设置自动规则</span> : rules.map(rule => <Badge key={rule.rule_id} variant="secondary">{ruleLabels[rule.kind] || rule.kind} · {rule.scope_key}{preview ? ` · ${matchCount(rule.rule_id)} 个` : ''}<button type="button" aria-label={`删除规则 ${rule.rule_id}`} onClick={() => { setRules(list => list.filter(item => item.rule_id !== rule.rule_id)); setPreview(null) }} style={{ border: 0, background: 'transparent', padding: '0 0 0 5px', cursor: 'pointer' }}><Trash2 size={12} /></button></Badge>)}</div>
    {exclusions.length > 0 && <div><strong style={{ fontSize: 13 }}>持续排除</strong><div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 6 }}>{exclusions.map(item => { const key = `${item.node_type}:${item.node_id}`; return <Badge key={key} variant="outline">{key}<button type="button" aria-label={`恢复 ${key}`} title="恢复规则继承" onClick={() => { setExclusions(list => list.filter(value => `${value.node_type}:${value.node_id}` !== key)); setPreview(null) }} style={{ border: 0, background: 'transparent', padding: '0 0 0 5px', cursor: 'pointer' }}><RotateCcw size={12} /></button></Badge> })}</div></div>}
    {preview && <p className="muted" style={{ margin: 0 }}>预览：新增 {preview.added_node_keys?.length || 0}，移除 {preview.removed_node_keys?.length || 0}，最终 {preview.nodes?.length || 0} 个节点。{preview.warnings?.join('；')}</p>}
    <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}><Button variant="outline" busy={busy} onClick={() => void runPreview()}>预览</Button><Button busy={busy} disabled={Boolean(plan.pending_revision_id) || !preview} onClick={() => void save()}><Save size={14} /> 保存规则</Button></div>
  </section>
}
