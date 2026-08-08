export type TokenDisplayUnit = 'Token' | 'K' | 'M'

export type AIProviderKind = 'openai' | 'anthropic' | 'custom'
export type AIAPIStyle = 'openai_responses' | 'openai_chat_completions' | 'anthropic_messages'
export type AIAuthMode = 'bearer' | 'x_api_key' | 'none'

export type AIEndpointTemplate = {
  name: string
  baseURL: string
  apiStyle: AIAPIStyle | ''
  authMode: AIAuthMode
  anthropicVersion: string
}

export function providerEndpointTemplate(kind: AIProviderKind): AIEndpointTemplate {
  if (kind === 'anthropic') {
    return { name: 'Native Claude', baseURL: 'https://api.anthropic.com/v1', apiStyle: 'anthropic_messages', authMode: 'x_api_key', anthropicVersion: '2023-06-01' }
  }
  return { name: kind === 'custom' ? 'Primary' : 'OpenAI Responses', baseURL: kind === 'custom' ? '' : 'https://api.openai.com/v1', apiStyle: kind === 'custom' ? '' : 'openai_responses', authMode: 'bearer', anthropicVersion: '' }
}

export function apiStyleLabel(style: AIAPIStyle): string {
  return ({ openai_responses: 'OpenAI Responses', openai_chat_completions: 'OpenAI Chat Completions', anthropic_messages: 'Anthropic Messages' } as Record<AIAPIStyle, string>)[style]
}

export function authModeLabel(mode: AIAuthMode): string {
  return ({ bearer: 'Bearer', x_api_key: 'x-api-key', none: '不认证' } as Record<AIAuthMode, string>)[mode]
}

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
