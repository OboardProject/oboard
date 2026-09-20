// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { lazyDialog } from './lazy-surface'

let host: HTMLDivElement
let root: Root
beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
})
afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  vi.restoreAllMocks()
})
it('keeps a slow-loading dialog accessible and cancellable', async () => {
  const close = vi.fn()
  const Deferred = lazyDialog(() => new Promise<{ default: React.ComponentType<any> }>(() => {}), '用户套餐')
  await act(async () => root.render(<Deferred onClose={close} />))
  const dialog = document.querySelector('[role="dialog"]')!
  expect(dialog.getAttribute('aria-modal')).toBe('true')
  expect(dialog.querySelector('[role="status"]')).not.toBeNull()
  await act(async () => (dialog.querySelector('[aria-label="关闭"]') as HTMLButtonElement).click())
  expect(close).toHaveBeenCalledOnce()
})
it('keeps the close action available after a chunk download fails', async () => {
  vi.spyOn(console, 'error').mockImplementation(() => {})
  const close = vi.fn()
  const Failed = lazyDialog(() => Promise.reject(new Error('chunk unavailable')), '远程终端')
  await act(async () => root.render(<Failed onClose={close} />))
  const dialog = document.querySelector('[role="dialog"]')!
  expect(dialog.querySelector('[role="alert"]')?.textContent).toContain('界面加载失败')
  await act(async () => (dialog.querySelector('[aria-label="关闭"]') as HTMLButtonElement).click())
  expect(close).toHaveBeenCalledOnce()
})
