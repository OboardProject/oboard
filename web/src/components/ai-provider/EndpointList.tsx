import { apiStyleLabel, authModeLabel } from '../../ai-provider'
import { CapabilityBadge } from './CapabilityBadge'
import type { AIProviderEndpoint } from './types'

export function EndpointList({ endpoints }: { endpoints: AIProviderEndpoint[] }) {
  return <div className="ai-endpoint-list">{endpoints.map(endpoint => <div className="ai-endpoint-row" key={endpoint.id}>
    <div><strong>{endpoint.name}</strong><span>{apiStyleLabel(endpoint.api_style)} · {authModeLabel(endpoint.auth_mode)}</span><small>{endpoint.model_override || '使用默认模型'} · 优先级 {endpoint.priority} · {endpoint.has_credential || endpoint.auth_mode === 'none' ? 'Credential 已就绪' : '缺少 Credential'}</small></div>
    <CapabilityBadge capability={endpoint.capability} />
  </div>)}</div>
}
