import * as React from 'react'
import { Eye, RefreshCw } from 'lucide-react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import type { TemplatePolicy } from './NodeOrderTemplateEditor'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

export type OrderTemplateSummary = {
  id: number
  name: string
  description: string
  enabled: boolean
  revision: number
  policy: TemplatePolicy
}

type TemplatePreview = {
  nodes: { node_key: string; name: string; old_position: number; new_position: number; bucket?: string; matched: boolean }[]
  warnings: string[]
  summary: { total: number; matched: number; unmatched: number }
}

export function ApplyOrderTemplateDialog({ open, planID, hasManualOrder, client, busy, onClose, onApply }: {
  open: boolean
  planID: number
  hasManualOrder: boolean
  client: AnyClient
  busy?: boolean
  onClose: () => void
  onApply: (template: OrderTemplateSummary, applyMode: 'preserve_manual' | 'rebuild') => Promise<void> | void
}) {
  const [templates, setTemplates] = React.useState<OrderTemplateSummary[]>([])
  const [templateID, setTemplateID] = React.useState(0)
  const [applyMode, setApplyMode] = React.useState<'preserve_manual' | 'rebuild'>('preserve_manual')
  const [preview, setPreview] = React.useState<TemplatePreview | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [previewing, setPreviewing] = React.useState(false)
  const [error, setError] = React.useState('')

  React.useEffect(() => {
    if (!open) return
    setLoading(true)
    setPreview(null)
    setError('')
    setApplyMode(hasManualOrder ? 'preserve_manual' : 'rebuild')
    void client.request<{ templates: OrderTemplateSummary[] }>('/node-order-templates')
      .then(result => {
        const items = result.templates || []
        setTemplates(items)
        setTemplateID(items[0]?.id || 0)
      })
      .catch(reason => setError(reason?.message || String(reason)))
      .finally(() => setLoading(false))
  }, [open, hasManualOrder, client])

  const selected = templates.find(item => item.id === templateID)
  const runPreview = async () => {
    if (!selected) return
    setPreviewing(true)
    setError('')
    try {
      const result = await client.request<TemplatePreview>(`/node-order-templates/${selected.id}/preview`, {
        method: 'POST',
        body: JSON.stringify({ plan_id: planID }),
      })
      setPreview(result)
    } catch (reason: any) {
      setError(reason?.message || String(reason))
    } finally {
      setPreviewing(false)
    }
  }

  return (
    <Dialog isOpen={open} onClose={onClose} title="应用排序模板" size="lg">
      <div className="apply-template-dialog">
        <label>
          <span>模板</span>
          <Select value={templateID} onChange={event => { setTemplateID(Number(event.target.value)); setPreview(null) }} disabled={loading}>
            <option value={0}>{loading ? '正在加载模板...' : '选择模板'}</option>
            {templates.map(item => <option key={item.id} value={item.id}>{item.name} · r{item.revision}</option>)}
          </Select>
        </label>
        {hasManualOrder && <p className="template-manual-warning">当前方案存在人工顺序。重新排列会替换当前调整。</p>}
        <div className="template-radio-list" role="radiogroup" aria-label="模板应用方式">
          <label>
            <input type="radio" name="template-apply-mode" checked={applyMode === 'preserve_manual'} onChange={() => setApplyMode('preserve_manual')} />
            <span><strong>保留当前人工顺序</strong><small>当前节点一项不动，仅更新以后新增节点的插入规则。</small></span>
          </label>
          <label>
            <input type="radio" name="template-apply-mode" checked={applyMode === 'rebuild'} onChange={() => setApplyMode('rebuild')} />
            <span><strong>按新模板重新排列全部节点</strong><small>当前人工调整将被替换。</small></span>
          </label>
        </div>
        <div className="apply-template-preview-actions">
          <Button variant="outline" size="sm" busy={previewing} disabled={!selected} onClick={() => void runPreview()}><Eye size={14} /> 查看预览</Button>
          {preview && applyMode === 'preserve_manual' && <span className="muted">本次应用不会改变现有节点位置。</span>}
        </div>
        {preview && applyMode === 'rebuild' && (
          <div className="template-apply-preview">
            <div className="template-preview-summary">
              <Badge variant="outline">总计 {preview.summary.total}</Badge>
              <Badge variant="success">匹配 {preview.summary.matched}</Badge>
              {preview.summary.unmatched > 0 && <Badge variant="warning">未识别 {preview.summary.unmatched}</Badge>}
            </div>
            <div className="template-preview-list" aria-label="模板应用前后顺序">
              <div className="template-preview-list-head"><span>应用前</span><span>节点</span><span>应用后</span></div>
              {preview.nodes.slice(0, 100).map(node => (
                <div key={node.node_key}><span>{node.old_position + 1}</span><strong>{node.name}</strong><span>{node.new_position + 1}</span></div>
              ))}
            </div>
            {preview.warnings?.map((warning, index) => <p key={index} className="muted">{warning}</p>)}
          </div>
        )}
        {error && <p className="danger-text" role="alert">{error}</p>}
        {!loading && templates.length === 0 && <p className="muted">没有可用的排序模板，请先在“排序模板”页面创建。</p>}
        <div className="dialog-actions">
          <Button variant="ghost" disabled={busy} onClick={onClose}>取消</Button>
          <Button busy={busy} disabled={!selected} onClick={() => selected && void onApply(selected, applyMode)}><RefreshCw size={14} /> 应用模板</Button>
        </div>
      </div>
    </Dialog>
  )
}
