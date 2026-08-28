import React from 'react'
import type { Server } from '../../proxy-path/types'

export function ServerStatusBadge({ server }: { server: Server }) {
  const isOnline = String(server.status||'').toLowerCase()==='online'
  const enrolled = Boolean(String(server.agent_id||'').trim())
  if(!enrolled) return <span className="server-workspace-status unenrolled"><i className="server-workspace-status-dot"/>未接入</span>
  return <span className={`server-workspace-status ${isOnline?'online':'offline'}`}><i className="server-workspace-status-dot"/>{isOnline?'在线':'离线'}</span>
}
