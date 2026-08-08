import * as React from 'react'
import { Archive, Copy, Eye, FilePlus2, Pencil, Save } from 'lucide-react'
import { NodeOrderTemplateEditor, type EntryOption, type TemplatePolicy } from '../components/node-ordering/NodeOrderTemplateEditor'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Input } from '../components/ui/input'
import { Select } from '../components/ui/select'
import { orderServerRegions } from '../region-order'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }
type Template = { id: number; name: string; description: string; enabled: boolean; revision: number; policy: TemplatePolicy; usage_count: number; updated_at: string; warnings?: string[] }

const emptyPolicy = (): TemplatePolicy => ({ version: 1, base_mode: 'exit_region', exit_region_order: ['JP', 'HK', 'SG', 'TW', 'US'], entry_region_order_mode: 'inherit_exit', entry_region_order: [], entry_order: [], new_node_placement: 'by_template', unmatched_placement: 'append' })

export function NodeOrderTemplatesPage({ data, client, notify }: { data: any; client: AnyClient; notify?: (message: string, tone?: any) => void }) {
  const [templates, setTemplates] = React.useState<Template[]>([])
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState('')
  const [editor, setEditor] = React.useState<Template | 'new' | null>(null)
  const [draft, setDraft] = React.useState<{ name: string; description: string; policy: TemplatePolicy }>({ name: '', description: '', policy: emptyPolicy() })
  const [busy, setBusy] = React.useState(false)
  const [previewPlanID, setPreviewPlanID] = React.useState(0)
  const [preview, setPreview] = React.useState<any>(null)

  const load = React.useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const res = await client.request<{ templates: Template[] }>('/node-order-templates?include_archived=1')
      setTemplates(res.templates || [])
    } catch (e: any) { setError(e?.message || String(e)) } finally { setLoading(false) }
  }, [client])
  React.useEffect(() => { void load() }, [load])

  const serversByID = new Map((data.servers || []).map((server: any) => [server.id, server]))
  const regionCodes = orderServerRegions(data.servers || []).map(region => region.code).filter(Boolean)
  const entries: EntryOption[] = (data.inbounds || []).map((entry: any) => ({ key: `inbound:${entry.id}`, label: entry.name || `入口 #${entry.id}`, region: String((serversByID.get(entry.server_id) as any)?.region_code || (serversByID.get(entry.server_id) as any)?.detected_region_code || '') }))
  const plans = data.subscription_plans || []

  const openEditor = (item?: Template) => {
    setEditor(item || 'new')
    setDraft(item ? { name: item.name, description: item.description, policy: structuredClone(item.policy) } : { name: '', description: '', policy: emptyPolicy() })
    setPreview(null)
    setPreviewPlanID(plans[0]?.id || 0)
  }

  const save = async () => {
    if (!draft.name.trim()) { setError('请输入模板名称'); return }
    setBusy(true); setError('')
    try {
      if (editor === 'new') {
        await client.request('/node-order-templates', { method: 'POST', body: JSON.stringify(draft) })
      } else if (editor) {
        await client.request(`/node-order-templates/${editor.id}`, { method: 'PATCH', body: JSON.stringify({ ...draft, expected_revision: editor.revision }) })
      }
      setEditor(null); await load(); notify?.('排序模板已保存', 'success')
    } catch (e: any) { const message = e?.message || String(e); setError(message.includes('409') ? '模板已被其他操作更新，请重新加载。' : message) } finally { setBusy(false) }
  }

  const clone = async (item: Template) => { try { await client.request(`/node-order-templates/${item.id}/clone`, { method: 'POST', body: JSON.stringify({ name: `${item.name} 副本` }) }); await load(); notify?.('模板副本已创建', 'success') } catch (e: any) { setError(e?.message || String(e)) } }
  const archive = async (item: Template) => { try { await client.request(`/node-order-templates/${item.id}/archive`, { method: 'POST', body: JSON.stringify({ expected_revision: item.revision }) }); await load(); notify?.('模板已归档', 'success') } catch (e: any) { setError(e?.message || String(e)) } }
  const runPreview = async () => {
    if (!previewPlanID || editor === 'new' || !editor) return
    setBusy(true); setError('')
    try { setPreview(await client.request(`/node-order-templates/${editor.id}/preview`, { method: 'POST', body: JSON.stringify({ plan_id: previewPlanID, policy: draft.policy }) })) } catch (e: any) { setError(e?.message || String(e)) } finally { setBusy(false) }
  }

  return (
    <div className="panel node-template-panel">
      <div className="panel-head template-page-head"><div><h2>排序模板</h2><p className="muted">模板保存可复用规则；方案使用独立快照。</p></div><Button onClick={() => openEditor()}><FilePlus2 size={15} /> 新建模板</Button></div>
      <div className="panel-body">
        {error && <p className="danger-text" role="alert">{error}</p>}
        {loading ? <p className="muted">正在加载排序模板...</p> : (
          <div className="card-custom" style={{ overflow: 'auto' }}><table className="user-data-table template-list-table"><thead><tr><th>模板名称</th><th>排序类型</th><th>新节点处理</th><th>版本</th><th>使用方案</th><th>状态</th><th>更新时间</th><th>操作</th></tr></thead><tbody>
            {templates.map(item => <tr key={item.id}><td><strong>{item.name}</strong>{item.description && <small>{item.description}</small>}{Boolean(item.warnings?.length) && <small className="danger-text">{item.warnings?.length} 条入口提示</small>}</td><td>{item.policy.base_mode === 'entry' ? '按入口' : '按出口地区'}</td><td>{{ by_template: '按模板自动插入', append: '追加到末尾', pending: '进入待排区' }[item.policy.new_node_placement]}</td><td>r{item.revision}</td><td>{item.usage_count} 个方案</td><td><Badge variant={item.enabled ? 'success' : 'secondary'}>{item.enabled ? '启用' : '已归档'}</Badge></td><td>{new Date(item.updated_at).toLocaleString()}</td><td className="table-actions"><Button variant="ghost" size="icon" onClick={() => openEditor(item)} aria-label={`编辑 ${item.name}`}><Pencil size={15} /></Button><Button variant="ghost" size="icon" onClick={() => void clone(item)} aria-label={`克隆 ${item.name}`}><Copy size={15} /></Button>{item.enabled && <Button variant="ghost" size="icon" onClick={() => void archive(item)} aria-label={`归档 ${item.name}`}><Archive size={15} /></Button>}</td></tr>)}
            {templates.length === 0 && <tr><td colSpan={8} className="muted empty-table-cell">尚未创建排序模板</td></tr>}
          </tbody></table></div>
        )}
      </div>
      <Dialog isOpen={editor !== null} onClose={() => setEditor(null)} title={editor === 'new' ? '新建排序模板' : `编辑模板 · ${(editor as Template)?.name}`} size="xl">
        <div className="template-editor-layout">
          <div className="template-editor-main"><section><h4>基础信息</h4><label><span>模板名称</span><Input value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} maxLength={100} autoFocus /></label><label><span>描述</span><Input value={draft.description} onChange={event => setDraft({ ...draft, description: event.target.value })} maxLength={500} /></label>{editor && editor !== 'new' && editor.warnings?.map((warning, index) => <p key={index} className="danger-text">{warning}</p>)}</section><NodeOrderTemplateEditor policy={draft.policy} onChange={policy => setDraft({ ...draft, policy })} regionCodes={regionCodes} entries={entries} /></div>
          <aside className="template-preview-pane"><h4>方案预览</h4><Select value={previewPlanID} onChange={event => setPreviewPlanID(Number(event.target.value))}><option value={0}>选择方案</option>{plans.map((plan: any) => <option key={plan.id} value={plan.id}>{plan.name}</option>)}</Select>{editor !== 'new' && <Button variant="outline" busy={busy} disabled={!previewPlanID} onClick={() => void runPreview()}><Eye size={15} /> 查看预览</Button>}{preview && <><div className="template-preview-summary"><span>总计 {preview.summary.total}</span><span>匹配 {preview.summary.matched}</span><span>未识别 {preview.summary.unmatched}</span></div><div className="template-preview-list">{preview.nodes.slice(0, 100).map((node: any) => <div key={node.node_key}><span>{node.old_position + 1}</span><strong>{node.name}</strong><span>{node.new_position + 1}</span></div>)}</div></>}</aside>
        </div>
        <div className="dialog-actions"><Button variant="ghost" onClick={() => setEditor(null)}>取消</Button><Button busy={busy} onClick={() => void save()}><Save size={15} /> 保存模板</Button></div>
      </Dialog>
    </div>
  )
}
