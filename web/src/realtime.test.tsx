// @vitest-environment jsdom

import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { realtimeHandshakeTimeoutMS, useRealtimeEvents, type RealtimeEvent, type RealtimeStatus } from './realtime'

class FakeWebSocket {
  static instances: FakeWebSocket[] = []

  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  closed = false

  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this)
  }

  close() {
    if (this.closed) return
    this.closed = true
    this.onclose?.({} as CloseEvent)
  }

  emit(event: RealtimeEvent) {
    this.onmessage?.({ data: JSON.stringify(event) } as MessageEvent)
  }
}

function Harness({ enabled = true, onStatus, onEvent }: { enabled?: boolean; onStatus: (status: RealtimeStatus) => void; onEvent: (event: RealtimeEvent) => void }) {
  const status = useRealtimeEvents(enabled, 'ws://localhost/events', onEvent)
  useEffect(() => { onStatus(status) }, [onStatus, status])
  return null
}

describe('useRealtimeEvents', () => {
  let root: Root
  let container: HTMLDivElement

  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
    container = document.createElement('div')
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('opens the first connection without marking ready as a resync', () => {
    const statuses: RealtimeStatus[] = []
    const events: RealtimeEvent[] = []
    act(() => root.render(<Harness onStatus={status => statuses.push(status)} onEvent={event => events.push(event)} />))

    expect(FakeWebSocket.instances).toHaveLength(1)
    act(() => FakeWebSocket.instances[0].emit({ type: 'ready', protocol: 1, sequence: 0 }))

    expect(statuses[statuses.length - 1]).toBe('open')
    expect(events).toEqual([{ type: 'ready', protocol: 1, sequence: 0, reconnected: false }])
    act(() => vi.advanceTimersByTime(realtimeHandshakeTimeoutMS))
    expect(FakeWebSocket.instances[0].closed).toBe(false)
  })

  it('reports connecting immediately when realtime is enabled after login', () => {
    const statuses: RealtimeStatus[] = []
    const onStatus = (status: RealtimeStatus) => { statuses.push(status) }
    const onEvent = () => {}
    act(() => root.render(<Harness enabled={false} onStatus={onStatus} onEvent={onEvent} />))
    expect(statuses[statuses.length - 1]).toBe('fallback')

    act(() => root.render(<Harness enabled onStatus={onStatus} onEvent={onEvent} />))
    expect(statuses[statuses.length - 1]).toBe('connecting')
    expect(FakeWebSocket.instances).toHaveLength(1)
  })

  it('falls back after the handshake timeout and stays there until reconnect is ready', () => {
    const statuses: RealtimeStatus[] = []
    const events: RealtimeEvent[] = []
    act(() => root.render(<Harness onStatus={status => statuses.push(status)} onEvent={event => events.push(event)} />))

    act(() => vi.advanceTimersByTime(realtimeHandshakeTimeoutMS))
    expect(statuses[statuses.length - 1]).toBe('fallback')
    expect(FakeWebSocket.instances[0].closed).toBe(true)

    act(() => vi.advanceTimersByTime(1_000))
    expect(FakeWebSocket.instances).toHaveLength(2)
    expect(statuses[statuses.length - 1]).toBe('fallback')

    act(() => FakeWebSocket.instances[1].emit({ type: 'ready', protocol: 1, sequence: 3 }))
    expect(statuses[statuses.length - 1]).toBe('open')
    expect(events[events.length - 1]).toEqual({ type: 'ready', protocol: 1, sequence: 3, reconnected: true })
  })
})
