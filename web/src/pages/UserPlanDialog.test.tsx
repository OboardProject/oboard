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
  })
})
