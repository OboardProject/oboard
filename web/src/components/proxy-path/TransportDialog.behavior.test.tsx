// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TransportDialog, type ProxyPathReusePreview, type ProxyPathReuseRequest } from './TransportDialog'

vi.mock('../ui/motion', () => ({
  MotionDialogPanel: ({ children }: { children: React.ReactNode }) => <section>{children}</section>,
}))

describe('TransportDialog preview', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.useRealTimers()
    vi.restoreAllMocks()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('keeps an in-flight topology preview across parent rerenders', async () => {
    let resolvePreview!: (preview: ProxyPathReusePreview) => void
    const firstPreview = vi.fn((_request: ProxyPathReuseRequest) => new Promise<ProxyPathReusePreview>(resolve => { resolvePreview = resolve }))
    const refreshedPreview = vi.fn(async (_request: ProxyPathReuseRequest): Promise<ProxyPathReusePreview> => previewResult)
    const target = {
      sourceLabel: '入口 A',
      targetLabel: '服务器 B',
      targetServerID: 20,
      targetInboundID: 30,
      sources: [{ inbound_id: 10 }],
    }
    const renderDialog = (onPreview: (request: ProxyPathReuseRequest) => Promise<ProxyPathReusePreview>) => (
      <TransportDialog
        target={target}
        currentMode="singbox"
        chainMethods={[]}
        onPreview={onPreview}
        onCancel={() => {}}
        onSubmit={() => {}}
      />
    )

    act(() => root.render(renderDialog(firstPreview)))
    await act(async () => { vi.advanceTimersByTime(120); await Promise.resolve() })
    expect(firstPreview).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('正在检查拓扑')

    act(() => root.render(renderDialog(refreshedPreview)))
    await act(async () => { vi.advanceTimersByTime(120); await Promise.resolve() })
    expect(firstPreview).toHaveBeenCalledTimes(1)
    expect(refreshedPreview).not.toHaveBeenCalled()

    await act(async () => { resolvePreview(previewResult); await Promise.resolve() })
    expect(container.textContent).toContain('拓扑检查通过')
    expect(container.textContent).not.toContain('正在检查拓扑')
  })

  it('keeps the mode settings region stable while switching transport modes', () => {
    act(() => root.render(
      <TransportDialog
        target={{ sourceLabel: '入口 A', targetLabel: '服务器 B' }}
        currentMode="singbox"
        chainMethods={[]}
        onCancel={() => {}}
        onSubmit={() => {}}
      />,
    ))

    const modeButtons = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]'))
    const settings = container.querySelector('.transport-mode-settings')
    expect(modeButtons).toHaveLength(3)
    expect(modeButtons[0].getAttribute('aria-checked')).toBe('true')
    expect(settings).not.toBeNull()

    act(() => modeButtons[2].click())
    expect(modeButtons[2].getAttribute('aria-checked')).toBe('true')
    expect(container.querySelector('.transport-mode-settings')).toBe(settings)
    expect(settings?.textContent).toContain('隧道类型')

    act(() => modeButtons[1].click())
    expect(modeButtons[1].getAttribute('aria-checked')).toBe('true')
    expect(container.querySelector('.transport-mode-settings')).toBe(settings)
    expect(settings?.textContent).toBe('')
  })
})

const previewResult: ProxyPathReusePreview = {
  target_options: [{
    kind: 'existing',
    inbound_id: 30,
    protocol: 'vless',
    label: '已有入口',
    visibility: 'existing_visible',
    active_reuse_count: 0,
    eligible: true,
  }],
  branch_options: [],
  valid: true,
  result_path_count: 1,
}
