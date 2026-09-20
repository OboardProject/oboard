import * as React from 'react'

export interface SignalEmptyStateProps {
  title: string
  description?: React.ReactNode
  action?: React.ReactNode
  className?: string
}

/**
 * SignalEmptyState: A minimalist, precision instrument empty state.
 * Uses a geometric two-endpoint connection graphic with a terracotta accent terminal,
 * avoiding generic cartoon illustrations or empty whitespace.
 */
export function SignalEmptyState({
  title,
  description,
  action,
  className = '',
}: SignalEmptyStateProps) {
  return (
    <div className={`signal-empty-state flex flex-col items-center justify-center text-center py-12 px-6 ${className}`}>
      {/* Precision two-endpoint connection diagram */}
      <div className="signal-empty-graphic mb-4 text-muted" aria-hidden="true">
        <svg width="72" height="28" viewBox="0 0 72 28" fill="none" xmlns="http://www.w3.org/2000/svg">
          {/* Left Node */}
          <circle cx="10" cy="14" r="5" stroke="currentColor" strokeWidth="1.5" />
          <circle cx="10" cy="14" r="2" fill="currentColor" opacity="0.6" />
          
          {/* Connecting Track with small gap */}
          <path
            d="M 16 14 L 33 14 M 39 14 L 56 14"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeDasharray="2 2"
            opacity="0.4"
          />

          {/* Center Signal Accent Ping */}
          <circle cx="36" cy="14" r="2.5" fill="var(--color-accent, #c4552d)" />

          {/* Right Node */}
          <circle cx="62" cy="14" r="5" stroke="currentColor" strokeWidth="1.5" />
          <circle cx="62" cy="14" r="2" fill="var(--color-accent, #c4552d)" />
        </svg>
      </div>

      <h4 className="signal-empty-title text-base font-semibold text-foreground tracking-tight mb-1.5">
        {title}
      </h4>

      {description && (
        <div className="signal-empty-desc text-sm text-secondary max-w-md leading-relaxed mb-5">
          {description}
        </div>
      )}

      {action && (
        <div className="signal-empty-action">
          {action}
        </div>
      )}
    </div>
  )
}
