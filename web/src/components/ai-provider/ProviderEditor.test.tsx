import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { EndpointEditor } from './EndpointEditor'
import { endpointDiscoveryRequest, endpointDraftRequest, endpointRequestPayload } from './ProviderEditor'
import type { EndpointDraft } from './types'

function endpoint(overrides: Partial<EndpointDraft> = {}): EndpointDraft {
  return { localID: 'local', id: 'saved', name: 'Primary', baseURL: 'https://api.example.com/v1', apiStyle: 'openai_responses', authMode: 'bearer', apiKey: '', removeCredential: false, hasCredential: true, anthropicVersion: '', headers: '', modelsPath: '', generatePath: '', modelOverride: '', priority: 10, enabled: true, timeoutMS: 60000, maxRetries: 2, allowPrivateNetwork: false, ...overrides }
}

describe('AI Provider endpoint editor', () => {
  it('preserves a saved credential when no replacement is entered', () => {
    const payload = endpointRequestPayload(endpoint())
    expect(payload).not.toHaveProperty('api_key')
    expect(payload).not.toHaveProperty('remove_credential')
  })

  it('only removes a credential through the explicit control', () => {
    expect(endpointRequestPayload(endpoint({ removeCredential: true }))).toMatchObject({ remove_credential: true })
    expect(endpointRequestPayload(endpoint({ apiKey: 'replacement' }))).toMatchObject({ api_key: 'replacement' })
  })

  it('tests unsaved endpoint fields while reusing the saved credential server-side', () => {
    const draft = endpoint({ baseURL: 'https://draft.example.com/v1', apiStyle: 'openai_chat_completions' })
    expect(endpointDraftRequest('provider', 'openai', draft, 'model')).toMatchObject({
      provider_id: 'provider',
      endpoint_id: 'saved',
      endpoint: { base_url: 'https://draft.example.com/v1', api_style: 'openai_chat_completions' },
      model: 'model',
    })
    expect(endpointDraftRequest('provider', 'openai', draft, 'model', true)).not.toHaveProperty('endpoint')
  })

  it('builds model discovery requests without provider-test-only model fields', () => {
    const saved = endpoint({ baseURL: 'https://opencode.ai/zen/go/v1' })
    expect(endpointDiscoveryRequest('provider', 'custom', saved)).toMatchObject({
      provider_id: 'provider',
      endpoint_id: 'saved',
      endpoint: { base_url: 'https://opencode.ai/zen/go/v1' },
    })
    expect(endpointDiscoveryRequest('provider', 'custom', saved)).not.toHaveProperty('model')

    const draft = endpoint({ id: '', hasCredential: false, apiKey: 'draft-key' })
    expect(endpointDiscoveryRequest('', 'custom', draft)).toMatchObject({
      provider_kind: 'custom',
      endpoint: { api_key: 'draft-key' },
    })
  })

  it('shows Anthropic Version only for native Messages and keeps priority editable', () => {
    const native = renderToStaticMarkup(<EndpointEditor endpoint={endpoint({ apiStyle: 'anthropic_messages', authMode: 'x_api_key', anthropicVersion: '2023-06-01' })} index={0} canDelete testing={false} onChange={vi.fn()} onDelete={vi.fn()} onTest={vi.fn()} />)
    const responses = renderToStaticMarkup(<EndpointEditor endpoint={endpoint()} index={0} canDelete testing={false} onChange={vi.fn()} onDelete={vi.fn()} onTest={vi.fn()} />)
    expect(native).toContain('Anthropic Version')
    expect(responses).not.toContain('Anthropic Version')
    expect(native).toContain('type="number"')
    expect(native).toContain('value="10"')
  })
})
