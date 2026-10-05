// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { DeleteServerDialog } from './main'
import type { Server } from './components/proxy-path/types'

let host: HTMLDivElement
let root: Root
const server = { id: 1, name: '测试服务器' } as Server

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
})

afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  vi.unstubAllGlobals()
})

it('uses a compact confirmation and blocks dismissal and repeat submission while deleting', async () => {
  const onCancel = vi.fn()
  const onSubmit = vi.fn()
  await act(async () => root.render(<DeleteServerDialog server={server} busy onCancel={onCancel} onSubmit={onSubmit} />))
  expect(document.querySelector('.dialog-layer')?.getAttribute('data-surface-kind')).toBe('compact')
  expect(document.querySelector('.server-delete-dialog')?.getAttribute('aria-labelledby')).toBe('server-delete-title')
  const submit = document.querySelector<HTMLButtonElement>('.danger-button')!
  expect(submit.textContent).toBe('删除中…')
  expect(submit.querySelector('.spin')).not.toBeNull()
  expect(submit.disabled).toBe(true)
  expect(document.querySelector<HTMLInputElement>('input')?.disabled).toBe(true)
  await act(async () => {
    submit.click()
    document.querySelector<HTMLButtonElement>('.dialog-close')!.click()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    document.querySelector<HTMLElement>('.dialog-backdrop')?.click()
  })
  expect(onCancel).not.toHaveBeenCalled()
  expect(onSubmit).not.toHaveBeenCalled()
})

it('keeps the failure in the dialog and allows retry with the selected uninstall option', async () => {
  const onSubmit = vi.fn()
  await act(async () => root.render(<DeleteServerDialog server={server} busy={false} error="删除失败，请重试" onCancel={vi.fn()} onSubmit={onSubmit} />))
  expect(document.querySelector('[role="alert"]')?.textContent).toBe('删除失败，请重试')
  await act(async () => document.querySelector<HTMLInputElement>('input')!.click())
  await act(async () => document.querySelector<HTMLButtonElement>('.danger-button')!.click())
  expect(onSubmit).toHaveBeenCalledExactlyOnceWith(true)
})
