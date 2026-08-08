import * as React from 'react'
import { RotateCcw, Save } from 'lucide-react'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Input } from '../ui/input'

export type PlanNameNode = {
  key: string
  effective_name: string
  global_name: string
  source_name: string
  display_name_override?: string | null
}

export function PlanNodeNameDialog({ node, busy, error, onClose, onSave }: {
  node: PlanNameNode | null
  busy?: boolean
  error?: string
  onClose: () => void
  onSave: (displayNameOverride: string | null) => Promise<void> | void
}) {
  const [value, setValue] = React.useState('')
  const [inherit, setInherit] = React.useState(true)

  React.useEffect(() => {
    const override = node?.display_name_override
    setValue(override || '')
    setInherit(override == null)
  }, [node])

  const submit = () => {
    const trimmed = value.trim()
    void onSave(inherit || !trimmed ? null : trimmed)
  }

  return (
    <Dialog isOpen={node !== null} onClose={onClose} title="方案内节点名称" size="sm">
      {node && (
        <div className="plan-node-name-dialog">
          <div className="node-name-summary">
            <span className="muted">全局名称</span>
            <strong>{node.global_name}</strong>
            <span className="muted">来源名称：{node.source_name}</span>
          </div>
          <div className="template-radio-list" role="radiogroup" aria-label="节点名称来源">
            <label>
              <input type="radio" name="plan-name-mode" checked={!inherit} onChange={() => setInherit(false)} />
              使用方案独立名称
            </label>
            <label>
              <input type="radio" name="plan-name-mode" checked={inherit} onChange={() => setInherit(true)} />
              继承全局名称
            </label>
          </div>
          {!inherit && (
            <label>
              <span>方案内名称</span>
              <Input value={value} onChange={event => setValue(event.target.value)} maxLength={100} autoFocus placeholder={node.global_name} />
            </label>
          )}
          <p className="muted plan-name-effective">保存后显示：<strong>{inherit ? node.global_name : value.trim() || node.global_name}</strong></p>
          {error && <p className="danger-text" role="alert">{error}</p>}
          <div className="dialog-actions">
            <Button variant="outline" disabled={busy || node.display_name_override == null} onClick={() => void onSave(null)}><RotateCcw size={14} /> 恢复全局名称</Button>
            <span />
            <Button variant="ghost" disabled={busy} onClick={onClose}>取消</Button>
            <Button busy={busy} disabled={!inherit && !value.trim()} onClick={submit}><Save size={14} /> 保存</Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}
