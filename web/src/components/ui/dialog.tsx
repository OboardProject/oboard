import * as React from "react"
import { createPortal } from "react-dom"
import { m, AnimatePresence, useReducedMotion } from "motion/react"

export interface DialogProps {
  isOpen: boolean
  onClose: () => void
  children: React.ReactNode
  title?: string
  className?: string
  size?: "default" | "sm" | "lg" | "xl"
}

export function Dialog({
  isOpen,
  onClose,
  children,
  title,
  className = "",
  size = "default"
}: DialogProps) {
  const shouldReduceMotion = useReducedMotion()
  const easeOut = [0.22, 1, 0.36, 1] as const
  const dialogRef = React.useRef<HTMLDivElement>(null)
  const previousFocusRef = React.useRef<HTMLElement | null>(null)
  const onCloseRef = React.useRef(onClose)
  const titleID = React.useId()

  React.useEffect(() => {
    onCloseRef.current = onClose
  }, [onClose])

  React.useEffect(() => {
    if (isOpen) {
      document.body.style.overflow = "hidden"
    } else {
      document.body.style.overflow = ""
    }
    return () => {
      document.body.style.overflow = ""
    }
  }, [isOpen])

  React.useEffect(() => {
    if (!isOpen) return
    previousFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const dialog = dialogRef.current
    const focusableSelector = 'button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'
    const activeElement = document.activeElement instanceof HTMLElement && dialog?.contains(document.activeElement) ? document.activeElement : null
    const initialFocus = activeElement || dialog?.querySelector<HTMLElement>('[autofocus]') || dialog?.querySelector<HTMLElement>(focusableSelector) || dialog
    initialFocus?.focus()

    const handleKeyDown = (event: KeyboardEvent) => {
      const root = dialog?.closest('.dialog-root')
      const roots = Array.from(document.querySelectorAll('.dialog-root'))
      if (!root || roots[roots.length - 1] !== root) return
      if (event.key === 'Escape') {
        event.preventDefault()
        onCloseRef.current()
        return
      }
      if (event.key !== 'Tab' || !dialog) return
      const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(focusableSelector)).filter(element => element.getClientRects().length > 0)
      if (focusable.length === 0) {
        event.preventDefault()
        dialog.focus()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      const previous = previousFocusRef.current
      if (previous?.isConnected) previous.focus()
    }
  }, [isOpen])

  const sizeClasses = {
    sm: "max-w-sm",
    default: "max-w-lg",
    lg: "max-w-2xl",
    xl: "max-w-5xl",
  }
  const isCompact = className.split(/\s+/).includes("dialog-host-compact")

  const content = (
    <AnimatePresence>
      {isOpen && (
        <div className="dialog-root fixed inset-0 flex items-center justify-center p-4">
          <m.div
            className="fixed inset-0 bg-black/50 backdrop-blur-md"
            onClick={onClose}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: shouldReduceMotion ? 0.01 : 0.25, ease: "easeOut" }}
          />

          <m.div
            ref={dialogRef}
            className={`relative w-full rounded-2xl bg-popover text-foreground border border-border/80 shadow-2xl flex flex-col max-h-[90vh] ${isCompact ? "p-4 gap-3" : "p-6"} ${sizeClasses[size]} ${className}`}
            role="dialog"
            aria-modal="true"
            aria-labelledby={title ? titleID : undefined}
            tabIndex={-1}
            initial={shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 14, scale: 0.95 }}
            animate={shouldReduceMotion ? { opacity: 1 } : { opacity: 1, y: 0, scale: 1 }}
            exit={shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 10, scale: 0.96 }}
            transition={{ duration: shouldReduceMotion ? 0.01 : 0.28, ease: [0.175, 0.885, 0.32, 1.5] as any }}
          >
            {title && (
              <div className={`flex items-center justify-between gap-3 ${isCompact ? "" : "border-b border-border pb-4 mb-4"}`}>
                <h3 id={titleID} className={`${isCompact ? "text-base" : "text-lg"} font-bold leading-snug tracking-tight text-foreground`}>
                  {title}
                </h3>
                <button
                  onClick={onClose}
                  className="ghost icon-button dialog-close"
                  aria-label="关闭"
                  type="button"
                >
                  <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                  </svg>
                </button>
              </div>
            )}
            <div className={`overflow-y-auto flex-1 min-h-0 ${isCompact ? "" : "pr-1"}`}>
              {children}
            </div>
          </m.div>
        </div>
      )}
    </AnimatePresence>
  )
  return typeof document === "undefined" ? content : createPortal(content, document.body)
}
