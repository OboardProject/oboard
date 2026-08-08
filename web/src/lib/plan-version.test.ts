import { describe, expect, it } from 'vitest'
import { formatPlanVersion } from './plan-version'

describe('formatPlanVersion', () => {
  it('renders a compact local timestamp with second precision', () => {
    expect(formatPlanVersion(new Date(2026, 7, 9, 14, 32, 5))).toBe('260809-143205')
  })

  it('uses an em dash when the creation time is unavailable', () => {
    expect(formatPlanVersion()).toBe('—')
    expect(formatPlanVersion('invalid')).toBe('—')
  })
})
