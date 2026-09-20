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
    container.id = 'root'
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
    document.body.querySelectorAll('.dialog-layer').forEach(element => element.remove())
    document.body.style.overflow = ''
    document.body.style.paddingRight = ''
    vi.unstubAllGlobals()
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
    expect(document.querySelector<HTMLElement>('[role="dialog"]')?.inert).toBe(true)
    expect(document.querySelector('[role="dialog"]')?.getAttribute('aria-modal')).toBeNull()
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(onClose).toHaveBeenCalledTimes(1)

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

  it('dismisses a portaled menu before its containing dialog', async () => {
    const { Dropdown, DropdownTrigger, DropdownContent, DropdownItem } = await import('./dropdown-menu')
    const onClose = vi.fn()
    await act(async () => root.render(<Dialog isOpen onClose={onClose} title="设置">
      <Dropdown><DropdownTrigger><button>更多设置</button></DropdownTrigger>
        <DropdownContent><DropdownItem>菜单操作</DropdownItem></DropdownContent>
      </Dropdown>
    </Dialog>))
    const trigger = Array.from(document.querySelectorAll('button')).find(button => button.textContent === '更多设置')!
    await act(async () => trigger.click())
    expect(document.querySelector('[data-popover-active="true"] [role="menu"]')).not.toBeNull()
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(onClose).not.toHaveBeenCalled()
    expect(document.querySelector('[role="menu"]')).toBeNull()
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('keeps the application inert until the last surface has exited', async () => {
    container.inert = false
    await act(async () => root.render(<Dialog isOpen onClose={() => {}} title="编辑"><input /></Dialog>))
    expect(container.inert).toBe(true)
    expect(document.querySelector<HTMLElement>('[role="dialog"]')?.closest('#root')).toBeNull()
    act(() => root.render(<Dialog isOpen={false} onClose={() => {}}><input /></Dialog>))
    expect(container.inert).toBe(true)
    await settle(EXIT_WAIT_MS)
    expect(container.inert).toBe(false)
  })

  it('does not suppress pinch zoom or close while an IME composition is active', async () => {
    const onClose = vi.fn()
    await act(async () => root.render(<Dialog isOpen onClose={onClose}><input autoFocus /></Dialog>))
    const panel = document.querySelector<HTMLElement>('[role="dialog"]')!
    const gesture = new Event('gesturestart', { bubbles: true, cancelable: true })
    const touch = new Event('touchstart', { bubbles: true, cancelable: true })
    Object.defineProperty(touch, 'touches', { value: [{}, {}] })
    panel.dispatchEvent(gesture)
    panel.dispatchEvent(touch)
    expect(gesture.defaultPrevented).toBe(false)
    expect(touch.defaultPrevented).toBe(false)
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true })))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('focuses the panel on touch devices without opening the software keyboard', async () => {
    vi.stubGlobal('matchMedia', vi.fn((query: string) => ({
      matches: query === '(pointer: coarse)', media: query,
      addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {},
    })))
    await act(async () => root.render(<Dialog isOpen onClose={() => {}} title="编辑"><input aria-label="名称" /></Dialog>))
    await settle(30)
    expect(document.activeElement?.getAttribute('role')).toBe('dialog')
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

// Nested surfaces can mount in one React commit (child effects run first).
describe('nested surface ownership', () => {
  it('keeps the child above its parent and suspends the parent menu', async () => {
    const { createPopoverPortal } = await import('./modal-layer')
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    function Menu() {
      return createPopoverPortal(<div role="menu"><button>菜单操作</button></div>, document.body)
    }
    try {
      await act(async () => root.render(
        <Dialog isOpen onClose={() => {}} title="父窗口">
          <Menu />
          <Dialog isOpen onClose={() => {}} title="子窗口"><button>子操作</button></Dialog>
        </Dialog>,
      ))
      const top = document.querySelector('[data-modal-top="true"] [role="dialog"]')!
      expect(top.textContent).toContain('子窗口')
      expect(document.querySelector('[data-popover-layer]')?.getAttribute('data-popover-active')).toBe('false')
      expect(document.querySelector<HTMLElement>('[data-popover-layer]')?.hasAttribute('inert')).toBe(true)
      expect(Array.from(document.querySelectorAll('[data-modal-index]')).map(node => node.getAttribute('data-modal-index')).sort()).toEqual(['0', '1'])
    } finally {
      await act(async () => root.unmount())
      container.remove()
      ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
    }
  })
})
