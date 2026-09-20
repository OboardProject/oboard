// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { PluginWebhookDialog } from './PluginWebhookDialog'
import { PluginDetails } from './PluginDetails'

vi.mock('../../components/ui/dialog', () => ({ Dialog: ({ isOpen, title, children }: any) => isOpen ? <section role="dialog" aria-label={title}>{children}</section> : null }))
let container: HTMLDivElement
let root: Root
let base: HTMLBaseElement
const id = 'a'.repeat(64)
const endpoint = { id, plugin_id: 3, binding_id: 5, binding_revision: 2, revision_id: 7, grant_id: 8, enabled: false, generation: 1 }
const trigger = { id: 5, plugin_id: 3, revision_id: 7, binding_revision: 2, kind: 'event', name: '外部通知', spec: { event: 'plugin.webhook' } }
const grant = { id: 8, plugin_id: 3, binding_id: 5, revision_id: 7 }
beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div'); document.body.append(container); root = createRoot(container)
  base = document.createElement('base'); base.href = '/panel/nested/'; document.head.append(base)
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } })
})
afterEach(() => { act(() => root.unmount()); container.remove(); base.remove(); vi.restoreAllMocks() })
async function render(view: React.ReactNode) { await act(async () => root.render(view)) }
async function click(text: string) {
  const button = Array.from(container.querySelectorAll('button')).find(item => item.textContent === text)
  expect(button, text).toBeTruthy()
  await act(async () => button!.click())
}
async function select(index: number, value: string) {
  await act(async () => { const input = container.querySelectorAll('select')[index]; input.value = value; input.dispatchEvent(new Event('change', { bubbles: true })) })
}
function client(initial = [] as any[]) {
  let items = initial
  return vi.fn(async (path: string, init?: RequestInit): Promise<any> => {
    if (path === '/plugin-triggers?plugin_id=3') return { triggers: [trigger, { ...trigger, id: 9, spec: { event: 'server.offline' } }] }
    if (path === '/plugin-grants?plugin_id=3') return { grants: [grant, { ...grant, id: 9, binding_id: null }, { ...grant, id: 10, binding_id: 6 }, { ...grant, id: 11, revision_id: 6 }, { ...grant, id: 12, revoked_at: '2026-01-01' }, { ...grant, id: 13, expires_at: '2000-01-01' }, { ...grant, id: 14, plugin_id: 4 }] }
    if (!init && path === '/plugin-webhooks?plugin_id=3') return { webhooks: items }
    if (path === '/plugin-webhooks' && init?.method === 'POST') { items = [endpoint]; return { webhook: endpoint, secret: 'new-secret-once' } }
    if (path === `/plugin-webhooks/${id}` && init?.method === 'PATCH') {
      const input = JSON.parse(init.body as string)
      items = [{ ...endpoint, generation: input.expected_generation + 1, enabled: input.enabled }]
      return { webhook: items[0], ...(input.rotate_secret ? { secret: 'rotated-secret-once' } : {}) }
    }
    throw new Error('unexpected request')
  })
}

it('creates from an exact binding/grant pair, copies the base-path URL and clears the one-time secret', async () => {
  const request = client()
  const onClose = vi.fn()
  const storage = vi.spyOn(Storage.prototype, 'setItem')
  await render(<PluginWebhookDialog pluginID={3} isAdmin request={request} onClose={onClose} />)
  expect(container.querySelectorAll('select')[0].options).toHaveLength(2)
  await select(0, '5')
  expect(container.querySelectorAll('select')[1].options).toHaveLength(2)
  await select(1, '8'); await click('创建端点')
  expect(JSON.parse(request.mock.calls.find(([path]) => path === '/plugin-webhooks')![1]!.body as string)).toEqual({ binding_id: 5, grant_id: 8 })
  expect(container.querySelector<HTMLInputElement>('input[type=password]')!.value).toBe('new-secret-once')
  await click('复制地址')
  expect(navigator.clipboard.writeText).toHaveBeenLastCalledWith(`${window.location.origin}/panel/nested/api/v1/plugin-webhooks/receive/${id}`)
  await click('复制密钥')
  expect(navigator.clipboard.writeText).toHaveBeenLastCalledWith('new-secret-once')
  expect(storage).not.toHaveBeenCalled()
  await click('关闭')
  expect(onClose).toHaveBeenCalledOnce()
  expect(container.querySelector('input[type=password]')).toBeNull()
  await render(null)
  await render(<PluginWebhookDialog pluginID={3} isAdmin request={request} onClose={onClose} />)
  expect(container.querySelector('input[type=password]')).toBeNull()
  expect(container.textContent).not.toContain('new-secret-once')
})

