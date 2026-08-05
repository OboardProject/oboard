import { describe, expect, it } from 'vitest'

import { auditHealthScoreTone, normalizeAuditHealthScore } from './ai-audit-score'

describe('AI audit health score', () => {
  it('uses green, yellow, and red score bands', () => {
    expect(auditHealthScoreTone(100)).toBe('good')
    expect(auditHealthScoreTone(80)).toBe('good')
    expect(auditHealthScoreTone(79)).toBe('warning')
    expect(auditHealthScoreTone(60)).toBe('warning')
    expect(auditHealthScoreTone(59)).toBe('danger')
    expect(auditHealthScoreTone(0)).toBe('danger')
  })

  it('keeps meter values within the supported integer range', () => {
    expect(normalizeAuditHealthScore(68.4)).toBe(68)
    expect(normalizeAuditHealthScore(-1)).toBe(0)
    expect(normalizeAuditHealthScore(101)).toBe(100)
  })
})
