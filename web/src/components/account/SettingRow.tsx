import React, { ReactNode } from 'react'

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

export function SettingRow({
  id,
  icon,
  title,
  description,
  status,
  action,
  expanded = false,
  children,
  className = '',
}: SettingRowProps) {
  return (
    <div
      id={id}
      tabIndex={id ? -1 : undefined}
      className={`setting-row ${expanded ? 'is-expanded' : ''} ${className}`}
    >
      <div className="setting-row__main">
        {icon && <div className="setting-row__icon">{icon}</div>}

        <div className="setting-row__content">
          <div className="setting-row__title">{title}</div>
          {description && <div className="setting-row__description">{description}</div>}
        </div>

        {status && <div className="setting-row__status">{status}</div>}

        {action && <div className="setting-row__action">{action}</div>}
      </div>

      {expanded && children && (
        <div className="setting-row__expanded" aria-expanded={expanded}>
          {children}
        </div>
      )}
    </div>
  )
}
