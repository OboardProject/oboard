// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { InspectorPanel } from './inspector-panel'

describe('InspectorPanel non-modal contextual panel', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    container = document.createElement('div')
    document.body.append(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
    vi.unstubAllGlobals()
  })

  it('renders non-modal region without trapping background focus or setting aria-modal', async () => {
    const onClose = vi.fn()
    await act(async () => {
      root.render(
        <InspectorPanel
          isOpen
          onClose={onClose}
          title="US-West-01 详情"
          badge={<span className="badge">在线</span>}
        >
          <div>面板内容</div>
        </InspectorPanel>
      )
    })

    const aside = container.querySelector('aside.inspector-panel')
    expect(aside).not.toBeNull()
    expect(aside?.getAttribute('role')).toBe('region')
    expect(aside?.getAttribute('aria-modal')).toBeNull()
    expect(container.textContent).toContain('US-West-01 详情')
    expect(container.textContent).toContain('在线')
    expect(container.textContent).toContain('面板内容')
  })

  it('closes on Escape key press or close button click', async () => {
    const onClose = vi.fn()
    await act(async () => {
      root.render(
        <InspectorPanel
          isOpen
          onClose={onClose}
          title="服务器概览"
        >
          <div>内容</div>
        </InspectorPanel>
      )
    })

    const closeBtn = container.querySelector<HTMLButtonElement>('.inspector-close')!
    await act(async () => closeBtn.click())
    expect(onClose).toHaveBeenCalledTimes(1)

    // Test Escape key
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(onClose).toHaveBeenCalledTimes(2)
  })
})
