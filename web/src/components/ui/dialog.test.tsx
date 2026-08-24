// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { AnimatePresence } from 'motion/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dialog } from './dialog'
import { MotionDialogPanel } from './motion'

const EXIT_WAIT_MS = 420

async function settle(ms = 0) {
  await act(async () => {
    await new Promise(resolve => window.setTimeout(resolve, ms))
  })
}

describe('Dialog modal stack', () => {
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
    document.body.querySelectorAll('.dialog-layer').forEach(element => element.remove())
    document.body.style.overflow = ''
    document.body.style.paddingRight = ''
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('focuses the requested field, handles Escape, and remains mounted for exit', async () => {
    const onClose = vi.fn()
    await act(async () => root.render(
      <Dialog isOpen onClose={onClose} title="编辑模板">
        <input autoFocus aria-label="模板名称" />
      </Dialog>,
    ))
    await settle()

    expect(document.activeElement?.getAttribute('aria-label')).toBe('模板名称')
    expect(document.querySelector('[role="dialog"]')?.getAttribute('aria-labelledby')).toBeTruthy()

    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(onClose).toHaveBeenCalledTimes(1)

    act(() => root.render(
      <Dialog isOpen={false} onClose={onClose} title="编辑模板">
        <input autoFocus aria-label="模板名称" />
      </Dialog>,
    ))
    expect(document.querySelector('.dialog-layer')).not.toBeNull()

    await settle(EXIT_WAIT_MS)
    expect(document.querySelector('.dialog-layer')).toBeNull()
  })

  it('allows only the top layer to close or receive interaction and restores lower focus', async () => {
    const lowerClose = vi.fn()
    const topClose = vi.fn()
    const trigger = document.createElement('button')
    trigger.textContent = 'trigger'
    document.body.appendChild(trigger)
    trigger.focus()
    document.body.style.overflow = 'scroll'
    document.body.style.paddingRight = '7px'

    const renderStack = (lowerOpen: boolean, topOpen: boolean) => root.render(<>
      <Dialog isOpen={lowerOpen} onClose={lowerClose} title="下层">
        <button autoFocus type="button">下层操作</button>
      </Dialog>
      <Dialog isOpen={topOpen} onClose={topClose} title="上层">
        <button autoFocus type="button">上层操作</button>
      </Dialog>
    </>)

    await act(async () => renderStack(true, false))
    await settle()
    const lowerButton = Array.from(document.querySelectorAll('button')).find(button => button.textContent === '下层操作') as HTMLButtonElement
    expect(document.activeElement).toBe(lowerButton)
    expect(document.body.style.overflow).toBe('hidden')
    expect(document.body.style.paddingRight).toBe('7px')

    await act(async () => renderStack(true, true))
    await settle(30)
    const layers = Array.from(document.querySelectorAll<HTMLElement>('.dialog-layer'))
    expect(layers).toHaveLength(2)
    expect(layers.map(layer => layer.dataset.modalIndex)).toEqual(['0', '1'])
    expect(layers[0].dataset.modalTop).toBe('false')
    expect(layers[1].dataset.modalTop).toBe('true')
    const lowerPanel = layers[0].querySelector<HTMLElement>('[role="dialog"]')!
    const topPanel = layers[1].querySelector<HTMLElement>('[role="dialog"]')!
    const topButton = Array.from(topPanel.querySelectorAll('button')).find(button => button.textContent === '上层操作') as HTMLButtonElement
    expect(document.activeElement).toBe(topButton)
    expect(lowerPanel.getAttribute('aria-hidden')).toBe('true')
    expect(lowerPanel.inert).toBe(true)
    expect(topPanel.getAttribute('aria-modal')).toBe('true')
    expect(topPanel.inert).toBe(false)

    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(lowerClose).not.toHaveBeenCalled()
    expect(topClose).toHaveBeenCalledTimes(1)

    act(() => layers[0].querySelector<HTMLElement>('.dialog-backdrop')!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })))
    expect(lowerClose).not.toHaveBeenCalled()
    act(() => layers[1].querySelector<HTMLElement>('.dialog-backdrop')!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })))
    expect(topClose).toHaveBeenCalledTimes(2)

    act(() => renderStack(true, false))
    expect(document.body.style.overflow).toBe('hidden')
    expect(document.querySelectorAll('.dialog-layer')).toHaveLength(2)
    await settle(EXIT_WAIT_MS)
    await settle(30)
    expect(document.querySelectorAll('.dialog-layer')).toHaveLength(1)
    expect(document.querySelector<HTMLElement>('.dialog-layer')?.dataset.modalTop).toBe('true')
    expect(document.activeElement).toBe(lowerButton)
    expect(document.body.style.overflow).toBe('hidden')

    act(() => renderStack(false, false))
    expect(document.body.style.overflow).toBe('hidden')
    await settle(EXIT_WAIT_MS)
    await settle(30)
    expect(document.body.style.overflow).toBe('scroll')
    expect(document.body.style.paddingRight).toBe('7px')
    expect(document.activeElement).toBe(trigger)
    trigger.remove()
  })

  it('restores the outside trigger when stacked dialogs close together', async () => {
    const trigger = document.createElement('button')
    trigger.textContent = 'outside trigger'
    document.body.appendChild(trigger)
    trigger.focus()

    const renderStack = (lowerOpen: boolean, topOpen: boolean) => root.render(<>
      <Dialog isOpen={lowerOpen} onClose={() => undefined} title="下层">
        <button autoFocus type="button">下层操作</button>
      </Dialog>
      <Dialog isOpen={topOpen} onClose={() => undefined} title="上层">
        <button autoFocus type="button">上层操作</button>
      </Dialog>
    </>)

    await act(async () => renderStack(true, false))
    await settle()
    await act(async () => renderStack(true, true))
    await settle(30)
    act(() => renderStack(false, false))
    await settle(EXIT_WAIT_MS)
    await settle(30)

    expect(document.querySelectorAll('.dialog-layer')).toHaveLength(0)
    expect(document.body.style.overflow).toBe('')
    expect(document.activeElement).toBe(trigger)
    trigger.remove()
  })

  it('keeps MotionDialogPanel mounted while its shared exit animation runs', async () => {
    const onClose = vi.fn()
    const renderPanel = (open: boolean) => root.render(
      <AnimatePresence>
        {open && <MotionDialogPanel onCancel={onClose} ariaLabel="动效弹窗"><button type="button">完成</button></MotionDialogPanel>}
      </AnimatePresence>,
    )

    await act(async () => renderPanel(true))
    await settle()
    expect(document.querySelector('[aria-label="动效弹窗"]')).not.toBeNull()

    act(() => renderPanel(false))
    expect(document.querySelector('[aria-label="动效弹窗"]')).not.toBeNull()
    await settle(EXIT_WAIT_MS)
    expect(document.querySelector('[aria-label="动效弹窗"]')).toBeNull()
  })

  it('traps forward and reverse Tab navigation inside the top dialog', async () => {
    const onClose = vi.fn()
    await act(async () => root.render(
      <Dialog isOpen onClose={onClose}>
        <button autoFocus type="button">第一项</button>
        <button type="button">最后一项</button>
      </Dialog>,
    ))
    await settle()

    const buttons = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button'))
    const first = buttons[0]
    const last = buttons[1]
    last.focus()
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true })))
    expect(document.activeElement).toBe(first)

    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true })))
    expect(document.activeElement).toBe(last)
  })
})
