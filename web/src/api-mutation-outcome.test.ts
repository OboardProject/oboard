// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { resolve } from 'node:path'
import { afterEach, expect, it, vi } from 'vitest'

import { markIndeterminateMutation } from './mutation-outcome'

const source = readFileSync(resolve(__dirname, 'main.tsx'), 'utf8')
const clientSource = source.slice(source.indexOf('function api(token:'), source.indexOf('\nfunction PortalLoader'))
const compiled = ts.transpileModule(clientSource, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText
// apiRequestError lives outside the extracted slice. These tests are about how
// a failed write is classified, not about message localization, so a stand-in
// that carries the same fields is enough.
function apiRequestError(data: any, res: Response) {
  const error = new Error(data?.error || res.statusText) as Error & { status?: number }
  error.status = res.status
  return error
}
const createClient = new Function('appPath', 'markIndeterminateMutation', 'apiRequestError', `${compiled}; return api`)((path: string) => path, markIndeterminateMutation, apiRequestError)

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
    message: expect.stringContaining('请刷新确认是否已生效'),
  })
})
