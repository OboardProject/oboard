// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DialogContext, type DialogApi } from '../components/ui/dialog-context'
import { NodeWorkspacePage } from './NodeWorkspacePage'

async function flushEffects() {
  await act(async () => { await new Promise(resolve => window.setTimeout(resolve, 0)) })
}

function renderWithDialogs(root: Root, node: React.ReactNode, dialogs: DialogApi) {
  root.render(<DialogContext.Provider value={dialogs}>{node}</DialogContext.Provider>)
}

describe('NodeWorkspacePage', () => {
  let container: HTMLDivElement
  let root: Root
  let dialogs: DialogApi

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    dialogs = {
      alert: vi.fn(async () => undefined),
      confirm: vi.fn(async () => true),
      prompt: vi.fn(async () => null),
    }
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('loads a role-none user workspace and exposes the three accessible tabs', async () => {
    const workspace = {
      subject: { id: 7, username: 'alice' },
      node_groups: [
        { id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 1 },
        { id: 2, kind: 'manual', name: '机场 B', node_count: 2 },
        { id: 3, kind: 'manual', name: '自建 C', node_count: 3 },
      ],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1, 2, 3] }],
    }
    const request = vi.fn(async (path: string) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [{ id: 'proxy_path:1', group_id: 1, name: '香港 01', protocol: 'vless', source: 'oboard', copyable: true }] }
      throw new Error(`unexpected request: ${path}`)
    })
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'none' }, current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />, dialogs)
    })
    await flushEffects()

    const tabs = Array.from(container.querySelectorAll('[role="tab"]'))
    expect(tabs.map(tab => tab.textContent)).toEqual(['节点库', '节点组', '组合订阅'])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(container.textContent).toContain('香港 01')

    act(() => (tabs[1] as HTMLButtonElement).click())
    expect(tabs[1].getAttribute('aria-selected')).toBe('true')
    expect(container.textContent).toContain('系统组')

    act(() => (tabs[2] as HTMLButtonElement).click())
    expect(container.textContent).toContain('默认组合')
    expect(container.textContent).toContain('复制订阅')
    expect(Array.from(container.querySelectorAll('.output-group-row > span')).map(item => item.textContent)).toEqual(['1. OBoard', '2. 机场 B', '3. 自建 C'])
    act(() => (container.querySelector('[aria-label="上移 自建 C"]') as HTMLButtonElement).click())
    expect(Array.from(container.querySelectorAll('.output-group-row > span')).map(item => item.textContent)).toEqual(['1. OBoard', '2. 自建 C', '3. 机场 B'])
  })

  it('edits subscription output filters and shows preview filter stats', async () => {
    const workspace = {
      subject: { id: 7, username: 'alice' },
      node_groups: [
        { id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 1 },
        { id: 2, kind: 'manual', name: '机场 B', node_count: 2 },
      ],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1, 2], filters: [{ type: 'drop_name', value: '广告' }] }],
    }
    const patched: any[] = []
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [] }
      if (path === '/subscription-outputs/2' && init?.method === 'PATCH') {
        patched.push(JSON.parse(String(init.body)))
        return { subscription_output: workspace.subscription_outputs[0] }
      }
      if (path === '/subscription-outputs/2/preview') {
        return {
          preview: {
            nodes: [{ id: 'private:1', name: '香港 01' }],
            content: '{}',
            filter_dropped: 1,
            filter_stats: [{ type: 'drop_name', value: '广告', matched: 1, dropped: 1, remaining: 1 }],
          },
          deduplicated_count: 0,
        }
      }
      throw new Error(`unexpected request: ${path} ${init?.method || ''}`)
    })
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'none' }, current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />, dialogs)
    })
    await flushEffects()
    act(() => (Array.from(container.querySelectorAll('[role="tab"]'))[2] as HTMLButtonElement).click())

    expect(container.textContent).toContain('过滤规则')
    expect(container.querySelectorAll('.output-filter-row').length).toBe(1)
    expect(container.querySelector('.output-filter-row input')?.getAttribute('value')).toBe('广告')

    const valueInput = () => container.querySelector('.output-filter-row input') as HTMLInputElement
    act(() => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!.call(valueInput(), '广告|测试')
      valueInput().dispatchEvent(new window.Event('input', { bubbles: true }))
    })
    act(() => (Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('添加规则')) as HTMLButtonElement).click())
    expect(container.querySelectorAll('.output-filter-row').length).toBe(2)
    act(() => (container.querySelectorAll('.output-filter-row select')[1] as HTMLSelectElement).value = 'drop_protocol')
    act(() => (container.querySelectorAll('.output-filter-row select')[1] as HTMLSelectElement).dispatchEvent(new window.Event('change', { bubbles: true })))
    act(() => (container.querySelectorAll('.output-filter-row select')[2] as HTMLSelectElement).value = 'trojan')
    act(() => (container.querySelectorAll('.output-filter-row select')[2] as HTMLSelectElement).dispatchEvent(new window.Event('change', { bubbles: true })))
    act(() => (Array.from(container.querySelectorAll('.output-actions button')).find(button => button.textContent?.includes('保存')) as HTMLButtonElement).click())
    await flushEffects()

    expect(patched.length).toBe(1)
    expect(patched[0].filters).toEqual([{ type: 'drop_name', value: '广告|测试' }, { type: 'drop_protocol', value: 'trojan' }])
    expect(patched[0].group_ids).toEqual([1, 2])

    act(() => (Array.from(container.querySelectorAll('.output-actions button')).find(button => button.textContent?.includes('预览')) as HTMLButtonElement).click())
    await flushEffects()
    expect(container.textContent).toContain('规则过滤 1 个')
    expect(container.querySelector('.filter-stat')?.textContent).toContain('名字正则排除')
  })

  it('edits a manual node and previews a remote subscription URL', async () => {
    const workspace = {
      subject: { id: 7, username: 'alice' },
      node_groups: [
        { id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 0 },
        { id: 2, kind: 'manual', name: '机场 B', node_count: 1 },
      ],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1, 2] }],
    }
    const requests: Array<{ path: string; body?: string; method?: string }> = []
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      requests.push({ path, body: String(init?.body || ''), method: init?.method })
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [{ id: 'private:1', group_id: 2, name: '香港 01', protocol: 'trojan', source: 'private', copyable: true, editable: true }] }
      if (path === '/node-library/private:1' && init?.method === 'PATCH') return { node: { id: 'private:1', name: '香港 02', protocol: 'trojan', enabled: true } }
      if (path === '/node-import-preview') return { nodes: [{ name: '预览节点', protocol: 'vless', fingerprint: 'fp' }], issues: [] }
      throw new Error(`unexpected request: ${path} ${init?.method || ''}`)
    })
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'none' }, current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />, dialogs)
    })
    await flushEffects()

    act(() => (container.querySelector('[aria-label="编辑 香港 01"]') as HTMLButtonElement).click())
    const nameInput = container.querySelector('.node-edit-form input') as HTMLInputElement
    act(() => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!.call(nameInput, '香港 02')
      nameInput.dispatchEvent(new window.Event('input', { bubbles: true }))
    })
    act(() => (Array.from(container.querySelectorAll('.node-edit-form button')).find(button => button.textContent?.includes('保存')) as HTMLButtonElement).click())
    await flushEffects()
    expect(requests).toContainEqual({ path: '/node-library/private:1', body: JSON.stringify({ name: '香港 02', content: '' }), method: 'PATCH' })

    act(() => (Array.from(container.querySelectorAll('[role="tab"]'))[1] as HTMLButtonElement).click())
    const urlInput = container.querySelector('.node-group-create input[type="url"]') as HTMLInputElement
    act(() => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!.call(urlInput, 'https://example.com/sub')
      urlInput.dispatchEvent(new window.Event('input', { bubbles: true }))
    })
    act(() => (Array.from(container.querySelectorAll('.node-import-actions button')).find(button => button.textContent?.includes('预览')) as HTMLButtonElement).click())
    await flushEffects()
    expect(requests).toContainEqual({ path: '/node-import-preview', body: JSON.stringify({ url: 'https://example.com/sub' }), method: 'POST' })
    expect(container.textContent).toContain('可导入 1 个节点')
  })

  it('switches to the administrator global view when the session role arrives after mount', async () => {
    const workspace = {
      subject: { id: 1, username: 'admin' },
      node_groups: [{ id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 0 }],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1] }],
    }
    const request = vi.fn(async (path: string) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [] }
      if (path.startsWith('/assignable-nodes?')) return { nodes: [], total: 0, page: 1, page_size: 50 }
      throw new Error(`unexpected request: ${path}`)
    })
    const load = vi.fn().mockResolvedValue(undefined)
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ current_user: { id: 1 } }} client={{ request }} load={load} />, dialogs)
    })
    await flushEffects()
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'admin' }, current_user: { id: 1 }, users: [{ id: 1, username: 'admin', status: 'active' }], servers: [], subscription_plans: [] }} client={{ request }} load={load} />, dialogs)
    })
    await flushEffects()

    expect(container.querySelector('[aria-label="节点管理模式"] [aria-pressed="true"]')?.textContent).toBe('全部节点')
    expect(container.querySelector('[role="tablist"]')).toBeNull()
  })

  it('never shows the platform-user layout before the administrator page data arrives', async () => {
    const request = vi.fn(async (path: string) => {
      if (path.startsWith('/assignable-nodes?')) return { nodes: [], total: 0, page: 1, page_size: 50 }
      throw new Error(`unexpected request: ${path}`)
    })
    const load = vi.fn().mockResolvedValue(undefined)
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{}} client={{ request }} load={load} sessionUser={{ role: 'admin' }} />, dialogs)
    })
    await flushEffects()

    expect(container.querySelector('[role="tablist"]')).toBeNull()
    expect(request.mock.calls.every(([path]) => !String(path).startsWith('/node-workspace') && !String(path).startsWith('/node-library'))).toBe(true)

    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'admin' }, current_user: { id: 1 }, users: [], servers: [], subscription_plans: [] }} client={{ request }} load={load} sessionUser={{ role: 'admin' }} />, dialogs)
    })
    await flushEffects()

    expect(container.querySelector('[aria-label="节点管理模式"] [aria-pressed="true"]')?.textContent).toBe('全部节点')
    expect(container.querySelector('[role="tablist"]')).toBeNull()
  })

  it('waits for the role instead of rendering the platform-user layout when it is unknown', async () => {
    const request = vi.fn(async (path: string) => { throw new Error(`unexpected request: ${path}`) })
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />, dialogs)
    })
    await flushEffects()

    expect(container.querySelector('[role="tablist"]')).toBeNull()
    expect(container.textContent).toContain('正在加载节点')
    expect(request).not.toHaveBeenCalled()
  })

  it('edits node groups including remote subscription URLs and manual node links', async () => {
    const workspace = {
      subject: { id: 7, username: 'alice' },
      node_groups: [
        { id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 1 },
        { id: 2, kind: 'manual', name: '机场 B', node_count: 1 },
        { id: 3, kind: 'remote', name: 'DDG', node_count: 0 },
      ],
      node_sources: [{ id: 9, group_id: 3, url_display: 'https://mweujowynb.rini.ma/...', status: 'ready', last_success_at: null }],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1] }],
    }
    const patched: Array<Record<string, unknown>> = []
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [] }
      if (path.startsWith('/node-groups/') && init?.method === 'PATCH') {
        const payload = JSON.parse(String(init.body)) as Record<string, unknown>
        patched.push({ path, ...payload })
        return { node_group: { id: Number(path.split('/')[2]) } }
      }
      throw new Error(`unexpected request: ${path} ${init?.method || ''}`)
    })
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'none' }, current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />, dialogs)
    })
    await flushEffects()
    act(() => (Array.from(container.querySelectorAll('[role="tab"]'))[1] as HTMLButtonElement).click())

    expect(container.textContent).toContain('DDG')
    act(() => (container.querySelector('[aria-label="编辑 DDG"]') as HTMLButtonElement).click())
    const remoteForm = container.querySelector('.node-group-edit') as HTMLFormElement
    expect(remoteForm).not.toBeNull()
    expect(remoteForm.textContent).toContain('HTTPS 订阅 URL')
    expect(remoteForm.textContent).toContain('当前 https://mweujowynb.rini.ma/...')
    const remoteURLInput = remoteForm.querySelector('input[type="url"]') as HTMLInputElement
    act(() => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!.call(remoteURLInput, 'https://new.example.com/sub')
      remoteURLInput.dispatchEvent(new window.Event('input', { bubbles: true }))
    })
    act(() => (Array.from(remoteForm.querySelectorAll('button')).find(button => button.textContent?.includes('保存')) as HTMLButtonElement).click())
    await flushEffects()
    expect(patched).toContainEqual({ path: '/node-groups/3', name: 'DDG', url: 'https://new.example.com/sub' })
    expect(container.querySelector('.node-group-edit')).toBeNull()

    act(() => (container.querySelector('[aria-label="编辑 机场 B"]') as HTMLButtonElement).click())
    const manualForm = container.querySelector('.node-group-edit') as HTMLFormElement
    expect(manualForm.textContent).toContain('新增节点链接')
    expect(manualForm.querySelector('input[type="url"]')).toBeNull()
    const textarea = manualForm.querySelector('textarea') as HTMLTextAreaElement
    act(() => {
      Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value')!.set!.call(textarea, 'ss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Extra-One')
      textarea.dispatchEvent(new window.Event('input', { bubbles: true }))
    })
    act(() => (Array.from(manualForm.querySelectorAll('button')).find(button => button.textContent?.includes('保存')) as HTMLButtonElement).click())
    await flushEffects()
    expect(patched).toContainEqual({ path: '/node-groups/2', name: '机场 B', content: 'ss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Extra-One' })

    act(() => (container.querySelector('[aria-label="编辑 OBoard"]') as HTMLButtonElement).click())
    const systemForm = container.querySelector('.node-group-edit') as HTMLFormElement
    expect(systemForm.querySelector('input[type="url"]')).toBeNull()
    expect(systemForm.querySelector('textarea')).toBeNull()
    act(() => (Array.from(systemForm.querySelectorAll('button')).find(button => button.textContent?.includes('取消')) as HTMLButtonElement).click())
    expect(container.querySelector('.node-group-edit')).toBeNull()
    expect(container.querySelectorAll('[aria-label^="编辑 "]').length).toBe(3)
  })

  it('waits for shared confirmation before deleting a node group', async () => {
    const workspace = {
      subject: { id: 7, username: 'alice' },
      node_groups: [
        { id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 1 },
        { id: 2, kind: 'manual', name: '机场 B', node_count: 2 },
      ],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1, 2] }],
    }
    let resolveConfirm!: (value: boolean) => void
    dialogs.confirm = vi.fn(() => new Promise<boolean>(resolve => { resolveConfirm = resolve }))
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [] }
      if (path === '/node-groups/2' && init?.method === 'DELETE') return {}
      throw new Error(`unexpected request: ${path} ${init?.method || ''}`)
    })
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'none' }, current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />, dialogs)
    })
    await flushEffects()
    act(() => (Array.from(container.querySelectorAll('[role="tab"]'))[1] as HTMLButtonElement).click())
    act(() => (container.querySelector('[aria-label="删除 机场 B"]') as HTMLButtonElement).click())

    expect(dialogs.confirm).toHaveBeenCalledWith(expect.objectContaining({ title: '删除节点组“机场 B”？', tone: 'danger' }))
    expect(request.mock.calls.some(([path, init]) => path === '/node-groups/2' && init?.method === 'DELETE')).toBe(false)

    await act(async () => {
      resolveConfirm(true)
      await new Promise(resolve => window.setTimeout(resolve, 0))
    })
    await flushEffects()
    expect(request.mock.calls.some(([path, init]) => path === '/node-groups/2' && init?.method === 'DELETE')).toBe(true)
  })

  it('creates a combination from the shared prompt result', async () => {
    const workspace = {
      subject: { id: 7, username: 'alice' },
      node_groups: [{ id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 1 }],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1] }],
    }
    dialogs.prompt = vi.fn(async () => '新组合')
    const created: Array<Record<string, unknown>> = []
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [] }
      if (path === '/subscription-outputs' && init?.method === 'POST') {
        created.push(JSON.parse(String(init.body)))
        return {}
      }
      throw new Error(`unexpected request: ${path} ${init?.method || ''}`)
    })
    await act(async () => {
      renderWithDialogs(root, <NodeWorkspacePage data={{ session: { role: 'none' }, current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />, dialogs)
    })
    await flushEffects()
    act(() => (Array.from(container.querySelectorAll('[role="tab"]'))[2] as HTMLButtonElement).click())
    act(() => (container.querySelector('[aria-label="新建组合"]') as HTMLButtonElement).click())
    await flushEffects()

    expect(dialogs.prompt).toHaveBeenCalledWith(expect.objectContaining({ title: '新建组合订阅', confirmText: '创建' }))
    expect(created).toEqual([{ name: '新组合', group_ids: [1] }])
  })

})
