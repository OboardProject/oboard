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

  it('keeps WebSSH confirmation on the settings page', () => {
    act(() => root.render(<RemoteAccessSettings data={{ settings: {}, servers: [] }} client={{ request: vi.fn() }} load={vi.fn()} notify={vi.fn()} />))

    expect(container.querySelector<HTMLInputElement>('input[aria-label="打开 WebSSH 前确认密码"]')?.checked).toBe(true)
    expect(container.querySelector<HTMLInputElement>('input[aria-label="全局启用 Web 远程终端"]')).toBeNull()
    expect(container.textContent).not.toContain('Structured Exec')
  })

  it('saves global MCP control from the server dialog', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/settings' && init?.method === 'POST') return {}
      return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: false } } }
    })
    act(() => root.render(<RemoteAccessSettings
      data={{ settings: { remote_terminal_enabled: true, mcp_enabled: false }, servers: [{ id: 7, name: '上海节点', status: 'online' }] }}
      client={{ request }} load={vi.fn(async () => undefined)} notify={vi.fn()}
    />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
    })

    await act(async () => {
      document.querySelector<HTMLInputElement>('input[aria-label="全局启用 MCP 远程控制"]')?.click()
      await Promise.resolve()
    })

    expect(request).toHaveBeenCalledWith('/settings', {
      method: 'POST',
      body: JSON.stringify({ mcp_enabled: true }),
    })
  })

  it('disables per-server remote switches while global terminal is enabled', async () => {
    const request = vi.fn(async () => ({ remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: false } } }))
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: false }, servers: [{ id: 7, name: '上海节点', status: 'online' }] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    const remoteSwitch = document.querySelector<HTMLInputElement>('input[aria-label="上海节点远程"]')
    expect(remoteSwitch?.disabled).toBe(true)
    expect(remoteSwitch?.checked).toBe(true)
  })

  it('loads server policies and applies a bulk remote-control change when global terminal is off', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (!init) return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: false } } }
      return { remote_access: { server: { remote_terminal_enabled: false, mcp_enabled: false } } }
    })
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: false, mcp_enabled: false }, servers: [{ id: 7, name: '上海节点', status: 'online' }] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
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
