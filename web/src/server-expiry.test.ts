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
    expect(serverExpiryStatusValue(undefined, false, now).label).toBe('未设置')
    expect(serverExpiryStatusValue('2026-08-01T00:00:00Z', false, now).label).toBe('已到期')
    expect(serverExpiryStatusValue('2026-08-01T00:00:00Z', true, now).label).toBe('等待自动续期')
    expect(serverExpiryStatusValue('2026-08-02T00:00:00Z', true, now).label).toBe('今天到期')
    expect(serverExpiryStatusValue('2026-08-05T00:00:00Z', true, now).label).toBe('即将到期')
    expect(serverExpiryStatusValue('2026-08-09T00:00:00Z', true, now).label).toBe('即将到期')
    expect(serverExpiryStatusValue('2026-09-02T00:00:00Z', true, now).label).toBe('31 天后到期')
  })

  it('formats labels and adds calendar days', () => {
    expect(serverExpiryDateLabel('2026-09-01T00:00:00Z')).not.toBe('未设置')
    expect(addDaysToExpiryDate('2026-08-01', 3)).toBe('2026-08-04')
    expect(addDaysToExpiryDate('2026-08-31', 1)).toBe('2026-09-01')
    expect(addDaysToExpiryDate('', 3)).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})