it('uses the latest expected generation for enable, rotation and disable, without redisplaying secrets on reads', async () => {
  const request = client([{ ...endpoint, secret: 'must-not-display', secret_encrypted: 'must-not-display' }])
  await render(<PluginWebhookDialog pluginID={3} isAdmin request={request} onClose={vi.fn()} />)
  expect(container.textContent).not.toContain('must-not-display')
  expect(container.querySelector('input[type=password]')).toBeNull()
  await click('启用端点'); expect(container.textContent).toContain('已启用 · 密钥与状态代次 2')
  await click('轮换密钥')
  expect(container.querySelector<HTMLInputElement>('input[type=password]')!.value).toBe('rotated-secret-once')
  await click('已保存，清除密钥'); expect(container.querySelector('input[type=password]')).toBeNull()
  await click('停用端点')
  expect(request.mock.calls.filter(([, init]) => init?.method === 'PATCH').map(([, init]) => JSON.parse(init!.body as string))).toEqual([
    { expected_generation: 1, enabled: true, rotate_secret: false },
    { expected_generation: 2, enabled: true, rotate_secret: true },
    { expected_generation: 3, enabled: false, rotate_secret: false },
  ])
  expect(container.textContent).toContain('已停用 · 密钥与状态代次 4')
})

it('refreshes after a conflict and never displays raw error bodies', async () => {
  const request = client([endpoint])
  await render(<PluginWebhookDialog pluginID={3} isAdmin request={request} onClose={vi.fn()} />)
  request.mockRejectedValueOnce(new Error('{"secret":"sensitive-error-body"}'))
  await click('启用端点')
  expect(container.querySelector('[role=alert]')?.textContent).toContain('状态已变化')
  expect(container.textContent).not.toContain('sensitive-error-body')
  expect(request.mock.calls.filter(([path]) => path === '/plugin-webhooks?plugin_id=3')).toHaveLength(2)
})

it('does not mount or request management data for non-admin users', async () => {
  const request = client()
  await render(<PluginWebhookDialog pluginID={3} isAdmin={false} request={request} onClose={vi.fn()} />)
  expect(container.innerHTML).toBe(''); expect(request).not.toHaveBeenCalled()
})

it.each([true, false])('shows the details entry only for an administrator (admin=%s)', async isAdmin => {
  const plugin = { id: 3, name: '测试插件', status: 'disabled' as const, description: '', owner_user_id: 1, created_at: '', updated_at: '' }
  const request = vi.fn(async (path: string): Promise<any> => {
    if (path === '/plugins/3') return { plugin }
    if (path.endsWith('/versions')) return { versions: [] }
    if (path.startsWith('/plugin-triggers')) return { triggers: [] }
    if (path.startsWith('/plugin-grants')) return { grants: [] }
    return { runs: [] }
  })
  await render(<PluginDetails plugin={plugin} request={request} isAdmin={isAdmin} servers={[]} onClose={vi.fn()} onChanged={vi.fn()} onDevelop={vi.fn()} />)
  await click('触发器')
  expect(container.textContent?.includes('管理 Webhook')).toBe(isAdmin)
  if (!isAdmin) expect(request.mock.calls.some(([path]) => path.startsWith('/plugin-grants') || path.startsWith('/plugin-webhooks'))).toBe(false)
})
