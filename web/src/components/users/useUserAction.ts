import { useRef, useState } from 'react'

export function useUserAction(localize: (message: string) => string = value => value) {
  const lock = useRef(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const run = async (action: () => Promise<unknown>) => {
    if (lock.current) return
    lock.current = true
    setPending(true)
    setError('')
    try { await action() }
    catch (error: any) { setError(localize(error?.message || String(error))) }
    finally { lock.current = false; setPending(false) }
  }
  return { pending, error, run }
}
