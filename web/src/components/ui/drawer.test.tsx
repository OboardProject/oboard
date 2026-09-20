// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { parse, type Rule } from 'postcss'
import { readFileSync } from 'node:fs'
import { Dialog } from './dialog'
import { MotionDialogPanel } from './motion'
import { ModalSurface } from './modal-layer'
import { Dropdown, DropdownTrigger, DropdownContent, DropdownItem } from './dropdown-menu'
const drawerCSS = readFileSync('src/components/ui/drawer.css', 'utf8')
const baseCSS = readFileSync('src/style.css', 'utf8')

async function settle(ms = 30) {
  await act(async () => { await new Promise(resolve => window.setTimeout(resolve, ms)) })
}

function Menu() {
  return <Dropdown>
    <DropdownTrigger><button type="button">菜单</button></DropdownTrigger>
    <DropdownContent><DropdownItem>操作</DropdownItem></DropdownContent>
  </Dropdown>
}

describe('explicit right drawer', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.append(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    await settle()
    container.remove()
    vi.unstubAllGlobals()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('preserves dialog semantics and forwards placement and width through both adapters', async () => {
    await act(async () => root.render(
      <Dialog isOpen placement="right" drawerSize="wide" title="编辑" onClose={() => {}} footer={<button>保存</button>}>
        <input autoFocus aria-label="名称" />
      </Dialog>,
    ))
    const panel = document.querySelector<HTMLElement>('[role="dialog"]')!
    expect(panel.getAttribute('aria-modal')).toBe('true')
    expect(document.getElementById(panel.getAttribute('aria-labelledby')!)?.textContent).toBe('编辑')
    expect(panel.dataset.drawerSize).toBe('wide')
    expect(panel.closest<HTMLElement>('.dialog-layer')?.dataset.modalPlacement).toBe('right')
    expect(panel.querySelector('.dialog-chrome-head')).not.toBeNull()
    expect(panel.querySelector('.dialog-chrome-body')).not.toBeNull()
    expect(panel.querySelector('.dialog-chrome-foot')).not.toBeNull()

    await act(async () => root.render(
      <MotionDialogPanel placement="right" onCancel={() => {}} ariaLabel="编辑">
        <div className="dialog-body"><input aria-label="名称" /></div>
      </MotionDialogPanel>,
    ))
    expect(document.querySelector('[data-modal-placement="right"] [role="dialog"]')?.getAttribute('data-drawer-size')).toBe('compact')
    await act(async () => root.render(
      <MotionDialogPanel placement="right" drawerSize="wide" onCancel={() => {}}><div className="dialog-body" /></MotionDialogPanel>,
    ))
    expect(document.querySelector('[role="dialog"]')?.getAttribute('data-drawer-size')).toBe('wide')
  })

  it('leaves default surfaces centered and ignores drawer sizing unless explicitly placed', async () => {
    for (const surface of [
      <ModalSurface onClose={() => {}} panelClassName="dialog" drawerSize="wide"><button>操作</button></ModalSurface>,
      <Dialog isOpen onClose={() => {}}><button>操作</button></Dialog>,
      <MotionDialogPanel onCancel={() => {}}><button>操作</button></MotionDialogPanel>,
    ]) {
      await act(async () => root.render(surface))
      expect(document.querySelector('.dialog-layer')?.getAttribute('data-modal-placement')).toBe('center')
      expect(document.querySelector('[role="dialog"]')?.hasAttribute('data-drawer-size')).toBe(false)
    }
  })

  it('dismisses its owned portaled menu before the drawer and traps Tab', async () => {
    const close = vi.fn()
    await act(async () => root.render(<Dialog isOpen placement="right" onClose={close} title="编辑"><Menu /><button>保存</button></Dialog>))
    await settle()
    const trigger = document.querySelector<HTMLButtonElement>('[aria-haspopup="menu"]')!
    await act(async () => trigger.click())
    const menu = document.querySelector<HTMLElement>('[data-popover-active="true"] [role="menu"]')!
    expect(menu).not.toBeNull()
    act(() => menu.querySelector<HTMLElement>('button')!.focus())
    expect(menu.contains(document.activeElement)).toBe(true)
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(close).not.toHaveBeenCalled()
    expect(document.querySelector('[role="menu"]')).toBeNull()
    expect(document.activeElement).toBe(trigger)

    const buttons = document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')
    act(() => buttons[buttons.length - 1].focus())
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true })))
    expect(document.activeElement).toBe(buttons[0])
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true })))
    expect(document.activeElement).toBe(buttons[buttons.length - 1])
    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('shares the stack with centered dialogs, suspends lower menus, and restores focus and scroll', async () => {
    const main = document.createElement('main')
    main.className = 'main'
    main.style.overflowY = 'auto'
    const trigger = document.createElement('button')
    document.body.style.overflow = 'scroll'
    document.body.style.paddingRight = '7px'
    const parentClose = vi.fn()
    const drawerClose = vi.fn()
    const render = (parentOpen: boolean, drawerOpen: boolean) => root.render(
      <Dialog isOpen={parentOpen} onClose={parentClose} title="父窗口">
        <Menu />
        <Dialog isOpen={drawerOpen} placement="right" onClose={drawerClose} title="编辑"><input autoFocus aria-label="名称" /></Dialog>
      </Dialog>,
    )
    // Keep the external focus target and main scroll container outside React's root.
    document.body.append(main, trigger)
    trigger.focus()
    try {
      await act(async () => render(true, false))
      const menuTrigger = document.querySelector<HTMLButtonElement>('[aria-haspopup="menu"]')!
      act(() => menuTrigger.focus())
      await act(async () => menuTrigger.click())
      await act(async () => render(true, true))
      await settle()
      expect(document.querySelector('[data-modal-top="true"]')?.getAttribute('data-modal-placement')).toBe('right')
      expect(document.querySelector<HTMLElement>('[data-modal-top="false"] [role="dialog"]')?.inert).toBe(true)
      expect(document.querySelector('[data-popover-layer]')?.getAttribute('data-popover-active')).toBe('false')
      expect(document.body.style.overflow).toBe('hidden')
      expect(main.style.overflowY).toBe('hidden')
      act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
      expect(drawerClose).toHaveBeenCalledTimes(1)
      expect(parentClose).not.toHaveBeenCalled()
      await act(async () => render(true, false))
      expect(document.body.style.overflow).toBe('hidden')
      await settle(420)
      await settle()
      expect(document.activeElement).toBe(menuTrigger)
      expect(document.body.style.overflow).toBe('hidden')
      await act(async () => render(false, false))
      await settle(420)
      await settle()
      expect(document.activeElement).toBe(trigger)
      expect(document.body.style.overflow).toBe('scroll')
      expect(document.body.style.paddingRight).toBe('7px')
      expect(main.style.overflowY).toBe('auto')
    } finally {
      main.remove()
      trigger.remove()
      document.body.style.overflow = ''
      document.body.style.paddingRight = ''
    }
  })

  it('uses and releases the existing visual viewport tracker for keyboard resizing', async () => {
    const viewport = Object.assign(new EventTarget(), { height: 740, offsetTop: 0 })
    vi.stubGlobal('visualViewport', viewport)
    await act(async () => root.render(<MotionDialogPanel placement="right" onCancel={() => {}}><div className="dialog-body"><input /></div></MotionDialogPanel>))
    expect(document.documentElement.style.getPropertyValue('--dialog-viewport-height')).toBe('740px')
    viewport.height = 280
    viewport.offsetTop = 35
    viewport.dispatchEvent(new Event('resize'))
    await settle()
    expect(document.documentElement.style.getPropertyValue('--dialog-viewport-height')).toBe('280px')
    expect(document.documentElement.style.getPropertyValue('--dialog-viewport-top')).toBe('35px')
    expect(document.documentElement.hasAttribute('data-dialog-compact-viewport')).toBe(true)
    await act(async () => root.render(null))
    expect(document.documentElement.style.getPropertyValue('--dialog-viewport-height')).toBe('')
    expect(document.documentElement.hasAttribute('data-dialog-compact-viewport')).toBe(false)
  })
})

