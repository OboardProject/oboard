export type UserDisplayTone = 'success' | 'warning' | 'danger' | 'neutral'
export type UserPlanDisplayBinding = { status?: string; enabled?: boolean; starts_at?: string; expires_at?: string }

export function userAccountDisplay(user: { status: string; traffic_quota_state?: string }) {
  if (user.status === 'disabled') return { label: '已停用', tone: 'neutral' as const, attention: true }
  if (user.status === 'suspended') return { label: '已暂停', tone: 'danger' as const, attention: true }
  if (user.traffic_quota_state === 'quota_exceeded') return { label: '流量已用完', tone: 'danger' as const, attention: true }
  if (user.status === 'active') return { label: '正常', tone: 'success' as const, attention: false }
  return { label: '状态未知', tone: 'warning' as const, attention: true }
}

export function userPlanDisplay(binding?: UserPlanDisplayBinding, plan?: { enabled?: boolean }, now = Date.now()): { label: string; tone: UserDisplayTone; expiring: boolean; expiry: string } {
  if (!binding) return { label: '未分配套餐', tone: 'neutral', expiring: false, expiry: '—' }
  const expiry = binding.expires_at ? new Date(binding.expires_at).getTime() : null
  const start = binding.starts_at ? new Date(binding.starts_at).getTime() : null
  const remainingDays = expiry !== null && Number.isFinite(expiry) ? Math.ceil((expiry - now) / 86400000) : null
  const expiryLabel = remainingDays === null ? binding.expires_at ? '到期时间未知' : '长期有效' : remainingDays <= 0 ? '已到期' : `剩余 ${remainingDays} 天`
  const result = { expiry: expiryLabel, expiring: false }
  if (binding.enabled === false || plan?.enabled === false) return { ...result, label: '已停用', tone: 'neutral' }
  if (!plan) return { ...result, label: '套餐不可用', tone: 'warning' }
  if (expiry !== null && expiry <= now) return { ...result, label: '已到期', tone: 'danger' }
  if (start !== null && start > now) return { ...result, label: '待开始', tone: 'neutral' }
  if (binding.status && binding.status !== 'active') return { ...result, label: binding.status === 'pending' ? '等待同步' : '待确认', tone: 'warning' }
  const expiring = remainingDays !== null && remainingDays > 0 && remainingDays <= 7
  return { ...result, expiring, label: expiring ? '即将到期' : '有效', tone: expiring ? 'warning' : 'success' }
}

export function userUsageDisplay(used: number, limit: number) {
  const bytes = Math.max(0, Number(used) || 0)
  const bounded = limit > 0
  const percent = bounded ? bytes / limit * 100 : 0
  return { bytes, bounded, percent, progress: Math.min(100, Math.max(0, percent)), remaining: bounded ? Math.max(0, limit - bytes) : null, tone: percent >= 100 ? 'danger' : percent >= 80 ? 'warning' : 'success' }
}
