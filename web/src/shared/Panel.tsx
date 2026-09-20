import type { ReactNode } from 'react'

export function Panel({ title, children, className = '', actions = null }: { title?: ReactNode; children?: ReactNode; className?: string; actions?: ReactNode }) {
  const hasHeader = Boolean(title || actions)
  return <section className={`panel${className ? ` ${className}` : ''}`}>
    {hasHeader && <div className="panel-head">{title && <h2>{title}</h2>}{actions}</div>}
    <div className="panel-body">{children}</div>
  </section>
}
