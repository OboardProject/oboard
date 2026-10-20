export type ServerExpiryTone = 'muted' | 'ok' | 'warning' | 'danger'

export type ServerExpiryStatus = {
  label: string
  tone: ServerExpiryTone
}

function pad(value: number) {
  return String(value).padStart(2, '0')
}

export function serverExpiryInputValue(iso?: string) {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

export function serverExpiryOutputValue(date: string) {
  if (!date) return undefined
  const [year, month, day] = date.split('-').map(Number)
  if (!year || !month || !day) return undefined
  return new Date(year, month - 1, day, 0, 0, 0, 0).toISOString()
}

export function serverExpiryStatusValue(expiresAt?: string, autoRenewEnabled = false, now = new Date()): ServerExpiryStatus {
  if (!expiresAt) return { label: '未设置', tone: 'muted' }
  const date = new Date(expiresAt)
  if (Number.isNaN(date.getTime())) return { label: '未设置', tone: 'muted' }
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const expiry = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  const days = Math.round((expiry.getTime() - today.getTime()) / 86400000)
  if (days < 0) {
    return autoRenewEnabled
      ? { label: '等待自动续期', tone: 'warning' }
      : { label: '已到期', tone: 'danger' }
  }
  if (days === 0) return { label: '今天到期', tone: 'danger' }
  if (days < 7) return { label: `${days} 天后到期`, tone: 'danger' }
  if (days < 15) return { label: `${days} 天后到期`, tone: 'warning' }
  return { label: `${days} 天后到期`, tone: 'ok' }
}

export function serverExpiryDateLabel(iso?: string) {
  if (!iso) return '未设置'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '未设置'
  return date.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' })
}

export function addDaysToExpiryDate(value: string, days: number) {
  const date = value ? new Date(`${value}T00:00:00`) : new Date()
  date.setDate(date.getDate() + days)
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

// Calendar-month extension: same day of the target month, clamped to its last day.
export function addMonthsToExpiryDate(value: string, months: number) {
  const base = value ? new Date(`${value}T00:00:00`) : new Date()
  const target = new Date(base.getFullYear(), base.getMonth() + months, 1)
  const lastDay = new Date(target.getFullYear(), target.getMonth() + 1, 0).getDate()
  target.setDate(Math.min(base.getDate(), lastDay))
  return `${target.getFullYear()}-${pad(target.getMonth() + 1)}-${pad(target.getDate())}`
}

export type ServerExpiryExtension = { days: number; months?: never } | { months: number; days?: never }

export const serverExpiryQuickExtensions: ServerExpiryExtension[] = [{ days: 3 }, { days: 7 }, { months: 1 }, { months: 3 }, { months: 12 }]

export function applyServerExpiryExtension(value: string, extension: ServerExpiryExtension) {
  return extension.months ? addMonthsToExpiryDate(value, extension.months) : addDaysToExpiryDate(value, extension.days || 0)
}

export function serverExpiryExtensionLabel(extension: ServerExpiryExtension) {
  if (!extension.months) return `${extension.days} 天`
  return extension.months % 12 === 0 ? `${extension.months / 12} 年` : `${extension.months} 个月`
}
