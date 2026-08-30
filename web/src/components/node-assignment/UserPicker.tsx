import * as React from 'react'
import { Badge } from '../ui/badge'
import { Input } from '../ui/input'

export type UserOption = { id: number; username: string; nickname?: string; status?: string }

export function UserPicker({ users, selected, onChange, maxHeight = 220 }: {
  users: UserOption[]
  selected: Set<number>
  onChange: (next: Set<number>) => void
  maxHeight?: number
}) {
  const [query, setQuery] = React.useState('')
  const q = query.trim().toLowerCase()
  const visible = users.filter(u => !q || u.username.toLowerCase().includes(q) || (u.nickname || '').toLowerCase().includes(q))
  const toggle = (id: number) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    onChange(next)
  }

  const selectAllVisible = () => {
    const next = new Set(selected)
    visible.forEach(u => next.add(u.id))
    onChange(next)
  }

  const clearVisible = () => {
    const next = new Set(selected)
    visible.forEach(u => next.delete(u.id))
    onChange(next)
  }

  return (
    <div className="user-picker">
      <div className="user-picker-toolbar">
        <Input
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder="搜索用户（用户名 / 昵称）"
          aria-label="搜索用户"
        />
        <button type="button" className="ghost" onClick={selectAllVisible}>
          全选
        </button>
        <button type="button" className="ghost" onClick={clearVisible}>
          清空
        </button>
      </div>
      <div className="card-custom" style={{ maxHeight, overflow: 'auto', padding: 6 }}>
        {visible.length === 0 && <p className="muted" style={{ padding: 8, margin: 0, textAlign: 'center', fontSize: 12 }}>没有匹配的用户</p>}
        {visible.map(u => (
          <label key={u.id} className="user-picker-row">
            <input type="checkbox" checked={selected.has(u.id)} onChange={() => toggle(u.id)} aria-label={`选择用户 ${u.username}`} />
            <span className="user-picker-username">{u.username}</span>
            {u.nickname ? <span className="muted user-picker-nickname">{u.nickname}</span> : null}
            {u.status === 'disabled' && <Badge variant="secondary">停用</Badge>}
          </label>
        ))}
      </div>
      <p className="muted" style={{ margin: 0, fontSize: 12 }}>已选 {selected.size} 个用户（共 {users.length} 个用户）</p>
    </div>
  )
}

