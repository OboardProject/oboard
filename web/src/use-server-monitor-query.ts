import { useMemo, useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'
import { isLatencyWindowPath, latencyWindowCache } from './latency-window-cache'

let nextClientKey = 0

type Client = { request: (path: string, init?: RequestInit) => Promise<any> }

export function useServerMonitorQuery<T>(client: Client, path: string, enabled: boolean) {
  const [revision, setRevision] = useState(0)
  const clientKey = useMemo(() => ++nextClientKey, [client])
  const cache = isLatencyWindowPath(path) ? latencyWindowCache(client) : null
  const cached = useMemo(() => cache?.get<T>(path) ?? null, [cache, clientKey, path, revision])
  const [state, setState] = useState<{
    path: string
    client: Client
    response: T | null
    loading: boolean
    error: unknown
  }>({ path, client, response: cached?.response ?? null, loading: !cached?.fresh, error: null })

  useCoalescedReadRequest<T>(`${clientKey}:${path}:${revision}`, signal => cache
    ? cache.read<T>(path, signal, sharedSignal => client.request(path, { signal: sharedSignal }))
    : client.request(path, { signal }), {
    onStart: () => setState(previous => ({ path, client, response: previous.path === path && previous.client === client ? previous.response : cached?.response ?? null, loading: !cache?.get<T>(path)?.fresh, error: null })),
    onSuccess: response => setState({ path, client, response, loading: false, error: null }),
    onError: error => setState(previous => ({ ...previous, loading: false, error })),
  }, { enabled })

  return {
    response: state.path === path && state.client === client ? state.response : cached?.response ?? null,
    loading: state.path === path && state.client === client ? state.loading : !cached?.fresh,
    error: state.path === path && state.client === client ? state.error : null,
    refresh: () => { cache?.remove(path); setRevision(value => value + 1) },
  }
}
