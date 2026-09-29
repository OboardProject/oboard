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

async function expandServerList() {
  await act(async () => document.querySelector<HTMLButtonElement>('button.remote-access-list-toggle')?.click())
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
    vi.unstubAllGlobals()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('keeps WebSSH confirmation on the settings page', () => {
    act(() => root.render(<RemoteAccessSettings data={{ settings: {}, servers: [] }} client={{ request: vi.fn() }} load={vi.fn()} notify={vi.fn()} />))

    expect(container.querySelector<HTMLInputElement>('input[aria-label="打开 WebSSH 前确认密码"]')?.checked).toBe(true)
    expect(container.querySelector<HTMLInputElement>('input[aria-label="全局启用 Web 远程终端"]')).toBeNull()
    expect(container.textContent).not.toContain('Structured Exec')
  })

  it.each([true, false])('requires identity verification before changing confirmation from %s', async initial => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/auth/step-up/begin') return { challenge_id: 'challenge-1', passkey_available: false }
      if (path === '/auth/step-up/password') return { step_up_token: 'verified-token' }
      return remoteAccessMock(path, init)
    })
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_password_confirmation_enabled: initial } }} client={{ request }} load={vi.fn(async () => undefined)} notify={vi.fn()} />))
    const toggle = container.querySelector<HTMLInputElement>('input[aria-label="打开 WebSSH 前确认密码"]')!
    await act(async () => toggle.click())
    expect(toggle.checked).toBe(initial)
    expect(request).toHaveBeenCalledWith('/auth/step-up/begin', {
      method: 'POST',
      body: JSON.stringify({ purpose: 'remote_terminal_settings', resource: { type: 'setting', id: `remote_terminal_password_confirmation_enabled:${!initial}` } }),
    })
    expect(request.mock.calls.some(([path]) => path === '/settings')).toBe(false)
    await act(async () => {
      document.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    expect(request).toHaveBeenCalledWith('/settings', {
      method: 'POST', body: JSON.stringify({ remote_terminal_password_confirmation_enabled: !initial, step_up_token: 'verified-token' }),
    })
    expect(toggle.checked).toBe(!initial)
  })

  it('keeps the setting unchanged after failed verification or cancellation', async () => {
    const request = vi.fn(async (path: string) => {
      if (path === '/auth/step-up/begin') return { challenge_id: 'challenge-1', passkey_available: false }
      throw new Error('密码错误')
    })
    act(() => root.render(<RemoteAccessSettings data={{ settings: {} }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))
    const toggle = container.querySelector<HTMLInputElement>('input[aria-label="打开 WebSSH 前确认密码"]')!
    await act(async () => toggle.click())
    await act(async () => document.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('密码错误')
    expect(toggle.checked).toBe(true)
    await act(async () => Array.from(document.querySelectorAll('button')).find(button => button.textContent === '取消')!.click())
    expect(toggle.checked).toBe(true)
    expect(toggle.disabled).toBe(false)
    expect(request.mock.calls.some(([path]) => path === '/settings')).toBe(false)
  })

  it('keeps the setting unchanged when the verified save fails', async () => {
    const request = vi.fn(async (path: string) => {
      if (path === '/auth/step-up/begin') return { challenge_id: 'challenge-1', passkey_available: false }
      if (path === '/auth/step-up/password') return { step_up_token: 'verified-token' }
      throw new Error('保存失败')
    })
    const notify = vi.fn()
    act(() => root.render(<RemoteAccessSettings data={{ settings: {} }} client={{ request }} load={vi.fn()} notify={notify} />))
    const toggle = container.querySelector<HTMLInputElement>('input[aria-label="打开 WebSSH 前确认密码"]')!
    await act(async () => toggle.click())
    await act(async () => document.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(toggle.checked).toBe(true)
    expect(toggle.disabled).toBe(false)
    expect(notify).toHaveBeenCalledWith('保存失败', 'error')
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
    expect(document.body.textContent).toContain('3 台服务器')
    await expandServerList()
    expect(document.body.textContent).toContain('北京节点')
    expect(document.body.textContent).toContain('广州节点')
  })

  it.each([true, false])('starts with the server list collapsed when narrow is %s', async narrow => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: narrow })))
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings data={{ settings: {} }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))
    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })
    const toggle = document.querySelector<HTMLButtonElement>('button.remote-access-list-toggle')
    expect(toggle?.getAttribute('aria-expanded')).toBe('false')
    expect(document.querySelector('input[aria-label="上海节点远程"]')).toBeNull()
    await act(async () => toggle?.click())
    expect(toggle?.getAttribute('aria-expanded')).toBe('true')
    expect(document.querySelector('input[aria-label="上海节点远程"]')).not.toBeNull()
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

  it('locks per-server terminal settings when global terminal is enabled', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: false }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    await expandServerList()
    const remoteSwitch = document.querySelector<HTMLInputElement>('input[aria-label="上海节点远程"]')
    expect(remoteSwitch?.disabled).toBe(true)
    expect(remoteSwitch?.checked).toBe(true)
    expect(document.body.textContent).toContain('逐台设置')
  })

  it('locks per-server MCP settings when global MCP is enabled', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: true, mcp_enabled: true }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    await expandServerList()
    const mcpSwitch = document.querySelector<HTMLInputElement>('input[aria-label="上海节点MCP"]')
    expect(mcpSwitch?.disabled).toBe(true)
    expect(mcpSwitch?.checked).toBe(true)
    expect(Array.from(document.querySelectorAll('button')).find(button => button.textContent === '开启 MCP')).toBeUndefined()
  })

  it('applies the per-server MCP choice when global MCP is off', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/servers' && !init) return { servers: [{ id: 7, name: '上海节点', status: 'online' }] }
      if (path.startsWith('/servers/') && path.endsWith('/remote-access') && !init) {
        return { remote_access: { server: { remote_terminal_enabled: true, mcp_enabled: true }, effective: { remote_terminal: true, mcp_enabled: true } } }
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

    await expandServerList()
    const mcpSwitch = document.querySelector<HTMLInputElement>('input[aria-label="上海节点MCP"]')
    expect(mcpSwitch?.checked).toBe(true)
    expect(mcpSwitch?.disabled).toBe(false)
    expect(document.body.textContent).toContain('关闭后按逐台设置生效')
  })

  it('shows all servers enabled when global MCP is on', async () => {
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

    await expandServerList()
    expect(document.querySelectorAll<HTMLInputElement>('input[aria-label$="MCP"]:checked').length).toBe(2)
  })

  it('includes a server without a per-server grant when global MCP is on', async () => {
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

    await expandServerList()
    expect(document.querySelector<HTMLInputElement>('input[aria-label="上海节点MCP"]')?.checked).toBe(true)
  })

  it('loads server policies and applies a bulk remote-control change when global terminal is off', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => remoteAccessMock(path, init))
    act(() => root.render(<RemoteAccessSettings data={{ settings: { remote_terminal_enabled: false, mcp_enabled: false }, servers: [] }} client={{ request }} load={vi.fn()} notify={vi.fn()} />))

    await act(async () => {
      Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('管理服务器'))?.click()
      await Promise.resolve()
      await Promise.resolve()
    })
    await expandServerList()
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
