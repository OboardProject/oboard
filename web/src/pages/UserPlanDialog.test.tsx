// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UserPlanDialog } from './UserPlanDialog'

async function flushEffects() {
  await act(async () => {
    await new Promise(resolve => window.setTimeout(resolve, 0))
  })
}

describe('UserPlanDialog', () => {
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
    document.querySelectorAll('.dialog-root').forEach(element => element.remove())
    container.remove()
    document.body.style.overflow = ''
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('uses a two-column desktop layout and keeps close outside the table', async () => {
    const request = vi.fn(async (path: string) => {
      if (path === '/users/7/nodes') return { nodes: [] }
      if (path.startsWith('/user-node-exceptions?')) return { user_node_exceptions: [] }
      throw new Error(`unexpected request: ${path}`)
    })

    await act(async () => {
      root.render(
        <UserPlanDialog
          isOpen
          user={{ id: 7, username: 'TEST' }}
          plans={[{ id: 1, name: '标准套餐', enabled: true }]}
          client={{ request }}
          onClose={() => undefined}
        />,
      )
    })
    await flushEffects()

    const dialog = document.body.querySelector('.user-plan-dialog')
    expect(dialog).toBeTruthy()
    expect(dialog?.querySelector('.user-plan-dialog-layout')).toBeTruthy()
    expect(dialog?.querySelectorAll('.user-plan-dialog-col')).toHaveLength(2)
    expect(dialog?.textContent).toContain('现有套餐')
    expect(dialog?.textContent).toContain('套餐分配')
    const leftSections = dialog?.querySelectorAll('.user-plan-dialog-col:first-child h3')
    expect(Array.from(leftSections || [], section => section.textContent)).toEqual(['套餐分配', '现有套餐'])
    expect(dialog?.textContent).toContain('有效节点')
    expect(dialog?.textContent).toContain('用户授权')

    const table = dialog?.querySelector('.user-plan-dialog-table')
    const footer = dialog?.querySelector('.dialog-chrome-foot')
    expect(table).toBeTruthy()
    expect(footer?.textContent).toContain('关闭')
    expect(table?.contains(footer as Node)).toBe(false)
    expect(dialog?.querySelector('.user-data-table')).toBeNull()
    expect(dialog?.textContent).not.toContain('预览影响')
    expect(dialog?.textContent).toContain('保存套餐')
  })

  it('saves a plan assignment without a preview step', async () => {
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/users/7/nodes') return { nodes: [] }
      if (path.startsWith('/user-node-exceptions?')) return { user_node_exceptions: [] }
      if (path === '/users/plan-assignment/apply' && init?.method === 'POST') {
        return { access_change_id: 33, status: 'queued' }
      }
      if (path === '/access-changes/33') return { change_id: 33, status: 'queued' }
      throw new Error(`unexpected request: ${path}`)
    })

    await act(async () => {
      root.render(
        <UserPlanDialog
          isOpen
          user={{ id: 7, username: 'TEST' }}
          binding={{ user_id: 7, plan_id: 1 }}
          plans={[{ id: 1, name: '标准套餐', enabled: true }]}
          client={{ request }}
          onClose={() => undefined}
        />,
      )
    })
    await flushEffects()

    const saveButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent === '保存套餐')
    expect(saveButton).toBeTruthy()
    act(() => saveButton?.click())
    await flushEffects()

    expect(request).toHaveBeenCalledWith('/users/plan-assignment/apply', expect.objectContaining({ method: 'POST' }))
    expect(request.mock.calls.some(([path]) => path === '/users/plan-assignment/preview')).toBe(false)
    expect(document.body.textContent).toContain('已保存分配：变更 #33')
  })

  it('removes only this user’s assignment after confirmation and refreshes the current plan', async () => {
    let removed = false
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/users/7/nodes') return { nodes: [] }
      if (path.startsWith('/user-node-exceptions?')) return { user_node_exceptions: [] }
      if (path === '/users/plan-assignment/apply' && init?.method === 'POST') {
        removed = true
        return { access_change_id: 34, status: 'queued' }
      }
      if (path === '/access-changes/34') return { change_id: 34, status: 'queued' }
      throw new Error(`unexpected request: ${path}`)
    })
    const refresh = vi.fn()
    function Harness() {
      const [binding, setBinding] = React.useState<{ user_id: number; plan_id: number } | undefined>({ user_id: 7, plan_id: 1 })
      return <UserPlanDialog isOpen user={{ id: 7, username: 'TEST' }} binding={binding}
        plans={[{ id: 1, name: '标准套餐', enabled: true }]} client={{ request }}
        onRefresh={async () => { refresh(); if (removed) setBinding(undefined) }} onClose={() => undefined} />
    }
    await act(async () => root.render(<Harness />))
    await flushEffects()
    const remove = document.body.querySelector<HTMLButtonElement>('[aria-label="移除套餐 标准套餐"]')
    expect(remove).toBeTruthy()
    act(() => remove?.click())
    await flushEffects()
    expect(request.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
    expect(document.body.textContent).toContain('套餐本身和单独设置的用户授权会保留')
    const cancel = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent === '取消')
    act(() => cancel?.click())
    await flushEffects()
    expect(removed).toBe(false)
    act(() => remove?.click())
    await flushEffects()
    const confirm = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent === '确认移除')
    act(() => confirm?.click())
    await flushEffects()
    expect(request).toHaveBeenCalledWith('/users/plan-assignment/apply', {
      method: 'POST', body: JSON.stringify({ user_ids: [7], plan_id: 0 }),
    })
    expect(request.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
    expect(refresh).toHaveBeenCalled()
    expect(document.querySelector('.user-plan-dialog')?.textContent).toContain('未绑定套餐')
    expect(document.querySelector('.user-plan-dialog')?.textContent).toContain('已提交移除套餐（变更 #34）')
  })

  it('resolves authorization names even for denied nodes outside the effective list', async () => {
    const request = vi.fn(async (path: string) => {
      if (path === '/users/7/nodes') return { nodes: [{ key: 'inbound:3', node_type: 'inbound', node_id: 3, name: '🇸🇬 独立节点', source: 'plan' }] }
      if (path.startsWith('/user-node-exceptions?')) return { user_node_exceptions: [
        { id: 4, user_id: 7, node_type: 'proxy_path', node_id: 20, effect: 'deny', reason: '限制访问' },
        { id: 5, user_id: 7, node_type: 'inbound', node_id: 3, effect: 'allow', reason: '单独授权' },
      ] }
      if (path === '/assignable-nodes/proxy_path/20') return { node: { key: 'proxy_path:20', name: '🇰🇷 韩国节点' } }
      throw new Error(`unexpected request: ${path}`)
    })
    await act(async () => root.render(<UserPlanDialog isOpen user={{ id: 7, username: 'TEST' }} plans={[]} client={{ request }} onClose={() => undefined} />))
    await flushEffects()
    const table = document.querySelector('.user-plan-dialog-table')
    expect(table?.textContent).toContain('🇰🇷 韩国节点')
    expect(table?.textContent).toContain('🇸🇬 独立节点')
    expect(table?.textContent).not.toContain('proxy_path:20')
    expect(table?.textContent).not.toContain('inbound:3')
    expect(request).not.toHaveBeenCalledWith('/assignable-nodes/inbound/3')
  })

  it('keeps the assigned plan and shows a removal failure inside the confirmation', async () => {
    const request = vi.fn(async (path: string) => {
      if (path === '/users/7/nodes') return { nodes: [] }
      if (path.startsWith('/user-node-exceptions?')) return { user_node_exceptions: [] }
      throw new Error('服务暂时不可用')
    })
    await act(async () => root.render(<UserPlanDialog isOpen user={{ id: 7, username: 'TEST' }}
      binding={{ user_id: 7, plan_id: 1 }} plans={[{ id: 1, name: '标准套餐', enabled: true }]}
      client={{ request }} onClose={() => undefined} />))
    await flushEffects()
    act(() => document.querySelector<HTMLButtonElement>('[aria-label="移除套餐 标准套餐"]')?.click())
    await flushEffects()
    const confirm = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent === '确认移除')
    act(() => confirm?.click())
    await flushEffects()
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('服务暂时不可用')
    expect(document.querySelector('[aria-label="移除套餐 标准套餐"]')).toBeTruthy()
    expect(confirm?.disabled).toBe(false)
  })
})
