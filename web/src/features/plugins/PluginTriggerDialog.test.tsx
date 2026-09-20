// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { PluginTriggerDialog } from './PluginTriggerDialog'
import type { Plugin } from './types'

let root: Root
let container: HTMLDivElement
beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container)
})
afterEach(() => { act(() => root.unmount()); container.remove() })
async function change(element: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement, value: string) {
  await act(async () => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(prototype, 'value')!.set!.call(element, value)
    element.dispatchEvent(new Event(element instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }))
  })
}
async function submit() { await act(async () => { document.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) }) }
const plugins = [{ id: 1, name: '第一插件' }, { id: 2, name: '第二插件' }] as Plugin[]
function requestMock() {
  return vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST') return { trigger: { id: 1 } }
    const id = Number(path.split('/').pop())
    return { revisions: [
      { id: id * 10, plugin_id: id, revision_number: 1, status: 'published' },
      { id: id * 10 + 1, plugin_id: id, revision_number: 2, status: 'published' },
      { id: id * 10 + 2, plugin_id: id, revision_number: 3, status: 'draft' },
    ], published: { id: id * 10 + 1 } }
  })
}
async function render(request = requestMock(), onSaved = vi.fn()) {
  await act(async () => { root.render(<PluginTriggerDialog plugins={plugins} servers={[{ id: 8, name: '节点八' }]} request={request} onClose={vi.fn()} onSaved={onSaved} />) })
  return { request, onSaved }
}
it('requires explicit plugin and fixed revision, then saves the second plugin’s older published revision', async () => {
  const { request, onSaved } = await render()
  const selects = document.querySelectorAll('select')
  expect(selects[0].value).toBe(''); expect(request).not.toHaveBeenCalled()
  await change(selects[0], '2')
  expect(selects[1].value).toBe('')
  expect([...selects[1].options].map(o => o.value)).toEqual(['', '20', '21'])
  await change(selects[1], '20')
  await change(document.querySelector('input')!, '离线巡检')
  for (const checkbox of document.querySelectorAll<HTMLInputElement>('input[type=checkbox]')) await act(async () => checkbox.click())
  await change(document.querySelector('textarea')!, '{"threshold":7}')
  await submit()
  const saved = request.mock.calls.find(([, init]) => init?.method === 'POST')!
  expect(saved[0]).toBe('/plugin-triggers')
  expect(JSON.parse(String(saved[1]?.body))).toEqual({ plugin_id: 2, revision_id: 20, name: '离线巡检', kind: 'event', enabled: false, params: { threshold: 7 }, spec: { event: 'server.offline', timezone: 'UTC', sustain_seconds: 0, subject_server_ids: [8], target_server_ids: [8] } })
  expect(onSaved).toHaveBeenCalledOnce()
})
it('clears the revision on plugin change and keeps validation and server failures inside the dialog', async () => {
  const { request, onSaved } = await render()
  const selects = document.querySelectorAll('select')
  await change(selects[0], '2'); await change(selects[1], '20'); await change(selects[0], '1')
  expect(selects[1].value).toBe('')
  await change(selects[1], '10'); await change(document.querySelector('input')!, '巡检')
  await submit()
  expect(document.querySelector('[role=alert]')?.textContent).toContain('监听服务器')
  for (const checkbox of document.querySelectorAll<HTMLInputElement>('input[type=checkbox]')) await act(async () => checkbox.click())
  await change(document.querySelector('textarea')!, '[]'); await submit()
  expect(document.querySelector('[role=alert]')?.textContent).toContain('JSON 对象')
  expect(request.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
  await change(document.querySelector('textarea')!, '{}')
  request.mockRejectedValueOnce(new Error('授权不足'))
  await submit()
  expect(document.querySelector('[role=alert]')?.textContent).toBe('授权不足')
  expect(onSaved).not.toHaveBeenCalled()
  expect(selects[1].value).toBe('10')
})
