// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RemoteAccessSettings } from './RemoteAccessSettings'

describe('RemoteAccessSettings', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('defaults remote control and WebSSH confirmation on while MCP control stays off', () => {
    act(() => root.render(<RemoteAccessSettings data={{ settings: {}, servers: [] }} client={{ request: vi.fn() }} load={vi.fn()} notify={vi.fn()} />))

    expect(container.querySelector<HTMLInputElement>('input[aria-label="启用远程控制"]')?.checked).toBe(true)
    expect(container.querySelector<HTMLInputElement>('input[aria-label="启用 MCP 控制"]')?.checked).toBe(false)
    expect(container.querySelector<HTMLInputElement>('input[aria-label="打开 WebSSH 前确认密码"]')?.checked).toBe(true)
    expect(container.textContent).not.toContain('Structured Exec')
    expect(container.textContent).not.toContain('Raw Shell')
  })

  it('enables MCP control', async () => {
    const request = vi.fn(async () => ({}))
    act(() => root.render(<RemoteAccessSettings
      data={{ settings: { remote_terminal_enabled: false, mcp_enabled: false } }}
      client={{ request }} load={vi.fn(async () => undefined)} notify={vi.fn()}
    />))

    await act(async () => container.querySelector<HTMLInputElement>('input[aria-label="启用 MCP 远程控制"]')?.click())

    expect(request).toHaveBeenCalledWith('/settings', {
      method: 'POST',
      body: JSON.stringify({ mcp_enabled: true }),
    })
  })

  it('loads server policies and applies a bulk remote-control change', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (!init) return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: false } } }
      return { remote_access: { server: { remote_terminal_enabled: false, mcp_enabled: false } } }
    })
    act(() => root.render(<RemoteAccessSettings data={{ settings: {}, servers: [{ id: 7, name: '上海节点', status: 'online' }] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
    })
    const select = document.querySelector<HTMLInputElement>('input[aria-label="选择 上海节点"]')
    expect(select).not.toBeNull()
    act(() => select?.click())
    await act(async () => {
      Array.from(document.querySelectorAll('button')).find(button => button.textContent === '关闭远程')?.click()
      await Promise.resolve()
    })

    expect(request).toHaveBeenCalledWith('/servers/7/remote-access', {
      method: 'PATCH',
      body: JSON.stringify({ remote_terminal_enabled: false }),
    })
  })
})
