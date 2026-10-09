// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DialogHost } from './main'
import type { DialogState } from './components/ui/dialog-context'

describe('DialogHost layout stability', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
    document.querySelectorAll('.dialog-layer').forEach(element => element.remove())
    document.body.style.overflow = ''
    document.body.style.paddingRight = ''
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('keeps actions outside the scrollable body and preserves compact geometry between confirmation steps', async () => {
    const confirmDialog: DialogState = {
      id: 1,
      kind: 'confirm',
      title: '强制结束更新任务？',
      message: '确认说明',
      confirmText: '继续确认',
      tone: 'danger',
      resolve: vi.fn(),
    }
    const promptDialog: DialogState = {
      id: 2,
      kind: 'prompt',
      title: '再次确认强制结束',
      message: '请输入确认短语。',
      placeholder: '强制结束更新任务',
      confirmText: '强制结束任务',
      tone: 'danger',
      resolve: vi.fn(),
    }

    await act(async () => root.render(<DialogHost dialog={confirmDialog} onClose={() => undefined} />))

    const initialPanel = document.querySelector<HTMLElement>('[role="dialog"]')!
    const initialAction = Array.from(initialPanel.querySelectorAll('button')).find(button => button.textContent === '继续确认')!
    expect(initialPanel.classList.contains('dialog-host-compact')).toBe(true)
    expect(initialAction.closest('.dialog-chrome-body')).toBeNull()
    expect(initialAction.closest('.dialog-chrome-foot')).not.toBeNull()

    await act(async () => root.render(<DialogHost dialog={promptDialog} onClose={() => undefined} />))

    const promptPanel = document.querySelector<HTMLElement>('[role="dialog"]')!
    const promptAction = Array.from(promptPanel.querySelectorAll('button')).find(button => button.textContent === '强制结束任务')!
    expect(promptPanel).toBe(initialPanel)
    expect(promptPanel.classList.contains('dialog-host-compact')).toBe(true)
    expect(promptPanel.classList.contains('dialog-host-prompt')).toBe(true)
    expect(promptAction.closest('.dialog-chrome-body')).toBeNull()
    expect(promptAction.closest('.dialog-chrome-foot')).not.toBeNull()
  })
  it('waits for deletion, blocks duplicate submission and dismissal, then closes on success', async () => {
    let finish!: () => void
    const operation = vi.fn(() => new Promise<void>(resolve => { finish = resolve }))
    const resolve = vi.fn()
    const onClose = vi.fn()
    const dialog: DialogState = { id: 3, kind: 'confirm', title: '删除入口', confirmText: '删除', pendingText: '删除中…', onConfirm: operation, resolve }
    await act(async () => root.render(<DialogHost dialog={dialog} onClose={onClose} />))
    const button = Array.from(document.querySelectorAll('button')).find(item => item.textContent === '删除')!
    await act(async () => { button.click(); button.click() })
    expect(operation).toHaveBeenCalledTimes(1)
    expect(button.disabled).toBe(true)
    expect(button.getAttribute('aria-busy')).toBe('true')
    expect(button.querySelector('.spin')).not.toBeNull()
    expect(button.textContent).toContain('删除中')
    await act(async () => {
      document.querySelector<HTMLButtonElement>('[aria-label="关闭"]')!.click()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(resolve).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
    await act(async () => finish())
    expect(resolve).toHaveBeenCalledWith(true)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('keeps a failed deletion open with an error and allows retry', async () => {
    const operation = vi.fn().mockRejectedValueOnce(new Error('删除失败，请重试')).mockResolvedValueOnce(undefined)
    const resolve = vi.fn()
    const onClose = vi.fn()
    const dialog: DialogState = { id: 4, kind: 'confirm', title: '删除区块', confirmText: '删除', onConfirm: operation, resolve }
    await act(async () => root.render(<DialogHost dialog={dialog} onClose={onClose} />))
    const button = Array.from(document.querySelectorAll('button')).find(item => item.textContent === '删除')!
    await act(async () => button.click())
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('删除失败')
    expect(onClose).not.toHaveBeenCalled()
    expect(button.disabled).toBe(false)
    await act(async () => button.click())
    expect(operation).toHaveBeenCalledTimes(2)
    expect(resolve).toHaveBeenCalledWith(true)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

})
