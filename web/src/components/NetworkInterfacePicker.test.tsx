// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { NetworkInterfacePicker, sourcePrefixSuggestion } from './NetworkInterfacePicker'

describe('sourcePrefixSuggestion', () => {
  it('canonicalizes reported IPv4 and IPv6 networks', () => {
    expect(sourcePrefixSuggestion('216.23.110.46/24')).toBe('216.23.110.0/24')
    expect(sourcePrefixSuggestion('216.23.110.46/32')).toBe('216.23.110.46/32')
    expect(sourcePrefixSuggestion('2401:0b60:003c:006b::a/64')).toBe('2401:b60:3c:6b::/64')
  })

  it('suggests a stable /64 for global IPv6 /128 addresses', () => {
    expect(sourcePrefixSuggestion('2001:b011:b000:8e73:1::b5b9/128')).toBe('2001:b011:b000:8e73::/64')
  })

  it('rejects loopback, link-local, and malformed addresses', () => {
    expect(sourcePrefixSuggestion('127.0.0.1/8')).toBeNull()
    expect(sourcePrefixSuggestion('169.254.10.2/16')).toBeNull()
    expect(sourcePrefixSuggestion('::1/128')).toBeNull()
    expect(sourcePrefixSuggestion('fe80::201:7ff:feb9:9fc3/64')).toBeNull()
    expect(sourcePrefixSuggestion('not-an-address')).toBeNull()
  })
})

