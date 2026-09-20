// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createAPIClientFactory, SupersededAuthRequestError } from './api-client'
import { SessionBoundary } from './session-boundary'

const api = createAPIClientFactory(path => `/panel${path}`, (data, response) =>
  Object.assign(new Error(data?.error || response.statusText), { status: response.status }))

afterEach(() => {
  sessionStorage.clear()
  vi.unstubAllGlobals()
})

for (const method of ['request', 'requestV2'] as const) {
  for (const status of [200, 401, 500]) {
    it(`${method} isolates late ${status} responses across cookie sessions`, async () => {
      let finish!: (response: Response) => void
      const fetcher = vi.fn(() => new Promise<Response>(resolve => { finish = resolve }))
      vi.stubGlobal('fetch', fetcher)
      const session = new SessionBoundary()
      const observer = vi.fn()
      const unauthorized = vi.fn(() => true)
      const oldClient = api('cookie', unauthorized, observer, session.capture())
      const pending = oldClient[method]('/servers', { method: 'POST' })
      expect(observer).toHaveBeenCalledExactlyOnceWith('/servers', { mutation_pending: true }, 'POST')
      session.advance()
      finish(new Response(JSON.stringify({ data: { value: 'old' } }), { status }))
      await expect(pending).rejects.toBeInstanceOf(SupersededAuthRequestError)
      expect(observer).toHaveBeenCalledTimes(1)
      expect(unauthorized).not.toHaveBeenCalled()
      await expect(oldClient[method]('/servers')).rejects.toBeInstanceOf(SupersededAuthRequestError)
      expect(fetcher).toHaveBeenCalledTimes(1)
    })
  }
}

it('does not notify a new session or retry a disconnected write', async () => {
  let reject!: (error: Error) => void
  const fetcher = vi.fn(() => new Promise<Response>((_, fail) => { reject = fail }))
  vi.stubGlobal('fetch', fetcher)
  const session = new SessionBoundary()
  const observer = vi.fn()
  const pending = api('cookie', undefined, observer, session.capture()).request('/servers', { method: 'POST' })
  session.advance()
  reject(new TypeError('Failed to fetch'))
  await expect(pending).rejects.toBeInstanceOf(SupersededAuthRequestError)
  expect(observer).toHaveBeenCalledTimes(1)
  expect(fetcher).toHaveBeenCalledTimes(1)
})

it('notifies successful writes through the explicit observer and preserves read cancellation', async () => {
  const fetcher = vi.fn(async () => new Response('{"configuration_revision":7}'))
  vi.stubGlobal('fetch', fetcher)
  const observer = vi.fn()
  const client = api('bearer', undefined, observer)
  await client.request('/servers', { method: 'POST' })
  expect(observer).toHaveBeenLastCalledWith('/servers', { configuration_revision: 7, mutation_pending: false }, 'POST')
  const controller = new AbortController()
  vi.stubGlobal('fetch', vi.fn((_path, init) => {
    expect(init.signal).toBe(controller.signal)
    return Promise.reject(new DOMException('Cancelled', 'AbortError'))
  }))
  await expect(client.request('/servers', { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' })
  expect(observer).toHaveBeenCalledTimes(2)
})

it('keeps base paths, form upload headers, download names and cookie credentials', async () => {
  const fetcher = vi.fn(async () => new Response('{}', { headers: { 'content-disposition': 'attachment; filename="backup.zip"' } }))
  vi.stubGlobal('fetch', fetcher)
  sessionStorage.setItem('oboard.csrf', 'csrf')
  const client = api('cookie')
  const form = new FormData()
  await client.upload('/backups/upload', form)
  expect(fetcher).toHaveBeenNthCalledWith(1, '/panel/api/v1/ui/backups/upload', {
    method: 'POST', body: form, credentials: 'same-origin', headers: { 'x-oboard-csrf': 'csrf' },
  })
  const download = await client.download('/backups/1/download')
  expect(download.filename).toBe('backup.zip')
  expect(download.blob.size).toBe(2)
  expect(fetcher).toHaveBeenNthCalledWith(2, '/panel/api/v1/ui/backups/1/download', { credentials: 'same-origin', headers: {} })
})

for (const method of ['upload', 'download'] as const) {
  it(`rejects a late ${method} after session replacement`, async () => {
    let finish!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { finish = resolve })))
    const session = new SessionBoundary()
    const client = api('cookie', undefined, undefined, session.capture())
    const pending = method === 'upload' ? client.upload('/backups', new FormData()) : client.download('/backups')
    session.advance()
    finish(new Response('{}'))
    await expect(pending).rejects.toBeInstanceOf(SupersededAuthRequestError)
  })
}
