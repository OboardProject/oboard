// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import type { LatencyProbeTask, Server } from '../proxy-path/types'
import { ReturnLatencyPage } from './ReturnLatencyPage'
import { ServerActionMenu } from './ServerActionMenu'
import { ReturnLatencyTaskForm } from './ReturnLatencyTaskForm'

const regions = ['广东', '北京'].flatMap(province => ['中国电信', '中国联通', '中国移动'].map(carrier => ({ province, carrier })))
const servers = [
  { id: 1, name: 'Hong Kong', agent_id: 'agent-1', status: 'online', latency_probe_enabled: true, latency_probe_mode: 'tcp', latency_probe_interval_seconds: 60, latency_probe_sample_count: 3, latency_probe_max_targets: 64, latency_probe_public_target: 'auto' },
  { id: 2, name: 'Tokyo', agent_id: 'agent-2', status: 'offline', latency_probe_enabled: true },
] as Server[]
const tasks: LatencyProbeTask[] = [
  { id: 7, method: 'tcp', address: '', port: 80, name: '广州电信', province: '广东', carrier: '中国电信', interval_seconds: 300, enabled: true, server_ids: [1] },
  { id: 8, method: 'icmp', address: '', port: 0, name: '北京移动巡检', province: '北京', carrier: '中国移动', interval_seconds: 3600, enabled: false, server_ids: [] },
]

