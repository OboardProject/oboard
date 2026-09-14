// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { LazyMotion, domAnimation } from 'motion/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UserManagement } from '../../main'
import { DialogContext } from '../ui/dialog-context'
const user = (id: number, username: string, status = 'active') => ({ id, username, nickname: '', status, role: 'viewer', traffic_used_bytes: 80, traffic_limit_bytes: 100, speed_limit_mbps: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1, subscription_token: 'test', traffic_period_end: '2026-10-01' })
const data = { session: { role: 'admin' }, users: [user(1, 'alice'), user(2, 'bob', 'disabled')], user_groups: [{ id: 1, name: '标准用户', role: 'viewer', enabled: true }], user_group_members: [{ id: 1, group_id: 1, user_id: 1, enabled: true }], subscription_plans: [{ id: 1, name: '月付套餐', enabled: true }], user_plan_bindings: [{ user_id: 1, plan_id: 1, status: 'active', starts_at: '2020-01-01', expires_at: '2099-01-01' }] }
describe('UserManagement', () => {
  let container: HTMLDivElement
  let root: Root
  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(() => { act(() => root.unmount()); container.remove(); document.body.style.overflow = '' })
  const button = (label: string) => Array.from(document.querySelectorAll('button')).find(item => item.textContent?.trim() === label)!
  const render = async (request = vi.fn(async () => ({})), load = vi.fn(async () => undefined)) => {
    await act(async () => root.render(<LazyMotion features={domAnimation}><DialogContext.Provider value={{ confirm: async () => true, alert: async () => undefined, prompt: async () => null }}><UserManagement data={data} client={{ request }} load={load} notify={() => undefined} /></DialogContext.Provider></LazyMotion>))
    return { request, load }
  }
  it('separates plan expiry from traffic reset and filters accounts', async () => {
    await render()
    expect(container.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(container.textContent).toContain('流量重置')
    expect(container.textContent).toContain('到期')
    expect(container.textContent).not.toContain('待开始')
    expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('80')
    await act(async () => (container.querySelectorAll('.user-overview-stat')[1] as HTMLButtonElement).click())
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1)
    expect(container.querySelector('tbody')?.textContent).toContain('bob')
    expect(container.querySelector('tbody')?.textContent).toContain('已停用')
  })
  it('keeps failed edits open, blocks duplicate saves and refreshes after success', async () => {
    let reject: (reason: Error) => void = () => undefined
    const request = vi.fn(() => new Promise<any>((_, no) => { reject = no }))
    const load = vi.fn(async () => undefined)
    await render(request, load)
    await act(async () => (container.querySelector('[aria-label="编辑 alice"]') as HTMLButtonElement).click())
    await act(async () => { const save = button('保存更改'); save.click(); save.click() })
    expect(request).toHaveBeenCalledTimes(1)
    expect(button('保存中…').disabled).toBe(true)
    await act(async () => reject(new Error('保存被拒绝')))
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('保存被拒绝')
    expect(button('保存更改')).toBeTruthy()
    expect(load).not.toHaveBeenCalled()
    request.mockResolvedValue({})
    await act(async () => button('保存更改').click())
    expect(load).toHaveBeenCalledTimes(1)
  })
  it('filters by group and removes a member only after confirmation', async () => {
    const { request, load } = await render()
    await act(async () => (container.querySelector('.user-scope-item-name')!.closest('button') as HTMLButtonElement).click())
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1)
    await act(async () => button('管理成员').click())
    await act(async () => button('移出用户组').click())
    expect(request).toHaveBeenCalledWith('/user-group-members/1', { method: 'DELETE' })
    expect(load).toHaveBeenCalledTimes(1)
  })
})
