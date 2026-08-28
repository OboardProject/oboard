import React from 'react'
import type { Server } from '../../proxy-path/types'

export function ServerDialogHeader({ server, title, subtitle, onClose }: { server: Server; title: string; subtitle?: string; onClose?: ()=>void }) {
  return (
    <header className="server-workspace-header">
      <div className="server-workspace-title">
        <h2>{title} · {server.name || `#${server.id}`}</h2>
        {subtitle ? <span className="server-workspace-subtitle">{subtitle}</span> : null}
      </div>
      {onClose ? <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭">×</button> : null}
    </header>
  )
}
