import * as React from "react"
import { AlertTriangle, CheckCircle2, CircleX, Info, X } from "lucide-react"
import { m, useReducedMotion } from "motion/react"

export interface ToastProps {
  message: string
  kind?: "success" | "error" | "warning" | "info"
  onClose: () => void
  duration?: number
}

export function Toast({ message, kind = "info", onClose, duration }: ToastProps) {
  const effectiveDuration = duration ?? (message.length > 80 ? 10000 : kind === "error" ? 6000 : 4000)
  const shouldReduceMotion = useReducedMotion()
  const onCloseRef = React.useRef(onClose)
  const timerRef = React.useRef<number | undefined>(undefined)
  const remainingRef = React.useRef(effectiveDuration)
  const startedAtRef = React.useRef(0)
  const pauseReasonsRef = React.useRef(new Set<"hover" | "focus">())
  onCloseRef.current = onClose

  const clearTimer = React.useCallback(() => {
    if (timerRef.current === undefined) return
    window.clearTimeout(timerRef.current)
    timerRef.current = undefined
  }, [])

  const close = React.useCallback(() => {
    clearTimer()
    onCloseRef.current()
  }, [clearTimer])

  const startTimer = React.useCallback(() => {
    if (pauseReasonsRef.current.size > 0) return
    startedAtRef.current = Date.now()
    timerRef.current = window.setTimeout(close, remainingRef.current)
  }, [close])

  React.useEffect(() => {
    remainingRef.current = effectiveDuration
    pauseReasonsRef.current.clear()
    startTimer()
    return clearTimer
  }, [clearTimer, effectiveDuration, startTimer])

  const pauseTimer = React.useCallback((reason: "hover" | "focus") => {
    if (pauseReasonsRef.current.has(reason)) return
    if (pauseReasonsRef.current.size === 0 && timerRef.current !== undefined) {
      remainingRef.current = Math.max(0, remainingRef.current - (Date.now() - startedAtRef.current))
      clearTimer()
    }
    pauseReasonsRef.current.add(reason)
  }, [clearTimer])

  const resumeTimer = React.useCallback((reason: "hover" | "focus") => {
    pauseReasonsRef.current.delete(reason)
    if (pauseReasonsRef.current.size > 0) return
    if (remainingRef.current <= 0) {
      close()
      return
    }
    startTimer()
  }, [close, startTimer])

  const icons = {
    success: CheckCircle2,
    error: CircleX,
    warning: AlertTriangle,
    info: Info,
  }
  const Icon = icons[kind]

  return (
    <m.div
      className={`toast toast-${kind}${message.includes("\n") ? " toast-multiline" : ""}`}
      role={kind === "error" ? "alert" : "status"}
      aria-live={kind === "error" ? "assertive" : "polite"}
      aria-atomic="true"
      onMouseEnter={() => pauseTimer("hover")}
      onMouseLeave={() => resumeTimer("hover")}
      onFocusCapture={() => pauseTimer("focus")}
      onBlurCapture={event => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) resumeTimer("focus")
      }}
      initial={shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: -14, scale: 0.94 }}
      animate={shouldReduceMotion
        ? { opacity: 1, transition: { duration: 0.01 } }
        : { opacity: 1, y: 0, scale: 1, transition: { type: "spring", stiffness: 480, damping: 28, mass: 0.72 } }}
      exit={shouldReduceMotion
        ? { opacity: 0, transition: { duration: 0.01 } }
        : { opacity: 0, y: -10, scale: 0.96, transition: { duration: 0.16, ease: [0.4, 0, 1, 1] } }}
    >
      <Icon className="toast-icon" aria-hidden="true" />
      <span className={`toast-message${message.includes("\n") ? " toast-message-multiline" : ""}`}>{message}</span>
      <button
        type="button"
        onClick={close}
        className="toast-close"
        aria-label="关闭"
        title="关闭"
      >
        <X aria-hidden="true" />
      </button>
    </m.div>
  )
}
