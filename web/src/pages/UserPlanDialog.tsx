import * as React from 'react'
import { Badge } from '../components/ui/badge'
import { AuthorizationStatusBadge } from '../components/authorization/AuthorizationStatusBadge'
import { useAccessChangeStatus } from '../hooks/useAccessChangeStatus'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Select } from '../components/ui/select'
import { Input } from '../components/ui/input'
import { DateTimePicker } from '../components/ui/datetime-picker'
import { RefreshCw, Trash2, Plus } from 'lucide-react'
import { useRegisterPageRefresh } from '../page-refresh-context'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type Plan = { id: number; name: string; enabled: boolean }
type Binding = { user_id: number; plan_id: number; status?: string; starts_at?: string; expires_at?: string; deployed_at?: string }
type EffectiveNode = { key: string; node_type: string; node_id: number; name?: string; source: string; plan_id?: number; plan_name?: string; effect?: string; reason?: string; expires_at?: string }
type Exception = { id: number; user_id: number; node_type: string; node_id: number; effect: 'allow' | 'deny'; reason: string; status?: string; starts_at?: string; expires_at?: string }
type CatalogNode = { type: string; id: number; key: string; name: string; entry_protocol?: string; exit_region?: string }

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
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

export function UserPlanDialog({ isOpen, user, binding, plans, client, onRefresh, onClose }: {
  isOpen: boolean
  user: { id: number; username: string }
  binding?: Binding
  plans: Plan[]
  client: AnyClient
  onRefresh?: () => Promise<unknown>
  onClose: () => void
}) {
  const [planID, setPlanID] = React.useState(binding?.plan_id || 0)
  const [startsAt, setStartsAt] = React.useState(toLocalInputValue(binding?.starts_at))
  const [expiresAt, setExpiresAt] = React.useState(toLocalInputValue(binding?.expires_at))
  const [applyBusy, setApplyBusy] = React.useState(false)
  const [removeOpen, setRemoveOpen] = React.useState(false)
  const [message, setMessage] = React.useState('')
  const [nodes, setNodes] = React.useState<EffectiveNode[]>([])
  const [exceptions, setExceptions] = React.useState<Exception[]>([])
  const [exceptionNames, setExceptionNames] = React.useState<Record<string, string>>({})
  const [exForm, setExForm] = React.useState({ node_key: '', effect: 'allow' as 'allow' | 'deny', reason: '', expires_at: '' })
  const [exBusy, setExBusy] = React.useState(false)
  const [searchQuery, setSearchQuery] = React.useState('')
  const [searchResults, setSearchResults] = React.useState<CatalogNode[]>([])
  const [changeID, setChangeID] = React.useState<number | null>(null)
  const { status: deliveryStatus, snapshot: changeSnapshot } = useAccessChangeStatus(client, changeID)

  const reload = async () => {
    try {
      const [nres, xres] = await Promise.all([
        client.request<{ nodes: EffectiveNode[] }>(`/users/${user.id}/nodes`),
        client.request<{ user_node_exceptions: Exception[] }>(`/user-node-exceptions?user_id=${user.id}`),
      ])
      setNodes(nres.nodes || [])
      setExceptions(xres.user_node_exceptions || [])
      const names: Record<string, string> = {}
      for (const node of nres.nodes || []) {
        if (node.name) names[node.key] = node.name
      }
      const missing = new Map((xres.user_node_exceptions || []).map(ex => [`${ex.node_type}:${ex.node_id}`, ex]))
      await Promise.all(Array.from(missing, async ([key, ex]) => {
        if (names[key]) return
        try {
          const res = await client.request<{ node: CatalogNode }>(`/assignable-nodes/${ex.node_type}/${ex.node_id}`)
          if (res.node?.name) names[key] = res.node.name
        } catch { /* Keep the authorization removable if its node is unavailable. */ }
      }))
      setExceptionNames(names)
      await onRefresh?.()
    } catch (e: any) {
      setMessage(e?.message || String(e))
    }
  }
  useRegisterPageRefresh(() => {
    if (!isOpen) return
    return reload()
  })

  React.useEffect(() => {
    if (!isOpen) return
    setPlanID(binding?.plan_id || 0)
    setStartsAt(toLocalInputValue(binding?.starts_at))
    setExpiresAt(toLocalInputValue(binding?.expires_at))
    setMessage('')
    setExForm({ node_key: '', effect: 'allow', reason: '', expires_at: '' })
    setSearchQuery('')
    setSearchResults([])
    setChangeID(null)
    setRemoveOpen(false)
    void reload()
  }, [isOpen, user.id])

  React.useEffect(() => {
    setPlanID(binding?.plan_id || 0)
    setStartsAt(toLocalInputValue(binding?.starts_at))
    setExpiresAt(toLocalInputValue(binding?.expires_at))
  }, [binding?.plan_id, binding?.starts_at, binding?.expires_at])

  React.useEffect(() => {
    if (isOpen && changeSnapshot?.change_id === changeID) void reload()
  }, [isOpen, changeID, changeSnapshot?.status])

  const applyAssignment = async (remove = false) => {
    if (!remove && !planID) { setMessage('请先选择套餐'); return }
    setApplyBusy(true)
    setMessage('')
    try {
      const res = await client.request<any>('/users/plan-assignment/apply', {
        method: 'POST',
        body: JSON.stringify(remove
          ? { user_ids: [user.id], plan_id: 0 }
          : { user_ids: [user.id], plan_id: planID, starts_at: fromLocalInputValue(startsAt), expires_at: fromLocalInputValue(expiresAt) }),
      })
      if (res.access_change_id) setChangeID(res.access_change_id)
      setRemoveOpen(false)
      setMessage(remove
        ? `已提交移除套餐${res.access_change_id ? `（变更 #${res.access_change_id}）` : ''}`
        : res.status === 'scheduled'
        ? `已排定：变更 #${res.access_change_id}，将于 ${fmtDate(res.activate_at)} 生效`
        : res.access_change_id ? `已保存分配：变更 #${res.access_change_id}（${res.status}）` : '已保存')
      await reload()
    } catch (e: any) {
      setMessage('应用失败：' + (e?.message || String(e)))
    } finally {
      setApplyBusy(false)
    }
  }

  const searchNodes = async (query: string) => {
    setSearchQuery(query)
    if (!query.trim()) { setSearchResults([]); return }
    try {
      const params = new URLSearchParams({ query, page: '1', page_size: '50' })
      const res = await client.request<{ nodes: CatalogNode[] }>('/assignable-nodes?' + params.toString())
      setSearchResults(res.nodes || [])
    } catch { setSearchResults([]) }
  }

  const createException = async () => {
    const node = searchResults.find(n => n.key === exForm.node_key)
    if (!node) { setMessage('请先搜索并选择节点'); return }
    setExBusy(true)
    setMessage('')
    try {
      const payload: any = {
        user_id: user.id, node_type: node.type, node_id: node.id,
        effect: exForm.effect, reason: exForm.reason.trim(),
      }
      const expires = fromLocalInputValue(exForm.expires_at)
      if (expires) payload.expires_at = expires
      const res = await client.request<any>('/user-node-exceptions', {
        method: 'POST',
        body: JSON.stringify(payload),
      })
      setExForm({ node_key: '', effect: 'allow', reason: '', expires_at: '' })
      setSearchResults([])
      if (res.access_change_id) setChangeID(res.access_change_id)
      setMessage(res.access_change_id ? `已创建例外，正在部署（变更 #${res.access_change_id}）` : '已创建例外')
      await reload()
    } catch (e: any) {
      setMessage('创建失败：' + (e?.message || String(e)))
    } finally {
      setExBusy(false)
    }
  }

  const revokeException = async (ex: Exception) => {
    setMessage('')
    try {
      const res = await client.request<any>(`/user-node-exceptions/${ex.id}`, { method: 'DELETE' })
      if (res.access_change_id) setChangeID(res.access_change_id)
      setMessage(res.revoking ? `正在撤销（变更 #${res.access_change_id}）` : '已删除例外')
      await reload()
    } catch (e: any) {
      setMessage('撤销失败：' + (e?.message || String(e)))
    }
  }

  const currentPlan = plans.find(p => p.id === (binding?.plan_id || 0))
  const exceptionName = (ex: Exception) => exceptionNames[`${ex.node_type}:${ex.node_id}`] || `节点名称不可用（#${ex.node_id}）`

  return (
    <>
    <Dialog
      isOpen={isOpen}
      onClose={onClose}
      title={`套餐与例外：${user.username}`}
      size="xl"
      className="user-plan-dialog"
      footer={<Button variant="outline" onClick={onClose}>关闭</Button>}
    >
      <div className="user-plan-dialog-stack">
        {message && <p role="status" className="user-plan-dialog-message" style={{ color: message.includes('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>{message}</p>}
        {changeID ? <AuthorizationStatusBadge status={deliveryStatus} /> : null}
        <div className="user-plan-dialog-layout">
          <div className="user-plan-dialog-col">
            <section className="card-custom user-plan-dialog-card">
              <h3>套餐分配</h3>
              <div className="user-plan-dialog-assign">
                <Select aria-label="分配套餐" value={planID} onChange={e => setPlanID(Number(e.target.value))}>
                  <option value={0}>选择套餐</option>
                  {plans.filter(p => p.enabled).map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                </Select>
                <DateTimePicker value={startsAt} onChange={setStartsAt} placeholder="生效时间（可选）" aria-label="生效时间" title="生效时间" />
                <DateTimePicker value={expiresAt} onChange={setExpiresAt} placeholder="到期时间（可选）" aria-label="到期时间" title="到期时间" />
                <Button size="sm" disabled={!planID || applyBusy} onClick={() => void applyAssignment()}>{applyBusy ? '保存中…' : '保存套餐'}</Button>
              </div>
            </section>
            <section className="card-custom user-plan-dialog-card">
              <h3>现有套餐</h3>
              {currentPlan ? (
                <div>
                  <div className="section-toolbar">
                    <Badge variant={currentPlan.enabled ? 'success' : 'secondary'}>{currentPlan.name}</Badge>
                    <Button variant="outline" size="sm" disabled={applyBusy} onClick={() => setRemoveOpen(true)} aria-label={`移除套餐 ${currentPlan.name}`}><Trash2 size={14} /> 移除套餐</Button>
                  </div>
                  <p className="muted">开始 {fmtDate(binding?.starts_at)} · 到期 {fmtDate(binding?.expires_at)}</p>
                </div>
              ) : <p className="muted">未绑定套餐</p>}
            </section>
          </div>
          <div className="user-plan-dialog-col">
            <section className="card-custom user-plan-dialog-card">
              <h3>有效节点（{nodes.length}）</h3>
              <div className="user-plan-dialog-nodes">
                {nodes.map(n => (
                  <div key={n.key} className="user-plan-dialog-node">
                    <span>{n.name || '未命名节点'}</span>
                    {n.source === 'plan' && <Badge variant="secondary">{n.plan_name || '套餐'}</Badge>}
                    {n.source === 'exception_allow' && <Badge variant="success">允许</Badge>}
                  </div>
                ))}
                {nodes.length === 0 && <p className="muted">暂无有效节点</p>}
              </div>
            </section>
            <section className="card-custom user-plan-dialog-card is-fill">
              <div className="section-toolbar">
                <div>
                  <h3>用户授权</h3>
                  <p className="muted">allow 先部署凭据再对订阅可见；deny 立即隐藏并撤销。时间留空则永久有效。</p>
                </div>
                <Button variant="ghost" size="icon" onClick={() => void reload()} aria-label="刷新授权" title="刷新授权"><RefreshCw size={14} /></Button>
              </div>
              <div className="user-plan-dialog-table-wrap">
                <table className="user-plan-dialog-table">
                  <thead><tr><th>节点</th><th>效果</th><th>状态</th><th>原因</th><th>到期</th><th>操作</th></tr></thead>
                  <tbody>
                    {exceptions.map(ex => (
                      <tr key={ex.id}>
                        <td>{exceptionName(ex)}</td>
                        <td><Badge variant={ex.effect === 'allow' ? 'success' : 'destructive'}>{ex.effect === 'allow' ? '允许' : '拒绝'}</Badge></td>
                        <td><Badge variant="outline">{ex.status || 'active'}</Badge></td>
                        <td className="muted">{ex.reason}</td>
                        <td className="muted">{fmtDate(ex.expires_at)}</td>
                        <td>
                          <Button variant="ghost" size="icon" onClick={() => void revokeException(ex)} aria-label={`撤销 ${exceptionName(ex)}`} title="撤销">
                            <Trash2 size={14} />
                          </Button>
                        </td>
                      </tr>
                    ))}
                    {exceptions.length === 0 && <tr><td colSpan={6} className="muted user-plan-dialog-empty">暂无例外</td></tr>}
                  </tbody>
                </table>
              </div>
              <div className="user-plan-dialog-exception-form">
                <Input aria-label="搜索授权节点" value={searchQuery} onChange={e => void searchNodes(e.target.value)} placeholder="搜索节点（输入至少 1 个字符）" />
                <Select aria-label="授权效果" value={exForm.effect} onChange={e => setExForm(f => ({ ...f, effect: e.target.value as 'allow' | 'deny' }))}>
                  <option value="allow">允许</option><option value="deny">拒绝</option>
                </Select>
                <DateTimePicker value={exForm.expires_at} onChange={val => setExForm(f => ({ ...f, expires_at: val }))} placeholder="到期时间（可选，永久）" aria-label="到期时间" title="到期时间" />
                <Input aria-label="授权原因" value={exForm.reason} onChange={e => setExForm(f => ({ ...f, reason: e.target.value }))} placeholder="原因（可选）" />
                <Button size="sm" disabled={exBusy} onClick={() => void createException()}><Plus size={14} /> 创建授权</Button>
              </div>
              {searchResults.length > 0 && (
                <div className="user-plan-dialog-search-results">
                  {searchResults.map(n => (
                    <label key={n.key}>
                      <input type="radio" name="exception-node" checked={exForm.node_key === n.key} onChange={() => setExForm(f => ({ ...f, node_key: n.key }))} />
                      <span>{n.name}</span>
                      <span className="muted">{n.entry_protocol || ''} {n.exit_region ? `· ${n.exit_region}` : ''}</span>
                    </label>
                  ))}
                </div>
              )}
            </section>
          </div>
        </div>
      </div>
    </Dialog>
    <Dialog isOpen={isOpen && removeOpen} onClose={() => { if (!applyBusy) setRemoveOpen(false) }} title="移除用户套餐" size="sm"
      footer={<>
        <Button variant="outline" disabled={applyBusy} onClick={() => setRemoveOpen(false)}>取消</Button>
        <Button variant="destructive" disabled={applyBusy} onClick={() => void applyAssignment(true)}>{applyBusy ? '移除中…' : '确认移除'}</Button>
      </>}
    >
      <p>将移除 {user.username} 的「{currentPlan?.name}」套餐分配，并撤销来自该套餐的节点权限。套餐本身和单独设置的用户授权会保留。</p>
      {message.includes('失败') && <p role="alert">{message}</p>}
    </Dialog>
    </>
  )
}
