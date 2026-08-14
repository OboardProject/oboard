import { useEffect, useRef, useState } from 'react'

export type RealtimeStatus = 'connecting' | 'open' | 'fallback'

export type ServerTelemetrySnapshot = {
  id: number
  status: string
  cpu_usage_percent: number
  memory_used_bytes: number
  memory_total_bytes: number
  agent_memory_bytes: number
  disk_bytes: number
  disk_total_bytes: number
  tcp_connection_count: number
  udp_connection_count: number
  process_count: number
  network_upload_bps: number
  network_download_bps: number
  traffic_upload_bytes: number
  traffic_download_bytes: number
  connectivity_status: string
  connectivity_latency_ms: number
  connectivity_checked_at?: string
  connectivity_error?: string
  telemetry_updated_at?: string
  last_seen_at?: string
}

export type RealtimeEvent = {
  type: 'ready' | 'invalidate' | 'resync_required' | 'server_snapshot'
  protocol?: number
  sequence: number
  resources?: string[]
  server_snapshots?: ServerTelemetrySnapshot[]
  reconnected?: boolean
}

export type PollRequest = (path: string, init?: RequestInit) => Promise<RealtimeEvent>

export const realtimeHandshakeTimeoutMS = 4_000

export function usePollingEvents(enabled: boolean, request: PollRequest, onEvent: (event: RealtimeEvent) => void): RealtimeStatus {
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
    let sequence = 0
    let attempt = 0
    let retryTimer: number | undefined
    let controller: AbortController | null = null
    let failed = false

    const poll = async () => {
      if (disposed || controller || !navigator.onLine) return
      controller = new AbortController()
      try {
        const event = await request(`/poll-events?since=${sequence}`, { signal: controller.signal })
        if (disposed) return
        const reconnected = failed
        failed = false
        attempt = 0
        sequence = Number(event.sequence || 0)
        setStatus('open')
        if (event.type !== 'ready' || reconnected) callbackRef.current({ ...event, reconnected })
      } catch (error: any) {
        if (disposed || error?.name === 'AbortError') return
        failed = true
        setStatus('fallback')
        const base = Math.min(30_000, 1000 * 2 ** Math.min(attempt, 5))
        const delay = Math.round(base * (0.8 + Math.random() * 0.4))
        attempt++
        retryTimer = window.setTimeout(() => {
          retryTimer = undefined
          void poll()
        }, delay)
      } finally {
        controller = null
        if (!disposed && retryTimer === undefined && navigator.onLine) void poll()
      }
    }

    const reconnectNow = () => {
      if (disposed || controller) return
      if (retryTimer !== undefined) window.clearTimeout(retryTimer)
      retryTimer = undefined
      attempt = 0
      void poll()
    }

    window.addEventListener('online', reconnectNow)
    void poll()
    return () => {
      disposed = true
      window.removeEventListener('online', reconnectNow)
      if (retryTimer !== undefined) window.clearTimeout(retryTimer)
      controller?.abort()
    }
  }, [enabled, request])

  useEffect(() => {
    enabledRef.current = enabled
  }, [enabled])

  if (!enabled) return 'fallback'
  return enabling ? 'connecting' : status
}

export function useServerTelemetry(enabled: boolean, url: string, onEvent: (event: RealtimeEvent) => void): RealtimeStatus {
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
        next.close(1000, 'telemetry handshake timeout')
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
          if (event.protocol !== 2) {
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
        if (!ready || event.type !== 'server_snapshot') {
          next.close(1002, ready ? 'telemetry event required' : 'ready event required')
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
