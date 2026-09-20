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
    expect(container.textContent).toContain('流量统计')
    expect(container.textContent).toContain('MTU')
    expect(container.textContent).toContain('BBR + FQ')
    expect(container.textContent).toContain('时间校准')
    expect(container.textContent).toContain('NTP 时间源')
    expect(container.textContent).toContain('统计时区')
    expect(container.textContent).not.toContain('达量后处理')
    expect(container.textContent).toContain('维护操作')
    expect(container.textContent).toContain('刷新全部节点配置')
  })

  it('defaults to Controller and saves the GitHub preference for Agent resources', async () => {
    const client = { request: vi.fn(async () => ({})) }
    const load = vi.fn(async () => undefined)
    const notify = vi.fn()
    act(() => root.render(<AgentSettingsPanel data={mockData} client={client} load={load} notify={notify} />))
    const toggle = container.querySelector<HTMLButtonElement>('[aria-label="优先从 GitHub 下载资源"]')!
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="资源下载说明"]')!.click())
    expect(document.querySelector('[role="tooltip"]')?.textContent).toContain('订阅中继固定从主控下载')
    await act(async () => toggle.click())
    expect(client.request).toHaveBeenCalledWith('/settings', { method: 'POST', body: JSON.stringify({ resource_download_source: 'github' }) })
    act(() => root.render(<AgentSettingsPanel data={{ settings: { resource_download_source: 'github' } }} client={client} load={load} notify={notify} />))
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    await act(async () => toggle.click())
    expect(client.request).toHaveBeenLastCalledWith('/settings', { method: 'POST', body: JSON.stringify({ resource_download_source: 'controller' }) })
  })

  it('saves and restores the mainland Controller preference independently', async () => {
    const client = { request: vi.fn(async () => ({})) }
    const load = vi.fn(async () => undefined)
    const notify = vi.fn()
    act(() => root.render(<AgentSettingsPanel data={mockData} client={client} load={load} notify={notify} />))
    const toggle = container.querySelector<HTMLButtonElement>('[aria-label="中国大陆服务器优先从主控下载"]')!
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    await act(async () => toggle.click())
    expect(client.request).toHaveBeenLastCalledWith('/settings', { method: 'POST', body: JSON.stringify({ resource_download_cn_controller: false }) })
    act(() => root.render(<AgentSettingsPanel data={{ settings: { resource_download_source: 'github', resource_download_cn_controller: false } }} client={client} load={load} notify={notify} />))
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    await act(async () => toggle.click())
    expect(client.request).toHaveBeenLastCalledWith('/settings', { method: 'POST', body: JSON.stringify({ resource_download_cn_controller: true }) })
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

  it('defaults BBR + FQ to true and auto-saves when toggled', async () => {
    const mockClient = { request: vi.fn(async () => ({ status: 'ok' })) }
    const mockLoad = vi.fn(async () => undefined)
    const mockNotify = vi.fn()

    act(() => {
      root.render(<AgentSettingsPanel data={{ settings: {} }} client={mockClient} load={mockLoad} notify={mockNotify} />)
    })

    const bbrSwitch = container.querySelector<HTMLInputElement>('input[role="switch"][aria-label="新服务器默认启用 BBR + FQ"]')!
    expect(bbrSwitch).not.toBeNull()
    expect(bbrSwitch.checked).toBe(true)

    await act(async () => {
      bbrSwitch.click()
    })

    expect(mockClient.request).toHaveBeenCalledWith('/settings', {
      method: 'POST',
      body: JSON.stringify({ server_default_bbr_enabled: false }),
    })
    expect(mockNotify).toHaveBeenCalledWith('BBR + FQ 设置已保存', 'success')
  })

  it('restores the persisted BBR value and shows an error when auto-save fails', async () => {
    const client = { request: vi.fn(async () => { throw new Error('保存失败，请重试') }) }
    act(() => root.render(<AgentSettingsPanel data={mockData} client={client} load={vi.fn()} notify={vi.fn()} />))
    const toggle = container.querySelector<HTMLInputElement>('input[role="switch"][aria-label="新服务器默认启用 BBR + FQ"]')!
    await act(async () => toggle.click())
    expect(toggle.checked).toBe(true)
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('保存失败，请重试')
    expect(toggle.disabled).toBe(false)
  })

  it('keeps NTP drafts after failure and disables saving an empty time source', async () => {
    const client = { request: vi.fn(async () => { throw new Error('NTP 保存失败') }) }
    act(() => root.render(<AgentSettingsPanel data={mockData} client={client} load={vi.fn()} notify={vi.fn()} />))
    const input = container.querySelector<HTMLInputElement>('[aria-label="NTP 时间源 1"]')!
    const save = [...container.querySelectorAll('button')].find(button => button.textContent === '保存 NTP 时间源')!
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    act(() => { setValue.call(input, ''); input.dispatchEvent(new Event('input', { bubbles: true })) })
    expect(save.disabled).toBe(true)
    act(() => { setValue.call(input, 'pool.ntp.org'); input.dispatchEvent(new Event('input', { bubbles: true })) })
    await act(async () => save.click())
    expect(input.value).toBe('pool.ntp.org')
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('NTP 保存失败')
    expect(save.disabled).toBe(false)
  })

  it('confirms before refreshing all node runtime configs', async () => {
    const mockClient = { request: vi.fn(async () => ({ reissued_servers: 3, delivery_retried: 3 })) }
    const mockLoad = vi.fn(async () => undefined)
    const mockNotify = vi.fn()
    const mockConfirm = vi.fn(async () => true)

    act(() => {
      root.render(<AgentSettingsPanel data={mockData} client={mockClient} load={mockLoad} notify={mockNotify} confirm={mockConfirm} />)
    })

    const refreshButton = Array.from(container.querySelectorAll('button')).find(btn => btn.textContent === '刷新全部节点配置')!
    await act(async () => {
      refreshButton.click()
    })

    expect(mockConfirm).toHaveBeenCalled()
    expect(mockClient.request).toHaveBeenCalledWith('/deployments/refresh-runtime', {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    })
    expect(mockNotify).toHaveBeenCalledWith('已向 3 台已接入服务器重新下发全部配置、授权、用户与探测计划', 'success')
    expect(mockLoad).toHaveBeenCalled()
  })

  it('does not refresh when the confirm dialog is cancelled', async () => {
    const mockClient = { request: vi.fn() }
    const mockConfirm = vi.fn(async () => false)

    act(() => {
      root.render(<AgentSettingsPanel data={mockData} client={mockClient} load={vi.fn()} notify={vi.fn()} confirm={mockConfirm} />)
    })

    const refreshButton = Array.from(container.querySelectorAll('button')).find(btn => btn.textContent === '刷新全部节点配置')!
    await act(async () => {
      refreshButton.click()
    })

    expect(mockConfirm).toHaveBeenCalled()
    // The read-only config-health lookup that builds the warning may run; the
    // refresh itself must not.
    expect(mockClient.request).not.toHaveBeenCalledWith('/deployments/refresh-runtime', expect.anything())
  })

  it('warns about blocking configuration before a fleet refresh without blocking it', async () => {
    const mockClient = {
      request: vi.fn(async (path: string) => {
        if (path === '/config-health') {
          return {
            revision: 7,
            report: {
              summary: { blocking: 1, warning: 0, notice: 0, total: 1 },
              findings: [{
                code: 'inbound.config.invalid', severity: 'blocking', scope: 'inbound',
                resource_id: 10, server_name: 'hk-1', title: '入口配置不符合当前协议模型',
                remedy: { kind: 'normalize' },
              }],
            },
          }
        }
        return { delivery_retried: 3 }
      }),
    }
    const mockConfirm = vi.fn(async () => true)

    act(() => {
      root.render(<AgentSettingsPanel data={mockData} client={mockClient} load={vi.fn()} notify={vi.fn()} confirm={mockConfirm} />)
    })
    const refreshButton = Array.from(container.querySelectorAll('button')).find(btn => btn.textContent === '刷新全部节点配置')!
    await act(async () => { refreshButton.click() })

    expect(String(mockConfirm.mock.calls[0][0].message)).toContain('1 项配置会导致下发失败')
    // Informing, not gating: confirming still runs the refresh.
    expect(mockClient.request).toHaveBeenCalledWith('/deployments/refresh-runtime', {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    })
  })

  it('still refreshes when the health lookup fails', async () => {
    const mockClient = {
      request: vi.fn(async (path: string) => {
        if (path === '/config-health') throw new Error('unavailable')
        return { delivery_retried: 1 }
      }),
    }
    const mockConfirm = vi.fn(async () => true)

    act(() => {
      root.render(<AgentSettingsPanel data={mockData} client={mockClient} load={vi.fn()} notify={vi.fn()} confirm={mockConfirm} />)
    })
    const refreshButton = Array.from(container.querySelectorAll('button')).find(btn => btn.textContent === '刷新全部节点配置')!
    await act(async () => { refreshButton.click() })

    expect(mockClient.request).toHaveBeenCalledWith('/deployments/refresh-runtime', {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    })
  })
})
