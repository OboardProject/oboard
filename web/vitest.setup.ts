// Node defines its own experimental `localStorage` global, which resolves to
// `undefined` unless the process was started with `--localstorage-file`. That
// global is installed before the jsdom environment populates its own, and it
// wins, so `localStorage` and `window.localStorage` both read back undefined
// even though jsdom built a perfectly good Storage. Tests that exercise stored
// preferences need a real one, so install a spec-shaped in-memory Storage
// whenever the runtime does not supply a usable one.

class MemoryStorage implements Storage {
  private entries = new Map<string, string>()

  get length() {
    return this.entries.size
  }

  key(index: number): string | null {
    return Array.from(this.entries.keys())[index] ?? null
  }

  getItem(key: string): string | null {
    return this.entries.has(String(key)) ? (this.entries.get(String(key)) as string) : null
  }

  setItem(key: string, value: string) {
    this.entries.set(String(key), String(value))
  }

  removeItem(key: string) {
    this.entries.delete(String(key))
  }

  clear() {
    this.entries.clear()
  }
}

function usable(candidate: unknown): boolean {
  try {
    const store = candidate as Storage | null | undefined
    if (!store || typeof store.getItem !== 'function') return false
    store.getItem('__oboard_probe__')
    return true
  } catch {
    return false
  }
}

function install(name: 'localStorage' | 'sessionStorage') {
  if (usable((globalThis as Record<string, unknown>)[name])) return
  const store = new MemoryStorage()
  Object.defineProperty(globalThis, name, { value: store, configurable: true, writable: true })
  if (typeof window !== 'undefined' && window !== (globalThis as unknown)) {
    Object.defineProperty(window, name, { value: store, configurable: true, writable: true })
  }
}

install('localStorage')
install('sessionStorage')
