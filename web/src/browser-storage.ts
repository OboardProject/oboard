// Browser storage is not always reachable. Private and hardened browsing modes
// expose `localStorage` but throw on access, an exhausted quota throws on write,
// and a non-browser runtime may not define it at all. A preference is never
// worth failing the surface that reads it, so every access goes through these
// helpers and a blocked store simply reads back as "nothing saved".

function storage(): Storage | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function readStoredValue(key: string): string | null {
  try {
    return storage()?.getItem(key) ?? null
  } catch {
    return null
  }
}

export function writeStoredValue(key: string, value: string) {
  try {
    storage()?.setItem(key, value)
  } catch {
    // The value still applies for this session.
  }
}

export function removeStoredValue(key: string) {
  try {
    storage()?.removeItem(key)
  } catch {
    // Nothing to clean up when the store is unreachable.
  }
}

export function readStoredJSON<T>(key: string, fallback: T): T {
  const raw = readStoredValue(key)
  if (raw === null) return fallback
  try {
    const value = JSON.parse(raw)
    return value === null || value === undefined ? fallback : (value as T)
  } catch {
    return fallback
  }
}

export function writeStoredJSON(key: string, value: unknown) {
  try {
    writeStoredValue(key, JSON.stringify(value))
  } catch {
    // A value that cannot be serialised is not worth failing the caller over.
  }
}
