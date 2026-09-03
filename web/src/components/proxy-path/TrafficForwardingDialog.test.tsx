// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Server } from './types'
import {
  TrafficForwardingDialog,
  emptyTrafficForwardDraft,
  trafficForwardPayload,
  validateTrafficForwardDraft,
  type TrafficForward,
} from './TrafficForwardingDialog'

vi.mock('../ui/motion', () => ({
  MotionDialogPanel: ({ children }: { children: React.ReactNode }) => <section>{children}</section>,
}))

describe('TrafficForwardingDialog', () => {
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
    document.body.querySelectorAll('.custom-select-menu').forEach(element => element.remove())
    vi.restoreAllMocks()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('uses the selected first-layer server as the list context', () => {
    const props = dialogProps()
    act(() => root.render(<TrafficForwardingDialog {...props} initialServerID={1} />))

    expect(container.textContent).toContain('流量转发')
    expect(container.textContent).toContain('转发-A')
    expect(container.textContent).not.toContain('转发-B')

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="选择入口服务器 东京入口"]')?.click())
    expect(container.textContent).toContain('转发-B')
    expect(container.textContent).not.toContain('转发-A')

    act(() => Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent?.includes('服务器隧道'))?.click())
    expect(props.onOpenTunnel).toHaveBeenCalledWith(2)
  })

  it('creates a complete enabled forward without closing the workspace', async () => {
    const props = dialogProps()
    act(() => root.render(<TrafficForwardingDialog {...props} initialServerID={1} />))

    const create = Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent?.includes('创建转发'))
    act(() => create?.click())

    expect(container.textContent).toContain('创建流量转发')
    expect(container.textContent).toContain('转发端点')
    expect(container.textContent).toContain('转发策略')
    expect(container.textContent).toContain('高级 JSON 配置')
    expect(container.querySelector<HTMLButtonElement>('[aria-label="目标服务器"]')?.textContent).toContain('不选择')

    const targetAddress = container.querySelector<HTMLInputElement>('[aria-label="目标地址"]')
    act(() => {
      if (!targetAddress) return
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
      setter?.call(targetAddress, '203.0.113.80')
      targetAddress.dispatchEvent(new Event('input', { bubbles: true }))
    })

    await act(async () => {
      container.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()
      await Promise.resolve()
    })

    expect(props.onSave).toHaveBeenCalledTimes(1)
    expect(props.onSave).toHaveBeenCalledWith(expect.objectContaining({
      source_server_id: 1,
      target_server_id: 0,
      target_address: '203.0.113.80',
      protocol: 'tcp',
      backend: 'realm',
      probe_mode: 'periodic',
      probe_interval_seconds: 300,
      config_json: '{}',
      enabled: true,
    }), undefined)
    expect(container.textContent).toContain('流量转发已创建')
  })

  it('edits by sending the full forward object and preserves its id separately', async () => {
    const props = dialogProps()
    act(() => root.render(<TrafficForwardingDialog {...props} initialServerID={1} />))

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="编辑 转发-A"]')?.click())
    const name = container.querySelector<HTMLInputElement>('[aria-label="转发名称"]')
    expect(name?.value).toBe('转发-A')
    act(() => {
      if (!name) return
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
      setter?.call(name, '香港入口到东京应用')
      name.dispatchEvent(new Event('input', { bubbles: true }))
    })

    await act(async () => {
      container.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()
      await Promise.resolve()
    })

    expect(props.onSave).toHaveBeenCalledWith(expect.objectContaining({
      name: '香港入口到东京应用',
      source_server_id: 1,
      target_server_id: 2,
      listen_ip: '',
      listen_port: 10001,
      target_address: '',
      target_port: 443,
      protocol: 'tcp',
      backend: 'realm',
      probe_mode: 'periodic',
      probe_interval_seconds: 300,
      priority: 100,
      config_json: '{}',
      enabled: true,
    }), 11)
  })

  it('supports immediate checking and protected deletion from the list', async () => {
    const props = dialogProps()
    act(() => root.render(<TrafficForwardingDialog {...props} initialServerID={1} />))

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="立即检查 转发-A"]')?.click()
      await Promise.resolve()
    })
    expect(props.onProbe).toHaveBeenCalledWith(expect.objectContaining({ id: 11 }))

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="删除 转发-A"]')?.click()
      await Promise.resolve()
    })
    expect(props.onDelete).toHaveBeenCalledWith(expect.objectContaining({ id: 11 }))
    expect(container.textContent).toContain('已删除「转发-A」')
  })

  it('does not reuse a late probe from the previous forward revision', () => {
    const props = dialogProps()
    act(() => root.render(<TrafficForwardingDialog
      {...props}
      initialServerID={1}
      forwards={props.forwards.map(item => item.id === 11 ? { ...item, updated_at: '2026-08-24T09:00:00Z' } : item)}
      probes={props.probes.map(item => item.port_forward_id === 11 ? { ...item, created_at: '2026-08-24T10:00:00Z', result_json: '{"success_count":5,"p95_latency_ms":31,"forward_updated_at":"2026-08-24T08:00:00Z"}' } : item)}
    />))

    expect(container.textContent).toContain('等待检查')
    expect(container.textContent).not.toContain('38 ms')
  })

  it('locks editor navigation and fields while a full save is in flight', async () => {
    let resolveSave!: () => void
    const props = dialogProps()
    props.onSave = vi.fn(() => new Promise<void>(resolve => { resolveSave = resolve }))
    act(() => root.render(<TrafficForwardingDialog {...props} initialServerID={1} />))
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="编辑 转发-A"]')?.click())

    await act(async () => {
      container.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()
      await Promise.resolve()
    })

    expect(Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent?.includes('返回列表'))?.disabled).toBe(true)
    expect(container.querySelector<HTMLInputElement>('[aria-label="转发名称"]')?.matches(':disabled')).toBe(true)

    await act(async () => { resolveSave(); await Promise.resolve() })
    expect(container.textContent).toContain('转发配置已保存')
  })
})

