import * as React from 'react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select } from '../ui/select'
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { ArrowDown, ArrowUp, ChevronDown, ChevronUp, GripVertical, MoveDown, MoveUp, Plus, RefreshCw, Save, Sparkles, Trash2 } from 'lucide-react'
import { Dialog } from '../ui/dialog'
import { ApplyOrderTemplateDialog, type OrderTemplateSummary } from './ApplyOrderTemplateDialog'
import type { TemplatePolicy } from './NodeOrderTemplateEditor'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

export type OrderingPlan = {
  id: number
  name: string
  lock_version: number
  current_revision_id: number
  latest_revision_id: number
  pending_revision_id?: number
}

type OrderingPolicy = {
  version: number
  mode: string
  manual_seed?: string
  exit_region_order: string[]
  entry_region_order_mode: string
  entry_region_order: string[]
  entry_order: string[]
  new_node_placement: 'by_template' | 'append' | 'pending'
  unmatched_placement: 'append' | 'pending'
}

type OrderingNode = {
  key: string
  node_type: string
  node_id: number
  name: string
  group: string
  entry_key?: string
  entry_name?: string
  entry_protocol?: string
  entry_port?: number
  entry_server_id?: number
  entry_region?: string
  exit_server_id?: number
  exit_region?: string
  manual_position?: number
  effective_position: number
  renderable: boolean
  warning?: string
}

type OrderingState = {
  plan_id: number
  lock_version: number
  base_revision_id: number
  revision_id: number
  version_no: number
  read_only: boolean
  is_current: boolean
  is_latest: boolean
  pending_revision_id: number
  policy: OrderingPolicy
  nodes: OrderingNode[]
  unplaced_count: number
  warnings: string[]
  order_template_id?: number
  order_template_revision?: number
  order_template?: OrderTemplateSummary
  template_update_available?: boolean
  template_archived?: boolean
}

const MODE_LABELS: Record<string, string> = {
  legacy_group_name: '兼容排序',
  exit_region: '出口地区',
  entry: '入口',
  manual: '手动',
}

const MODES = ['exit_region', 'entry', 'manual'] as const

function emptyPolicy(mode: string): OrderingPolicy {
  return {
    version: 2,
    mode,
    manual_seed: 'exit_region',
    exit_region_order: [],
    entry_region_order_mode: 'inherit_exit',
    entry_region_order: [],
    entry_order: [],
    new_node_placement: 'by_template',
    unmatched_placement: 'append',
  }
}

function normalizeOrderingPolicy(policy: OrderingPolicy): OrderingPolicy {
  return {
    ...policy,
    exit_region_order: policy.exit_region_order || [],
    entry_region_order: policy.entry_region_order || [],
    entry_order: policy.entry_order || [],
    new_node_placement: policy.new_node_placement || 'pending',
    unmatched_placement: policy.unmatched_placement || 'pending',
  }
}

function templatePolicyFromOrdering(policy: OrderingPolicy): TemplatePolicy {
  return {
    version: 1,
    base_mode: policy.mode === 'entry' || (policy.mode === 'manual' && policy.manual_seed === 'entry') ? 'entry' : 'exit_region',
    exit_region_order: policy.exit_region_order || [],
    entry_region_order_mode: policy.entry_region_order_mode === 'custom' ? 'custom' : 'inherit_exit',
    entry_region_order: policy.entry_region_order || [],
    entry_order: policy.entry_order || [],
    new_node_placement: policy.new_node_placement || 'pending',
    unmatched_placement: policy.unmatched_placement || 'pending',
  }
}

function normalizeRegion(code: string) {
  return (code || '').trim().toUpperCase()
}

function moveItem<T>(list: T[], index: number, delta: number): T[] {
  const next = list.slice()
  const target = index + delta
  if (target < 0 || target >= next.length) return next
  const [item] = next.splice(index, 1)
  next.splice(target, 0, item)
  return next
}

