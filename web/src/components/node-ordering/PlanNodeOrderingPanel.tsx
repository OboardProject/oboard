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

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type OrderingPolicy = {
  version: number
  mode: string
  manual_seed?: string
  exit_region_order: string[]
  entry_region_order_mode: string
  entry_region_order: string[]
  entry_order: string[]
}

type OrderingNode = {
  key: string
  node_type: string
  node_id: number
  name: string
  group: string
  entry_key?: string
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
  plan_revision: number
  revision_id: number
  revision_status: string
  editable: boolean
  policy: OrderingPolicy
  nodes: OrderingNode[]
  unplaced_count: number
  warnings: string[]
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
    version: 1,
    mode,
    manual_seed: 'exit_region',
    exit_region_order: [],
    entry_region_order_mode: 'inherit_exit',
    entry_region_order: [],
    entry_order: [],
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

export function PlanNodeOrderingPanel({ data, client, notify, onSaved }: { data: any; client: AnyClient; notify?: (message: string, tone?: 'success' | 'error' | 'warning') => void; onSaved?: () => void }) {
  const plans: { id: number; name: string; revision: number; draft_revision_id?: number; active_revision_id?: number }[] = data.subscription_plans || []
  const [planID, setPlanID] = React.useState(0)
  const [state, setState] = React.useState<OrderingState | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')
  const [draftPolicy, setDraftPolicy] = React.useState<OrderingPolicy | null>(null)
  const [manualOrder, setManualOrder] = React.useState<string[]>([])
  const [previewNodes, setPreviewNodes] = React.useState<OrderingNode[] | null>(null)
  const [previewWarnings, setPreviewWarnings] = React.useState<string[]>([])
  const [previewUnplaced, setPreviewUnplaced] = React.useState(0)
  const [regionInput, setRegionInput] = React.useState('')
  const [previewing, setPreviewing] = React.useState(false)

  const loadOrdering = React.useCallback(async (id: number) => {
    setLoading(true)
    setError('')
    try {
      const res = await client.request<OrderingState>(`/subscription-plans/${id}/ordering?revision=draft`)
      setState(res)
      setDraftPolicy(JSON.parse(JSON.stringify(res.policy)))
      const placed = (res.nodes || []).filter(n => n.manual_position !== undefined).sort((a, b) => (a.manual_position ?? 0) - (b.manual_position ?? 0))
      setManualOrder(placed.map(n => n.key))
      setPreviewNodes(null)
      setPreviewWarnings([])
      setPreviewUnplaced(0)
    } catch (e: any) {
      setError(e?.message || String(e))
      setState(null)
      setDraftPolicy(null)
    } finally {
      setLoading(false)
    }
  }, [client])

  const selectPlan = (id: number) => {
    setPlanID(id)
    if (id) void loadOrdering(id)
  }

  const setMode = (mode: string) => {
    if (!draftPolicy) return
    const next: OrderingPolicy = {
      ...draftPolicy,
      version: 1,
      mode,
      manual_seed: draftPolicy.manual_seed || 'exit_region',
    }
    setDraftPolicy(next)
    setPreviewNodes(null)
  }

  const setRegionList = (field: 'exit_region_order' | 'entry_region_order', list: string[]) => {
    if (!draftPolicy) return
    setDraftPolicy({ ...draftPolicy, [field]: list })
    setPreviewNodes(null)
  }

  const addRegion = (field: 'exit_region_order' | 'entry_region_order') => {
    if (!draftPolicy) return
    const code = normalizeRegion(regionInput)
    if (!code || draftPolicy[field].includes(code)) return
    setRegionList(field, [...draftPolicy[field], code])
    setRegionInput('')
  }

  const entryLabel = (key: string, node: OrderingNode) => {
    const region = node.entry_region ? `[${node.entry_region}] ` : ''
    const server = (data.servers || []).find((s: any) => s.id === node.entry_server_id)
    return `${region}${server?.name || key}（${key}）`
  }

  const entryKeys = React.useMemo(() => {
    const seen = new Set<string>()
    const out: { key: string; label: string; count: number }[] = []
    for (const node of state?.nodes || []) {
      const key = node.entry_key
      if (!key || seen.has(key)) continue
      seen.add(key)
      const count = (state?.nodes || []).filter(n => n.entry_key === key).length
      out.push({ key, label: entryLabel(key, node), count })
    }
    const order = new Map(draftPolicy?.entry_order.map((k, i) => [k, i]) || [])
    out.sort((a, b) => {
      const ai = order.get(a.key), bi = order.get(b.key)
      if (ai !== undefined && bi !== undefined) return ai - bi
      if (ai !== undefined) return -1
      if (bi !== undefined) return 1
      return a.key.localeCompare(b.key)
    })
    return out
  }, [state, draftPolicy])

  const setEntryOrder = (list: string[]) => {
    if (!draftPolicy) return
    setDraftPolicy({ ...draftPolicy, entry_order: list })
    setPreviewNodes(null)
  }

  const entryKeyList = React.useMemo(() => entryKeys.map(e => e.key), [entryKeys])

  const runPreview = async (policyOverride?: OrderingPolicy, manualOverride?: string[]) => {
    if (!planID) return
    setPreviewing(true)
    setError('')
    try {
      const policy = policyOverride || draftPolicy || emptyPolicy('exit_region')
      const order = manualOverride !== undefined ? manualOverride : manualOrder
      const res = await client.request<{ nodes: OrderingNode[]; unplaced_count: number; warnings: string[] }>(`/subscription-plans/${planID}/ordering/preview`, {
        method: 'POST',
        body: JSON.stringify({ policy, manual_node_order: order }),
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
    if (!draftPolicy) return
    const seedPolicy: OrderingPolicy = { ...draftPolicy, mode: 'manual', manual_seed: draftPolicy.manual_seed || 'exit_region' }
    setPreviewing(true)
    setError('')
    try {
      const res = await client.request<{ nodes: OrderingNode[] }>(`/subscription-plans/${planID}/ordering/preview`, {
        method: 'POST',
        body: JSON.stringify({ policy: seedPolicy, manual_node_order: [] }),
      })
      const ordered = (res.nodes || []).map(n => n.key)
      setManualOrder(ordered)
      setDraftPolicy(seedPolicy)
      setPreviewNodes(null)
      notify?.('已按规则生成手动顺序，保存后生效', 'success')
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
    if (!planID || !state || !draftPolicy) return
    setBusy(true)
    setError('')
    try {
      const res = await client.request<{ revision_id: number }>(`/subscription-plans/${planID}/ordering`, {
        method: 'PUT',
        body: JSON.stringify({
          expected_revision: state.plan_revision,
          policy: draftPolicy,
          manual_node_order: draftPolicy.mode === 'manual' ? manualOrder : [],
        }),
      })
      notify?.(`排序已保存到方案草稿（修订 v${res.revision_id}），发布后生效`, 'success')
      await loadOrdering(planID)
      onSaved?.()
    } catch (e: any) {
      const message = e?.message || String(e)
      setError(message.includes('conflict') || message.includes('409') ? '方案已发生变化，请重新加载后重试' : message)
    } finally {
      setBusy(false)
    }
  }

  const placedKeys = new Set(manualOrder)
  const unplacedNodes = (state?.nodes || []).filter(n => !placedKeys.has(n.key))

  return (
    <div className="plan-ordering-panel" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
        <Select value={planID} onChange={e => selectPlan(Number(e.target.value))} style={{ minWidth: 220 }}>
          <option value={0}>选择方案</option>
          {plans.map(p => <option key={p.id} value={p.id}>{p.name}{p.draft_revision_id ? '（有草稿）' : ''}</option>)}
        </Select>
        {state && (
          <span className="muted" style={{ marginLeft: 'auto' }}>
            {state.editable ? '草稿可编辑' : '只读（活动版本）'} · 修订 v{state.plan_revision} · {state.nodes.length} 个节点
          </span>
        )}
      </div>

      {error && <p style={{ color: 'var(--color-danger)' }}>{error}</p>}

      {!planID && <p className="muted">请先选择方案。排序策略属于方案修订：修改保存在草稿，发布后才会改变订阅输出顺序。</p>}

      {loading && <p className="muted">正在加载排序状态...</p>}

      {state && draftPolicy && !loading && (
        <>
          <div>
            <h3 style={{ marginTop: 0 }}>排序模式</h3>
            <div className="section-toolbar" style={{ gap: 8 }}>
              {MODE_LABELS[draftPolicy.mode] && draftPolicy.mode === 'legacy_group_name' && (
                <Badge variant="secondary">当前为兼容排序</Badge>
              )}
              {MODES.map(mode => (
                <Button key={mode} variant={draftPolicy.mode === mode ? 'default' : 'outline'} size="sm" onClick={() => setMode(mode)}>
                  {MODE_LABELS[mode]}
                </Button>
              ))}
            </div>
            {draftPolicy.mode === 'legacy_group_name' && (
              <p className="muted" style={{ marginTop: 8 }}>当前方案使用兼容排序（分组 → 名称）。切换到出口地区、入口或手动排序后，订阅节点顺序将发生变化。</p>
            )}
          </div>

          {draftPolicy.mode === 'exit_region' && (
            <div>
              <h3>出口地区顺序</h3>
              <p className="muted">已配置的地区优先，未配置的有效地区按代码排序，未解析地区最后。</p>
              <RegionOrderEditor list={draftPolicy.exit_region_order} onChange={list => setRegionList('exit_region_order', list)} />
              <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                <Input value={regionInput} onChange={e => setRegionInput(e.target.value)} placeholder="如 JP" style={{ width: 120 }} onKeyDown={e => { if (e.key === 'Enter') addRegion('exit_region_order') }} />
                <Button variant="outline" size="sm" onClick={() => addRegion('exit_region_order')}><Plus size={14} /> 添加地区</Button>
              </div>
            </div>
          )}

          {draftPolicy.mode === 'entry' && (
            <>
              <div>
                <h3>入口地区顺序</h3>
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, marginBottom: 8 }}>
                  <input
                    type="checkbox"
                    checked={draftPolicy.entry_region_order_mode !== 'custom'}
                    onChange={e => setDraftPolicy({ ...draftPolicy, entry_region_order_mode: e.target.checked ? 'inherit_exit' : 'custom' })}
                  />
                  入口地区顺序跟随出口地区顺序
                </label>
                {draftPolicy.entry_region_order_mode === 'custom' && (
                  <>
                    <RegionOrderEditor list={draftPolicy.entry_region_order} onChange={list => setRegionList('entry_region_order', list)} />
                    <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                      <Input value={regionInput} onChange={e => setRegionInput(e.target.value)} placeholder="如 JP" style={{ width: 120 }} onKeyDown={e => { if (e.key === 'Enter') addRegion('entry_region_order') }} />
                      <Button variant="outline" size="sm" onClick={() => addRegion('entry_region_order')}><Plus size={14} /> 添加地区</Button>
                    </div>
                  </>
                )}
              </div>
              <div>
                <h3>同地区内的入口顺序</h3>
                <p className="muted">未列出的入口按服务器 ID 与入口 ID 稳定排序，放在已列出的入口之后。相同入口的节点在订阅中始终连续。</p>
                {entryKeys.length === 0 ? (
                  <p className="muted">该修订没有带 OBoard 入口的节点。</p>
                ) : (
                  <SortableList items={entryKeyList} onReorder={setEntryOrder} renderRow={(key, index) => {
                    const entry = entryKeys[index]
                    if (!entry) return null
                    return (
                      <>
                        <span style={{ flex: 1, fontSize: 13, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={entry.label}>{entry.label} <span className="muted">({entry.count} 个节点)</span></span>
                        <Button variant="ghost" size="icon" disabled={index === 0} onClick={() => setEntryOrder(moveItem(entryKeyList, index, -1))} aria-label={`上移 ${entry.key}`}><ChevronUp size={14} /></Button>
                        <Button variant="ghost" size="icon" disabled={index === entryKeys.length - 1} onClick={() => setEntryOrder(moveItem(entryKeyList, index, 1))} aria-label={`下移 ${entry.key}`}><ChevronDown size={14} /></Button>
                      </>
                    )
                  }} />
                )}
              </div>
            </>
          )}

          {draftPolicy.mode === 'manual' && (
            <>
              <div>
                <h3>手动排序</h3>
                <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap' }}>
                  <Select value={draftPolicy.manual_seed || 'exit_region'} onChange={e => setDraftPolicy({ ...draftPolicy, manual_seed: e.target.value })} style={{ width: 180 }}>
                    <option value="exit_region">基础规则：按出口地区</option>
                    <option value="entry">基础规则：按入口</option>
                  </Select>
                  <Button variant="outline" size="sm" busy={previewing} onClick={() => void generateManualSeed()}><Sparkles size={14} /> 按规则生成手动顺序</Button>
                  <Button variant="ghost" size="sm" onClick={appendAllUnplaced}><MoveDown size={14} /> 把待排节点追加到末尾</Button>
                </div>
                <p className="muted" style={{ marginTop: 8 }}>按住手柄拖拽调整顺序，也可使用上移、下移、移到顶部、移到底部按钮；新加入方案的节点不会自动插入已有顺序。</p>
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

          <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap' }}>
            <Button variant="outline" size="sm" busy={previewing} onClick={() => void runPreview()}><RefreshCw size={14} /> 预览排序</Button>
            <Button size="sm" busy={busy} onClick={() => void saveOrdering()} disabled={!state.editable}><Save size={14} /> 保存到草稿</Button>
            {!state.editable && <span className="muted">当前查看的是活动版本，不可直接编辑</span>}
          </div>

          {previewWarnings.length > 0 && (
            <div>
              <h4 style={{ margin: '6px 0' }}>预览提示</h4>
              {previewWarnings.map((w, i) => <p key={i} className="muted" style={{ margin: '2px 0' }}>{w}</p>)}
            </div>
          )}

          {previewNodes && (
            <div>
              <h4 style={{ margin: '6px 0' }}>预览顺序（{previewNodes.length} 个节点{previewUnplaced > 0 ? `，${previewUnplaced} 个待排` : ''}）</h4>
              <div className="card-custom" style={{ maxHeight: 320, overflow: 'auto' }}>
                <table className="user-data-table">
                  <thead><tr><th>#</th><th>节点</th><th>分组</th><th>入口</th><th>出口</th><th>状态</th></tr></thead>
                  <tbody>
                    {previewNodes.map(node => (
                      <tr key={node.key}>
                        <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{node.effective_position}</td>
                        <td style={{ fontWeight: 600 }}>{node.name}</td>
                        <td className="muted">{node.group}</td>
                        <td className="muted">{node.entry_key || '—'}</td>
                        <td className="muted">{node.exit_region || '—'}</td>
                        <td>
                          {node.manual_position === undefined && <Badge variant="outline">待排</Badge>}
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
        </>
      )}
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
