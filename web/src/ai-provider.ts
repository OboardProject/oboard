export type TokenDisplayUnit = 'Token' | 'K' | 'M'

const tokenUnitMultipliers: Record<TokenDisplayUnit, number> = {
  Token: 1,
  K: 1_000,
  M: 1_000_000,
}

export function tokenLimitToDisplay(limit: number): { amount: string; unit: TokenDisplayUnit } {
  const normalized = Number.isSafeInteger(limit) && limit >= 0 ? limit : 0
  if (normalized >= 1_000_000 && normalized % 1_000 === 0) {
    return { amount: String(normalized / tokenUnitMultipliers.M), unit: 'M' }
  }
  if (normalized >= 1_000) {
    return { amount: String(normalized / tokenUnitMultipliers.K), unit: 'K' }
  }
  return { amount: String(normalized), unit: 'Token' }
}

export function tokenDisplayToLimit(amount: string, unit: TokenDisplayUnit): number | null {
  const trimmed = amount.trim()
  if (!/^\d+(?:\.\d{1,3})?$/.test(trimmed)) return null
  const value = Number(trimmed) * tokenUnitMultipliers[unit]
  if (!Number.isSafeInteger(value) || value < 0) return null
  return value
}

export function formatTokenLimit(limit: number): string {
  return `${new Intl.NumberFormat('zh-CN').format(limit)} Token`
}
