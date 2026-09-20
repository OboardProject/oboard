// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'

import { createAPIClientFactory } from './api-client'

const createClient = createAPIClientFactory(path => path, (data, res) => {
  const error = new Error(data?.error || res.statusText) as Error & { status?: number }
  error.status = res.status
  return error
})

afterEach(() => {
  sessionStorage.clear()
  vi.unstubAllGlobals()
})

it('uses renewed CSRF credentials on every request from an existing cookie client', async () => {
  const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({ data: {} }), { status: 200 }))
  vi.stubGlobal('fetch', fetcher)
  sessionStorage.setItem('oboard.csrf', 'before-renewal')
  const client = createClient('cookie')
  await client.request('/controller-update/activity', { method: 'POST' })
  expect(new Headers(fetcher.mock.calls[0][1]?.headers).get('x-oboard-csrf')).toBe('before-renewal')
  sessionStorage.setItem('oboard.csrf', 'after-renewal')
  await client.request('/controller-update/install', { method: 'POST' })
  await client.requestV2('/changesets', { method: 'POST' })
  await client.upload('/upload', new FormData())
  for (const call of fetcher.mock.calls.slice(1)) {
    expect(new Headers(call[1]?.headers).get('x-oboard-csrf')).toBe('after-renewal')
  }
  sessionStorage.removeItem('oboard.csrf')
  await client.request('/controller-update/activity', { method: 'POST' })
  expect(new Headers(fetcher.mock.calls.at(-1)?.[1]?.headers).has('x-oboard-csrf')).toBe(false)
})

it('does not send cookie CSRF credentials with bearer authentication', async () => {
  const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}', { status: 200 }))
  vi.stubGlobal('fetch', fetcher)
  sessionStorage.setItem('oboard.csrf', 'cookie-token')
  await createClient('bearer-token').request('/controller-update/install', { method: 'POST' })
  const headers = new Headers(fetcher.mock.calls[0][1]?.headers)
  expect(headers.get('authorization')).toBe('Bearer bearer-token')
  expect(headers.has('x-oboard-csrf')).toBe(false)
})
