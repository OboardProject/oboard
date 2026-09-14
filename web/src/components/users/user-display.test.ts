import { describe, expect, it } from 'vitest'
import { userAccountDisplay, userPlanDisplay, userUsageDisplay } from './user-display'
const now = Date.parse('2026-09-14T00:00:00Z')
describe('user overview display', () => {
  it('never labels a disabled or over-quota account as normal', () => {
    expect(userAccountDisplay({ status: 'disabled' })).toMatchObject({ label: '已停用', attention: true })
    expect(userAccountDisplay({ status: 'active', traffic_quota_state: 'quota_exceeded' }).label).toBe('流量已用完')
  })
  it('distinguishes past starts, future starts, expiry and pending delivery', () => {
    expect(userPlanDisplay({ starts_at: '2026-09-01', status: 'active' }, {}, now).label).toBe('有效')
    expect(userPlanDisplay({ starts_at: '2026-10-01' }, {}, now).label).toBe('待开始')
    expect(userPlanDisplay({ expires_at: '2026-09-14T00:00:00Z' }, {}, now).label).toBe('已到期')
    expect(userPlanDisplay({ status: 'pending' }, {}, now).label).toBe('等待同步')
    expect(userPlanDisplay({ expires_at: '2026-09-20' }, {}, now)).toMatchObject({ expiring: true, expiry: '剩余 6 天' })
    expect(userPlanDisplay({ expires_at: '2026-09-20' }, { enabled: false }, now).expiring).toBe(false)
  })
  it('bounds progress without concealing overage or treating unlimited as full', () => {
    expect(userUsageDisplay(120, 100)).toMatchObject({ percent: 120, progress: 100, remaining: 0, tone: 'danger' })
    expect(userUsageDisplay(80, 100).tone).toBe('warning')
    expect(userUsageDisplay(100, 0)).toMatchObject({ bounded: false, remaining: null })
  })
})
