import { useCallback, useRef, useState } from "react"

export type ToastNotify = (message: string, tone?: "success" | "error" | "warning" | "info") => void

export function useErrorToast(notify?: ToastNotify) {
  const [error, setError] = useState("")
  const lastError = useRef("")
  const notifyRef = useRef(notify)
  notifyRef.current = notify
  const report = useCallback((message: string) => {
    setError(message)
    if (message && message !== lastError.current) notifyRef.current?.(message, "error")
    lastError.current = message
  }, [])
  return [error, report] as const
}
