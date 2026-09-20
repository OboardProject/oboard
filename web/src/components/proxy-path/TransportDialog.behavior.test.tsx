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
        target={{ ...target, sources: target.sources.map(source => ({ ...source })) }}
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

  const button = (text: string) => Array.from(container.querySelectorAll('button')).find(node => node.textContent === text)!
  const flushPreview = async () => { await act(async () => { vi.advanceTimersByTime(120) }) }
  const reuseTarget = { sourceLabel: 'A', targetLabel: 'B', targetServerID: 20, targetInboundID: 30, sources: [{ inbound_id: 10 }] }

  it('requires a matching preview and ignores an obsolete response', async () => {
    const pending: Array<(value: ProxyPathReusePreview) => void> = []
    const onPreview = vi.fn(() => new Promise<ProxyPathReusePreview>(resolve => pending.push(resolve)))
    const onSubmit = vi.fn()
    act(() => root.render(<TransportDialog target={reuseTarget} chainMethods={[]} onPreview={onPreview} onCancel={() => {}} onSubmit={onSubmit} />))
    await flushPreview()
    await act(async () => pending[0](previewResult))
    expect(button('确定').disabled).toBe(false)
    act(() => button('全部分支').click())
    expect(button('确定').disabled).toBe(true)
    await flushPreview()
    act(() => button('不复制').click())
    await flushPreview()
    await act(async () => pending[1](previewResult))
    expect(button('确定').disabled).toBe(true)
    await act(async () => pending[2](previewResult))
    expect(button('确定').disabled).toBe(false)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('retries request failures and explains invalid previews', async () => {
    const onPreview = vi.fn().mockRejectedValueOnce(new Error('网络不可用')).mockResolvedValueOnce({ ...previewResult, valid: false }).mockResolvedValue(previewResult)
    act(() => root.render(<TransportDialog target={reuseTarget} chainMethods={[]} onPreview={onPreview} onCancel={() => {}} onSubmit={() => {}} />))
    await flushPreview()
    expect(container.textContent).toContain('网络不可用')
    act(() => button('重试检查').click())
    await flushPreview()
    expect(container.textContent).toContain('拓扑检查未通过')
    expect(button('确定').disabled).toBe(true)
    act(() => button('全部分支').click())
    await flushPreview()
    expect(button('确定').disabled).toBe(false)
  })

  it('retains branch choices while asking for a single branch', async () => {
    const onPreview = vi.fn().mockResolvedValue({ ...previewResult, branch_options: [{ path_id: 7, name: '分支七', kind: 'direct', eligible: true, step_count: 0 }] })
    act(() => root.render(<TransportDialog target={reuseTarget} chainMethods={[]} onPreview={onPreview} onCancel={() => {}} onSubmit={() => {}} />))
    await flushPreview()
    act(() => button('不复制').dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })))
    expect(document.activeElement).toBe(button('单条分支'))
    expect(button('单条分支').tabIndex).toBe(0)
    expect(button('不复制').tabIndex).toBe(-1)
    expect(button('单条分支').getAttribute('aria-checked')).toBe('true')
    expect(button('不复制').getAttribute('aria-checked')).toBe('false')
    expect(container.textContent).toContain('请选择一条可复制的分支')
    expect(button('确定').disabled).toBe(true)
    act(() => container.querySelector<HTMLInputElement>('input[name="branch-path"]')!.click())
    await flushPreview()
    expect(onPreview.mock.lastCall?.[0].branch_path_id).toBe(7)
    expect(button('确定').disabled).toBe(false)
  })

  it('prevents duplicate saves and cancellation and retains save failures', async () => {
    let reject!: (error: Error) => void
    const onSubmit = vi.fn().mockImplementationOnce(() => new Promise<void>((_resolve, fail) => { reject = fail })).mockResolvedValue(undefined)
    const onCancel = vi.fn()
    act(() => root.render(<TransportDialog target={{ sourceLabel: 'A', targetLabel: 'B', editing: true }} chainMethods={[]} onCancel={onCancel} onSubmit={onSubmit} />))
    const confirm = button('确定')
    act(() => { confirm.click(); confirm.click() })
    expect(onSubmit).toHaveBeenCalledTimes(1)
    expect(button('取消').disabled).toBe(true)
    expect(container.querySelector<HTMLButtonElement>('[aria-label="关闭"]')!.disabled).toBe(true)
    act(() => button('取消').click())
    expect(onCancel).not.toHaveBeenCalled()
    await act(async () => reject(new Error('保存被拒绝')))
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('保存被拒绝')
    expect(button('取消').disabled).toBe(false)
    await act(async () => button('确定').click())
    expect(onSubmit).toHaveBeenCalledTimes(2)
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
