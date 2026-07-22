import * as React from "react"
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

  const sizeClasses = {
    sm: "max-w-sm",
    default: "max-w-lg",
    lg: "max-w-2xl",
    xl: "max-w-5xl",
  }
  const isCompact = className.split(/\s+/).includes("dialog-host-compact")

  return (
    <AnimatePresence>
      {isOpen && (
        <div className="dialog-root fixed inset-0 z-[1400] flex items-center justify-center p-4">
          <m.div
            className="fixed inset-0 bg-black/40 backdrop-blur-sm"
            onClick={onClose}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: shouldReduceMotion ? 0.01 : 0.2, ease: "easeOut" }}
          />

          <m.div
            className={`relative w-full rounded-2xl bg-popover text-foreground border border-border/80 shadow-2xl flex flex-col max-h-[90vh] ${isCompact ? "p-4 gap-3" : "p-6"} ${sizeClasses[size]} ${className}`}
            role="dialog"
            aria-modal="true"
            initial={shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 12, scale: 0.98 }}
            animate={shouldReduceMotion ? { opacity: 1 } : { opacity: 1, y: 0, scale: 1 }}
            exit={shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 8, scale: 0.985 }}
            transition={{ duration: shouldReduceMotion ? 0.01 : 0.22, ease: easeOut as any }}
          >
            {title && (
              <div className={`flex items-center justify-between gap-3 ${isCompact ? "" : "border-b border-border pb-4 mb-4"}`}>
                <h3 className={`${isCompact ? "text-base" : "text-lg"} font-bold leading-snug tracking-tight text-foreground`}>
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
}
