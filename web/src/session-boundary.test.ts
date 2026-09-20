// @vitest-environment jsdom
import { act, createElement, useMemo } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { createAPIClientFactory, SupersededAuthRequestError } from './api-client'
import { useSessionBoundary } from './session-boundary'

it('refreshes the mounted session client on consecutive cookie logins and fences old reads, writes and 401s', async () => {
  const api = createAPIClientFactory(path => path, (_data, response) => new Error(response.statusText))
  const observer = vi.fn()
  const unauthorized = vi.fn(() => true)
  const finishes: ((response: Response) => void)[] = []
  const fetcher = vi.fn(() => new Promise<Response>(resolve => finishes.push(resolve)))
  vi.stubGlobal('fetch', fetcher)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  let current!: ReturnType<typeof api>
  let login!: () => void
  function Harness() {
    const { advance, isCurrentSession } = useSessionBoundary()
    login = advance
    current = useMemo(() => api('cookie', unauthorized, observer, isCurrentSession), [isCurrentSession])
    return null
  }
  const container = document.createElement('div')
  const root = createRoot(container)
  try {
    await act(async () => root.render(createElement(Harness)))
    for (let iteration = 0; iteration < 2; iteration++) {
      const old = current
      const start = finishes.length
      const read = old.request('/servers')
      const write = old.request('/servers', { method: 'POST', body: '{}' })
      const expired = old.request('/account')
      const rejected = [read, write, expired].map(pending => expect(pending).rejects.toBeInstanceOf(SupersededAuthRequestError))
      const callsBeforeLogin = observer.mock.calls.length
      await act(async () => login())
      finishes[start](new Response('{"old":true}'))
      finishes[start + 1](new Response('{"old":true}'))
      finishes[start + 2](new Response('{}', { status: 401 }))
      await Promise.all(rejected)
      expect(current).not.toBe(old)
      expect(observer).toHaveBeenCalledTimes(callsBeforeLogin)
      expect(unauthorized).not.toHaveBeenCalled()
      const callsBeforeStaleRequest = fetcher.mock.calls.length
      await expect(old.request('/servers')).rejects.toBeInstanceOf(SupersededAuthRequestError)
      expect(fetcher).toHaveBeenCalledTimes(callsBeforeStaleRequest)
      const fresh = current.request('/servers')
      finishes[start + 3](new Response('{"fresh":true}'))
      await expect(fresh).resolves.toEqual({ fresh: true })
    }
    expect(fetcher).toHaveBeenCalledTimes(8)
  } finally {
    await act(async () => root.unmount())
    vi.unstubAllGlobals()
  }
})
