import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { CapabilityBadge, capabilityOutputModeLabel, capabilityStatusLabel } from './CapabilityBadge'
import { ProviderTest } from './ProviderTest'
import type { AIProviderCapability } from './types'

function capability(overrides: Partial<AIProviderCapability> = {}): AIProviderCapability {
  return {
    provider_profile_version: 'provider-profile-v3',
    provider_id: 'provider',
    endpoint_id: 'endpoint',
    api_style: 'openai_chat_completions',
    model: 'model',
    config_digest: 'digest',
    tested_at: '2026-08-17T00:00:00Z',
    connectivity_ok: true,
    authentication_ok: true,
    text_supported: true,
    models_supported: true,
    audit_ready: true,
    structured_output: 'json_schema',
    output_mode: 'strict_schema',
    usage_supported: true,
    finish_reason_supported: true,
    streaming_supported: true,
    provider_request_id_supported: true,
    max_verified_output_tokens: 4096,
    latency_ms: 120,
    ...overrides,
  }
}

describe('AI Provider capability status', () => {
  it('uses outcome labels instead of grades', () => {
    expect(capabilityStatusLabel(capability())).toBe('可用于审计')
    expect(capabilityStatusLabel(capability({ audit_ready: false, structured_output: 'none', output_mode: 'text' }))).toBe('文本可用')
    expect(capabilityStatusLabel(capability({ audit_ready: false, text_supported: false, structured_output: 'none', output_mode: 'text' }))).toBe('不可用')
    expect(capabilityStatusLabel(capability({ audit_ready: false, connectivity_ok: false, authentication_ok: false, structured_output: 'none', output_mode: 'text' }))).toBe('不可用')
    expect(capabilityStatusLabel()).toBe('未测试')

    const markup = renderToStaticMarkup(<CapabilityBadge capability={capability()} />)
    expect(markup).toContain('is-audit-ready')
    expect(markup).not.toMatch(/[ABC] 级/)
  })

  it('names every supported output mode', () => {
    expect(capabilityOutputModeLabel(capability())).toBe('严格 Schema')
    expect(capabilityOutputModeLabel(capability({ structured_output: 'json_object', output_mode: 'json_object' }))).toBe('JSON Object')
    expect(capabilityOutputModeLabel(capability({ structured_output: 'prompted_json', output_mode: 'text' }))).toBe('提示词 JSON')
    expect(capabilityOutputModeLabel(capability({ audit_ready: false, structured_output: 'none', output_mode: 'text' }))).toBe('普通文本')
  })

  it('does not present a text-only result as a green success', () => {
    const textOnly = capability({ audit_ready: false, structured_output: 'none', output_mode: 'text' })
    const markup = renderToStaticMarkup(<ProviderTest result={{ ok: true, message: '文本调用可用', capability: textOnly }} loading={false} onClose={vi.fn()} />)
    expect(markup).toContain('ai-test-banner is-text')
    expect(markup).toContain('文本可用')
    expect(markup).toContain('输出 普通文本')
    expect(markup).not.toContain('ai-test-banner is-ready')
    expect(markup).not.toContain('Schema 0%')
  })
})
