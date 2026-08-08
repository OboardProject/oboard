import type { AIAPIStyle, AIAuthMode, AIProviderKind, TokenDisplayUnit } from '../../ai-provider'

export type AIProviderCapability = {
  provider_profile_version: string
  provider_id: string
  endpoint_id: string
  api_style: AIAPIStyle
  model: string
  config_digest: string
  tested_at: string
  connectivity_ok: boolean
  authentication_ok: boolean
  models_supported: boolean
  audit_grade: 'A' | 'B' | 'C' | 'unusable'
  structured_output: 'json_schema' | 'json_object' | 'none'
  output_mode: 'strict_schema' | 'json_object' | 'text'
  schema_success_rate: number
  usage_supported: boolean
  finish_reason_supported: boolean
  streaming_supported: boolean
  provider_request_id_supported: boolean
  max_verified_output_tokens: number
  latency_ms: number
  notes?: string[]
  note?: string
}

export type AIProviderEndpoint = {
  id: string
  provider_id: string
  name: string
  base_url: string
  api_style: AIAPIStyle
  auth_mode: AIAuthMode
  anthropic_version?: string
  headers_json: string
  models_path?: string
  generate_path?: string
  model_override?: string
  priority: number
  enabled: boolean
  timeout_ms: number
  max_retries: number
  allow_private_network: boolean
  has_credential: boolean
  capability?: AIProviderCapability
}

export type AIProvider = {
  id: string
  name: string
  provider_kind: AIProviderKind
  default_model: string
  routing_strategy: 'ordered_failover'
  enabled: boolean
  allow_raw_audit: boolean
  daily_token_limit: number
  has_credential: boolean
  endpoints: AIProviderEndpoint[]
}

export type EndpointDraft = {
  localID: string
  id: string
  name: string
  baseURL: string
  apiStyle: AIAPIStyle | ''
  authMode: AIAuthMode
  apiKey: string
  removeCredential: boolean
  hasCredential: boolean
  anthropicVersion: string
  headers: string
  modelsPath: string
  generatePath: string
  modelOverride: string
  priority: number
  enabled: boolean
  timeoutMS: number
  maxRetries: number
  allowPrivateNetwork: boolean
  capability?: AIProviderCapability
}

export type ProviderDraft = {
  name: string
  providerKind: AIProviderKind
  defaultModel: string
  tokenAmount: string
  tokenUnit: TokenDisplayUnit
  enabled: boolean
  allowRawAudit: boolean
  endpoints: EndpointDraft[]
}

export type RequestV2 = <T = any>(path: string, init?: RequestInit) => Promise<T>
export type Notify = (message: string, tone?: 'success' | 'error' | 'info') => void
export type Confirm = (options: { title: string; message: string; confirmText?: string; tone?: 'danger' | 'default' }) => Promise<boolean>

export type AIProviderTestResult = {
  ok?: boolean
  message?: string
  status_code?: number
  duration_ms?: number
  content?: string
  request_json?: string
  response_json?: string
  capability?: AIProviderCapability
}