describe('NetworkInterfacePicker', () => {
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

  it('loads interfaces and selects one without replacing the manual value first', async () => {
    const onChange = vi.fn()
    const request = vi.fn(async () => ({
      task: {
        id: 7,
        status: 'succeeded',
        result_json: JSON.stringify({ interfaces: [
          { name: 'eth0', up: true, running: true, loopback: false, addresses: ['192.0.2.10/24'] },
          { name: 'lo', up: true, running: true, loopback: true, addresses: ['127.0.0.1/8'] },
        ] }),
      },
    }))
    act(() => root.render(<NetworkInterfacePicker serverID={3} value="manual0" onChange={onChange} client={{ request }} />))

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="读取网卡"]')!.click()
      await Promise.resolve()
    })

    expect(request).toHaveBeenCalledWith('/servers/3/network-interfaces', { method: 'POST', body: '{}' })
    expect(container.querySelector<HTMLInputElement>('input')?.value).toBe('manual0')
    expect(container.querySelector<HTMLInputElement>('input')?.getAttribute('aria-label')).toBe('出口网卡')
    expect(container.textContent).toContain('已读取 2 个网卡')

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="选择 Agent 网卡"]')!.click())
    const option = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.custom-select-option')).find(button => button.textContent?.includes('eth0'))
    act(() => option!.click())
    expect(onChange).toHaveBeenCalledWith('eth0')
  })

  it('shows an immediate task failure and keeps manual entry available', async () => {
    const request = vi.fn(async () => ({ task: { id: 8, status: 'failed', result_json: '{"error":"Agent 离线"}' } }))
    act(() => root.render(<NetworkInterfacePicker serverID={3} value="eth9" onChange={() => {}} client={{ request }} />))

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="读取网卡"]')!.click()
      await Promise.resolve()
    })

    expect(container.textContent).toContain('Agent 离线')
    expect(container.querySelector<HTMLInputElement>('input')?.value).toBe('eth9')
    expect(container.querySelector('[aria-label="选择 Agent 网卡"]')).toBeNull()
  })

  it('reports an empty result and a polling timeout', async () => {
    const emptyRequest = vi.fn(async () => ({ task: { id: 9, status: 'succeeded', result_json: '{"interfaces":[]}' } }))
    act(() => root.render(<NetworkInterfacePicker serverID={3} value="" onChange={() => {}} client={{ request: emptyRequest }} />))
    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="读取网卡"]')!.click()
      await Promise.resolve()
    })
    expect(container.textContent).toContain('Agent 未返回网卡')

    const pendingRequest = vi.fn(async (path: string) => path.includes('network-interfaces')
      ? { task: { id: 10, status: 'pending', result_json: '{}' } }
      : { task: { id: 10, status: 'running', result_json: '{}' } })
    act(() => root.render(<NetworkInterfacePicker serverID={4} value="" onChange={() => {}} client={{ request: pendingRequest }} pollIntervalMS={10} maxPollAttempts={1} />))
    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="读取网卡"]')!.click()
      await Promise.resolve()
      vi.advanceTimersByTime(10)
      await Promise.resolve()
    })
    expect(container.textContent).toContain('读取网卡超时')
  })

  it('renders all IP addresses in normal state and includes all IPs in tooltip title', async () => {
    const request = vi.fn(async () => ({
      task: {
        id: 11,
        status: 'succeeded',
        result_json: JSON.stringify({ interfaces: [
          {
            name: 'eth0',
            up: true,
            running: true,
            loopback: false,
            addresses: [
              '10.7.0.68/23',
              '2408:820c:7509:b244:be24:11ff:fe46:70e2/64',
              'fe80::be24:11ff:fe46:70e2/64',
              '10.7.0.69/23',
            ],
          },
          {
            name: 'eth1',
            up: false,
            running: false,
            loopback: false,
            addresses: [],
          },
        ] }),
      },
    }))

    act(() => root.render(<NetworkInterfacePicker serverID={3} value="eth0" onChange={() => {}} client={{ request }} />))

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="读取网卡"]')!.click()
      await Promise.resolve()
    })

    // Check trigger display in normal state
    const trigger = container.querySelector<HTMLButtonElement>('.custom-select-trigger')
    expect(trigger?.textContent).toContain('10.7.0.68/23 · 2408:820c:7509:b244:be24:11ff:fe46:70e2/64 · fe80::be24:11ff:fe46:70e2/64 · 10.7.0.69/23')
    expect(trigger?.querySelector('.network-interface-option')?.getAttribute('title')).toBe('10.7.0.68/23\n2408:820c:7509:b244:be24:11ff:fe46:70e2/64\nfe80::be24:11ff:fe46:70e2/64\n10.7.0.69/23')

    // Open dropdown and check option items
    act(() => trigger!.click())
    const options = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.custom-select-option'))
    const eth0Option = options.find(btn => btn.textContent?.includes('eth0'))
    expect(eth0Option?.textContent).toContain('10.7.0.68/23 · 2408:820c:7509:b244:be24:11ff:fe46:70e2/64 · fe80::be24:11ff:fe46:70e2/64 · 10.7.0.69/23')
    expect(eth0Option?.querySelector('.network-interface-option')?.getAttribute('title')).toBe('10.7.0.68/23\n2408:820c:7509:b244:be24:11ff:fe46:70e2/64\nfe80::be24:11ff:fe46:70e2/64\n10.7.0.69/23')

    const eth1Option = options.find(btn => btn.textContent?.includes('eth1'))
    expect(eth1Option?.textContent).toContain('无 IP 地址')
    expect(eth1Option?.querySelector('.network-interface-option')?.getAttribute('title')).toBe('无 IP 地址')
  })

  it('offers every usable interface address as a canonical source prefix', async () => {
    const onChange = vi.fn()
    const request = vi.fn(async () => ({
      task: {
        id: 12,
        status: 'succeeded',
        result_json: JSON.stringify({ interfaces: [
          {
            name: 'eth0',
            up: true,
            running: true,
            loopback: false,
            addresses: [
              '216.23.110.46/24',
              '216.23.110.47/24',
              '2001:b011:b000:8e73:1::b5b9/128',
              '240d:1b:c8:e910:a::7c47/128',
              '2401:b60:3c:6b::a/64',
              'fe80::201:7ff:feb9:9fc3/64',
            ],
          },
          { name: 'lo', up: true, running: true, loopback: true, addresses: ['127.0.0.1/8', '::1/128'] },
        ] }),
      },
    }))

    act(() => root.render(<NetworkInterfacePicker mode="source-prefix" serverID={3} value="manual-prefix/64" onChange={onChange} client={{ request }} />))
    expect(container.querySelector<HTMLInputElement>('input')?.getAttribute('aria-label')).toBe('源地址前缀')
    expect(container.querySelector<HTMLInputElement>('input')?.value).toBe('manual-prefix/64')

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="读取网卡 IP"]')!.click()
      await Promise.resolve()
    })

    expect(container.textContent).toContain('已读取 5 个可用 IP')
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="选择 Agent 网卡 IP"]')!.click())
    const options = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.custom-select-option'))
    expect(options).toHaveLength(5)
    expect(options.map(option => option.textContent)).toEqual(expect.arrayContaining([
      expect.stringMatching(/216\.23\.110\.46\/24.*216\.23\.110\.0\/24/),
      expect.stringMatching(/216\.23\.110\.47\/24.*216\.23\.110\.0\/24/),
      expect.stringMatching(/2001:b011:b000:8e73:1::b5b9\/128.*2001:b011:b000:8e73::\/64/),
      expect.stringMatching(/240d:1b:c8:e910:a::7c47\/128.*240d:1b:c8:e910::\/64/),
      expect.stringMatching(/2401:b60:3c:6b::a\/64.*2401:b60:3c:6b::\/64/),
    ]))
    expect(options.some(option => option.textContent?.includes('fe80::'))).toBe(false)
    expect(options.some(option => option.textContent?.includes('127.0.0.1'))).toBe(false)

    const dynamicIPv6 = options.find(option => option.textContent?.includes('2001:b011:b000:8e73:1::b5b9'))
    act(() => dynamicIPv6!.click())
    expect(onChange).toHaveBeenCalledWith('2001:b011:b000:8e73::/64')
  })
})
