import { useEffect, useRef, useState } from 'react'

export type RealtimeStatus = 'connecting' | 'open' | 'fallback'

export type RealtimeEvent = {
  type: 'ready' | 'invalidate' | 'resync_required'
  protocol?: number
  sequence: number
  resources?: string[]
  reconnected?: boolean
}

export const realtimeHandshakeTimeoutMS = 4_000

export function useRealtimeEvents(enabled: boolean, url: string, onEvent: (event: RealtimeEvent) => void): RealtimeStatus {
  const [status, setStatus] = useState<RealtimeStatus>(enabled ? 'connecting' : 'fallback')
  const callbackRef = useRef(onEvent)
  const enabledRef = useRef(enabled)
  const enabling = enabled && !enabledRef.current
  callbackRef.current = onEvent

  useEffect(() => {
    if (!enabled) {
      setStatus('fallback')
      return
    }

    let disposed = false
    let socket: WebSocket | null = null
    let reconnectTimer: number | undefined
    let handshakeTimer: number | undefined
    let attempt = 0
    let connectionFailed = false
    let openedOnce = false

    const clearHandshakeTimer = () => {
      if (handshakeTimer === undefined) return
      window.clearTimeout(handshakeTimer)
      handshakeTimer = undefined
    }

    const scheduleReconnect = () => {
      if (disposed || reconnectTimer !== undefined) return
      connectionFailed = true
      setStatus('fallback')
      const base = Math.min(30_000, 1000 * 2 ** Math.min(attempt, 5))
      const delay = Math.round(base * (0.8 + Math.random() * 0.4))
      attempt++
      reconnectTimer = window.setTimeout(() => {
        reconnectTimer = undefined
        connect()
      }, delay)
    }

    const connect = () => {
      if (disposed || socket) return
      if (!connectionFailed && !openedOnce) setStatus('connecting')
      let next: WebSocket
      try {
        next = new WebSocket(url)
      } catch {
        scheduleReconnect()
        return
      }
      socket = next
      let ready = false
      handshakeTimer = window.setTimeout(() => {
        if (disposed || socket !== next || ready) return
        connectionFailed = true
        setStatus('fallback')
        next.close(1000, 'realtime handshake timeout')
      }, realtimeHandshakeTimeoutMS)
      next.onmessage = message => {
        let event: RealtimeEvent
        try {
          event = JSON.parse(String(message.data)) as RealtimeEvent
        } catch {
          next.close(1002, 'invalid event')
          return
        }
        if (event.type === 'ready') {
          if (event.protocol !== 1) {
            next.close(1002, 'unsupported protocol')
            return
          }
          ready = true
          clearHandshakeTimer()
          const reconnected = connectionFailed || openedOnce
          attempt = 0
          openedOnce = true
          setStatus('open')
          callbackRef.current({ ...event, reconnected })
          return
        }
        if (!ready) {
          next.close(1002, 'ready event required')
          return
        }
        callbackRef.current(event)
      }
      next.onerror = () => next.close()
      next.onclose = () => {
        clearHandshakeTimer()
        if (socket === next) socket = null
        scheduleReconnect()
      }
    }

    const reconnectNow = () => {
      if (disposed || socket || !navigator.onLine) return
      if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer)
      reconnectTimer = undefined
      attempt = 0
      connect()
    }

    window.addEventListener('online', reconnectNow)
    connect()
    return () => {
      disposed = true
      window.removeEventListener('online', reconnectNow)
      if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer)
      clearHandshakeTimer()
      if (socket) {
        socket.onerror = null
        socket.onmessage = null
        socket.onclose = null
        socket.close(1000, 'session closed')
      }
    }
  }, [enabled, url])

  useEffect(() => {
    enabledRef.current = enabled
  }, [enabled])

  if (!enabled) return 'fallback'
  return enabling ? 'connecting' : status
}
