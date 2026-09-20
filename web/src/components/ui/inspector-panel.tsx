import * as React from 'react'

export interface InspectorPanelProps {
  isOpen: boolean
  onClose: () => void
  title: React.ReactNode
  badge?: React.ReactNode
  actions?: React.ReactNode
  children: React.ReactNode
  className?: string
  width?: number | string
  ariaLabel?: string
}

/**
 * InspectorPanel: A non-modal, continuous-view contextual side panel (400-440px).
 * Sits alongside main workspace on wide screens, allowing uninterrupted interaction
 * with the primary list while inspecting details. No backdrop lock, no modal focus trap.
 */
export function InspectorPanel({
  isOpen,
  onClose,
  title,
  badge,
  actions,
  children,
  className = '',
  width = 420,
  ariaLabel = '详情检查面板',
}: InspectorPanelProps) {
  const panelRef = React.useRef<HTMLDivElement>(null)

  // Allow Escape key to dismiss the inspector without blocking global handlers
  React.useEffect(() => {
    if (!isOpen) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', handleKeyDown, true)
    return () => window.removeEventListener('keydown', handleKeyDown, true)
  }, [isOpen, onClose])

  if (!isOpen) return null

  return (
    <aside
      ref={panelRef}
      role="region"
      aria-label={ariaLabel}
      className={`inspector-panel flex flex-col bg-surface border-l border-border transition-all duration-200 ${className}`}
      style={{
        width: typeof width === 'number' ? `${width}px` : width,
        minWidth: typeof width === 'number' ? `${width}px` : width,
      }}
    >
      {/* Inspector Chrome Head */}
      <div className="inspector-head flex items-center justify-between gap-3 px-5 py-4 border-b border-border bg-surface shrink-0">
        <div className="inspector-title-wrap flex items-center gap-2.5 min-w-0">
          <div className="inspector-title text-base font-semibold text-foreground truncate">
            {title}
          </div>
          {badge && <div className="inspector-badge shrink-0">{badge}</div>}
        </div>
        <div className="inspector-head-actions flex items-center gap-2 shrink-0">
          {actions}
          <button
            type="button"
            onClick={onClose}
            className="ghost icon-button inspector-close"
            aria-label="关闭详情面板"
            title="关闭 (Esc)"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </div>
      </div>

      {/* Inspector Scrollable Body */}
      <div className="inspector-body flex-1 min-h-0 overflow-y-auto px-5 py-5 space-y-5">
        {children}
      </div>
    </aside>
  )
}