describe('return latency probe tasks', () => {
  let container: HTMLDivElement
  let root: Root
  const click = async (element: HTMLElement) => { await act(async () => element.click()) }
  const buttonIn = (scope: ParentNode, label: string) => Array.from(scope.querySelectorAll<HTMLButtonElement>('button')).find(element => element.textContent?.trim() === label)
  const pickOption = async (ariaLabel: string, label: string) => {
    await click(document.body.querySelector<HTMLButtonElement>(`[aria-label="${ariaLabel}"]`)!)
    const option = Array.from(document.body.querySelectorAll<HTMLElement>('[data-option-index]')).find(element => element.textContent?.trim() === label)!
    await click(option)
  }
  const setInput = async (element: HTMLInputElement, value: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(element, value)
      element.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }

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

  it('creates one task per target with its own interval and executing servers', async () => {
    const submit = vi.fn()
    await act(async () => root.render(<ReturnLatencyTaskForm regions={regions} servers={servers} onSubmit={submit} onCancel={() => {}} />))
    expect(container.querySelector('[aria-label="预设目标"]')).toBeNull()
    await setInput(container.querySelector<HTMLInputElement>('[aria-label="目标地址"]')!, 'example.com')
    await click(container.querySelector<HTMLInputElement>('[aria-label="选择 Hong Kong"]')!)
    await click(buttonIn(container, '创建任务')!)
    expect(submit).toHaveBeenCalledWith({ name: 'example.com', method: 'tcp', address: 'example.com', port: 80, province: '', carrier: '', interval_seconds: 300, enabled: true, server_ids: [1] })
  })

  it('keeps an explicit task name so several tasks can share one target', async () => {
    const submit = vi.fn()
    await act(async () => root.render(<ReturnLatencyTaskForm regions={regions} servers={servers} onSubmit={submit} onCancel={() => {}} />))
    await setInput(container.querySelector<HTMLInputElement>('[aria-label="目标地址"]')!, 'example.com')
    await setInput(container.querySelector<HTMLInputElement>('[aria-label="任务名称"]')!, '广州电信 高频')
    await click(buttonIn(container, '创建任务')!)
    expect(submit.mock.calls[0][0]).toMatchObject({ name: '广州电信 高频', address: 'example.com', method: 'tcp' })
  })

  it('requires a complete target before the task can be created', async () => {
    await act(async () => root.render(<ReturnLatencyTaskForm regions={regions} servers={servers} onSubmit={vi.fn()} onCancel={() => {}} />))
    expect(buttonIn(container, '创建任务')!.disabled).toBe(true)
    await setInput(container.querySelector<HTMLInputElement>('[aria-label="目标地址"]')!, 'https://example.com')
    expect(buttonIn(container, '创建任务')!.disabled).toBe(true)
    await click(buttonIn(container, 'HTTP')!)
    expect(buttonIn(container, '创建任务')!.disabled).toBe(false)
  })

  it('filters optional presets and fills just the selected address', async () => {
    const submit = vi.fn()
    const targets = [
      { province: '广东', carrier: '中国电信', address: 'gd.example.com' },
      { province: '北京', carrier: '中国移动', address: 'bj.example.com' },
    ]
    await act(async () => root.render(<ReturnLatencyTaskForm regions={regions} targets={targets} servers={servers} onSubmit={submit} onCancel={() => {}} />))
    await click(buttonIn(container, '从预设中选择')!)
    await pickOption('探测目标省份', '广东')
    await pickOption('探测目标运营商', '中国电信')
    expect(container.querySelectorAll('.probe-preset-option')).toHaveLength(1)
    await click(container.querySelector<HTMLButtonElement>('.probe-preset-option')!)
    expect(container.querySelector('[aria-label="预设目标"]')).toBeNull()
    expect(container.querySelector<HTMLInputElement>('[aria-label="目标地址"]')!.value).toBe('gd.example.com')
    await click(buttonIn(container, '创建任务')!)
    expect(submit.mock.calls[0][0]).toMatchObject({ name: '广东 · 中国电信', address: 'gd.example.com', province: '', carrier: '', method: 'tcp', port: 80 })
  })

  it.each(['Ping', 'HTTP'])('saves %s without presets and reports submission errors in the dialog', async method => {
    const submit = vi.fn().mockRejectedValue(new Error('任务名称已存在'))
    await act(async () => root.render(<ReturnLatencyTaskForm regions={[]} servers={servers} error="资源不可用" onSubmit={submit} onCancel={() => {}} />))
    await click(buttonIn(container, method)!)
    expect(container.querySelector('[aria-label="目标端口"]')).toBeNull()
    const address = method === 'HTTP' ? 'https://example.com/health' : 'example.com'
    await setInput(container.querySelector<HTMLInputElement>('[aria-label="目标地址"]')!, address)
    await click(buttonIn(container, '创建任务')!)
    expect(submit.mock.calls[0][0]).toMatchObject({ address, method: method === 'HTTP' ? 'http' : 'icmp', port: 0 })
    expect(container.querySelector('[role="alert"]')!.textContent).toBe('任务名称已存在')
    expect(buttonIn(container, '创建任务')!.disabled).toBe(false)
  })

  it('switches between separate horizontal task and node tables with the keyboard', async () => {
    const request = vi.fn(async (path: string) => path === '/latency-probe-resource' ? { regions } : { latency_probe_tasks: tasks })
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage={false} onRefresh={() => {}} renderHistory={() => null} />))
    expect(container.querySelector('#probe-targets-panel table')).toBeTruthy()
    expect(container.querySelector('#probe-nodes-panel')).toBeNull()
    expect(container.querySelector<HTMLButtonElement>('[aria-label="编辑 广州电信"]')!.disabled).toBe(true)
    await act(async () => container.querySelector('#probe-targets-tab')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true })))
    expect(container.querySelector('#probe-targets-panel')).toBeNull()
    expect(container.querySelectorAll('#probe-nodes-panel tbody tr')).toHaveLength(2)
    expect(document.activeElement?.id).toBe('probe-nodes-tab')
    expect(container.querySelector('#probe-nodes-panel')!.textContent).toContain('等待结果')
    expect(container.querySelector('#probe-nodes-panel')!.textContent).toContain('离线')
  })

  it('lists tasks by name and target without the removed wizard and subtitle', async () => {
    const request = vi.fn(async (path: string) => path === '/latency-probe-resource' ? { regions } : { latency_probe_tasks: tasks })
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage onRefresh={() => {}} renderHistory={() => null} />))
    expect(container.textContent).toContain('广州电信')
    expect(container.textContent).toContain('广东 · 中国电信')
    expect(container.textContent).toContain('5 分钟')
    expect(container.textContent).toContain('1 小时')
    expect(container.textContent).not.toContain('从服务器探测各省份与运营商')
    expect(container.textContent).not.toContain('1. 选择服务器')
    expect(container.textContent).not.toContain('2. 配置')
    expect(buttonIn(container, '创建探测任务')).toBeTruthy()
  })

  it('toggles and deletes a task through the task endpoints', async () => {
    const request = vi.fn(async (path: string) => path === '/latency-probe-resource' ? { regions } : { latency_probe_tasks: tasks })
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage onRefresh={() => {}} renderHistory={() => null} />))
    const cards = Array.from(container.querySelectorAll('.probe-task-card'))
    await click(buttonIn(cards[0], '停用')!)
    const patch = request.mock.calls.find(([path, init]) => path === '/latency-probe-tasks/7' && (init as RequestInit)?.method === 'PATCH')!
    expect(JSON.parse((patch[1] as RequestInit).body as string)).toEqual({ enabled: false })
    await click(buttonIn(Array.from(container.querySelectorAll('.probe-task-card'))[1], '删除')!)
    expect(request.mock.calls.some(([path, init]) => path === '/latency-probe-tasks/8' && (init as RequestInit)?.method === 'DELETE')).toBe(true)
  })

  it('saves per-server probe parameters without any target list', async () => {
    const request = vi.fn(async (path: string) => path === '/latency-probe-resource' ? { regions } : { latency_probe_tasks: tasks })
    const refresh = vi.fn()
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage onRefresh={refresh} renderHistory={() => null} />))
    await click(container.querySelector<HTMLButtonElement>('#probe-nodes-tab')!)
    const card = Array.from(container.querySelectorAll('.return-latency-server')).find(element => element.textContent?.includes('Hong Kong'))!
    await click(buttonIn(card, '探测参数')!)
    await click(buttonIn(document.body, '保存参数')!)
    const write = request.mock.calls.find(([path, init]) => path === '/servers/1' && (init as RequestInit)?.method === 'PATCH')!
    const body = JSON.parse((write[1] as RequestInit).body as string)
    expect(Object.keys(body).every(key => key.startsWith('latency_probe_'))).toBe(true)
    expect(body).not.toHaveProperty('latency_probe_regions')
    expect(refresh).toHaveBeenCalled()
  })

  it('queues an immediate probe only for an online server with probing enabled', async () => {
    const request = vi.fn(async (path: string) => {
      if (path === '/latency-probe-resource') return { regions }
      if (path === '/latency-probe-tasks') return { latency_probe_tasks: tasks }
      return { task_id: 17 }
    })
    await act(async () => root.render(<ReturnLatencyPage servers={servers} client={{ request }} canManage onRefresh={() => {}} renderHistory={() => null} />))
    await click(container.querySelector<HTMLButtonElement>('#probe-nodes-tab')!)
    const offline = Array.from(container.querySelectorAll('.return-latency-server')).find(element => element.textContent?.includes('Tokyo'))!
    expect(buttonIn(offline, '立即探测')!.disabled).toBe(true)
    const live = Array.from(container.querySelectorAll('.return-latency-server')).find(element => element.textContent?.includes('Hong Kong'))!
    await click(buttonIn(live, '立即探测')!)
    expect(request.mock.calls.map(([path]) => path)).toContain('/servers/1/latency-probe')
    expect(request.mock.calls.map(([path]) => path)).not.toContain('/servers/2/latency-probe')
    expect(container.textContent).toContain('探测已排队 #17')
  })

  it('groups server actions and routes return latency to its own surface', async () => {
    const action = vi.fn()
    await act(async () => root.render(<ServerActionMenu server={servers[0]} role="admin" onAction={action} />))
    await click(container.querySelector('[aria-label="打开服务器操作菜单"]')!)
    const menu = document.body.querySelector('[role="menu"]')!
    expect(menu.textContent).toContain('资料与设置')
    expect(menu.textContent).toContain('监控与诊断')
    expect(menu.textContent).toContain('运维操作')
    const item = Array.from(menu.querySelectorAll('button')).find(element => element.textContent === '网络探测')!
    await click(item)
    expect(action).toHaveBeenCalledWith('return-latency', servers[0])
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })
})