describe('drawer layout CSS contract', () => {
  const sheet = parse(drawerCSS)
  function declarations(selector: string, mobile = false) {
    const values: Record<string, string> = {}
    sheet.walkRules(selector, rule => {
      if ((rule.parent?.type === 'atrule') !== mobile) return
      rule.walkDecls(decl => { values[decl.prop] = decl.value + (decl.important ? ' !important' : '') })
    })
    return values
  }
  const layer = '.dialog-layer[data-modal-placement="right"]'
  const panel = `:root ${layer} > .dialog-panel[data-drawer-size]`

  it('scopes every drawer rule to explicit placement without changing centered layout', () => {
    sheet.walkRules(rule => { expect(rule.selector).toContain('[data-modal-placement="right"]') })
    let centered: Rule | undefined
    parse(baseCSS).walkRules('.dialog-layer', rule => {
      if (rule.parent?.type === 'root') centered = rule
    })
    expect(centered?.nodes.some(node => node.type === 'decl' && node.prop === 'place-items' && node.value === 'center')).toBe(true)
    expect(declarations(layer)).toMatchObject({ padding: '0', 'place-items': 'stretch end', '--dialog-available-height': 'var(--dialog-viewport-height, 100dvh)' })
    expect(declarations(panel)).toMatchObject({ width: 'min(var(--drawer-width), 100%)', '--drawer-width': '36rem', height: 'var(--dialog-available-height)', 'border-radius': '0', background: 'var(--surface-solid)' })
    expect(declarations(`:root ${layer} > .dialog-panel[data-drawer-size="wide"]`)['--drawer-width']).toBe('52rem')
  })

  it('fills narrow viewports, respects safe areas, and scrolls only the body even with a keyboard', () => {
    let breakpoint = ''
    sheet.walkAtRules('media', rule => { breakpoint = rule.params })
    expect(breakpoint).toBe('(max-width: 720px)')
    expect(declarations(panel, true)).toMatchObject({ width: '100%', 'border-left': '0' })
    for (const edge of ['top', 'right', 'bottom', 'left']) expect(declarations(panel).padding).toContain(`env(safe-area-inset-${edge}, 0px)`)
    expect(declarations(`${layer} > .dialog-panel > :is(.dialog-head, .dialog-chrome-head, .dialog-actions, .dialog-chrome-foot)`).flex).toBe('0 0 auto')
    expect(declarations(`:root ${layer} > .dialog-panel > :is(.dialog-body, .dialog-chrome-body)`)).toMatchObject({ flex: '1 1 0', 'min-height': '0', 'overflow-y': 'auto !important', 'overflow-x': 'hidden !important' })
    expect(declarations(`:root[data-dialog-compact-viewport] ${layer} > .dialog-panel[data-drawer-size]`).overflow).toBe('hidden')
  })
})
