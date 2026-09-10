const MAX_BYTES = 8 * 1024 * 1024
const MAX_ENTRY_BYTES = 2 * 1024 * 1024
const MAX_ENTRIES = 32
const FRESH_MS = 30_000
const RETAIN_MS = 150_000

type Entry = { json: string; bytes: number; fetchedAt: number }
type Flight = { controller: AbortController; promise: Promise<unknown>; waiters: number }

export class LatencyWindowCache {
  private entries = new Map<string, Entry>()
  private flights = new Map<string, Flight>()
  private bytes = 0
  private epoch = 0

  constructor(private capacity = MAX_BYTES, private entryLimit = MAX_ENTRY_BYTES) {}

  get<T>(key: string, now = Date.now()): { response: T; fetchedAt: number; fresh: boolean } | null {
    const entry = this.entries.get(key)
    if (!entry) return null
    if (now - entry.fetchedAt >= RETAIN_MS) { this.remove(key); return null }
    this.entries.delete(key)
    this.entries.set(key, entry)
    return { response: JSON.parse(entry.json) as T, fetchedAt: entry.fetchedAt, fresh: now - entry.fetchedAt < FRESH_MS }
  }

  put(key: string, response: unknown, now = Date.now()) {
    let fetchedAt = now
    if (response && typeof response === 'object' && 'metadata' in response) {
      const meta = response.metadata
      if (meta && typeof meta === 'object') {
        if ('stale' in meta && meta.stale === true) return
        if ('generated_at' in meta && typeof meta.generated_at === 'string') {
          const generated = Date.parse(meta.generated_at)
          if (Number.isFinite(generated)) fetchedAt = Math.min(now, generated)
        }
      }
    }
    if (now - fetchedAt >= RETAIN_MS) return
    const json = JSON.stringify(response)
    if (json === undefined) return
    // Retain serialized UTF-16 strings, not unbounded parsed object graphs.
    const bytes = (json.length + key.length) * 2
    this.remove(key)
    if (bytes > this.entryLimit || bytes > this.capacity) return
    while (this.entries.size && (this.bytes + bytes > this.capacity || this.entries.size >= MAX_ENTRIES)) {
      this.remove(this.entries.keys().next().value!)
    }
    this.entries.set(key, { json, bytes, fetchedAt })
    this.bytes += bytes
  }

  remove(key: string) {
    const entry = this.entries.get(key)
    if (entry) { this.bytes -= entry.bytes; this.entries.delete(key) }
  }

  clear() {
    this.epoch++
    this.entries.clear()
    this.bytes = 0
    for (const flight of this.flights.values()) flight.controller.abort()
    this.flights.clear()
  }

  async read<T>(key: string, signal: AbortSignal, load: (signal: AbortSignal) => Promise<T>, force = false): Promise<T> {
    if (signal.aborted) throw new DOMException('Cancelled', 'AbortError')
    const cached = !force && this.get<T>(key)
    if (cached && cached.fresh) return cached.response
    let flight = this.flights.get(key)
    if (!flight) {
      if (this.flights.size >= 8) throw new Error('图表读取繁忙，请稍后重试')
      const epoch = this.epoch
      const controller = new AbortController()
      flight = { controller, promise: Promise.resolve(), waiters: 0 }
      const current = flight
      const timer = setTimeout(() => controller.abort(new Error('图表读取超时，请重试')), 10_000)
      let cancelRead = () => {}
      const cancelled = new Promise<never>((_, reject) => {
        cancelRead = () => reject(controller.signal.reason ?? new DOMException('Cancelled', 'AbortError'))
        controller.signal.addEventListener('abort', cancelRead, { once: true })
      })
      current.promise = Promise.race([Promise.resolve().then(() => load(controller.signal)), cancelled]).then(response => {
        if (epoch === this.epoch && !controller.signal.aborted) this.put(key, response)
        return response
      }).finally(() => {
        clearTimeout(timer)
        controller.signal.removeEventListener('abort', cancelRead)
        if (this.flights.get(key) === current) this.flights.delete(key)
      })
      this.flights.set(key, current)
    }
    const current = flight
    current.waiters++
    return new Promise<T>((resolve, reject) => {
      let settled = false
      const finish = () => {
        if (settled) return false
        settled = true
        signal.removeEventListener('abort', abort)
        current.waiters--
        if (current.waiters === 0 && this.flights.get(key) === current) {
          this.flights.delete(key)
          current.controller.abort()
        }
        return true
      }
      const abort = () => { if (finish()) reject(new DOMException('Cancelled', 'AbortError')) }
      signal.addEventListener('abort', abort, { once: true })
      current.promise.then(value => { if (finish()) resolve(value as T) }, error => { if (finish()) reject(error) })
    })
  }
}

let scope: object | undefined
const windows = new LatencyWindowCache()

export function latencyWindowCache(client: object): LatencyWindowCache {
  if (scope !== client) { windows.clear(); scope = client }
  return windows
}

export function clearLatencyWindowCache() { windows.clear(); scope = undefined }

export function isLatencyWindowPath(path: string) {
  if (!/^\/servers\/\d+\/connectivity\?/.test(path)) return false
  const view = new URLSearchParams(path.slice(path.indexOf('?') + 1)).get('view')
  return view === null || view === 'chart'
}
