import React from 'react'

export function ServerOperationCard({ title, description, action, disabled, disabledReason }: { title: string; description?: string; action: React.ReactNode; disabled?: boolean; disabledReason?: string }) {
  return (
    <div className="server-operation-card">
      <div>
        <strong>{title}</strong>
        {description ? <small className="muted">{description}</small> : null}
        {disabled && disabledReason ? <small className="muted">{disabledReason}</small> : null}
      </div>
      <div>{action}</div>
    </div>
  )
}
