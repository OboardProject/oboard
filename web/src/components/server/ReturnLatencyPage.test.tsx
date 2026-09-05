// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import type { Server } from '../proxy-path/types'
import { ReturnLatencyPage } from './ReturnLatencyPage'
import { ServerActionMenu } from './ServerActionMenu'
import { ReturnLatencySettings } from './ReturnLatencySettings'

const regions = ['广东', '北京'].flatMap(province => ['中国电信', '中国联通', '中国移动'].map(carrier => ({ province, carrier })))
const servers = [
  { id: 1, name: 'Hong Kong', agent_id: 'agent-1', status: 'online', latency_probe_enabled: true },
  { id: 2, name: 'Tokyo', agent_id: 'agent-2', status: 'offline', latency_probe_enabled: true },
] as Server[]

describe('return latency management', () => {
  let container: HTMLDivElement
  let root: Root
  const button = (label: string) => Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find(element => element.textContent?.trim() === label)!
  const click = async (element: HTMLElement) => { await act(async () => element.click()) }
  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    window.history.replaceState({}, '', '/')
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('province selection respects the carrier filter and preserves other selected targets', async () => {
    const save = vi.fn()
    await act(async () => root.render(<ReturnLatencySettings draft={{ latency_probe_regions: [{ province: '北京', carrier: '中国移动' }] }} regions={regions} serverCount={1} onSave={save} />))
    await click(button('中国电信'))
    await click(button('选择当前省份'))
    await click(button('应用到 1 台服务器'))
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ latency_probe_regions: expect.arrayContaining([{ province: '北京', carrier: '中国移动' }]) }))
    const targets = save.mock.calls[0][0].latency_probe_regions
    expect(targets).toHaveLength(2)
    expect(targets.filter((region: any) => region.carrier === '中国联通')).toHaveLength(0)
  })

  it('blocks an oversized selection and permits saving after removing unavailable targets', async () => {
    const save = vi.fn()
    await act(async () => root.render(<ReturnLatencySettings draft={{ latency_probe_max_targets: 1 }} regions={regions} serverCount={1} onSave={save} />))
    await click(button('选择全部目标'))
    expect(button('应用到 1 台服务器').disabled).toBe(true)
    await act(async () => root.render(<ReturnLatencySettings key="removed" draft={{ latency_probe_regions: [{ province: '旧目标', carrier: '中国电信' }] }} regions={regions} serverCount={1} onSave={save} />))
    expect(button('应用到 1 台服务器').disabled).toBe(true)
    await click(button('移除失效目标'))
    await click(button('应用到 1 台服务器'))
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ latency_probe_regions: [] }))
  })

  it('applies only probe fields, reports partial failure, and lets the operator select failures for retry', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/latency-probe-resource') return { regions }
      if (path === '/servers/2' && init?.method === 'PATCH') throw new Error('Tokyo unavailable')
      return {}
    })
    const refresh = vi.fn()
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage onRefresh={refresh} renderHistory={() => null} />))
    await click(container.querySelector('[aria-label="选择当前服务器结果"]')!)
    await click(button('应用到 2 台服务器'))
    const writes = request.mock.calls.filter(([, init]) => init?.method === 'PATCH')
    expect(writes.map(([path]) => path)).toEqual(['/servers/1', '/servers/2'])
    expect(Object.keys(JSON.parse(writes[0][1]!.body as string)).every(key => key.startsWith('latency_probe_'))).toBe(true)
    expect(container.textContent).toContain('成功 1 · 失败 1 · 跳过 0')
    expect(container.textContent).toContain('Tokyo unavailable')
    expect(refresh).toHaveBeenCalledOnce()
    await click(button('仅选择失败服务器'))
    expect(button('应用到 1 台服务器').disabled).toBe(false)
    expect((container.querySelector('[aria-label="选择 Tokyo"]') as HTMLInputElement).checked).toBe(true)
  })

  it('queues tests only for online enabled agents and shows skipped servers without claiming completion', async () => {
    const request = vi.fn(async (path: string) => path === '/latency-probe-resource' ? { regions } : { task_id: 17 })
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage onRefresh={() => {}} renderHistory={() => null} />))
    await click(container.querySelector('[aria-label="选择当前服务器结果"]')!)
    await click(button('立即测试 1 台'))
    expect(request.mock.calls.map(([path]) => path)).toContain('/servers/1/latency-probe')
    expect(request.mock.calls.map(([path]) => path)).not.toContain('/servers/2/latency-probe')
    expect(container.textContent).toContain('测试已排队 #17')
    expect(container.textContent).toContain('已跳过：Agent 未在线')
  })

  it('retains hidden selections during search and keeps configuration loading explicit', async () => {
    const request = vi.fn(async () => ({ regions }))
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage onRefresh={() => {}} renderHistory={() => null} />))
    await click(container.querySelector('[aria-label="选择当前服务器结果"]')!)
    const input = container.querySelector<HTMLInputElement>('[aria-label="搜索回程测试服务器"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'Tokyo')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(container.textContent).toContain('另有 1 台已选服务器不在当前筛选中')
    expect(button('应用到 2 台服务器')).toBeTruthy()
    expect(container.textContent).toContain('新配置：')
    await click(button('载入此配置'))
    expect(container.textContent).toContain('配置来源：Tokyo')
  })
  it('groups server actions and routes return latency to its own surface', async () => {
    const action = vi.fn()
    await act(async () => root.render(<ServerActionMenu server={servers[0]} role="admin" onAction={action} />))
    await click(container.querySelector('[aria-label="打开服务器操作菜单"]')!)
    const menu = document.body.querySelector('[role="menu"]')!
    expect(menu.textContent).toContain('资料与设置')
    expect(menu.textContent).toContain('监控与诊断')
    expect(menu.textContent).toContain('运维操作')
    const item = Array.from(menu.querySelectorAll('button')).find(element => element.textContent === '回程延迟')!
    await click(item)
    expect(action).toHaveBeenCalledWith('return-latency', servers[0])
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

})
