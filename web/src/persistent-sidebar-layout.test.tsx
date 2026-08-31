// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import path from 'node:path'
import { Dialog } from './components/ui/dialog'

const stylesheet = readFileSync(path.resolve(__dirname, 'style.css'), 'utf8')

describe('Persistent Sidebar Layout CSS contracts', () => {
  it('defines the required layout tokens in :root', () => {
    expect(stylesheet).toMatch(/--sidebar-width:\s*248px/)
    expect(stylesheet).toMatch(/--sidebar-collapsed-width:\s*72px/)
    expect(stylesheet).toMatch(/--app-viewport-height:\s*100dvh/)
    expect(stylesheet).toMatch(/--app-sticky-header-clearance:\s*76px/)
    expect(stylesheet).toMatch(/--settings-sidebar-width:\s*minmax\(176px,\s*208px\)/)
    expect(stylesheet).toMatch(/--layout-scrollbar-gutter:\s*stable/)
  })

  it('configures desktop .app as a fixed 100dvh viewport with hidden overflow', () => {
    expect(stylesheet).toMatch(/\.app\s*\{[^}]*height:\s*var\(--app-viewport-height,\s*100dvh\)[^}]*min-height:\s*0[^}]*overflow:\s*hidden/s)
  })

  it('configures desktop .sidebar as an inherited-height flex column with non-scrolling brand/footer and scrollable nav-list', () => {
    expect(stylesheet).toMatch(/\.sidebar\s*\{[^}]*height:\s*100%[^}]*min-height:\s*0[^}]*max-height:\s*100%[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*overflow:\s*hidden/s)
    expect(stylesheet).toMatch(/\.brand\s*\{[^}]*flex-shrink:\s*0/s)
    expect(stylesheet).toMatch(/\.nav-list\s*\{[^}]*flex:\s*1 1 0[^}]*min-height:\s*0[^}]*overflow-y:\s*auto/s)
    expect(stylesheet).toMatch(/\.sidebar-footer\s*\{[^}]*flex-shrink:\s*0/s)
  })

  it('configures desktop .main as the primary scroll container with stable scrollbar gutter and contain overscroll', () => {
    expect(stylesheet).toMatch(/\.main\s*\{[^}]*height:\s*100%[^}]*min-height:\s*0[^}]*overflow-y:\s*auto[^}]*overscroll-behavior-y:\s*contain[^}]*scrollbar-gutter:\s*var\(--layout-scrollbar-gutter,\s*stable\)/s)
  })

  it('configures settings sidebar as a persistent secondary sidebar under sticky header clearance with bounded scrolling tabs', () => {
    expect(stylesheet).toMatch(/\.settings-shell\s*\{[^}]*grid-template-columns:\s*var\(--settings-sidebar-width,\s*minmax\(176px,\s*208px\)\)\s*minmax\(0,\s*1fr\)/s)
    expect(stylesheet).toMatch(/\.settings-sidebar\s*\{[^}]*position:\s*sticky[^}]*top:\s*var\(--app-sticky-header-clearance,\s*76px\)[^}]*max-height:\s*calc\(100dvh\s*-\s*var\(--app-sticky-header-clearance,\s*76px\)\s*-\s*32px\)/s)
    expect(stylesheet).toMatch(/\.settings-sidebar\s+\.settings-tabs\s*\{[^}]*overflow-y:\s*auto[^}]*min-height:\s*0/s)
  })

  it('resets mobile app and main to natural page scrolling below 901px', () => {
    expect(stylesheet).toMatch(/\.app\s*\{[^}]*height:\s*auto[^}]*overflow-y:\s*visible/s)
    expect(stylesheet).toMatch(/\.main\s*\{[^}]*height:\s*auto[^}]*overflow-y:\s*visible[^}]*scrollbar-gutter:\s*auto/s)
  })

  it('resets mobile settings navigation to horizontal scrolling tabs below 901px', () => {
    expect(stylesheet).toMatch(/\.settings-sidebar\s*\{[^}]*position:\s*static[^}]*max-height:\s*none/s)
    expect(stylesheet).toMatch(/\.settings-sidebar\s+\.settings-tabs\s*\{[^}]*flex-direction:\s*row[^}]*overflow-x:\s*auto[^}]*overflow-y:\s*visible/s)
  })

  it('locks .main scroll in CSS when dialog backdrops or modal roots are present', () => {
    expect(stylesheet).toMatch(/body:has\(\.dialog-backdrop\)\s+\.main[^{]*\{[^}]*overflow-y:\s*hidden/s)
  })
})

describe('ModalLayer runtime scroll lock on .main', () => {
  let container: HTMLDivElement
  let root: Root
  let mainElement: HTMLDivElement

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    mainElement = document.createElement('div')
    mainElement.className = 'main'
    mainElement.style.overflowY = 'auto'
    document.body.appendChild(mainElement)

    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
    mainElement.remove()
    document.body.querySelectorAll('.dialog-layer').forEach(element => element.remove())
    document.body.style.overflow = ''
    document.body.style.paddingRight = ''
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('locks .main overflowY when dialog opens and restores original overflowY when dialog closes', async () => {
    expect(mainElement.style.overflowY).toBe('auto')

    await act(async () => {
      root.render(
        <Dialog isOpen={true} onClose={() => undefined} title="测试对话框">
          <p>对话框内容</p>
        </Dialog>
      )
    })

    expect(document.body.style.overflow).toBe('hidden')
    expect(mainElement.style.overflowY).toBe('hidden')

    await act(async () => {
      root.render(
        <Dialog isOpen={false} onClose={() => undefined} title="测试对话框">
          <p>对话框内容</p>
        </Dialog>
      )
    })

    // Wait for exit animation
    await act(async () => {
      await new Promise(resolve => window.setTimeout(resolve, 450))
    })

    expect(document.body.style.overflow).toBe('')
    expect(mainElement.style.overflowY).toBe('auto')
  })
})
