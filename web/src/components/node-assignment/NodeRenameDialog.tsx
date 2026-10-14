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
  const session = React.useRef(0)
  const draft = React.useRef(value)
  draft.current = value

  React.useEffect(() => {
    session.current++
    setValue(node?.global_name_override || '')
    setError('')
    setBusy(false)
  }, [node])

  const save = async (clear = false) => {
    if (!node || busy) return
    const savingSession = session.current
    const submitted = value
    setBusy(true)
    setError('')
    try {
      await client.request(`/assignable-nodes/${node.type}/${node.id}/metadata`, {
        method: 'PATCH',
        body: JSON.stringify({
          display_name_override: clear || !submitted.trim() ? null : submitted.trim(),
          expected_lock_version: node.metadata_lock_version || 0,
        }),
      })
      if (savingSession === session.current && (clear || draft.current === submitted)) onClose()
      void onSaved()
    } catch (e: any) {
      if (savingSession !== session.current) return
      const message = e?.message || String(e)
      setError(e?.status === 409 || message.includes('409') || message.includes('conflict') ? '节点名称已被其他操作更新，请重新加载后再试。' : message)
    } finally {
      if (savingSession === session.current) setBusy(false)
    }
  }

  const inherited = node?.plans.filter(plan => !plan.has_display_name_override).length || 0
  const overridden = node?.plans.filter(plan => plan.has_display_name_override).length || 0

  return (
    <Dialog isOpen={node !== null} onClose={onClose} title="修改全局节点名称" size="default" className="nw-dialog">
      {node && (
        <div className="nw-form">
          <dl className="nw-facts">
            <div><dt>当前显示名称</dt><dd>{node.name}</dd></div>
            <div><dt>来源名称</dt><dd>{node.source_name}</dd></div>
          </dl>
          <label>
            <span>新的全局名称</span>
            <Input value={value} onChange={event => setValue(event.target.value)} maxLength={100} autoFocus placeholder={node.source_name} aria-label="全局节点名称" />
          </label>
          <p className="nw-form-hint" role="status">留空将恢复来源名称。{node.plans.length > 0 ? `${inherited} 个套餐继承全局名称，将同步变化；${overridden} 个套餐设置了独立名称，不受影响。` : '该节点尚未加入任何套餐。'}</p>
          {error && <p className="nw-form-error" role="alert">{error}</p>}
          <div className="nw-form-actions">
            <Button variant="ghost" disabled={busy || !node.global_name_override} onClick={() => void save(true)}><RotateCcw size={14} /> 恢复来源名称</Button>
            <span className="nw-spacer" />
            <Button variant="outline" disabled={busy} onClick={onClose}>取消</Button>
            <Button busy={busy} onClick={() => void save()}><Save size={14} /> 保存</Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}
