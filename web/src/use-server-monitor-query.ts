import { useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'

type Client = { request: (path: string, init?: RequestInit) => Promise<any> }

export function useServerMonitorQuery<T>(client: Client, path: string, enabled: boolean) {
  const [revision, setRevision] = useState(0)
  const [state, setState] = useState<{
    path: string
    response: T | null
    loading: boolean
    error: unknown
  }>({ path, response: null, loading: true, error: null })

  useCoalescedReadRequest<T>(`${path}:${revision}`, signal => client.request(path, { signal }), {
    onStart: () => setState(previous => ({ path, response: previous.path === path ? previous.response : null, loading: true, error: null })),
    onSuccess: response => setState({ path, response, loading: false, error: null }),
    onError: error => setState(previous => ({ ...previous, loading: false, error })),
  }, { enabled })

  return {
    response: state.path === path ? state.response : null,
    loading: state.path !== path || state.loading,
    error: state.path === path ? state.error : null,
    refresh: () => setRevision(value => value + 1),
  }
}
