import * as React from "react"
import { AlertTriangle, CheckCircle2, CircleX, Info, X } from "lucide-react"

export interface ToastProps {
  message: string
  kind?: "success" | "error" | "warning" | "info"
  onClose: () => void
  duration?: number
}

export function Toast({ message, kind = "info", onClose, duration }: ToastProps) {
  const effectiveDuration = duration ?? (message.length > 80 ? 10000 : kind === "error" ? 6000 : 4000)

  React.useEffect(() => {
    const timer = setTimeout(onClose, effectiveDuration)
    return () => clearTimeout(timer)
  }, [effectiveDuration, message, kind, onClose])

  const icons = {
    success: CheckCircle2,
    error: CircleX,
    warning: AlertTriangle,
    info: Info,
  }
  const Icon = icons[kind]

  return (
    <div className="top-toast-viewport">
      <div
        className={`toast toast-${kind}`}
        role={kind === "error" ? "alert" : "status"}
        aria-live={kind === "error" ? "assertive" : "polite"}
        aria-atomic="true"
      >
        <Icon className="toast-icon" aria-hidden="true" />
        <span className="toast-message">{message}</span>
        <button
          type="button"
          onClick={onClose}
          className="toast-close"
          aria-label="关闭"
          title="关闭"
        >
          <X aria-hidden="true" />
        </button>
      </div>
    </div>
  )
}
