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
    expect(dialog?.textContent).toContain('当前套餐')
    expect(dialog?.textContent).toContain('更换套餐')
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
})
