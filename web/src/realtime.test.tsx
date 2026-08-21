// @vitest-environment jsdom

import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { realtimeHandshakeTimeoutMS, usePollingEvents, useServerTelemetry, type PollRequest, type RealtimeEvent, type RealtimeStatus } from './realtime'

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

function TelemetryHarness({ enabled = true, onStatus, onEvent }: { enabled?: boolean; onStatus: (status: RealtimeStatus) => void; onEvent: (event: RealtimeEvent) => void }) {
  const status = useServerTelemetry(enabled, 'ws://localhost/events', onEvent)
  useEffect(() => { onStatus(status) }, [onStatus, status])
  return null
}

function PollingHarness({ request, onStatus, onEvent }: { request: PollRequest; onStatus: (status: RealtimeStatus) => void; onEvent: (event: RealtimeEvent) => void }) {
  const status = usePollingEvents(true, request, onEvent)
  useEffect(() => { onStatus(status) }, [onStatus, status])
  return null
}

let visibilityOverride: 'visible' | 'hidden' = 'visible'
function setVisibility(visible: boolean) {
  visibilityOverride = visible ? 'visible' : 'hidden'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibilityOverride })
  document.dispatchEvent(new Event('visibilitychange'))
}

describe('panel transports', () => {
  let root: Root
  let container: HTMLDivElement

  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
    setVisibility(true)
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

  it('accepts protocol 2 telemetry snapshots without business invalidations', () => {
    const statuses: RealtimeStatus[] = []
    const events: RealtimeEvent[] = []
    act(() => root.render(<TelemetryHarness onStatus={status => statuses.push(status)} onEvent={event => events.push(event)} />))

    expect(FakeWebSocket.instances).toHaveLength(1)
    act(() => FakeWebSocket.instances[0].emit({ type: 'ready', protocol: 2, sequence: 0, server_snapshots: [] }))
    act(() => FakeWebSocket.instances[0].emit({ type: 'server_snapshot', sequence: 1, server_snapshots: [{ id: 1, status: 'online' } as any] }))

    expect(statuses[statuses.length - 1]).toBe('open')
    expect(events[1].type).toBe('server_snapshot')
    expect(events[1].server_snapshots?.[0].id).toBe(1)
    act(() => vi.advanceTimersByTime(realtimeHandshakeTimeoutMS))
    expect(FakeWebSocket.instances[0].closed).toBe(false)
  })

  it('rejects business invalidations on the telemetry socket', () => {
    act(() => root.render(<TelemetryHarness onStatus={() => {}} onEvent={() => {}} />))
    act(() => FakeWebSocket.instances[0].emit({ type: 'ready', protocol: 2, sequence: 0 }))
    act(() => FakeWebSocket.instances[0].emit({ type: 'invalidate', sequence: 1, resources: ['tasks'] }))
    expect(FakeWebSocket.instances[0].closed).toBe(true)
  })

  it('accepts consecutive server patches', () => {
    const events: RealtimeEvent[] = []
    act(() => root.render(<TelemetryHarness onStatus={() => {}} onEvent={event => events.push(event)} />))
    act(() => FakeWebSocket.instances[0].emit({ type: 'ready', protocol: 2, sequence: 5, server_snapshots: [] }))
    act(() => FakeWebSocket.instances[0].emit({ type: 'server_patch', sequence: 6, server_patches: [{ server_id: 1, fields: { status: 'online' } }] }))

    expect(events[1]).toMatchObject({ type: 'server_patch', sequence: 6 })
    expect(FakeWebSocket.instances[0].closed).toBe(false)
  })

  it('requests a resync and reconnects after a server patch sequence gap', () => {
    const events: RealtimeEvent[] = []
    act(() => root.render(<TelemetryHarness onStatus={() => {}} onEvent={event => events.push(event)} />))
    act(() => FakeWebSocket.instances[0].emit({ type: 'ready', protocol: 2, sequence: 5, server_snapshots: [] }))
    act(() => FakeWebSocket.instances[0].emit({ type: 'server_patch', sequence: 7, server_patches: [{ server_id: 1, fields: { status: 'online' } }] }))

    expect(events[1]).toEqual({ type: 'resync_required', sequence: 7 })
    expect(FakeWebSocket.instances[0].closed).toBe(true)
    act(() => vi.advanceTimersByTime(1_000))
    expect(FakeWebSocket.instances).toHaveLength(2)
  })

  it('falls back after a telemetry handshake timeout', () => {
    const statuses: RealtimeStatus[] = []
    act(() => root.render(<TelemetryHarness onStatus={status => statuses.push(status)} onEvent={() => {}} />))
    act(() => vi.advanceTimersByTime(realtimeHandshakeTimeoutMS))
    expect(statuses[statuses.length - 1]).toBe('fallback')
    expect(FakeWebSocket.instances[0].closed).toBe(true)
  })

  it('uses HTTP polling for business invalidations and advances the sequence', async () => {
    const statuses: RealtimeStatus[] = []
    const events: RealtimeEvent[] = []
    const pending = new Promise<RealtimeEvent>(() => {})
    const request = vi.fn<PollRequest>()
      .mockResolvedValueOnce({ type: 'invalidate', sequence: 7, resources: ['tasks'] })
      .mockImplementation(() => pending)

    await act(async () => {
      root.render(<PollingHarness request={request} onStatus={status => statuses.push(status)} onEvent={event => events.push(event)} />)
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(request.mock.calls[0][0]).toBe('/poll-events?since=0')
    expect(request.mock.calls[1][0]).toBe('/poll-events?since=7')
    expect(events).toEqual([{ type: 'invalidate', sequence: 7, resources: ['tasks'], reconnected: false }])
    expect(statuses[statuses.length - 1]).toBe('open')
  })

  it('pauses HTTP polling while hidden and resumes from the saved sequence', async () => {
    const events: RealtimeEvent[] = []
    let pendingSignal: AbortSignal | undefined
    let rejectPending!: (error: Error) => void
    const pending = new Promise<RealtimeEvent>((_resolve, reject) => { rejectPending = reject })
    const request = vi.fn<PollRequest>()
      .mockResolvedValueOnce({ type: 'invalidate', sequence: 7, resources: ['tasks'] })
      .mockImplementation((_path, init) => {
        pendingSignal = init?.signal
        pendingSignal?.addEventListener('abort', () => rejectPending(new DOMException('aborted', 'AbortError')))
        return pending
      })

    await act(async () => {
      root.render(<PollingHarness request={request} onStatus={() => {}} onEvent={event => events.push(event)} />)
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(request.mock.calls[1][0]).toBe('/poll-events?since=7')
    expect(pendingSignal).toBeDefined()

    await act(async () => {
      setVisibility(false)
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(pendingSignal?.aborted).toBe(true)
    act(() => vi.advanceTimersByTime(30_000))
    expect(request).toHaveBeenCalledTimes(2)

    act(() => setVisibility(true))
    expect(request).toHaveBeenCalledTimes(3)
    expect(request.mock.calls[2][0]).toBe('/poll-events?since=7')
  })

  it('closes telemetry while hidden and reconnects with reconnect ready on visible', () => {
    const events: RealtimeEvent[] = []
    act(() => root.render(<TelemetryHarness onStatus={() => {}} onEvent={event => events.push(event)} />))
    act(() => FakeWebSocket.instances[0].emit({ type: 'ready', protocol: 2, sequence: 5, server_snapshots: [] }))
    const first = FakeWebSocket.instances[0]

    act(() => setVisibility(false))
    expect(first.closed).toBe(true)
    act(() => vi.advanceTimersByTime(30_000))
    expect(FakeWebSocket.instances).toHaveLength(1)

    act(() => setVisibility(true))
    expect(FakeWebSocket.instances).toHaveLength(2)
    act(() => FakeWebSocket.instances[1].emit({ type: 'ready', protocol: 2, sequence: 5, server_snapshots: [] }))
    expect(events[events.length - 1]).toMatchObject({ type: 'ready', reconnected: true })
  })
})
