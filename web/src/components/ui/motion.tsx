import * as React from "react"
import { createPortal } from "react-dom"
import { m, useReducedMotion } from "motion/react"

const easeOut = [0.22, 1, 0.36, 1] as const
const easeIn = [0.4, 0, 1, 1] as const

// Page/tab crossfade: opacity only. Parent must use AnimatePresence mode="popLayout"
// so the exiting layer is pulled out of document flow and the stage height does not jump.
export function MotionPage({
  children,
  className = "",
  ...props
}: {
  children: React.ReactNode
  className?: string
  [key: string]: any
}) {
  const shouldReduceMotion = useReducedMotion()
  const duration = shouldReduceMotion ? 0.01 : 0.24
  const exitDuration = shouldReduceMotion ? 0.01 : 0.16

  return (
    <m.div
      className={className ? `page-layer ${className}` : "page-layer"}
      initial={{ opacity: 0 }}
      animate={{
        opacity: 1,
        transition: { duration, ease: easeOut as any },
      }}
      exit={{
        opacity: 0,
        transition: { duration: exitDuration, ease: easeIn as any },
      }}
      {...props}
    >
      {children}
    </m.div>
  )
}

// Dialog backdrop + panel. Keep a short y settle for modal focus, not for page chrome.
export function MotionDialogPanel({
  onCancel,
  children,
  className = "",
  nested = false,
  system = false,
  ariaLabel = "对话框",
  restoreFocus,
}: {
  onCancel: () => void
  children: React.ReactNode
  className?: string
  nested?: boolean
  system?: boolean
  ariaLabel?: string
  restoreFocus?: HTMLElement | null
}) {
  const shouldReduceMotion = useReducedMotion()
  const panelRef = React.useRef<HTMLElement | null>(null)
  const previousFocusRef = React.useRef<HTMLElement | null>(
    restoreFocus || (typeof document !== "undefined" && document.activeElement instanceof HTMLElement ? document.activeElement : null),
  )
  const onCancelRef = React.useRef(onCancel)
  onCancelRef.current = onCancel
  const backdropClass = nested
    ? "dialog-backdrop dialog-backdrop-nested"
    : (system ? "dialog-backdrop dialog-backdrop-system" : "dialog-backdrop")

  React.useEffect(() => {
    const panel = panelRef.current
    if (!panel) return
    const focusable = panel.querySelector<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])')
    window.requestAnimationFrame(() => (focusable || panel).focus())
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onCancelRef.current()
        return
      }
      if (event.key !== 'Tab') return
      const elements = Array.from(panel.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])'))
      if (!elements.length) {
        event.preventDefault()
        panel.focus()
        return
      }
      const first = elements[0]
      const last = elements[elements.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('keydown', onKeyDown)
      if (previousFocusRef.current?.isConnected) previousFocusRef.current.focus()
    }
  }, [])

  const content = (
    <m.div
      className={backdropClass}
      role="presentation"
      onMouseDown={e => {
        if (e.target === e.currentTarget) onCancel()
      }}
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: shouldReduceMotion ? 0.01 : 0.2, ease: "easeOut" }}
    >
      <m.section
        ref={panelRef}
        className={`dialog ${className}`}
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        tabIndex={-1}
        initial={shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 12, scale: 0.98 }}
        animate={shouldReduceMotion ? { opacity: 1 } : { opacity: 1, y: 0, scale: 1 }}
        exit={shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 8, scale: 0.985 }}
        transition={{ duration: shouldReduceMotion ? 0.01 : 0.22, ease: easeOut as any }}
      >
        {children}
      </m.section>
    </m.div>
  )

  return typeof document === "undefined" ? content : createPortal(content, document.body)
}

// List container. No stagger by default — page crossfade already handles entrance.
export function MotionList({
  children,
  className = "",
  tag = "div",
  stagger = 0,
  ...props
}: {
  children: React.ReactNode
  className?: string
  tag?: "div" | "ul" | "ol" | "section"
  stagger?: number
  [key: string]: any
}) {
  const Component = m[tag] as any
  const shouldReduceMotion = useReducedMotion()

  const listVariants = {
    hidden: { opacity: 1 },
    visible: {
      opacity: 1,
      transition: {
        staggerChildren: shouldReduceMotion ? 0 : stagger,
      },
    },
  }

  return (
    <Component
      className={className}
      variants={listVariants as any}
      initial="hidden"
      animate="visible"
      {...props}
    >
      {children}
    </Component>
  )
}

// Card item. Opacity-only entrance so tab switches never reintroduce vertical motion.
export function MotionCard({
  children,
  className = "",
  tag = "div",
  hoverEffect = true,
  ...props
}: {
  children: React.ReactNode
  className?: string
  tag?: "div" | "article" | "section" | "li"
  hoverEffect?: boolean
  [key: string]: any
}) {
  const Component = m[tag] as any
  const shouldReduceMotion = useReducedMotion()

  const itemVariants = {
    hidden: { opacity: shouldReduceMotion ? 1 : 0 },
    visible: {
      opacity: 1,
      transition: {
        duration: shouldReduceMotion ? 0.01 : 0.18,
        ease: easeOut,
      },
    },
  }

  return (
    <Component
      className={className}
      variants={itemVariants as any}
      whileHover={
        shouldReduceMotion || !hoverEffect
          ? {}
          : { y: -2 }
      }
      transition={{ duration: shouldReduceMotion ? 0.01 : 0.2, ease: easeOut as any }}
      {...props}
    >
      {children}
    </Component>
  )
}

export function MotionFade({
  children,
  className = "",
  show = true,
  ...props
}: {
  children: React.ReactNode
  className?: string
  show?: boolean
  [key: string]: any
}) {
  const shouldReduceMotion = useReducedMotion()
  if (!show) return null
  return (
    <m.div
      className={className}
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: shouldReduceMotion ? 0.01 : 0.18, ease: easeOut as any }}
      {...props}
    >
      {children}
    </m.div>
  )
}