export function PlanNodeOrderingPanel({ plan, data, client, notify, onSaved }: {
  plan: OrderingPlan
  data: any
  client: AnyClient
  notify?: (message: string, tone?: 'success' | 'error' | 'warning') => void
  onSaved?: () => void
}) {
  const [state, setState] = React.useState<OrderingState | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')
  const [workingPolicy, setWorkingPolicy] = React.useState<OrderingPolicy | null>(null)
  const [manualOrder, setManualOrder] = React.useState<string[]>([])
  const [previewNodes, setPreviewNodes] = React.useState<OrderingNode[] | null>(null)
  const [previewWarnings, setPreviewWarnings] = React.useState<string[]>([])
  const [previewUnplaced, setPreviewUnplaced] = React.useState(0)
  const [regionInput, setRegionInput] = React.useState('')
  const [previewing, setPreviewing] = React.useState(false)
  const [applyTemplateOpen, setApplyTemplateOpen] = React.useState(false)
  const [saveTemplateOpen, setSaveTemplateOpen] = React.useState(false)
  const [templateName, setTemplateName] = React.useState('')
  const [templateDescription, setTemplateDescription] = React.useState('')

  const loadOrdering = React.useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      // Defaults to the latest saved version: the editor's working base.
      const res = await client.request<OrderingState>(`/subscription-plans/${plan.id}/ordering`)
      setState(res)
      setWorkingPolicy(normalizeOrderingPolicy(JSON.parse(JSON.stringify(res.policy))))
      const placed = (res.nodes || []).filter(n => n.manual_position !== undefined).sort((a, b) => (a.manual_position ?? 0) - (b.manual_position ?? 0))
      setManualOrder(placed.map(n => n.key))
      setPreviewNodes(null)
      setPreviewWarnings([])
      setPreviewUnplaced(0)
    } catch (e: any) {
      setError(e?.message || String(e))
      setState(null)
      setWorkingPolicy(null)
    } finally {
      setLoading(false)
    }
  }, [plan.id, client])

  React.useEffect(() => { void loadOrdering() }, [loadOrdering])

  const setMode = (mode: string) => {
    if (!workingPolicy) return
    const previousMode = workingPolicy.mode
    const next: OrderingPolicy = {
      ...workingPolicy,
      version: 2,
      mode,
      manual_seed: mode === 'manual' && (previousMode === 'exit_region' || previousMode === 'entry') ? previousMode : workingPolicy.manual_seed || 'exit_region',
      new_node_placement: workingPolicy.new_node_placement || 'pending',
      unmatched_placement: workingPolicy.unmatched_placement || 'pending',
    }
    if (mode === 'manual' && previousMode !== 'manual') {
      setManualOrder((state?.nodes || []).slice().sort((a, b) => a.effective_position - b.effective_position).map(node => node.key))
      notify?.('已进入自定义排序；以后新增节点仍按当前规则处理', 'success')
    }
    setWorkingPolicy(next)
    setPreviewNodes(null)
  }

  const setRegionList = (field: 'exit_region_order' | 'entry_region_order', list: string[]) => {
    if (!workingPolicy) return
    setWorkingPolicy({ ...workingPolicy, [field]: list })
    setPreviewNodes(null)
  }

  const addRegion = (field: 'exit_region_order' | 'entry_region_order') => {
    if (!workingPolicy) return
    const code = normalizeRegion(regionInput)
    if (!code || workingPolicy[field].includes(code)) return
    setRegionList(field, [...workingPolicy[field], code])
    setRegionInput('')
  }

  const serverName = (id?: number) => {
    const server = (data.servers || []).find((s: any) => s.id === id)
    return server?.name || (id ? `服务器 #${id}` : '')
  }

  const entryLabel = (node: OrderingNode) => {
    const region = node.entry_region ? `[${node.entry_region}] ` : ''
    const name = node.entry_name || serverName(node.entry_server_id) || node.entry_key || '未解析入口'
    const detail = [node.entry_protocol, node.entry_port ? String(node.entry_port) : ''].filter(Boolean).join(' · ')
    return `${region}${name}${detail ? ` · ${detail}` : ''}`
  }

  const entryKeys = React.useMemo(() => {
    const seen = new Set<string>()
    const out: { key: string; label: string; count: number }[] = []
    for (const node of state?.nodes || []) {
      const key = node.entry_key
      if (!key || seen.has(key)) continue
      seen.add(key)
      const count = (state?.nodes || []).filter(n => n.entry_key === key).length
      out.push({ key, label: entryLabel(node), count })
    }
    const order = new Map(workingPolicy?.entry_order.map((k, i) => [k, i]) || [])
    out.sort((a, b) => {
      const ai = order.get(a.key), bi = order.get(b.key)
      if (ai !== undefined && bi !== undefined) return ai - bi
      if (ai !== undefined) return -1
      if (bi !== undefined) return 1
      return a.key.localeCompare(b.key)
    })
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state, workingPolicy])

  const setEntryOrder = (list: string[]) => {
    if (!workingPolicy) return
    setWorkingPolicy({ ...workingPolicy, entry_order: list })
    setPreviewNodes(null)
  }

  const entryKeyList = React.useMemo(() => entryKeys.map(e => e.key), [entryKeys])

  const runPreview = async (policyOverride?: OrderingPolicy, manualOverride?: string[]) => {
    if (!state) return
    setPreviewing(true)
    setError('')
    try {
      const policy = policyOverride || workingPolicy || emptyPolicy('exit_region')
      const order = manualOverride !== undefined ? manualOverride : manualOrder
      const res = await client.request<{ nodes: OrderingNode[]; unplaced_count: number; warnings: string[] }>(`/subscription-plans/${plan.id}/ordering/preview`, {
        method: 'POST',
        body: JSON.stringify({ base_revision_id: state.base_revision_id, policy, manual_node_order: order }),
      })
      setPreviewNodes(res.nodes || [])
      setPreviewUnplaced(res.unplaced_count || 0)
      setPreviewWarnings(res.warnings || [])
    } catch (e: any) {
      setError(e?.message || String(e))
    } finally {
      setPreviewing(false)
    }
  }

  const generateManualSeed = async () => {
    if (!workingPolicy) return
    const seedPolicy: OrderingPolicy = { ...workingPolicy, mode: 'manual', manual_seed: workingPolicy.manual_seed || 'exit_region' }
    setPreviewing(true)
    setError('')
    try {
      const res = await client.request<{ nodes: OrderingNode[] }>(`/subscription-plans/${plan.id}/ordering/preview`, {
        method: 'POST',
        body: JSON.stringify({ base_revision_id: state?.base_revision_id || 0, policy: seedPolicy, manual_node_order: [] }),
      })
      const ordered = (res.nodes || []).map(n => n.key)
      setManualOrder(ordered)
      setWorkingPolicy(seedPolicy)
      setPreviewNodes(null)
      notify?.('已按规则生成手动顺序，保存为新版本后生效', 'success')
    } catch (e: any) {
      setError(e?.message || String(e))
    } finally {
      setPreviewing(false)
    }
  }

  const moveManual = (key: string, delta: number) => {
    setManualOrder(prev => {
      const index = prev.indexOf(key)
      if (index < 0) return prev
      return moveItem(prev, index, delta)
    })
  }

  const moveManualToEdge = (key: string, top: boolean) => {
    setManualOrder(prev => {
      const index = prev.indexOf(key)
      if (index < 0) return prev
      const next = prev.slice()
      const [item] = next.splice(index, 1)
      if (top) next.unshift(item)
      else next.push(item)
      return next
    })
  }

  const removeManual = (key: string) => {
    setManualOrder(prev => prev.filter(k => k !== key))
  }

  const appendUnplaced = (key: string) => {
    setManualOrder(prev => (prev.includes(key) ? prev : [...prev, key]))
  }

  const appendAllUnplaced = () => {
    const placed = new Set(manualOrder)
    const unplaced = (state?.nodes || []).map(n => n.key).filter(k => !placed.has(k))
    setManualOrder(prev => [...prev, ...unplaced])
  }

  const saveOrdering = async () => {
    if (!state || !workingPolicy) return
    setBusy(true)
    setError('')
    try {
      const res = await client.request<{ version_no: number; effective_immediately: boolean; no_change?: boolean }>(`/subscription-plans/${plan.id}/ordering/versions`, {
        method: 'POST',
        body: JSON.stringify({
          base_revision_id: state.base_revision_id,
          expected_lock_version: state.lock_version,
          policy: workingPolicy,
          manual_node_order: workingPolicy.mode === 'manual' ? manualOrder : [],
        }),
      })
      if (res.no_change) {
        notify?.('排序没有变化，未创建新版本', 'warning')
      } else {
        notify?.(`已保存为新版本 V${res.version_no}，订阅立即生效`, 'success')
      }
      await loadOrdering()
      onSaved?.()
    } catch (e: any) {
      const message = e?.message || String(e)
      setError(message.includes('conflict') || message.includes('409') ? '套餐已发生变化（版本冲突），请重新加载后重试' : message)
    } finally {
      setBusy(false)
    }
  }

  const applyTemplate = async (template: OrderTemplateSummary, applyMode: 'preserve_manual' | 'rebuild') => {
    if (!state) return
    setBusy(true)
    setError('')
    try {
      const result = await client.request<{ no_change?: boolean }>(`/subscription-plans/${plan.id}/ordering/apply-template`, {
        method: 'POST',
        body: JSON.stringify({
          template_id: template.id,
          template_revision: template.revision,
          base_revision_id: state.base_revision_id,
          expected_lock_version: state.lock_version,
          apply_mode: applyMode,
          change_summary: `应用模板「${template.name}」r${template.revision}`,
        }),
      })
      notify?.(result.no_change ? '排序规则没有变化' : `已应用「${template.name}」r${template.revision}`, result.no_change ? 'warning' : 'success')
      setApplyTemplateOpen(false)
      await loadOrdering()
      onSaved?.()
    } catch (reason: any) {
      const message = reason?.message || String(reason)
      setError(message.includes('409') || message.includes('conflict') ? '方案或模板已被其他操作更新，请重新加载后重试。' : message)
    } finally {
      setBusy(false)
    }
  }

  const saveRuleAsTemplate = async () => {
    if (!workingPolicy || !templateName.trim()) return
    setBusy(true)
    setError('')
    try {
      await client.request('/node-order-templates', {
        method: 'POST',
        body: JSON.stringify({ name: templateName.trim(), description: templateDescription.trim(), policy: templatePolicyFromOrdering(workingPolicy) }),
      })
      notify?.('排序规则已另存为模板', 'success')
      setSaveTemplateOpen(false)
      setTemplateName('')
      setTemplateDescription('')
    } catch (reason: any) {
      setError(reason?.message || String(reason))
    } finally {
      setBusy(false)
    }
  }

  const placedKeys = new Set(manualOrder)
  const isManual = workingPolicy?.mode === 'manual'
  const unplacedNodes = isManual ? (state?.nodes || []).filter(n => !placedKeys.has(n.key)) : []
  const applying = Boolean(state?.pending_revision_id)

  return (
    <div className="plan-ordering-panel" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
        <div>
          <h3 style={{ margin: 0 }}>订阅排序 · {plan.name}</h3>
          {state && (
            <p className="muted" style={{ margin: '4px 0 0' }}>
              最新 V{state.version_no} · {state.nodes.length} 个节点
              {state.is_current ? ' · 当前生效' : ''}
              {state.is_latest && !state.is_current ? ' · 最新保存' : ''}
              {applying ? ' · 有版本正在应用' : ''}
              {!state.is_latest ? ' · 只读（历史版本）' : ''}
            </p>
          )}
        </div>
      </div>

      {error && <p style={{ color: 'var(--color-danger)' }}>{error}</p>}
      {loading && <p className="muted">正在加载排序状态...</p>}
      {applying && (
        <p style={{ color: 'var(--color-warning)', margin: 0 }}>
          有套餐版本正在应用，应用完成前不能保存新的排序版本。
        </p>
      )}

      {state && workingPolicy && !loading && !state.read_only && (
        <>
          <div className="plan-order-template-status">
            <div>
              <span className="muted">当前排序</span>
              <strong>{MODE_LABELS[workingPolicy.mode] || workingPolicy.mode}</strong>
            </div>
            <div>
              <span className="muted">来源模板</span>
              <strong>{state.order_template?.name || '未使用模板'}</strong>
              {state.order_template_id && <small>方案快照 r{state.order_template_revision || 0}{state.order_template ? ` · 最新 r${state.order_template.revision}` : ''}</small>}
            </div>
            <div>
              <span className="muted">新增节点</span>
              <strong>{{ by_template: '按模板自动插入', append: '追加到末尾', pending: '进入待排区' }[workingPolicy.new_node_placement || 'pending']}</strong>
            </div>
            <div className="plan-order-template-actions">
              {state.template_update_available && <Badge variant="warning">模板有更新</Badge>}
              {state.template_archived && <Badge variant="secondary">模板已归档</Badge>}
              <Button variant="outline" size="sm" onClick={() => setApplyTemplateOpen(true)}>应用模板</Button>
              <Button variant="ghost" size="sm" onClick={() => setSaveTemplateOpen(true)}>另存规则为模板</Button>
            </div>
          </div>
          <div>
            <h3 style={{ marginTop: 0 }}>排序模式</h3>
            <div className="section-toolbar" style={{ gap: 8 }}>
              {MODE_LABELS[workingPolicy.mode] && workingPolicy.mode === 'legacy_group_name' && (
                <Badge variant="secondary">当前为兼容排序</Badge>
              )}
              {MODES.map(mode => (
                <Button key={mode} variant={workingPolicy.mode === mode ? 'default' : 'outline'} size="sm" onClick={() => setMode(mode)}>
                  {MODE_LABELS[mode]}
                </Button>
              ))}
            </div>
            {workingPolicy.mode === 'legacy_group_name' && (
              <p className="muted" style={{ marginTop: 8 }}>当前套餐使用兼容排序（分组 → 名称）。切换到出口地区、入口或手动排序后，订阅节点顺序将发生变化。</p>
            )}
          </div>

          {workingPolicy.mode === 'exit_region' && (
            <div>
              <h3>出口地区顺序</h3>
              <p className="muted">已配置的地区优先，未配置的有效地区按代码排序，未解析地区最后。</p>
              <RegionOrderEditor list={workingPolicy.exit_region_order} onChange={list => setRegionList('exit_region_order', list)} />
              <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                <Input value={regionInput} onChange={e => setRegionInput(e.target.value)} placeholder="如 JP" style={{ width: 120 }} onKeyDown={e => { if (e.key === 'Enter') addRegion('exit_region_order') }} />
                <Button variant="outline" size="sm" onClick={() => addRegion('exit_region_order')}><Plus size={14} /> 添加地区</Button>
              </div>
            </div>
          )}

          {workingPolicy.mode === 'entry' && (
            <>
              <div>
                <h3>入口地区顺序</h3>
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, marginBottom: 8 }}>
                  <input
                    type="checkbox"
                    checked={workingPolicy.entry_region_order_mode !== 'custom'}
                    onChange={e => setWorkingPolicy({ ...workingPolicy, entry_region_order_mode: e.target.checked ? 'inherit_exit' : 'custom' })}
                  />
                  入口地区顺序跟随出口地区顺序
                </label>
                {workingPolicy.entry_region_order_mode === 'custom' && (
                  <>
                    <RegionOrderEditor list={workingPolicy.entry_region_order} onChange={list => setRegionList('entry_region_order', list)} />
                    <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                      <Input value={regionInput} onChange={e => setRegionInput(e.target.value)} placeholder="如 JP" style={{ width: 120 }} onKeyDown={e => { if (e.key === 'Enter') addRegion('entry_region_order') }} />
                      <Button variant="outline" size="sm" onClick={() => addRegion('entry_region_order')}><Plus size={14} /> 添加地区</Button>
                    </div>
                  </>
                )}
              </div>
              <div>
                <h3>同地区内入口顺序</h3>
                <p className="muted">拖拽调整每个入口组的顺序；入口不能跨越地区。</p>
                {entryKeys.length === 0 ? (
                  <p className="muted">没有可排序的入口。</p>
                ) : (
                  <SortableList items={entryKeyList} onReorder={setEntryOrder} renderRow={(key, index) => {
                    const entry = entryKeys.find(e => e.key === key)
                    return (
                      <>
                        <span style={{ width: 28, fontSize: 12, color: 'var(--color-muted)' }}>{index + 1}</span>
                        <span style={{ flex: 1, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={entry?.label}>
                          {entry?.label || key}
                        </span>
                        <span className="muted" style={{ fontSize: 12 }}>{entry?.count || 0} 个节点</span>
                        <Button variant="ghost" size="icon" disabled={index === 0} onClick={() => setEntryOrder(moveItem(entryKeyList, index, -1))} aria-label="上移"><ChevronUp size={14} /></Button>
                        <Button variant="ghost" size="icon" disabled={index === entryKeyList.length - 1} onClick={() => setEntryOrder(moveItem(entryKeyList, index, 1))} aria-label="下移"><ChevronDown size={14} /></Button>
                      </>
                    )
                  }} />
                )}
              </div>
            </>
          )}

          {workingPolicy.mode === 'manual' && (
            <>
              <div>
                <h3>手动排序</h3>
                <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap' }}>
                  <Select value={workingPolicy.manual_seed || 'exit_region'} onChange={e => setWorkingPolicy({ ...workingPolicy, manual_seed: e.target.value })} style={{ width: 180 }}>
                    <option value="exit_region">基础规则：按出口地区</option>
                    <option value="entry">基础规则：按入口</option>
                  </Select>
                  <Button variant="outline" size="sm" busy={previewing} onClick={() => void generateManualSeed()}><Sparkles size={14} /> 按规则生成手动顺序</Button>
                  <Button variant="ghost" size="sm" onClick={appendAllUnplaced}><MoveDown size={14} /> 把待排节点追加到末尾</Button>
                </div>
                <p className="muted" style={{ marginTop: 8 }}>按住手柄拖拽调整顺序，也可使用上移、下移、移到顶部、移到底部按钮。已有节点的人工相对顺序会被保留。</p>
                <div style={{ marginTop: 8 }}>
                  {manualOrder.length === 0 ? (
                    <p className="muted">尚未放置节点，点击“按规则生成手动顺序”开始。</p>
                  ) : (
                    <SortableList items={manualOrder} onReorder={setManualOrder} renderRow={(key, index) => {
                      const node = (state.nodes || []).find(n => n.key === key)
                      if (!node) return null
                      return (
                        <>
                          <span style={{ width: 28, fontSize: 12, color: 'var(--color-muted)' }}>{index + 1}</span>
                          <span style={{ flex: 1, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={node.name}>
                            {node.name}
                            {node.entry_region && <span className="muted"> · 入口 {node.entry_region}</span>}
                            {node.exit_region && <span className="muted"> · 出口 {node.exit_region}</span>}
                          </span>
                          <Button variant="ghost" size="icon" disabled={index === 0} onClick={() => moveManualToEdge(key, true)} aria-label="移到顶部"><MoveUp size={14} /></Button>
                          <Button variant="ghost" size="icon" disabled={index === 0} onClick={() => moveManual(key, -1)} aria-label="上移"><ArrowUp size={14} /></Button>
                          <Button variant="ghost" size="icon" disabled={index === manualOrder.length - 1} onClick={() => moveManual(key, 1)} aria-label="下移"><ArrowDown size={14} /></Button>
                          <Button variant="ghost" size="icon" disabled={index === manualOrder.length - 1} onClick={() => moveManualToEdge(key, false)} aria-label="移到底部"><MoveDown size={14} /></Button>
                          <Button variant="ghost" size="icon" onClick={() => removeManual(key)} aria-label="移出顺序"><Trash2 size={14} /></Button>
                        </>
                      )
                    }} />
                  )}
                </div>
                {unplacedNodes.length > 0 && (
                  <div style={{ marginTop: 10 }}>
                    <h4 style={{ margin: '6px 0' }}>待排节点（{unplacedNodes.length}）</h4>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                      {unplacedNodes.map(node => (
                        <div key={node.key} className="card-custom" style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px' }}>
                          <span style={{ flex: 1, fontSize: 13 }}>{node.name} <Badge variant="outline">待排</Badge></span>
                          <Button variant="outline" size="sm" onClick={() => appendUnplaced(node.key)}>追加到末尾</Button>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            </>
          )}

          {workingPolicy.mode === 'manual' && (
            <div className="manual-placement-settings">
              <h3>新增节点处理</h3>
              <div className="template-radio-list" role="radiogroup" aria-label="新增节点处理方式">
                <label><input type="radio" name="plan-new-placement" checked={workingPolicy.new_node_placement === 'by_template'} onChange={() => setWorkingPolicy({ ...workingPolicy, version: 2, new_node_placement: 'by_template' })} /> 按当前规则自动插入</label>
                <label><input type="radio" name="plan-new-placement" checked={workingPolicy.new_node_placement === 'append'} onChange={() => setWorkingPolicy({ ...workingPolicy, version: 2, new_node_placement: 'append' })} /> 追加到末尾</label>
                <label><input type="radio" name="plan-new-placement" checked={workingPolicy.new_node_placement === 'pending'} onChange={() => setWorkingPolicy({ ...workingPolicy, version: 2, new_node_placement: 'pending' })} /> 进入待排区</label>
              </div>
              {workingPolicy.new_node_placement === 'by_template' && (
                <label className="template-unmatched">无法识别的新节点
                  <Select value={workingPolicy.unmatched_placement || 'append'} onChange={event => setWorkingPolicy({ ...workingPolicy, version: 2, unmatched_placement: event.target.value as 'append' | 'pending' })}>
                    <option value="append">追加到已排序节点末尾</option>
                    <option value="pending">进入待排区</option>
                  </Select>
                </label>
              )}
            </div>
          )}

          <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap' }}>
            <Button variant="outline" size="sm" busy={previewing} onClick={() => void runPreview()}><RefreshCw size={14} /> 预览排序</Button>
            <Button size="sm" busy={busy} onClick={() => void saveOrdering()} disabled={applying}><Save size={14} /> 保存为新版本</Button>
            {applying && <span className="muted">有版本正在应用，暂不能保存</span>}
          </div>
        </>
      )}

      {state && workingPolicy && !loading && state.read_only && (
        <p className="muted">当前查看的是历史版本，只读；编辑默认基于最新保存版本。</p>
      )}

      {previewWarnings.length > 0 && (
        <div>
          <h4 style={{ margin: '6px 0' }}>预览提示</h4>
          {previewWarnings.map((w, i) => <p key={i} className="muted" style={{ margin: '2px 0' }}>{w}</p>)}
        </div>
      )}

      {previewNodes && (
        <div>
          <h4 style={{ margin: '6px 0' }}>预览顺序（{previewNodes.length} 个节点{isManual && previewUnplaced > 0 ? `，${previewUnplaced} 个待排` : ''}）</h4>
          <div className="card-custom" style={{ maxHeight: 320, overflow: 'auto' }}>
            <table className="user-data-table">
              <thead><tr><th>#</th><th>节点</th><th>分组</th><th>入口</th><th>出口</th><th>状态</th></tr></thead>
              <tbody>
                {previewNodes.map(node => (
                  <tr key={node.key}>
                    <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{node.effective_position}</td>
                    <td style={{ fontWeight: 600 }}>{node.name}</td>
                    <td className="muted">{node.group}</td>
                    <td className="muted">{node.entry_name || serverName(node.entry_server_id) || node.entry_key || '—'}</td>
                    <td className="muted">{node.exit_region || '—'}</td>
                    <td>
                      {isManual && node.manual_position === undefined && <Badge variant="outline">待排</Badge>}
                      {!node.renderable && <Badge variant="destructive">不可渲染</Badge>}
                      {node.warning && <span className="muted"> {node.warning}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <ApplyOrderTemplateDialog open={applyTemplateOpen} planID={plan.id} hasManualOrder={workingPolicy?.mode === 'manual'} client={client} busy={busy} onClose={() => setApplyTemplateOpen(false)} onApply={applyTemplate} />
      <Dialog isOpen={saveTemplateOpen} onClose={() => setSaveTemplateOpen(false)} title="另存规则为模板" size="sm">
        <div className="save-template-dialog">
          <p className="muted">只保存地区、入口和新节点处理规则，不包含当前逐节点人工顺序。</p>
          <label><span>模板名称</span><Input value={templateName} onChange={event => setTemplateName(event.target.value)} maxLength={100} autoFocus /></label>
          <label><span>描述</span><Input value={templateDescription} onChange={event => setTemplateDescription(event.target.value)} maxLength={500} /></label>
          <div className="dialog-actions"><Button variant="ghost" disabled={busy} onClick={() => setSaveTemplateOpen(false)}>取消</Button><Button busy={busy} disabled={!templateName.trim()} onClick={() => void saveRuleAsTemplate()}><Save size={14} /> 保存模板</Button></div>
        </div>
      </Dialog>
    </div>
  )
}

function RegionOrderEditor({ list, onChange }: { list: string[]; onChange: (list: string[]) => void }) {
  if (list.length === 0) return <p className="muted">未配置自定义顺序。</p>
  return (
    <SortableList items={list} onReorder={onChange} renderRow={(code, index) => (
      <>
        <span style={{ flex: 1, fontSize: 13, fontFamily: 'var(--font-mono)' }}>{code}</span>
        <Button variant="ghost" size="icon" disabled={index === 0} onClick={() => onChange(moveItem(list, index, -1))} aria-label={`上移 ${code}`}><ChevronUp size={14} /></Button>
        <Button variant="ghost" size="icon" disabled={index === list.length - 1} onClick={() => onChange(moveItem(list, index, 1))} aria-label={`下移 ${code}`}><ChevronDown size={14} /></Button>
        <Button variant="ghost" size="icon" onClick={() => onChange(list.filter(c => c !== code))} aria-label={`移除 ${code}`}><Trash2 size={14} /></Button>
      </>
    )} />
  )
}

function SortableList({ items, onReorder, renderRow }: {
  items: string[]
  onReorder: (next: string[]) => void
  renderRow: (item: string, index: number) => React.ReactNode
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )
  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event
    if (!over || active.id === over.id) return
    const oldIndex = items.indexOf(String(active.id))
    const newIndex = items.indexOf(String(over.id))
    if (oldIndex === -1 || newIndex === -1) return
    onReorder(arrayMove(items, oldIndex, newIndex))
  }
  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
      <SortableContext items={items} strategy={verticalListSortingStrategy}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          {items.map((item, index) => (
            <SortableRow key={item} id={item}>{renderRow(item, index)}</SortableRow>
          ))}
        </div>
      </SortableContext>
    </DndContext>
  )
}

function SortableRow({ id, children }: { id: string; children: React.ReactNode }) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id })
  return (
    <div
      ref={setNodeRef}
      className="card-custom"
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 6,
        padding: '6px 10px',
        transform: CSS.Transform.toString(transform),
        transition,
        opacity: isDragging ? 0.6 : 1,
        position: 'relative',
        zIndex: isDragging ? 20 : undefined,
      }}
    >
      <button
        ref={setActivatorNodeRef}
        type="button"
        className="sortable-handle"
        aria-label="拖拽排序"
        title="拖拽排序；也可使用上移/下移按钮"
        {...attributes}
        {...listeners}
      >
        <GripVertical size={14} />
      </button>
      {children}
    </div>
  )
}
