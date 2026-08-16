import type { AIProviderCapability } from './types'

export type AIProviderCapabilityStatus = 'audit-ready' | 'text-available' | 'unavailable' | 'untested'

type OutputCapability = Pick<AIProviderCapability, 'structured_output' | 'output_mode'>

export function capabilityStatus(capability?: AIProviderCapability): AIProviderCapabilityStatus {
  if (!capability) return 'untested'
  if (capability.audit_ready) return 'audit-ready'
  if (capability.connectivity_ok && capability.authentication_ok && capability.text_supported) return 'text-available'
  return 'unavailable'
}

export function capabilityStatusLabel(capability?: AIProviderCapability): string {
  return ({
    'audit-ready': '可用于审计',
    'text-available': '文本可用',
    unavailable: '不可用',
    untested: '未测试',
  } as const)[capabilityStatus(capability)]
}

export function capabilityOutputModeLabel(capability?: OutputCapability): string {
  if (!capability) return '输出模式未知'
  switch (capability.structured_output) {
    case 'json_schema': return '严格 Schema'
    case 'json_object': return 'JSON Object'
    case 'prompted_json': return '提示词 JSON'
    default: return capability.output_mode === 'text' ? '普通文本' : '输出模式未知'
  }
}

export function CapabilityBadge({ capability }: { capability?: AIProviderCapability }) {
  const status = capabilityStatus(capability)
  const details = capability?.notes?.join('；') || capability?.note
  const title = [capabilityStatusLabel(capability), capability ? capabilityOutputModeLabel(capability) : '', details].filter(Boolean).join('；') || '尚未测试'
  return <span className={`ai-provider-status is-${status}`} title={title}>{capabilityStatusLabel(capability)}</span>
}
