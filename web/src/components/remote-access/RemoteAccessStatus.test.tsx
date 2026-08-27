// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RemoteAccessStatus } from './RemoteAccessStatus'

describe('RemoteAccessStatus', () => {
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

  it('renders per-server switches without a terminal entry', async () => {
    const request = vi.fn(async () => ({
      remote_access: {
        server: { remote_terminal_enabled: true, mcp_remote_operations_enabled: false, mcp_structured_exec_enabled: false, mcp_raw_shell_enabled: false },
        effective: { remote_terminal: true },
        active_terminals: 1,
        unavailable_reasons: [],
      },
    }))
    await act(async () => {
      root.render(<RemoteAccessStatus serverId={7} client={{ request }} notify={vi.fn()} />)
      await Promise.resolve()
    })

    expect(container.querySelector<HTMLInputElement>('input[aria-label="在此服务器启用远程控制"]')?.checked).toBe(true)
    expect(container.querySelector<HTMLInputElement>('input[aria-label="在此服务器启用 MCP 控制"]')?.checked).toBe(false)
    expect(container.textContent).toContain('活动终端 1 / 2')
    expect(container.textContent).not.toContain('打开终端')
  })
})
