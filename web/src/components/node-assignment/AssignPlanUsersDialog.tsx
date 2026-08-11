import * as React from 'react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import { Input } from '../ui/input'
import { Switch } from '../ui/switch'
import { UserPicker, type UserOption } from './UserPicker'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type PlanChangePreview = {
  users_affected: number
  users_unchanged: number
  nodes_added: string[]
  nodes_removed: string[]
  nodes_unchanged: number
  affected_servers: number[]
  affected_paths: number
  task_count: number
  offline_servers: number[]
  capacity_issues: string[]
}

function toLocalInputValue(iso?: string) {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fromLocalInputValue(value: string): string | undefined {
  if (!value) return undefined
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

function fmtDate(iso?: string) {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

export function AssignPlanUsersDialog({ open, defaultPlanID, plans, users, client, notify, onClose, onDone }: {
  open: boolean
  defaultPlanID: number
  plans: { id: number; name: string; enabled?: boolean }[]
  users: UserOption[]
  client: AnyClient
  notify: (message: string, tone?: 'success' | 'error' | 'warning') => void
  onClose: () => void
  onDone: () => void | Promise<void>
}) {
  const [planID, setPlanID] = React.useState(defaultPlanID)
  const [userIDs, setUserIDs] = React.useState<Set<number>>(new Set())
  const [deploy, setDeploy] = React.useState(true)
  const [startsAt, setStartsAt] = React.useState('')
  const [expiresAt, setExpiresAt] = React.useState('')
  const [preview, setPreview] = React.useState<PlanChangePreview | null>(null)
  const [previewBusy, setPreviewBusy] = React.useState(false)
  const [applyBusy, setApplyBusy] = React.useState(false)
  const [message, setMessage] = React.useState('')

  React.useEffect(() => {
    if (open) {
      setPlanID(defaultPlanID)
      setUserIDs(new Set())
      setDeploy(true)
      setStartsAt('')
      setExpiresAt('')
      setPreview(null)
      setMessage('')
    }
  }, [open, defaultPlanID])

  const runPreview = async () => {
    if (!planID) { setMessage('请先选择套餐'); return }
    if (userIDs.size === 0) { setMessage('请先选择用户'); return }
    setPreviewBusy(true)
    setMessage('')
    try {
      const res = await client.request<{ preview: PlanChangePreview }>('/users/plan-assignment/preview', {
        method: 'POST',
        body: JSON.stringify({
          user_ids: [...userIDs],
          plan_id: planID,
          deploy,
          starts_at: fromLocalInputValue(startsAt),
          expires_at: fromLocalInputValue(expiresAt),
        }),
      })
      setPreview(res.preview || null)
    } catch (e: any) {
      setMessage('预览失败：' + (e?.message || String(e)))
    } finally {
      setPreviewBusy(false)
    }
  }

  const applyAssignment = async () => {
    if (!planID || !preview) return
    setApplyBusy(true)
    setMessage('')
    try {
      const res = await client.request<{ applied: boolean; affected_users: number; access_change_id: number; status: string; queued_tasks: number; activate_at?: string }>('/users/plan-assignment/apply', {
        method: 'POST',
        body: JSON.stringify({
          user_ids: [...userIDs],
          plan_id: planID,
          deploy,
          starts_at: fromLocalInputValue(startsAt),
          expires_at: fromLocalInputValue(expiresAt),
        }),
      })
      notify?.(res.status === 'scheduled'
        ? `已排定套餐分配：变更 #${res.access_change_id}，将于 ${fmtDate(res.activate_at)} 生效`
        : `已提交套餐分配：变更 #${res.access_change_id}（${res.status}），排队 ${res.queued_tasks} 个任务`, 'success')
      setPreview(null)
      await onDone()
    } catch (e: any) {
      setMessage('应用失败：' + (e?.message || String(e)))
    } finally {
      setApplyBusy(false)
    }
  }

  return (
    <Dialog isOpen={open} onClose={onClose} title="将此套餐分配给用户" size="lg">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <p className="muted" style={{ margin: 0 }}>批量把用户绑定到套餐。每个用户最多绑定一个有效套餐；绑定通过 access-change 两阶段生效。</p>
        <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap' }}>
          <Select value={planID} onChange={e => setPlanID(Number(e.target.value))} style={{ minWidth: 200 }} aria-label="选择套餐">
            <option value={0}>选择套餐</option>
            {plans.map(p => <option key={p.id} value={p.id}>{p.name}{p.enabled === false ? '（已停用）' : ''}</option>)}
          </Select>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, cursor: 'pointer' }}>
            <Switch size="sm" checked={deploy} onChange={setDeploy} ariaLabel="立即下发部署任务" /> 立即下发部署任务
          </label>
        </div>
        <UserPicker users={users} selected={userIDs} onChange={setUserIDs} />
        <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap' }}>
          <Input type="datetime-local" value={startsAt} onChange={e => setStartsAt(e.target.value)} aria-label="开始时间（可选）" title="开始时间（可选）" style={{ maxWidth: 200 }} />
          <Input type="datetime-local" value={expiresAt} onChange={e => setExpiresAt(e.target.value)} aria-label="到期时间（可选）" title="到期时间（可选）" style={{ maxWidth: 200 }} />
          <Button variant="outline" size="sm" busy={previewBusy} onClick={() => void runPreview()}>预览影响</Button>
        </div>
        {message && <p style={{ margin: 0, color: message.includes('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>{message}</p>}
        {preview && (
          <div className="card-custom" style={{ padding: 12, display: 'flex', flexDirection: 'column', gap: 8 }}>
            <p className="muted" style={{ margin: 0 }}>
              受影响用户 {preview.users_affected} · 不变 {preview.users_unchanged} · 新增节点 {preview.nodes_added?.length || 0} · 移除节点 {preview.nodes_removed?.length || 0} · 部署任务 {preview.task_count}
            </p>
            {(preview.capacity_issues || []).length > 0 && <p style={{ color: 'var(--color-danger)', margin: 0 }}>{preview.capacity_issues.join('；')}</p>}
            {preview.offline_servers?.length > 0 && <p className="muted" style={{ margin: 0 }}>离线服务器 {preview.offline_servers.join('、')} 的节点将排队等待上线后下发。</p>}
            <div style={{ display: 'flex', gap: 8 }}>
              <Button size="sm" busy={applyBusy} onClick={() => void applyAssignment()}>应用并分配</Button>
              <Button size="sm" variant="ghost" onClick={() => setPreview(null)}>取消</Button>
            </div>
          </div>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <Button variant="outline" onClick={onClose}>关闭</Button>
        </div>
      </div>
    </Dialog>
  )
}
