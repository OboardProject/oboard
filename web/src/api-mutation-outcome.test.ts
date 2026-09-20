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

it('reports a write the controller never answered as unconfirmed', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
  const client = createClient('token')
  await expect(client.request('/servers/1', { method: 'DELETE' })).rejects.toMatchObject({
    unknownOutcome: true,
    message: expect.stringContaining('提交结果未知'),
  })
})

it('keeps a refused write a plain failure', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: '名称已存在' }), { status: 409 })))
  const client = createClient('token')
  await expect(client.request('/servers', { method: 'POST' })).rejects.toMatchObject({ message: '名称已存在' })
  await expect(client.request('/servers', { method: 'POST' })).rejects.not.toMatchObject({ unknownOutcome: true })
})

it('does not relabel a read that failed', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
  const client = createClient('token')
  await expect(client.request('/servers')).rejects.toMatchObject({ message: 'Failed to fetch' })
})

it('reports a controller error on a write as unconfirmed rather than failed', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'internal server error' }), { status: 500 })))
  const client = createClient('token')
  await expect(client.request('/users/7/subscription-token/rotate', { method: 'POST' })).rejects.toMatchObject({
    unknownOutcome: true,
    message: expect.stringContaining('确认前不要重复提交'),
  })
})
