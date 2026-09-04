import * as React from "react"
import { createPortal } from "react-dom"
import { AnimatePresence } from "motion/react"
import { ModalSurface } from "./modal-layer"

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
  const titleID = React.useId()
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
        <ModalSurface
          key="dialog-surface"
          onClose={onClose}
          rootClassName="dialog-root p-4"
          panelClassName={`dialog relative w-full rounded-2xl bg-popover text-foreground border border-border/80 shadow-2xl flex flex-col max-h-[90vh] ${isCompact ? "p-4 gap-3" : "p-6"} ${sizeClasses[size]} ${className}`}
          ariaLabelledBy={title ? titleID : undefined}
          ariaLabel={title ? undefined : "对话框"}
          portal={false}
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
        </ModalSurface>
      )}
    </AnimatePresence>
  )
  return typeof document === "undefined" ? content : createPortal(content, document.body)
}
