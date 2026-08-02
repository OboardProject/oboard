import { describe, expect, it } from 'vitest'

import { formatTokenLimit, tokenDisplayToLimit, tokenLimitToDisplay } from './ai-provider'

describe('AI Provider token limit display', () => {
  it('selects a compact unit without losing precision', () => {
    expect(tokenLimitToDisplay(0)).toEqual({ amount: '0', unit: 'Token' })
    expect(tokenLimitToDisplay(999)).toEqual({ amount: '999', unit: 'Token' })
    expect(tokenLimitToDisplay(100_000)).toEqual({ amount: '100', unit: 'K' })
    expect(tokenLimitToDisplay(1_500_000)).toEqual({ amount: '1.5', unit: 'M' })
    expect(tokenLimitToDisplay(1_234_567)).toEqual({ amount: '1234.567', unit: 'K' })
  })

  it('converts Token, K, and M values to exact integer limits', () => {
    expect(tokenDisplayToLimit('250', 'Token')).toBe(250)
    expect(tokenDisplayToLimit('100', 'K')).toBe(100_000)
    expect(tokenDisplayToLimit('1.5', 'M')).toBe(1_500_000)
    expect(tokenDisplayToLimit('0', 'M')).toBe(0)
  })

  it('rejects invalid or lossy values', () => {
    expect(tokenDisplayToLimit('', 'K')).toBeNull()
    expect(tokenDisplayToLimit('-1', 'K')).toBeNull()
    expect(tokenDisplayToLimit('1.2345', 'K')).toBeNull()
    expect(tokenDisplayToLimit('0.001', 'Token')).toBeNull()
  })

  it('formats the stored integer with grouping separators', () => {
    expect(formatTokenLimit(1_500_000)).toBe('1,500,000 Token')
  })
})
