import { useEffect, useRef, useState } from 'react'

export function useDocumentVisible(): boolean {
  const [visible, setVisible] = useState(() => typeof document === 'undefined' || document.visibilityState === 'visible')

  useEffect(() => {
    const update = () => setVisible(document.visibilityState === 'visible')
    update()
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])

  return visible
}

export function usePausedInterval(callback: () => void, delayMS: number, active = true): void {
  const callbackRef = useRef(callback)
  const visible = useDocumentVisible()
  callbackRef.current = callback

  useEffect(() => {
    if (!active || !visible) return
    const timer = window.setInterval(() => callbackRef.current(), delayMS)
    return () => window.clearInterval(timer)
  }, [active, delayMS, visible])
}
