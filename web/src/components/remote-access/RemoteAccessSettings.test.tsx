// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RemoteAccessSettings } from './RemoteAccessSettings'

function remoteAccessMock(path: string, init?: RequestInit, servers: Array<{ id: number; name: string; status?: string }> = [{ id: 7, name: '上海节点', status: 'online' }]) {
  if (path === '/servers' && !init) return { servers }
  if (path.startsWith('/servers/') && path.endsWith('/remote-access') && !init) {
    return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: false } } }
  }
  if (path.startsWith('/servers/') && path.endsWith('/remote-access') && init?.method === 'PATCH') {
    return { remote_access: { server: { remote_terminal_enabled: false, mcp_enabled: false } } }
  }
  if (path === '/settings' && init?.method === 'POST') return {}
  throw new Error(`unexpected request: ${path}`)
}

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

  it('loads all servers from the servers API when opening the dialog', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init, [
      { id: 7, name: '上海节点', status: 'online' },
      { id: 8, name: '北京节点', status: 'offline' },
      { id: 9, name: '广州节点', status: 'online' },
    ]))
    act(() => root.render(<RemoteAccessSettings data={{ settings: {}, servers: [{ id: 7, name: '上海节点', status: 'online' }] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(request).toHaveBeenCalledWith('/servers')
    expect(document.body.textContent).toContain('共 3 台服务器')
    expect(document.body.textContent).toContain('北京节点')
    expect(document.body.textContent).toContain('广州节点')
  })

  it('saves global MCP control from the server dialog', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings
      data={{ settings: { remote_terminal_enabled: true, mcp_enabled: false }, servers: [] }}
      client={{ request }} load={vi.fn(async () => undefined)} notify={vi.fn()}
    />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
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

  it('keeps per-server switches enabled even when global terminal is enabled', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: false }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    const remoteSwitch = document.querySelector<HTMLInputElement>('input[aria-label="上海节点远程"]')
    expect(remoteSwitch?.disabled).toBe(false)
    expect(remoteSwitch?.checked).toBe(true)
    // Effective hint: global on + server on => 已生效
    expect(document.body.textContent).toContain('已生效')
  })

  it('keeps MCP per-server switch enabled even when global MCP is enabled', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: true }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    const mcpSwitch = document.querySelector<HTMLInputElement>('input[aria-label="上海节点MCP"]')
    // Server mcp_enabled = false per mock, but switch should remain enabled (not disabled by global)
    expect(mcpSwitch?.disabled).toBe(false)
    expect(mcpSwitch?.checked).toBe(false)
    // Bulk buttons are disabled only when nothing selected, not by globalMcp
    const bulkEnableMcp = Array.from(document.querySelectorAll('button')).find(button => button.textContent === '开启 MCP') as HTMLButtonElement | undefined
    expect(bulkEnableMcp?.disabled).toBe(true) // no selection yet
    const select = document.querySelector<HTMLInputElement>('input[aria-label="选择 上海节点"]')
    act(() => select?.click())
    await act(async () => { await Promise.resolve() })
    expect(bulkEnableMcp?.disabled).toBe(false)
  })

  it('shows ineffective hint when server MCP is on but global MCP is off', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/servers' && !init) return { servers: [{ id: 7, name: '上海节点', status: 'online' }] }
      if (path.startsWith('/servers/') && path.endsWith('/remote-access') && !init) {
        return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: true }, effective: { remote_terminal: false, mcp_enabled: false } } }
      }
      if (path === '/settings' && init?.method === 'POST') return {}
      throw new Error(`unexpected request: ${path}`)
    })
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: false }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    const mcpSwitch = document.querySelector<HTMLInputElement>('input[aria-label="上海节点MCP"]')
    expect(mcpSwitch?.checked).toBe(true)
    expect(mcpSwitch?.disabled).toBe(false)
    expect(document.body.textContent).toContain('未生效')
    expect(document.body.textContent).toContain('全局已关闭')
  })

  it('shows global MCP enabled banner with counts when global is on', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/servers' && !init) return { servers: [{ id: 7, name: '上海节点', status: 'online' }, { id: 8, name: '北京节点', status: 'online' }] }
      if (path.startsWith('/servers/') && path.endsWith('/remote-access') && !init) {
        // first server mcp true, second false
        if (path.includes('/servers/7/')) return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: true }, effective: { remote_terminal: true, mcp_enabled: true } } }
        return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: false }, effective: { remote_terminal: true, mcp_enabled: false } } }
      }
      if (path === '/settings' && init?.method === 'POST') return {}
      throw new Error(`unexpected: ${path}`)
    })
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: true }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(document.body.textContent).toContain('1 / 2 台服务器可被 MCP 远程控制')
  })

  it('shows warning when global MCP is on but no server is authorized', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/servers' && !init) return { servers: [{ id: 7, name: '上海节点', status: 'online' }] }
      if (path.startsWith('/servers/') && path.endsWith('/remote-access') && !init) {
        return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: false }, effective: { remote_terminal: true, mcp_enabled: false } } }
      }
      if (path === '/settings' && init?.method === 'POST') return {}
      throw new Error(`unexpected: ${path}`)
    })
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: true }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(document.body.textContent).toContain('没有任何服务器授权 MCP 远程控制')
  })

  it('loads server policies and applies a bulk remote-control change when global terminal is off', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: false, mcp_enabled: false }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

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
