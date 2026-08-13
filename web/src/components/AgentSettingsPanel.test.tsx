// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentSettingsPanel } from './AgentSettingsPanel'

describe('AgentSettingsPanel', () => {
  let container: HTMLDivElement
  let root: Root

  const mockData = {
    settings: {
      server_default_mtu_mode: 'detect',
      server_default_bbr_enabled: 'true',
      server_default_time_correction_mode: 'auto',
      time_check_ntp_servers: ['time.cloudflare.com', 'time.google.com', 'ntp.aliyun.com'],
      traffic_timezone: 'Asia/Shanghai',
      traffic_enforcement_mode: 'disconnect_and_reject',
    },
  }

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

  it('renders all sections and labels correctly', () => {
    const mockClient = { request: vi.fn() }
    const mockLoad = vi.fn()
    const mockNotify = vi.fn()

    act(() => {
      root.render(<AgentSettingsPanel data={mockData} client={mockClient} load={mockLoad} notify={mockNotify} />)
    })

    expect(container.textContent).toContain('新服务器默认值')
    expect(container.textContent).toContain('流量控制')
    expect(container.textContent).toContain('MTU')
    expect(container.textContent).toContain('BBR + FQ')
    expect(container.textContent).toContain('时间校准')
    expect(container.textContent).toContain('NTP 时间源')
    expect(container.textContent).toContain('统计时区')
    expect(container.textContent).toContain('达量后处理')
  })

  it('auto-saves when MTU setting is changed', async () => {
    const mockClient = { request: vi.fn(async () => ({ status: 'ok' })) }
    const mockLoad = vi.fn(async () => undefined)
    const mockNotify = vi.fn()

    act(() => {
      root.render(<AgentSettingsPanel data={mockData} client={mockClient} load={mockLoad} notify={mockNotify} />)
    })

    const applyOptionBtn = Array.from(container.querySelectorAll('button')).find(btn => btn.textContent === '检测并应用')!
    expect(applyOptionBtn).not.toBeNull()

    await act(async () => {
      applyOptionBtn.click()
    })

    expect(mockClient.request).toHaveBeenCalledWith('/settings', {
      method: 'POST',
      body: JSON.stringify({ server_default_mtu_mode: 'apply' }),
    })
    expect(mockNotify).toHaveBeenCalledWith('MTU 设置已保存', 'success')
  })

  it('enables NTP save button when text input is modified and saves on click', async () => {
    const mockClient = { request: vi.fn(async () => ({ status: 'ok' })) }
    const mockLoad = vi.fn(async () => undefined)
    const mockNotify = vi.fn()

    act(() => {
      root.render(<AgentSettingsPanel data={mockData} client={mockClient} load={mockLoad} notify={mockNotify} />)
    })

    const ntpInputs = container.querySelectorAll<HTMLInputElement>('.agent-ntp-list input')
    expect(ntpInputs.length).toBe(3)

    const saveButton = Array.from(container.querySelectorAll('button')).find(btn => btn.textContent?.includes('保存 NTP 时间源'))!
    expect(saveButton.disabled).toBe(true)

    await act(async () => {
      const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set
      nativeSetter?.call(ntpInputs[0], 'pool.ntp.org')
      ntpInputs[0].dispatchEvent(new Event('input', { bubbles: true }))
      ntpInputs[0].dispatchEvent(new Event('change', { bubbles: true }))
    })

    expect(saveButton.disabled).toBe(false)

    await act(async () => {
      saveButton.click()
    })

    expect(mockClient.request).toHaveBeenCalledWith('/settings', {
      method: 'POST',
      body: JSON.stringify({
        time_check_ntp_servers: ['pool.ntp.org', 'time.google.com', 'ntp.aliyun.com'],
      }),
    })
    expect(mockNotify).toHaveBeenCalledWith('NTP 时间源已保存', 'success')
  })
})
