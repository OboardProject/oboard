import type { ReactNode } from 'react'

export interface SettingRowProps {
  id?: string
  icon?: ReactNode
  title: ReactNode
  description?: ReactNode
  status?: ReactNode
  action?: ReactNode
  expanded?: boolean
  children?: ReactNode
  className?: string
}

export function SettingRow({ id, icon, title, description, status, action, expanded = false, children, className = '' }: SettingRowProps) {
  return <div id={id} tabIndex={id ? -1 : undefined} className={`signal-account-row ${className}`}>
    <div className="signal-account-row-main">
      {icon && <span className="signal-account-row-icon" aria-hidden="true">{icon}</span>}
      <div className="signal-account-row-copy">
        <div className="signal-account-row-title"><h3 id={id ? `${id}-title` : undefined}>{title}</h3>{status}</div>
        {description && <p>{description}</p>}
      </div>
      {action && <div className="signal-account-row-actions">{action}</div>}
    </div>
    {expanded && children && <div id={id ? `${id}-content` : undefined} role="region" aria-labelledby={id ? `${id}-title` : undefined} className="signal-account-row-content">{children}</div>}
  </div>
}
