// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DialogContext } from '../ui/dialog-context'
import { NodePresetsPanel } from '../NodePresetsPanel'
import { SnellProfilesPanel, emptySnellDraft } from '../SnellProfilesPanel'
import { SubscriptionTemplatesPanel, type SubscriptionClientTemplate } from '../SubscriptionTemplatesPanel'

let root: Root
let host: HTMLDivElement
const confirm = vi.fn(async () => true)
const notify = vi.fn()
const load = vi.fn(async () => undefined)

beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  confirm.mockReset().mockResolvedValue(true)
  notify.mockReset()
  load.mockClear()
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
})
async function render(ui: React.ReactNode) {
  await act(async () => root.render(<DialogContext.Provider value={{ confirm, alert: vi.fn(), prompt: vi.fn() }}>{ui}</DialogContext.Provider>))
}
function button(text: string) {
  const found = [...document.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === text || item.getAttribute('aria-label') === text)
  expect(found, text).toBeTruthy()
  return found!
}
async function click(text: string) { await act(async () => button(text).click()) }
async function fill(label: string, value: string) {
  const input = document.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[aria-label="${label}"]`)!
  expect(input).toBeTruthy()
  await act(async () => {
    const prototype = input.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
const preset = { id: 1, name: 'Reality', protocol: 'vless', kind: 'vless-reality', default_port: 443, config_json: '{}', enabled: true, builtin: true, usage_count: 0, remark: '' }

describe('node and Snell preset settings', () => {
  it('locks builtin kinds, keeps failed edits and confirms discarding a draft', async () => {
    const request = vi.fn(async (_path: string, _options?: any) => { throw new Error('端口冲突，请修改端口') })
    await render(<NodePresetsPanel data={{ node_presets: [preset] }} client={{ request }} load={load} notify={notify} />)
    expect(document.querySelector('[aria-label="删除 Reality"]')).toBeNull()
    await click('编辑 Reality')
    expect(button('配置类型').disabled).toBe(true)
    await fill('预设名称', 'Reality changed')
    await click('保存预设')
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('端口冲突')
    expect((document.querySelector('[aria-label="预设名称"]') as HTMLInputElement).value).toBe('Reality changed')
    expect(JSON.parse(request.mock.calls[0][1].body)).toMatchObject({ name: 'Reality changed', protocol: 'vless', kind: 'vless-reality' })
    confirm.mockResolvedValueOnce(false)
    await click('取消')
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ title: '放弃未保存的修改？' }))
    expect(document.querySelector('[aria-label="预设名称"]')).not.toBeNull()
  })

  it('blocks dismissal and input changes while a preset save is pending', async () => {
    let reject!: (error: Error) => void
    const request = vi.fn(() => new Promise((_, fail) => { reject = fail }))
    await render(<NodePresetsPanel data={{ node_presets: [preset] }} client={{ request }} load={load} notify={notify} />)
    await click('编辑 Reality')
    await click('保存预设')
    expect(document.querySelector('fieldset')?.disabled).toBe(true)
    await click('关闭')
    expect(document.querySelector('[aria-label="预设名称"]')).not.toBeNull()
    await act(async () => reject(new Error('保存失败')))
    expect(document.querySelector('fieldset')?.disabled).toBe(false)
  })

  it('restores system presets only after confirmation and retains the destructive boundary', async () => {
    const request = vi.fn(async () => ({}))
    await render(<NodePresetsPanel data={{}} client={{ request }} load={load} notify={notify} />)
    confirm.mockResolvedValueOnce(false)
    await click('恢复系统模板')
    expect(request).not.toHaveBeenCalled()
    await click('恢复系统模板')
    expect(confirm).toHaveBeenLastCalledWith(expect.objectContaining({ message: expect.stringContaining('不会影响自定义预设与入口引用'), tone: 'danger' }))
    expect(request).toHaveBeenCalledWith('/node-presets/restore-system', { method: 'POST', body: '{}' })
  })

  it('keeps referenced Snell profiles undeletable and masks PSK without changing its payload', async () => {
    const profile = { ...emptySnellDraft(6), id: 2, name: 'Snell v6', psk: 'test-profile-psk', builtin: false, usage_count: 3 }
    const request = vi.fn(async () => ({}))
    await render(<SnellProfilesPanel data={{ snell_profiles: [profile] }} client={{ request }} load={load} notify={notify} />)
    expect(host.textContent).not.toContain(profile.psk)
    expect(button('删除 Snell v6').disabled).toBe(true)
    await click('编辑 Snell v6')
    const psk = document.querySelector<HTMLInputElement>('[aria-label="PSK"]')!
    expect(psk.type).toBe('password')
    await click('显示 PSK')
    expect(psk.type).toBe('text')
    expect(document.querySelector('[aria-label="混淆模式"]')).toBeNull()
    expect(document.querySelector('[aria-label="v6 传输模式"]')).not.toBeNull()
    await click('保存预设')
    expect(request).toHaveBeenCalledWith('/snell-profiles/2', { method: 'PUT', body: JSON.stringify(emptyPayload(profile)) })
    expect(notify).toHaveBeenCalledWith('预设已更新，引用入口将在下次部署时生效', 'success')
  })
})
function emptyPayload(profile: ReturnType<typeof emptySnellDraft> & { id: number; builtin: boolean; usage_count: number }) {
  const { id, builtin, usage_count, ...draft } = profile
  return draft
}

const template: SubscriptionClientTemplate = { format: 'mihomo', label: 'Mihomo', content: 'original', source: 'custom', revision: 7, builtin_digest: 'abcdef1234567890', base_builtin_digest: 'fedcba9876543210', builtin_updated: true, markers: ['{{nodes}}'] }

describe('subscription template settings', () => {
  it('retries failed initial loading instead of showing an empty successful list', async () => {
    const request = vi.fn().mockRejectedValueOnce(new Error('网络不可用')).mockResolvedValue({ subscription_templates: [template] })
    await render(<SubscriptionTemplatesPanel client={{ request }} notify={notify} />)
    expect(host.querySelector('[role="alert"]')?.textContent).toBe('网络不可用')
    await click('重新加载')
    expect(host.textContent).toContain('Mihomo')
    expect(host.querySelector('details')?.open).toBe(true)
  })

  it('invalidates stale previews, preserves failed drafts and sends expected_revision', async () => {
    const request = vi.fn(async (path: string, options?: any) => {
      if (path.endsWith('/preview')) return { content: 'preview content' }
      if (options?.method === 'PUT') throw new Error('修订已变化，请重新加载后合并')
      return { subscription_templates: [template] }
    })
    await render(<SubscriptionTemplatesPanel client={{ request }} notify={notify} />)
    await act(async () => host.querySelector<HTMLButtonElement>('.subscription-template-row')!.click())
    expect(button('保存').disabled).toBe(true)
    expect(document.body.textContent).toContain('不会自动覆盖')
    await click('预览')
    expect(document.querySelector('pre')?.textContent).toBe('preview content')
    await fill('Mihomo 模板内容', 'changed')
    expect(document.querySelector('pre')).toBeNull()
    await click('保存')
    expect(request).toHaveBeenCalledWith('/subscription-templates/mihomo', { method: 'PUT', body: JSON.stringify({ content: 'changed', expected_revision: 7 }) })
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('修订已变化')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('changed')
    confirm.mockResolvedValueOnce(false)
    await click('关闭')
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ title: '放弃未保存的修改？' }))
    expect(document.querySelector('textarea')).not.toBeNull()
  })

  it('restores only after confirmation and adopts the returned revision', async () => {
    const restored = { ...template, content: 'system', source: 'builtin', revision: 8, builtin_updated: false }
    const request = vi.fn(async (path: string) => path.endsWith('/reset') ? { subscription_template: restored } : { subscription_templates: [template] })
    await render(<SubscriptionTemplatesPanel client={{ request }} notify={notify} />)
    await act(async () => host.querySelector<HTMLButtonElement>('.subscription-template-row')!.click())
    confirm.mockResolvedValueOnce(false)
    await click('恢复系统默认')
    expect(request).not.toHaveBeenCalledWith('/subscription-templates/mihomo/reset', expect.anything())
    await click('恢复系统默认')
    expect(request).toHaveBeenCalledWith('/subscription-templates/mihomo/reset', { method: 'POST', body: '{}' })
    expect(document.querySelector('textarea')?.value).toBe('system')
    expect(document.querySelector('.subscription-template-editor')?.textContent).toContain('版本 8')
    expect(document.querySelector('.subscription-template-editor')?.textContent).not.toContain('恢复系统默认')
  })
})
