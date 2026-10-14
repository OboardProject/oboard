import * as React from 'react'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import type { CatalogNode } from './catalog'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }
type Notify = (message: string, tone?: 'success' | 'error' | 'warning') => void
type BatchOp = 'add' | 'remove' | 'replace'

const opLabels: Record<BatchOp, string> = { add: '加入套餐', remove: '从套餐移除', replace: '替换套餐节点' }

export function NodeBatchPlanDialog({ open, nodes, plans, client, notify, onClose, onDone }: {
  open: boolean
  nodes: CatalogNode[]
  plans: Array<{ id: number; name: string }>
  client: AnyClient
  notify?: Notify
  onClose: () => void
  onDone: () => Promise<void>
}) {
  const [planID, setPlanID] = React.useState(0)
  const [op, setOp] = React.useState<BatchOp>('add')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  React.useEffect(() => { if (open) setError('') }, [open])

  const run = async () => {
    if (!planID || nodes.length === 0) return
    setBusy(true)
    setError('')
    try {
      const plan = await client.request<{ subscription_plan?: { lock_version: number; latest_revision_id: number } }>(`/subscription-plans/${planID}`)
      const lockVersion = plan.subscription_plan?.lock_version || 0
      const baseRevisionID = plan.subscription_plan?.latest_revision_id || 0
      if (!lockVersion || !baseRevisionID) throw new Error('无法获取套餐版本信息，请刷新后重试')
      const res = await client.request<{ access_change_id?: number; no_change?: boolean }>(`/subscription-plans/${planID}/nodes/apply`, {
        method: 'POST',
        body: JSON.stringify({ op, nodes: nodes.map(node => ({ node_type: node.type, node_id: node.id })), base_revision_id: baseRevisionID, expected_lock_version: lockVersion }),
      })
      if (res.no_change) notify?.('节点没有变化', 'warning')
      else if (res.access_change_id) notify?.(`已保存，正在应用变更 #${res.access_change_id}`, 'success')
      else notify?.('已保存为新版本', 'success')
      await onDone()
    } catch (e: any) {
      setError('操作失败：' + (e?.message || String(e)))
    } finally {
      setBusy(false)
    }
  }

  return <Dialog isOpen={open} onClose={() => { if (!busy) onClose() }} title="批量调整套餐" size="lg" className="nw-dialog" footer={<>
    <Button variant="outline" onClick={onClose} disabled={busy}>取消</Button>
    <Button disabled={!planID || busy || nodes.length === 0} busy={busy} onClick={() => void run()}>{opLabels[op]}</Button>
  </>}>
    <div className="nw-form">
      <section className="nw-batch-selection" aria-label="已选节点">
        <h4>已选 {nodes.length} 个节点</h4>
        <div>{nodes.map(node => <span key={node.key}>{node.name}</span>)}</div>
      </section>
      <div className="nw-form-row">
        <label><span>目标套餐</span>
          <Select value={planID} onChange={event => setPlanID(Number(event.target.value))} aria-label="目标套餐">
            <option value={0}>选择目标套餐</option>
            {plans.map(plan => <option key={plan.id} value={plan.id}>{plan.name}</option>)}
          </Select>
        </label>
        <label><span>操作</span>
          <Select value={op} onChange={event => setOp(event.target.value as BatchOp)} aria-label="批量操作">
            {(Object.keys(opLabels) as BatchOp[]).map(value => <option key={value} value={value}>{opLabels[value]}</option>)}
          </Select>
        </label>
      </div>
      {op === 'replace' && <p className="nw-callout" role="note">将用这 {nodes.length} 个节点替换目标套餐的全部节点，不在名单中的节点会从套餐移除。</p>}
      <p className="nw-form-hint">保存后会生成新的套餐版本，并在后台同步到相关用户。</p>
      {error && <p className="nw-form-error" role="alert">{error}</p>}
    </div>
  </Dialog>
}
