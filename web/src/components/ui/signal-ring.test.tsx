// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SignalRing } from './SignalRing'
import { SignalEmptyState } from './SignalEmptyState'

describe('Signal Brand Elements', () => {
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
  })

  it('renders SignalRing with accessible label and terracotta accent segment', async () => {
    await act(async () => {
      root.render(<SignalRing size={20} ariaLabel="Oboard 标识" />)
    })
    const svg = container.querySelector('svg.signal-ring')
    expect(svg).not.toBeNull()
    expect(svg?.getAttribute('width')).toBe('20')
    expect(svg?.getAttribute('aria-label')).toBe('Oboard 标识')
    expect(container.querySelector('.signal-ring-base')).not.toBeNull()
    expect(container.querySelector('.signal-ring-accent')).not.toBeNull()
  })

  it('renders SignalEmptyState with geometric diagram, title, description, and action button', async () => {
    await act(async () => {
      root.render(
        <SignalEmptyState
          title="暂无服务器"
          description="通过一键脚本快速接入节点，接入后即可在此编排链路。"
          action={<button type="button">接入首台服务器</button>}
        />
      )
    })
    expect(container.textContent).toContain('暂无服务器')
    expect(container.textContent).toContain('通过一键脚本快速接入节点')
    expect(container.querySelector('button')?.textContent).toBe('接入首台服务器')
    expect(container.querySelector('.signal-empty-graphic svg')).not.toBeNull()
  })
})
