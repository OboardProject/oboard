// @vitest-environment jsdom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { UserDashboardPage, type UserDashboardOverview } from './UserDashboardPage'

const normalOverview: UserDashboardOverview = {
  assigned_node_count: 4,
  account_status: 'normal',
  status_reasons: [],
  has_active_plan: true,
  traffic: { used_bytes: 512 * 1024 * 1024, limit_bytes: 1024 * 1024 * 1024, quota_state: 'active' },
  audit: { enabled: true, risk: false },
}

describe('UserDashboardPage', () => {
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
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('shows the effective node count, healthy account and current traffic', () => {
    let navigated = false
    act(() => root.render(<UserDashboardPage overview={normalOverview} displayName="小明" onNavigateSubscriptions={() => { navigated = true }} />))

    expect(container.textContent).toContain('欢迎回来，小明')
    expect(container.textContent).toContain('已分配节点4')
    expect(container.textContent).toContain('账号状态正常')
    expect(container.textContent).toContain('512 MB')
    expect(container.textContent).toContain('总量 1.0 GB')
    expect(container.querySelector('.dash-watermark')?.textContent).toBe('4')
    expect(container.textContent).toContain('订阅')
    expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('50')

    const subButton = container.querySelector('.dash-welcome-actions button') as HTMLButtonElement
    expect(subButton).not.toBeNull()
    act(() => subButton.click())
    expect(navigated).toBe(true)
  })

  it('shows only boolean audit status and account attention reasons', () => {
    act(() => root.render(<UserDashboardPage overview={{
      ...normalOverview,
      assigned_node_count: 0,
      account_status: 'attention',
      status_reasons: ['no_active_plan', 'subscription_suspended', 'audit_risk'],
      has_active_plan: false,
      traffic: { used_bytes: 0, limit_bytes: 0, quota_state: 'active' },
      audit: { enabled: true, risk: true },
    }} displayName="用户" />))

    expect(container.textContent).toContain('需要关注')
    expect(container.textContent).toContain('未开通有效套餐 · 订阅已暂停 · 审计判定存在风险')
    expect(container.textContent).toContain('审计存在风险')
    expect(container.textContent).not.toMatch(/风险分|55|100/)
    expect(container.textContent).toContain('总量 未开通')
  })

  it('distinguishes unlimited traffic from disabled audit', () => {
    act(() => root.render(<UserDashboardPage overview={{
      ...normalOverview,
      traffic: { used_bytes: 2048, limit_bytes: 0, quota_state: 'active' },
      audit: { enabled: false, risk: false },
    }} displayName="用户" />))

    expect(container.textContent).toContain('总量 不限量')
    expect(container.textContent).toContain('审计未启用')
    expect(container.querySelector('[role="progressbar"]')).toBeNull()
  })
})
