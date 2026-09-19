import { Check, X } from 'lucide-react'
import { useState } from 'react'

import { MotionDialogPanel } from '../ui/motion'
import type { ProxyPathReuseSource } from './TransportDialog'
import type { GraphSourceOption } from './graph-sources'

export type GraphSourceSelectionRequest = {
  title: string
  options: GraphSourceOption[]
  resolve: (value: ProxyPathReuseSource[] | null) => void
}

export function GraphSourceSelectionDialog({ request, onCancel, onSubmit }: {
  request: GraphSourceSelectionRequest
  onCancel: () => void
  onSubmit: (sources: ProxyPathReuseSource[]) => void
}) {
  const [selected, setSelected] = useState('')
  const submitSelection = () => {
    const option = request.options.find(item => item.key === selected)
    if (option) onSubmit([option.source])
  }

  return <MotionDialogPanel onCancel={onCancel} className="graph-source-dialog" aria-labelledby="graph-source-dialog-title">
    <header className="dialog-head">
      <div><h2 id="graph-source-dialog-title">选择入口</h2><p className="muted">{request.title} · 选择一个入口</p></div>
      <button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><X size={16} /></button>
    </header>
    <div className="dialog-body">
      <div className="graph-source-options" role="radiogroup" aria-label={`${request.title}的入口`}>
        {request.options.map(option => {
          const active = selected === option.key
          return <button
            key={option.key}
            type="button"
            role="radio"
            aria-checked={active}
            className={active ? 'is-selected' : ''}
            onClick={() => setSelected(option.key)}
          >
            <span className="graph-source-choice-indicator" aria-hidden="true">{active && <Check size={14} />}</span>
            <span><strong>{option.label}</strong><small>{option.detail}</small></span>
          </button>
        })}
      </div>
    </div>
    <footer className="dialog-actions">
      <button type="button" className="ghost" onClick={onCancel}>取消</button>
      <button type="button" disabled={!selected} onClick={submitSelection}>继续</button>
    </footer>
  </MotionDialogPanel>
}
