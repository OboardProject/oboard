import * as React from 'react'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Select } from '../components/ui/select'
import { Input } from '../components/ui/input'
import { DateTimePicker } from '../components/ui/datetime-picker'
import { RefreshCw, Trash2, Plus } from 'lucide-react'

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

export function UserPlanDialog({ user, binding, plans, client, onClose }: {
  user: { id: number; username: string }
  binding?: Binding
  plans: Plan[]
  client: AnyClient
  onClose: () => void
}) {
  const [planID, setPlanID] = React.useState(binding?.plan_id || 0)
  const [startsAt, setStartsAt] = React.useState(toLocalInputValue(binding?.starts_at))
  const [expiresAt, setExpiresAt] = React.useState(toLocalInputValue(binding?.expires_at))
  const [preview, setPreview] = React.useState<any>(null)
  const [previewBusy, setPreviewBusy] = React.useState(false)
  const [applyBusy, setApplyBusy] = React.useState(false)
  const [message, setMessage] = React.useState('')
  const [nodes, setNodes] = React.useState<EffectiveNode[]>([])
  const [exceptions, setExceptions] = React.useState<Exception[]>([])
  const [exForm, setExForm] = React.useState({ node_key: '', effect: 'allow' as 'allow' | 'deny', reason: '', expires_at: '' })
  const [exBusy, setExBusy] = React.useState(false)
  const [searchQuery, setSearchQuery] = React.useState('')
  const [searchResults, setSearchResults] = React.useState<CatalogNode[]>([])

  const reload = async () => {
    try {
      const [nres, xres] = await Promise.all([
        client.request<{ nodes: EffectiveNode[] }>(`/users/${user.id}/nodes`),
        client.request<{ user_node_exceptions: Exception[] }>(`/user-node-exceptions?user_id=${user.id}`),
      ])
      setNodes(nres.nodes || [])
      setExceptions(xres.user_node_exceptions || [])
    } catch (e: any) {
      setMessage(e?.message || String(e))
    }
  }

  React.useEffect(() => { void reload() }, [])

  const runPreview = async () => {
    if (!planID) { setMessage('请先选择套餐'); return }
    setPreviewBusy(true)
    setMessage('')
    try {
      const res = await client.request<any>('/users/plan-assignment/preview', {
        method: 'POST',
        body: JSON.stringify({ user_ids: [user.id], plan_id: planID, starts_at: fromLocalInputValue(startsAt), expires_at: fromLocalInputValue(expiresAt) }),
      })
      setPreview(res.preview || res)
    } catch (e: any) {
      setMessage('预览失败：' + (e?.message || String(e)))
    } finally {
      setPreviewBusy(false)
    }
  }

  const applyAssignment = async () => {
    if (!planID) { setMessage('请先选择套餐'); return }
    setApplyBusy(true)
    setMessage('')
    try {
      const res = await client.request<any>('/users/plan-assignment/apply', {
        method: 'POST',
        body: JSON.stringify({ user_ids: [user.id], plan_id: planID, starts_at: fromLocalInputValue(startsAt), expires_at: fromLocalInputValue(expiresAt) }),
      })
      setPreview(null)
      setMessage(res.status === 'scheduled'
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
      setMessage(res.revoking ? `正在撤销（变更 #${res.access_change_id}）` : '已删除例外')
      await reload()
    } catch (e: any) {
      setMessage('撤销失败：' + (e?.message || String(e)))
    }
  }

  const currentPlan = plans.find(p => p.id === (binding?.plan_id || 0))

  return (
    <Dialog isOpen onClose={onClose} title={`套餐与例外：${user.username}`} size="xl">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(240px, 1fr))', gap: 12 }}>
          <div className="card-custom" style={{ padding: 14 }}>
            <h3 style={{ marginTop: 0 }}>当前套餐</h3>
            {currentPlan ? (
              <div>
                <Badge variant={currentPlan.enabled ? 'success' : 'secondary'}>{currentPlan.name}</Badge>
                <div className="muted" style={{ fontSize: 12, marginTop: 6 }}>
                  状态：{binding?.status || 'active'} · 开始 {fmtDate(binding?.starts_at)} · 到期 {fmtDate(binding?.expires_at)}
                </div>
              </div>
            ) : <p className="muted">未绑定套餐</p>}
          </div>
          <div className="card-custom" style={{ padding: 14 }}>
            <h3 style={{ marginTop: 0 }}>有效节点（{nodes.length}）</h3>
            <div style={{ maxHeight: 140, overflow: 'auto', display: 'flex', flexDirection: 'column', gap: 4 }}>
              {nodes.map(n => (
                <div key={n.key} style={{ fontSize: 13, display: 'flex', justifyContent: 'space-between', gap: 8 }}>
                  <span style={{ fontWeight: 600 }}>{n.name || n.key}</span>
                  {n.source === 'plan' && <Badge variant="secondary">{n.plan_name || '套餐'}</Badge>}
                  {n.source === 'exception_allow' && <Badge variant="success">允许</Badge>}
                </div>
              ))}
              {nodes.length === 0 && <p className="muted" style={{ fontSize: 12 }}>暂无有效节点</p>}
            </div>
          </div>
        </div>

        <div className="card-custom" style={{ padding: 14 }}>
          <h3 style={{ marginTop: 0 }}>更换套餐</h3>
          <div className="form" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))' }}>
            <Select value={planID} onChange={e => setPlanID(Number(e.target.value))}>
              <option value={0}>选择套餐</option>
              {plans.filter(p => p.enabled).map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
            </Select>
            <DateTimePicker value={startsAt} onChange={setStartsAt} placeholder="生效时间（可选）" aria-label="生效时间" title="生效时间" />
            <DateTimePicker value={expiresAt} onChange={setExpiresAt} placeholder="到期时间（可选）" aria-label="到期时间" title="到期时间" />
            <Button variant="outline" size="sm" disabled={previewBusy} onClick={() => void runPreview()}>预览影响</Button>
          </div>
          {preview && (
            <div style={{ marginTop: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <p className="muted">新增 {preview.nodes_added?.length || 0} · 移除 {preview.nodes_removed?.length || 0} · 受影响服务器 {preview.affected_servers?.length || 0} 台</p>
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {(preview.nodes_added || []).map((k: string) => <Badge key={k} variant="success">+ {k}</Badge>)}
                {(preview.nodes_removed || []).map((k: string) => <Badge key={k} variant="destructive">− {k}</Badge>)}
              </div>
              <div style={{ display: 'flex', gap: 8 }}>
                <Button size="sm" disabled={applyBusy} onClick={() => void applyAssignment()}>{applyBusy ? '保存中...' : '保存分配'}</Button>
                <Button size="sm" variant="ghost" onClick={() => setPreview(null)}>取消</Button>
              </div>
            </div>
          )}
        </div>

        <div className="card-custom" style={{ padding: 14 }}>
          <div className="section-toolbar">
            <div><h3 style={{ margin: 0 }}>用户授权</h3><p className="muted">allow 先部署凭据再对订阅可见；deny 立即隐藏并撤销。时间留空则永久有效。</p></div>
            <Button variant="ghost" size="sm" onClick={() => void reload()}><RefreshCw size={14} /></Button>
          </div>
          <table className="user-data-table" style={{ width: '100%' }}>
            <thead><tr><th>节点</th><th>效果</th><th>状态</th><th>原因</th><th>到期</th><th style={{ textAlign: 'right' }}>操作</th></tr></thead>
            <tbody>
              {exceptions.map(ex => (
                <tr key={ex.id}>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{ex.node_type}:{ex.node_id}</td>
                  <td><Badge variant={ex.effect === 'allow' ? 'success' : 'destructive'}>{ex.effect === 'allow' ? '允许' : '拒绝'}</Badge></td>
                  <td><Badge variant="outline">{ex.status || 'active'}</Badge></td>
                  <td className="muted">{ex.reason}</td>
                  <td className="muted">{fmtDate(ex.expires_at)}</td>
                  <td style={{ textAlign: 'right' }}><Button variant="ghost" size="sm" onClick={() => void revokeException(ex)}><Trash2 size={14} /></Button></td>
                </tr>
              ))}
              {exceptions.length === 0 && <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 12 }}>暂无例外</td></tr>}
            </tbody>
          </table>
          <div style={{ marginTop: 10, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            <Input value={searchQuery} onChange={e => void searchNodes(e.target.value)} placeholder="搜索节点（输入至少 1 个字符）" style={{ maxWidth: 220 }} />
            <Select value={exForm.effect} onChange={e => setExForm(f => ({ ...f, effect: e.target.value as 'allow' | 'deny' }))}>
              <option value="allow">允许</option><option value="deny">拒绝</option>
            </Select>
            <DateTimePicker value={exForm.expires_at} onChange={val => setExForm(f => ({ ...f, expires_at: val }))} placeholder="到期时间（可选，永久）" aria-label="到期时间" title="到期时间" style={{ maxWidth: 200 }} />
            <Input value={exForm.reason} onChange={e => setExForm(f => ({ ...f, reason: e.target.value }))} placeholder="原因（可选）" style={{ maxWidth: 200 }} />
            <Button size="sm" disabled={exBusy} onClick={() => void createException()}><Plus size={14} /> 创建授权</Button>
          </div>
          {searchResults.length > 0 && (
            <div className="card-custom" style={{ marginTop: 8, maxHeight: 180, overflow: 'auto' }}>
              {searchResults.map(n => (
                <label key={n.key} style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '6px 8px', cursor: 'pointer' }}>
                  <input type="radio" name="exception-node" checked={exForm.node_key === n.key} onChange={() => setExForm(f => ({ ...f, node_key: n.key }))} />
                  <span style={{ fontWeight: 600 }}>{n.name}</span>
                  <span className="muted" style={{ fontSize: 12 }}>{n.entry_protocol || ''} {n.exit_region ? `· ${n.exit_region}` : ''}</span>
                </label>
              ))}
            </div>
          )}
        </div>

        {message && <p style={{ color: message.includes('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>{message}</p>}
        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <Button variant="outline" onClick={onClose}>关闭</Button>
        </div>
      </div>
    </Dialog>
  )
}
