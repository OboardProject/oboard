import { describe, expect, it } from 'vitest'
import { addDaysToExpiryDate, serverExpiryDateLabel, serverExpiryInputValue, serverExpiryOutputValue, serverExpiryStatusValue } from './server-expiry'

describe('server expiry helpers', () => {
  it('converts RFC3339 to a local date input and back', () => {
    const input = serverExpiryInputValue('2026-09-01T00:00:00Z')
    expect(input).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    const output = serverExpiryOutputValue(input)
    expect(output).toBeTruthy()
    expect(serverExpiryInputValue(output)).toBe(input)
  })

  it('derives statuses from expiry date and auto renewal', () => {
    const now = new Date(2026, 7, 2)
    expect(serverExpiryStatusValue(undefined, false, now)).toEqual({ label: '未设置', tone: 'muted' })
    expect(serverExpiryStatusValue('2026-08-01T00:00:00Z', false, now)).toEqual({ label: '已到期', tone: 'danger' })
    expect(serverExpiryStatusValue('2026-08-01T00:00:00Z', true, now)).toEqual({ label: '等待自动续期', tone: 'warning' })
    expect(serverExpiryStatusValue('2026-08-02T00:00:00Z', true, now)).toEqual({ label: '今天到期', tone: 'danger' })
    expect(serverExpiryStatusValue('2026-08-05T00:00:00Z', true, now)).toEqual({ label: '3 天后到期', tone: 'danger' })
    expect(serverExpiryStatusValue('2026-08-09T00:00:00Z', true, now)).toEqual({ label: '7 天后到期', tone: 'danger' })
    expect(serverExpiryStatusValue('2026-08-10T00:00:00Z', true, now)).toEqual({ label: '8 天后到期', tone: 'warning' })
    expect(serverExpiryStatusValue('2026-08-17T00:00:00Z', true, now)).toEqual({ label: '15 天后到期', tone: 'warning' })
    expect(serverExpiryStatusValue('2026-08-18T00:00:00Z', true, now)).toEqual({ label: '16 天后到期', tone: 'ok' })
    expect(serverExpiryStatusValue('2026-09-02T00:00:00Z', true, now)).toEqual({ label: '31 天后到期', tone: 'ok' })
  })

  it('formats labels and adds calendar days', () => {
    expect(serverExpiryDateLabel('2026-09-01T00:00:00Z')).not.toBe('未设置')
    expect(addDaysToExpiryDate('2026-08-01', 3)).toBe('2026-08-04')
    expect(addDaysToExpiryDate('2026-08-31', 1)).toBe('2026-09-01')
    expect(addDaysToExpiryDate('', 3)).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})
