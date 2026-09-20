// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ServerInspectorPanel } from './ServerInspectorPanel'
import type { Server } from '../proxy-path/types'

describe('ServerInspectorPanel', () => {
  let container: HTMLDivElement
  let root: Root

  const mockServer: Server = {
    id: 1,
    name: 'US-West-01',
    status: 'online',
    agent_id: 'agent-123',
    public_ipv4: '198.51.100.12',
    cpu_usage_percent: 42,
    memory_used_bytes: 1024 * 1024 * 512,
    memory_total_bytes: 1024 * 1024 * 1024,
    disk_bytes: 1024 * 1024 * 1024 * 10,
    disk_total_bytes: 1024 * 1024 * 1024 * 50,
    network_upload_bps: 1024 * 256,
    network_download_bps: 1024 * 1024 * 2,
    agent_version: 'v1.4.0',
    sing_box_version: '1.11.0',
    distro_name: 'Debian 12',
    arch: 'amd64',
    detected_region_code: 'US',
    tcp_connection_count: 32,
    udp_connection_count: 14,
  }

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

  it('renders server telemetry, status, ip, and triggers action buttons', async () => {
    const onClose = vi.fn()
    const onAction = vi.fn()

    await act(async () => {
      root.render(
        <ServerInspectorPanel
          server={mockServer}
          role="admin"
          onClose={onClose}
          onAction={onAction}
        />
      )
    })

    expect(container.textContent).toContain('US-West-01')
    expect(container.textContent).toContain('#1')
    expect(container.textContent).toContain('在线')
    expect(container.textContent).toContain('42%')
    expect(container.textContent).toContain('198.51.100.12')
    expect(container.textContent).toContain('v1.4.0')
    expect(container.textContent).toContain('1.11.0')

    // Click on "设置" shortcut
    const settingsBtn = Array.from(container.querySelectorAll('button')).find(
      btn => btn.textContent?.includes('设置')
    )
    expect(settingsBtn).toBeDefined()
    await act(async () => settingsBtn?.click())
    expect(onAction).toHaveBeenCalledWith('basic-settings', mockServer)

    // Click on "网络" shortcut
    const networkBtn = Array.from(container.querySelectorAll('button')).find(
      btn => btn.textContent?.includes('网络')
    )
    expect(networkBtn).toBeDefined()
    await act(async () => networkBtn?.click())
    expect(onAction).toHaveBeenCalledWith('network', mockServer)
  })

  it('returns null when server is null', async () => {
    await act(async () => {
      root.render(
        <ServerInspectorPanel
          server={null}
          role="admin"
          onClose={vi.fn()}
          onAction={vi.fn()}
        />
      )
    })
    expect(container.innerHTML).toBe('')
  })
})
