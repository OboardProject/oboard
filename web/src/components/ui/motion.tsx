import * as React from "react"
import { m, useReducedMotion } from "motion/react"
import { ModalSurface } from "./modal-layer"

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
  const duration = shouldReduceMotion ? 0.01 : 0.11
  const exitDuration = shouldReduceMotion ? 0.01 : 0.07

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

// Dialog backdrop + panel. Stack order and interaction are owned by ModalSurface.
export function MotionDialogPanel({
  onCancel,
  children,
  className = "",
  ariaLabel = "对话框",
  restoreFocus,
  "aria-labelledby": ariaLabelledBy,
}: {
  onCancel: () => void
  children: React.ReactNode
  className?: string
  ariaLabel?: string
  restoreFocus?: HTMLElement | null
  "aria-labelledby"?: string
}) {
  return (
    <ModalSurface
      onClose={onCancel}
      panelClassName={`dialog ${className}`}
      ariaLabel={ariaLabel}
      ariaLabelledBy={ariaLabelledBy}
      restoreFocus={restoreFocus}
    >
      {children}
    </ModalSurface>
  )
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
