import { ChevronDown, KeyRound, Send, Trash2 } from 'lucide-react'
import type { AIAPIStyle, AIAuthMode } from '../../ai-provider'
import { FormField } from '../ui/form-field'
import { Switch } from '../ui/switch'
import { Select } from '../ui/select'
import { CapabilityBadge } from './CapabilityBadge'
import type { EndpointDraft } from './types'

export function EndpointEditor({ endpoint, index, canDelete, testing, onChange, onDelete, onTest }: { endpoint: EndpointDraft; index: number; canDelete: boolean; testing: boolean; onChange: (next: EndpointDraft) => void; onDelete: () => void; onTest: () => void }) {
  const update = <K extends keyof EndpointDraft>(key: K, value: EndpointDraft[K]) => onChange({ ...endpoint, [key]: value })
  const needsCredential = endpoint.authMode !== 'none'
  const hasCredential = endpoint.hasCredential && !endpoint.removeCredential || Boolean(endpoint.apiKey.trim())
  return <section className="ai-endpoint-editor">
    <div className="ai-endpoint-editor-head">
      <div><strong>{endpoint.name || `Endpoint ${index + 1}`}</strong><span>优先级 {endpoint.priority}</span><CapabilityBadge capability={endpoint.capability} /></div>
      <div>
        <button type="button" className="ghost icon-button" disabled={testing || (needsCredential && !hasCredential)} onClick={onTest} title="测试 Endpoint" aria-label={`测试 ${endpoint.name || `Endpoint ${index + 1}`}`}><Send size={15} /></button>
        <button type="button" className="ghost icon-button danger-text" disabled={!canDelete} onClick={onDelete} title="删除 Endpoint" aria-label={`删除 ${endpoint.name || `Endpoint ${index + 1}`}`}><Trash2 size={15} /></button>
      </div>
    </div>
    <div className="two-column">
      <FormField label="Endpoint 名称" required><input required value={endpoint.name} onChange={event => update('name', event.target.value)} /></FormField>
      <FormField label="优先级" hint="数值越小越优先"><input type="number" min={1} max={1000000} required value={endpoint.priority} onChange={event => update('priority', Number(event.target.value))} /></FormField>
    </div>
    <FormField label="Base URL" required><input required value={endpoint.baseURL} onChange={event => update('baseURL', event.target.value)} placeholder="https://api.example.com/v1" /></FormField>
    <div className="two-column">
      <FormField label="API Style" required><Select value={endpoint.apiStyle} placeholder="请选择 API Style" onChange={event => update('apiStyle', event.target.value as AIAPIStyle)}><option value="openai_responses">OpenAI Responses</option><option value="openai_chat_completions">OpenAI Chat Completions</option><option value="anthropic_messages">Anthropic Messages</option></Select></FormField>
      <FormField label="认证方式" required><Select value={endpoint.authMode} onChange={event => update('authMode', event.target.value as AIAuthMode)}><option value="bearer">Bearer</option><option value="x_api_key">x-api-key</option><option value="none">不认证</option></Select></FormField>
    </div>
    {needsCredential ? <FormField label="API Key" required={!endpoint.hasCredential} hint={endpoint.hasCredential ? '留空保留当前 Key' : undefined}>
      <div className="ai-endpoint-secret"><KeyRound size={15} aria-hidden="true" /><input required={!endpoint.hasCredential} type="password" autoComplete="new-password" value={endpoint.apiKey} disabled={endpoint.removeCredential} placeholder={endpoint.hasCredential ? '••••••••••' : ''} onChange={event => update('apiKey', event.target.value)} /></div>
      {endpoint.hasCredential ? <div className="switch-form-row ai-endpoint-remove-secret"><span className="switch-form-label">移除 Credential</span><Switch size="sm" checked={endpoint.removeCredential} onChange={checked => onChange({ ...endpoint, removeCredential: checked, apiKey: checked ? '' : endpoint.apiKey })} ariaLabel="移除 Credential" /></div> : null}
    </FormField> : null}
    {endpoint.apiStyle === 'anthropic_messages' ? <FormField label="Anthropic Version" required><input required value={endpoint.anthropicVersion} onChange={event => update('anthropicVersion', event.target.value)} /></FormField> : null}
    <details className="automation-advanced">
      <summary><span><strong>高级设置</strong><small>路径、超时、重试、私网和 Header</small></span><ChevronDown size={16} /></summary>
      <div className="automation-advanced-body">
        <div className="two-column"><FormField label="模型覆盖"><input value={endpoint.modelOverride} onChange={event => update('modelOverride', event.target.value)} /></FormField><FormField label="超时 (ms)"><input type="number" min={1000} max={600000} value={endpoint.timeoutMS} onChange={event => update('timeoutMS', Number(event.target.value))} /></FormField></div>
        <div className="two-column"><FormField label="Models Path"><input value={endpoint.modelsPath} onChange={event => update('modelsPath', event.target.value)} placeholder="/models" /></FormField><FormField label="Generate Path"><input value={endpoint.generatePath} onChange={event => update('generatePath', event.target.value)} placeholder="使用协议默认路径" /></FormField></div>
        <div className="two-column"><FormField label="重试次数"><input type="number" min={0} max={10} value={endpoint.maxRetries} onChange={event => update('maxRetries', Number(event.target.value))} /></FormField><FormField label="Custom Headers" hint="每行 Name: Value"><textarea rows={3} value={endpoint.headers} onChange={event => update('headers', event.target.value)} placeholder="X-Tenant: production" /></FormField></div>
        <div className="switch-form-row" style={{ padding: '4px 0' }}>
          <span className="switch-form-label">启用 Endpoint</span>
          <Switch checked={endpoint.enabled} onChange={checked => update('enabled', checked)} />
        </div>
        <div className="switch-form-row" style={{ padding: '4px 0' }}>
          <span className="switch-form-label">允许访问私网或本机地址</span>
          <Switch checked={endpoint.allowPrivateNetwork} onChange={checked => update('allowPrivateNetwork', checked)} />
        </div>
      </div>
    </details>
  </section>
}
