import * as React from 'react'
import { createPortal } from 'react-dom'
import { ChevronRight } from 'lucide-react'

export type ScopeNode = {
  type: string
  id: number
  key: string
  name: string
  entry_key?: string
  entry_server_id?: number
  entry_server_name?: string
  exit_server_id?: number
  exit_external_outbound_id?: number
  exit_region?: string
  path_servers?: { server_id: number; server_name: string; roles: string[] }[]
}

export type NodeScopeRequest = {
  kind: string
  server_id?: number
  region?: string
  external_outbound_id?: number
}

const ROLE_LABELS: Record<string, string> = { entry: '入口', transit: '中转', exit: '出口' }

export function NodeScopeMenu({ x, y, node, onSelect, onClose }: {
  x: number
  y: number
  node: ScopeNode | null
  onSelect: (scope: NodeScopeRequest) => void
  onClose: () => void
}) {
  const [showPathServers, setShowPathServers] = React.useState(false)

  React.useEffect(() => {
    setShowPathServers(false)
  }, [node])

  React.useEffect(() => {
    if (!node) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    let width = window.innerWidth
    const onResize = () => {
      if (window.innerWidth === width) return
      width = window.innerWidth
      onClose()
    }
    window.addEventListener('keydown', onKey)
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', onResize)
    }
  }, [node, onClose])

  if (!node) return null
  const menuWidth = 300
  const menuHeight = showPathServers ? 430 : 330
  const left = Math.max(8, Math.min(x, window.innerWidth - menuWidth - 8))
  const top = Math.max(8, Math.min(y, window.innerHeight - menuHeight - 8))

  const pick = (scope: NodeScopeRequest) => { onSelect(scope); onClose() }
  const pathServers = node.path_servers || []

  return createPortal(
    <>
      <div
        className="node-scope-menu-overlay"
        onMouseDown={onClose}
        onContextMenu={e => { e.preventDefault(); onClose() }}
      />
      <div className="node-scope-menu" role="menu" aria-label="选择节点范围" style={{ left, top }}>
        <div className="node-scope-menu-title">
          <span style={{ fontWeight: 600 }}>{node.name}</span>
          <span className="muted" style={{ fontSize: 12 }}>选择节点范围</span>
        </div>
        <MenuButton label="仅选择此节点" onClick={() => pick({ kind: 'node' })} />
        <MenuButton
          label="选择同一入口的全部节点"
          disabled={!node.entry_key}
          reason={node.entry_key ? undefined : '当前节点没有 OBoard 入口'}
          onClick={() => pick({ kind: 'entry_inbound' })}
        />
        <MenuButton
          label="选择同一入口服务器的全部节点"
          disabled={!node.entry_server_id}
          reason={node.entry_server_id ? undefined : '当前节点没有受管入口服务器'}
          onClick={() => pick({ kind: 'entry_server' })}
        />
        {pathServers.length > 0 && (
          <div className="node-scope-menu-sub">
            <MenuButton label="按路径服务器选择…" sub onClick={() => setShowPathServers(v => !v)} />
            {showPathServers && (
              <div className="node-scope-menu-sub-list">
                {pathServers.map(s => (
                  <MenuButton
                    key={s.server_id}
                    label={s.server_name}
                    hint={s.roles.map(r => ROLE_LABELS[r] || r).join(' / ')}
                    onClick={() => pick({ kind: 'path_server', server_id: s.server_id })}
                  />
                ))}
              </div>
            )}
          </div>
        )}
        {pathServers.length === 0 && (
          <MenuButton label="按路径服务器选择…" disabled reason="当前节点没有可确定的路径服务器" />
        )}
        <MenuButton
          label="选择同一出口服务器的全部节点"
          disabled={!node.exit_server_id}
          reason={node.exit_server_id ? undefined : '当前节点无法确定受管出口服务器'}
          onClick={() => pick({ kind: 'exit_server' })}
        />
        <MenuButton
          label="选择同一出口地区的全部节点"
          disabled={!node.exit_region}
          reason={node.exit_region ? undefined : '当前节点出口地区未解析'}
          onClick={() => pick({ kind: 'exit_region' })}
        />
        <MenuButton
          label="选择使用同一导入出口的节点"
          disabled={!node.exit_external_outbound_id}
          reason={node.exit_external_outbound_id ? undefined : '当前节点不使用导入出口'}
          onClick={() => pick({ kind: 'external_outbound' })}
        />
      </div>
    </>,
    document.body,
  )
}

function MenuButton({ label, hint, reason, sub, disabled, onClick }: {
  label: string
  hint?: string
  reason?: string
  sub?: boolean
  disabled?: boolean
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      className="node-scope-menu-item"
      role="menuitem"
      disabled={disabled}
      onClick={onClick}
      title={reason}
    >
      <span className="node-scope-menu-item-copy">
        <span className="node-scope-menu-item-label">{label}</span>
        {(hint || reason) && <span className="node-scope-menu-reason">{hint || reason}</span>}
      </span>
      {sub && <ChevronRight size={14} className="muted" aria-hidden="true" />}
    </button>
  )
}
