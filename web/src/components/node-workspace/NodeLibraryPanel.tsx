import * as React from 'react'
import { Check, Copy, Pencil, Search, X } from 'lucide-react'
import { useDialogs } from '../ui/dialog-context'
import { Dialog } from '../ui/dialog'
import { errorText, type LibraryNode, type NodeGroup } from './types'

export function NodeLibraryPanel({ nodes, groups, busy, onCopy, onEdit }: {
  nodes: LibraryNode[]
  groups: NodeGroup[]
  busy: boolean
  onCopy: (node: LibraryNode) => Promise<void>
  onEdit: (node: LibraryNode, patch: { name?: string; content?: string }) => Promise<void>
}) {
  const [query, setQuery] = React.useState('')
  const [groupID, setGroupID] = React.useState(0)
  const [protocol, setProtocol] = React.useState('')
  const [editing, setEditing] = React.useState<LibraryNode | null>(null)
  const [editName, setEditName] = React.useState('')
  const [editContent, setEditContent] = React.useState('')
  const [editBusy, setEditBusy] = React.useState(false)
  const [editError, setEditError] = React.useState('')
  const [editStatus, setEditStatus] = React.useState('')
  const saving = React.useRef(false)
  const session = React.useRef(0)
  const draft = React.useRef({ name: editName, content: editContent })
  draft.current = { name: editName, content: editContent }
  const dialogs = useDialogs()
  const groupNames = new Map(groups.map(group => [group.id, group.name]))
  const needle = query.trim().toLowerCase()
  const visible = nodes.filter(node => (!needle || node.name.toLowerCase().includes(needle)) && (!groupID || node.group_id === groupID) && (!protocol || node.protocol === protocol))
  const protocols = Array.from(new Set(nodes.map(node => node.protocol))).sort()
  const filtered = Boolean(needle || groupID || protocol)

  const openEdit = (node: LibraryNode) => { session.current++; setEditing(node); setEditName(node.name); setEditContent(''); setEditError(''); setEditStatus('') }
  const closeEdit = async () => {
    const closing = session.current
    if ((editName !== editing?.name || editContent) && !await dialogs.confirm({ title: '关闭编辑？', message: editBusy ? '已提交的修改会继续处理。关闭将丢弃这里尚未保存的输入。' : '尚未保存的输入会丢失。', confirmText: '放弃并关闭', tone: 'danger' })) return
    if (session.current !== closing) return
    session.current++
    setEditing(null)
  }
  const saveEdit = async () => {
    if (!editing || saving.current || (!editName.trim() && !editContent.trim())) return
    const savingSession = session.current
    const snapshot = draft.current
    saving.current = true
    setEditBusy(true)
    setEditError('')
    setEditStatus('')
    try {
      await onEdit(editing, { name: snapshot.name.trim(), content: snapshot.content.trim() })
      if (session.current !== savingSession) return
      if (draft.current.name === snapshot.name && draft.current.content === snapshot.content) setEditing(null)
      else setEditStatus('已保存提交的内容；当前还有未保存的修改。')
    } catch (error) {
      if (session.current === savingSession) setEditError(errorText(error))
    } finally {
      saving.current = false
      setEditBusy(false)
    }
  }

  return <section className="nw-section" aria-label="节点库">
    <div className="nw-toolbar">
      <label className="nw-query"><Search size={15} aria-hidden="true" /><span className="sr-only">搜索节点</span><input type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索节点名称" /></label>
      <select value={groupID} onChange={event => setGroupID(Number(event.target.value))} aria-label="按节点组筛选"><option value={0}>全部节点组</option>{groups.map(group => <option key={group.id} value={group.id}>{group.name}</option>)}</select>
      <select value={protocol} onChange={event => setProtocol(event.target.value)} aria-label="按协议筛选"><option value="">全部协议</option>{protocols.map(value => <option key={value}>{value}</option>)}</select>
      <span className="nw-toolbar-meta">{filtered ? `${visible.length} / ${nodes.length}` : nodes.length} 个节点</span>
    </div>

    {visible.length === 0
      ? <div className="nw-empty"><strong>{filtered ? '没有符合条件的节点' : '还没有可用节点'}</strong><span>{filtered ? '调整搜索词或筛选条件后再试。' : '管理员授权的节点和你添加的节点组会出现在这里。'}</span></div>
      : <div className="nw-table-wrap"><table className="nw-table nw-library-table">
        <thead><tr><th>节点</th><th>节点组</th><th>协议</th><th>来源</th><th><span className="sr-only">操作</span></th></tr></thead>
        <tbody>{visible.map(node => <tr key={node.id}>
          <td><strong className="nw-cell-title">{node.name}</strong></td>
          <td className="nw-cell-muted">{groupNames.get(node.group_id) || '—'}</td>
          <td><span className="nw-mono-chip">{node.protocol}</span></td>
          <td className="nw-cell-muted">{node.source === 'oboard' ? 'OBoard' : '第三方'}</td>
          <td><div className="nw-row-actions">
            {node.editable && <button type="button" className="ghost icon-button" title="编辑节点" aria-label={`编辑 ${node.name}`} disabled={busy} onClick={() => openEdit(node)}><Pencil size={15} /></button>}
            <button type="button" className="ghost icon-button" title={node.copyable ? '复制节点链接' : '此节点无法无损复制'} aria-label={`复制 ${node.name} 的节点链接`} disabled={busy || !node.copyable} onClick={() => void onCopy(node)}><Copy size={15} /></button>
          </div></td>
        </tr>)}</tbody>
      </table></div>}

    <Dialog isOpen={editing !== null} onClose={() => void closeEdit()} title={`编辑 ${editing?.name || '节点'}`} size="lg" className="nw-dialog">
      <div className="nw-form nw-node-edit">
        <label><span>节点名称</span><input value={editName} onChange={event => setEditName(event.target.value)} maxLength={80} placeholder="留空时使用新配置中的名称" /></label>
        <label><span>替换节点配置</span><textarea value={editContent} onChange={event => setEditContent(event.target.value)} rows={4} placeholder="粘贴新的节点链接或配置；留空则只修改名称" /></label>
        {editError && <p className="nw-form-error" role="alert">{editError}</p>}
        {editStatus && <p className="nw-form-status" role="status">{editStatus}</p>}
        <div className="nw-form-actions"><button type="button" className="ghost" onClick={() => void closeEdit()}><X size={15} />关闭</button><button type="button" disabled={editBusy || (!editName.trim() && !editContent.trim())} onClick={() => void saveEdit()}><Check size={15} />{editBusy ? '保存中…' : '保存'}</button></div>
      </div>
    </Dialog>
  </section>
}
