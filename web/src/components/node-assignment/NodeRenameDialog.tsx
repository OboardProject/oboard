import * as React from 'react'
import { RotateCcw, Save } from 'lucide-react'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Input } from '../ui/input'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

export type RenameNode = {
  type: string
  id: number
  name: string
  source_name: string
  global_name_override?: string | null
  metadata_lock_version: number
  plans: { has_display_name_override?: boolean }[]
}

export function NodeRenameDialog({ node, client, onClose, onSaved }: {
  node: RenameNode | null
  client: AnyClient
  onClose: () => void
  onSaved: () => Promise<void> | void
}) {
  const [value, setValue] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  React.useEffect(() => {
    setValue(node?.global_name_override || '')
    setError('')
  }, [node])

  const save = async (clear = false) => {
    if (!node) return
    setBusy(true)
    setError('')
    try {
      await client.request(`/assignable-nodes/${node.type}/${node.id}/metadata`, {
        method: 'PATCH',
        body: JSON.stringify({
          display_name_override: clear || !value.trim() ? null : value.trim(),
          expected_lock_version: node.metadata_lock_version || 0,
        }),
      })
      await onSaved()
      onClose()
    } catch (e: any) {
      const message = e?.message || String(e)
      setError(message.includes('409') || message.includes('conflict') ? '节点名称已被其他操作更新，请重新加载后再试。' : message)
    } finally {
      setBusy(false)
    }
  }

  const inherited = node?.plans.filter(plan => !plan.has_display_name_override).length || 0
  const overridden = node?.plans.filter(plan => plan.has_display_name_override).length || 0

  return (
    <Dialog isOpen={node !== null} onClose={onClose} title="修改全局节点名称" size="sm">
      {node && (
        <div className="form" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div className="node-name-summary">
            <span className="muted">当前显示名称</span>
            <strong>{node.name}</strong>
            <span className="muted">来源名称：{node.source_name}</span>
          </div>
          <label>
            <span>新的全局名称</span>
            <Input value={value} onChange={event => setValue(event.target.value)} maxLength={100} autoFocus placeholder={node.source_name} />
          </label>
          <div className="node-rename-impact" role="status">
            <strong>影响范围</strong>
            <span>{inherited} 个方案正在继承全局名称，将同步变化</span>
            <span>{overridden} 个方案设置了独立名称，不受影响</span>
          </div>
          {error && <p className="danger-text" role="alert">{error}</p>}
          <div className="dialog-actions">
            <Button variant="outline" disabled={busy || !node.global_name_override} onClick={() => void save(true)}><RotateCcw size={14} /> 恢复来源名称</Button>
            <span style={{ flex: 1 }} />
            <Button variant="ghost" disabled={busy} onClick={onClose}>取消</Button>
            <Button busy={busy} onClick={() => void save()}><Save size={14} /> 保存</Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}
