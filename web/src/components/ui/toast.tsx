import * as React from "react"

export interface ToastProps {
  message: string
  kind?: "success" | "error" | "warning" | "info"
  onClose: () => void
  duration?: number
}

export function Toast({ message, kind = "info", onClose, duration = 4000 }: ToastProps) {
  React.useEffect(() => {
    const timer = setTimeout(onClose, duration)
    return () => clearTimeout(timer)
  }, [message, kind, onClose, duration])

  const styles = {
    success: "bg-emerald-50 border-emerald-200 text-emerald-800 dark:bg-emerald-950/60 dark:border-emerald-900/50 dark:text-emerald-300",
    error: "bg-rose-50 border-rose-200 text-rose-800 dark:bg-rose-950/60 dark:border-rose-900/50 dark:text-rose-300",
    warning: "bg-amber-50 border-amber-200 text-amber-800 dark:bg-amber-950/60 dark:border-amber-900/50 dark:text-amber-300",
    info: "bg-sky-50 border-sky-200 text-sky-800 dark:bg-sky-950/60 dark:border-sky-900/50 dark:text-sky-300",
  }

  const icons = {
    success: (
      <svg className="h-5 w-5 text-emerald-600 dark:text-emerald-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
        <path strokeLinecap="round" strokeLinejoin="round" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
      </svg>
    ),
    error: (
      <svg className="h-5 w-5 text-rose-600 dark:text-rose-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
        <path strokeLinecap="round" strokeLinejoin="round" d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z" />
      </svg>
    ),
    warning: (
      <svg className="h-5 w-5 text-amber-600 dark:text-amber-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
        <path strokeLinecap="round" strokeLinejoin="round" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
      </svg>
    ),
    info: (
      <svg className="h-5 w-5 text-sky-600 dark:text-sky-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
        <path strokeLinecap="round" strokeLinejoin="round" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
      </svg>
    ),
  }

  return (
    <div className="fixed top-5 left-0 right-0 z-[90] w-full max-w-sm mx-auto px-4 pointer-events-none animate-toast-in">
      <div className={`pointer-events-auto flex items-center justify-between gap-3 rounded-2xl border p-3.5 shadow-lg backdrop-blur-md ${styles[kind]}`}>
        <div className="flex items-center gap-2.5">
          {icons[kind]}
          <span className="text-sm font-semibold tracking-wide">{message}</span>
        </div>
        <button
          onClick={onClose}
          className="h-7 min-h-0 w-7 shrink-0 rounded-full border-0 bg-transparent p-0 text-current shadow-none hover:bg-black/5 dark:hover:bg-white/5 transition-colors duration-200"
          aria-label="关闭"
        >
          <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
            <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </button>
      </div>
    </div>
  )
}
