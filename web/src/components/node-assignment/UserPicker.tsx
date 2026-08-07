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
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Input value={query} onChange={e => setQuery(e.target.value)} placeholder="搜索用户（用户名 / 昵称）" aria-label="搜索用户" />
      <div className="card-custom" style={{ maxHeight, overflow: 'auto', padding: 6 }}>
        {visible.length === 0 && <p className="muted" style={{ padding: 8, margin: 0 }}>没有匹配的用户</p>}
        {visible.map(u => (
          <label key={u.id} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 8px', cursor: 'pointer', borderRadius: 8 }}>
            <input type="checkbox" checked={selected.has(u.id)} onChange={() => toggle(u.id)} aria-label={`选择用户 ${u.username}`} />
            <span style={{ fontWeight: 600 }}>{u.username}</span>
            {u.nickname ? <span className="muted" style={{ fontSize: 12 }}>{u.nickname}</span> : null}
            {u.status === 'disabled' && <Badge variant="secondary">停用</Badge>}
          </label>
        ))}
      </div>
      <p className="muted" style={{ margin: 0 }}>已选 {selected.size} 个用户</p>
    </div>
  )
}
