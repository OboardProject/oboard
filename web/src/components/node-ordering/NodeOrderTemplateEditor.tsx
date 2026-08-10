import * as React from 'react'
import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { ChevronDown, ChevronUp, GripVertical, Plus, RotateCcw } from 'lucide-react'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Switch } from '../ui/switch'

export type TemplatePolicy = {
  version: number
  base_mode: 'exit_region' | 'entry'
  exit_region_order: string[]
  entry_region_order_mode: 'inherit_exit' | 'custom'
  entry_region_order: string[]
  entry_order: string[]
  new_node_placement: 'by_template' | 'append' | 'pending'
  unmatched_placement: 'append' | 'pending'
}

export type EntryOption = { key: string; label: string; region: string }

function SortableRuleRow({ id, label, index, count, onMove }: { id: string; label: string; index: number; count: number; onMove: (delta: number) => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id })
  return (
    <div ref={setNodeRef} className={`template-rule-row${isDragging ? ' is-dragging' : ''}`} style={{ transform: CSS.Transform.toString(transform), transition }}>
      <button type="button" className="ghost icon-button" {...attributes} {...listeners} aria-label={`拖动 ${label}`}><GripVertical size={15} /></button>
      <code>{id}</code><span>{label}</span>
      <Button variant="ghost" size="icon" disabled={index === 0} onClick={() => onMove(-1)} aria-label={`上移 ${label}`}><ChevronUp size={14} /></Button>
      <Button variant="ghost" size="icon" disabled={index === count - 1} onClick={() => onMove(1)} aria-label={`下移 ${label}`}><ChevronDown size={14} /></Button>
    </div>
  )
}

function RuleList({ items, labels, onChange }: { items: string[]; labels?: Record<string, string>; onChange: (items: string[]) => void }) {
  const sensors = useSensors(useSensor(PointerSensor), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))
  const dragEnd = (event: DragEndEvent) => {
    if (!event.over || event.active.id === event.over.id) return
    const from = items.indexOf(String(event.active.id))
    const to = items.indexOf(String(event.over.id))
    if (from >= 0 && to >= 0) onChange(arrayMove(items, from, to))
  }
  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={dragEnd}>
      <SortableContext items={items} strategy={verticalListSortingStrategy}>
        <div className="template-rule-list">
          {items.map((item, index) => <SortableRuleRow key={item} id={item} label={labels?.[item] || item} index={index} count={items.length} onMove={delta => onChange(arrayMove(items, index, index + delta))} />)}
        </div>
      </SortableContext>
    </DndContext>
  )
}

export function NodeOrderTemplateEditor({ policy, onChange, regionCodes, entries }: {
  policy: TemplatePolicy
  onChange: (policy: TemplatePolicy) => void
  regionCodes: string[]
  entries: EntryOption[]
}) {
  const [regionInput, setRegionInput] = React.useState('')
  const update = <K extends keyof TemplatePolicy>(key: K, value: TemplatePolicy[K]) => onChange({ ...policy, [key]: value })
  const activeRegions = policy.base_mode === 'entry' && policy.entry_region_order_mode === 'custom' ? policy.entry_region_order : policy.exit_region_order
  const regionField = policy.base_mode === 'entry' && policy.entry_region_order_mode === 'custom' ? 'entry_region_order' : 'exit_region_order'
  const addRegion = () => {
    const code = regionInput.trim().toUpperCase()
    if (!/^[A-Z]{2}$/.test(code) || activeRegions.includes(code)) return
    update(regionField, [...activeRegions, code] as any)
    setRegionInput('')
  }
  const entryLabels = Object.fromEntries(entries.map(entry => [entry.key, `${entry.label}${entry.region ? ` · ${entry.region}` : ''}`]))
  const availableEntries = entries.filter(entry => !policy.entry_order.includes(entry.key))

  return (
    <div className="template-editor-sections">
      <section>
        <h4>基础排序</h4>
        <div className="segmented-actions" role="radiogroup" aria-label="基础排序方式">
          <Button type="button" variant={policy.base_mode === 'exit_region' ? 'default' : 'outline'} size="sm" onClick={() => update('base_mode', 'exit_region')}>按出口地区</Button>
          <Button type="button" variant={policy.base_mode === 'entry' ? 'default' : 'outline'} size="sm" onClick={() => update('base_mode', 'entry')}>按入口</Button>
        </div>
      </section>
      <section>
        <div className="template-section-heading"><h4>{policy.base_mode === 'entry' ? '入口地区顺序' : '出口地区顺序'}</h4><Button type="button" variant="ghost" size="sm" onClick={() => update(regionField, regionCodes as any)}><RotateCcw size={14} /> 恢复默认</Button></div>
        {policy.base_mode === 'entry' && (
          <div className="switch-setting-row" style={{ padding: '4px 0', marginBottom: 8 }}>
            <span className="switch-setting-label">跟随出口地区顺序</span>
            <Switch checked={policy.entry_region_order_mode === 'inherit_exit'} onChange={checked => update('entry_region_order_mode', checked ? 'inherit_exit' : 'custom')} />
          </div>
        )}
        <RuleList items={activeRegions} onChange={items => update(regionField, items as any)} />
        <div className="template-add-rule"><Input value={regionInput} onChange={event => setRegionInput(event.target.value)} placeholder="地区代码，如 JP" maxLength={2} onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); addRegion() } }} /><Button type="button" variant="outline" size="sm" onClick={addRegion}><Plus size={14} /> 添加</Button></div>
      </section>
      {policy.base_mode === 'entry' && (
        <section>
          <h4>具体入口顺序</h4>
          <RuleList items={policy.entry_order} labels={entryLabels} onChange={items => update('entry_order', items)} />
          {availableEntries.length > 0 && <select aria-label="添加入口" value="" onChange={event => event.target.value && update('entry_order', [...policy.entry_order, event.target.value])}><option value="">添加入口...</option>{availableEntries.map(entry => <option key={entry.key} value={entry.key}>{entryLabels[entry.key]}</option>)}</select>}
        </section>
      )}
      <section>
        <h4>新节点处理</h4>
        <div className="template-radio-list">
          <label><input type="radio" name="new-placement" checked={policy.new_node_placement === 'by_template'} onChange={() => update('new_node_placement', 'by_template')} /> 按模板自动插入</label>
          <label><input type="radio" name="new-placement" checked={policy.new_node_placement === 'append'} onChange={() => update('new_node_placement', 'append')} /> 始终追加到末尾</label>
          <label><input type="radio" name="new-placement" checked={policy.new_node_placement === 'pending'} onChange={() => update('new_node_placement', 'pending')} /> 进入待排区</label>
        </div>
        {policy.new_node_placement === 'by_template' && <label className="template-unmatched">无法识别的新节点<select value={policy.unmatched_placement} onChange={event => update('unmatched_placement', event.target.value as TemplatePolicy['unmatched_placement'])}><option value="append">追加到已排序节点末尾</option><option value="pending">进入待排区</option></select></label>}
      </section>
    </div>
  )
}
