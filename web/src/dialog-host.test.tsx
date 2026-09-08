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
})