describe('traffic forwarding draft helpers', () => {
  it('starts without a managed target and uses the next unused recommended port', () => {
    const draft = emptyTrafficForwardDraft(servers, forwards, 1)
    expect(draft.source_server_id).toBe(1)
    expect(draft.target_server_id).toBe(0)
    expect(draft.listen_port).toBe(10000)
    expect(draft.enabled).toBe(true)
    expect(validateTrafficForwardDraft(draft)).toContain('目标地址')
  })

  it('normalizes the full payload and validates the remaining fields', () => {
    const base = emptyTrafficForwardDraft(servers, forwards, 1)
    expect(trafficForwardPayload({ ...base, name: '  转发  ', target_address: ' edge.example.com ', config_json: '  {}  ' })).toEqual(expect.objectContaining({
      name: '转发',
      target_address: 'edge.example.com',
      config_json: '{}',
    }))
    expect(validateTrafficForwardDraft({ ...base, target_address: '198.51.100.25', probe_interval_seconds: 60 })).toContain('300 秒')
    expect(validateTrafficForwardDraft({ ...base, target_address: '198.51.100.25', protocol: 'udp' })).toBe('')
    expect(validateTrafficForwardDraft({ ...base, target_server_id: 2 })).toBe('')
    expect(validateTrafficForwardDraft({ ...base, target_address: '198.51.100.25' })).toBe('')
  })
})

const servers = [
  server({ id: 1, name: '香港入口', entry_address: 'hk.example.com', entry_ip_mode: 'custom', port_range_start: 10000, port_range_end: 20000 }),
  server({ id: 2, name: '东京入口', entry_address: 'jp.example.com', entry_ip_mode: 'custom', port_range_start: 10000, port_range_end: 20000 }),
]

const forwards: TrafficForward[] = [
  forward({ id: 11, name: '转发-A', source_server_id: 1, target_server_id: 2, listen_port: 10001 }),
  forward({ id: 12, name: '转发-B', source_server_id: 2, target_server_id: 1, listen_port: 10001 }),
]

function dialogProps() {
  return {
    servers,
    forwards,
    probes: [{ id: 21, port_forward_id: 11, server_id: 1, mode: 'manual', available: true, latency_ms: 28, sample_count: 5, error: '', result_json: '{"success_count":5,"p95_latency_ms":31}', created_at: '2026-08-24T08:00:00Z' }],
    onCancel: vi.fn(),
    onSave: vi.fn(async () => {}),
    onDelete: vi.fn(async () => true),
    onProbe: vi.fn(async () => {}),
    onOpenTunnel: vi.fn(),
  }
}

function forward(patch: Partial<TrafficForward>): TrafficForward {
  return {
    id: 1,
    name: '转发',
    source_server_id: 1,
    target_server_id: 2,
    listen_ip: '',
    listen_port: 10000,
    target_address: '',
    target_port: 443,
    protocol: 'tcp',
    backend: 'realm',
    probe_mode: 'periodic',
    probe_interval_seconds: 300,
    priority: 100,
    config_json: '{}',
    enabled: true,
    ...patch,
  }
}

function server(patch: Partial<Server>): Server {
  return {
    id: 1,
    name: '服务器',
    entry_address: '',
    public_ipv4: '',
    public_ipv6: '',
    interface_ipv6: '',
    region_code: '',
    detected_region_code: '',
    region_mode: 'auto',
    entry_ip_mode: 'auto',
    listen_ip: '',
    listen_mode: 'auto',
    ip_stack: 'auto',
    udp_inbound_mode: 'allow',
    mtu_mode: 'off',
    mtu_value: 0,
    mtu_probe_host: '',
    mtu_probe_port: 0,
    mtu_overhead_bytes: 0,
    bbr_enabled: false,
    port_range_start: 10000,
    port_range_end: 20000,
    internal_port_range_start: 30000,
    internal_port_range_end: 59999,
    status: 'online',
    os: '',
    distro_id: '',
    distro_version: '',
    distro_name: '',
    libc: '',
    service_manager: '',
    package_manager: '',
    arch: '',
    cpu: '',
    cpu_usage_percent: 0,
    memory_used_bytes: 0,
    memory_total_bytes: 0,
    agent_memory_bytes: 0,
    disk_bytes: 0,
    disk_total_bytes: 0,
    tcp_connection_count: 0,
    udp_connection_count: 0,
    process_count: 0,
    agent_version: '',
    agent_build: '',
    sing_box_version: '',
    monitoring_mode: 'lightweight',
    resource_history_enabled: false,
    traffic_reset_mode: '',
    traffic_reset_day: 1,
    network_upload_bps: 0,
    network_download_bps: 0,
    traffic_upload_bytes: 0,
    traffic_download_bytes: 0,
    latency_probe_enabled: false,
    latency_probe_mode: 'tcp',
    latency_probe_public_target: 'auto',
    connection_audit_enabled: false,
    ...patch,
  } as Server
}
