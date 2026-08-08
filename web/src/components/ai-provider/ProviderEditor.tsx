import { useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { Bot, ClipboardList, Edit3, PauseCircle, Play, Plus, Send, Trash2, X } from 'lucide-react'
import { apiStyleLabel, formatTokenLimit, providerEndpointTemplate, tokenDisplayToLimit, tokenLimitToDisplay, type AIProviderKind, type TokenDisplayUnit } from '../../ai-provider'
import { FormField } from '../ui/form-field'
import { MotionDialogPanel } from '../ui/motion'
import { Select } from '../ui/select'
import { EndpointEditor } from './EndpointEditor'
import { EndpointList } from './EndpointList'
import { ModelSelector } from './ModelSelector'
import { ProviderTest } from './ProviderTest'
import type { AIProvider, AIProviderEndpoint, AIProviderTestResult, Confirm, EndpointDraft, Notify, ProviderDraft, RequestV2 } from './types'

function localID() {
  return `endpoint-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

function draftEndpoint(endpoint?: AIProviderEndpoint, kind: AIProviderKind = 'openai'): EndpointDraft {
  const template = providerEndpointTemplate(kind)
  const headers = endpoint?.headers_json ? Object.entries(JSON.parse(endpoint.headers_json) as Record<string, string>).map(([name, value]) => `${name}: ${value}`).join('\n') : ''
  return {
    localID: endpoint?.id || localID(), id: endpoint?.id || '', name: endpoint?.name || template.name,
    baseURL: endpoint?.base_url || template.baseURL, apiStyle: endpoint?.api_style || template.apiStyle,
    authMode: endpoint?.auth_mode || template.authMode, apiKey: '', removeCredential: false,
    hasCredential: Boolean(endpoint?.has_credential), anthropicVersion: endpoint?.anthropic_version || template.anthropicVersion,
    headers, modelsPath: endpoint?.models_path || '', generatePath: endpoint?.generate_path || '', modelOverride: endpoint?.model_override || '',
    priority: endpoint?.priority || 100, enabled: endpoint?.enabled ?? true, timeoutMS: endpoint?.timeout_ms || 60000,
    maxRetries: endpoint?.max_retries ?? 2, allowPrivateNetwork: Boolean(endpoint?.allow_private_network), capability: endpoint?.capability,
  }
}

function newProviderDraft(kind: AIProviderKind = 'openai'): ProviderDraft {
  return { name: '', providerKind: kind, defaultModel: '', tokenAmount: '100', tokenUnit: 'K', enabled: true, allowRawAudit: false, endpoints: [draftEndpoint(undefined, kind)] }
}

function providerDraft(provider: AIProvider): ProviderDraft {
  const token = tokenLimitToDisplay(provider.daily_token_limit || 0)
  return { name: provider.name, providerKind: provider.provider_kind, defaultModel: provider.default_model, tokenAmount: token.amount, tokenUnit: token.unit, enabled: provider.enabled, allowRawAudit: provider.allow_raw_audit, endpoints: provider.endpoints.map(endpoint => draftEndpoint(endpoint, provider.provider_kind)) }
}

export function parseCustomHeaders(raw: string): Record<string, string> {
  const headers: Record<string, string> = {}
  for (const line of raw.split('\n').map(value => value.trim()).filter(Boolean)) {
    const separator = line.indexOf(':')
    if (separator <= 0 || !line.slice(separator + 1).trim()) throw new Error('Custom Headers 必须使用每行 Name: Value 格式')
    headers[line.slice(0, separator).trim()] = line.slice(separator + 1).trim()
  }
  return headers
}

export function endpointRequestPayload(endpoint: EndpointDraft) {
  const payload: Record<string, unknown> = {
    name: endpoint.name, base_url: endpoint.baseURL, api_style: endpoint.apiStyle, auth_mode: endpoint.authMode,
    anthropic_version: endpoint.apiStyle === 'anthropic_messages' ? endpoint.anthropicVersion : '', headers: parseCustomHeaders(endpoint.headers),
    models_path: endpoint.modelsPath, generate_path: endpoint.generatePath, model_override: endpoint.modelOverride,
    priority: endpoint.priority, enabled: endpoint.enabled, timeout_ms: endpoint.timeoutMS, max_retries: endpoint.maxRetries,
    allow_private_network: endpoint.allowPrivateNetwork,
  }
  if (endpoint.apiKey.trim()) payload.api_key = endpoint.apiKey.trim()
  if (endpoint.removeCredential) payload.remove_credential = true
  return payload
}

export function endpointDraftRequest(providerID: string, providerKind: AIProviderKind, endpoint: EndpointDraft, model: string, savedOnly = false) {
  if (providerID && endpoint.id) {
    return savedOnly
      ? { provider_id: providerID, endpoint_id: endpoint.id, model }
      : { provider_id: providerID, endpoint_id: endpoint.id, endpoint: endpointRequestPayload(endpoint), model }
  }
  return { provider_kind: providerKind, endpoint: endpointRequestPayload(endpoint), model }
}

export function endpointDiscoveryRequest(providerID: string, providerKind: AIProviderKind, endpoint: EndpointDraft) {
  if (providerID && endpoint.id) {
    return { provider_id: providerID, endpoint_id: endpoint.id, endpoint: endpointRequestPayload(endpoint) }
  }
  return { provider_kind: providerKind, endpoint: endpointRequestPayload(endpoint) }
}

type Props = { providers: AIProvider[]; requestV2: RequestV2; refresh: () => Promise<void>; notify: Notify; confirm: Confirm; onOpenLogs: () => void }

export function ProviderEditor({ providers, requestV2, refresh, notify, confirm, onOpenLogs }: Props) {
  const [editing, setEditing] = useState<AIProvider | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [draft, setDraft] = useState<ProviderDraft>(() => newProviderDraft())
  const [working, setWorking] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [modelStatus, setModelStatus] = useState('')
  const [discovering, setDiscovering] = useState(false)
  const [testResult, setTestResult] = useState<AIProviderTestResult | null>(null)
  const [testing, setTesting] = useState(false)
  const [testOpen, setTestOpen] = useState(false)

  const open = (provider?: AIProvider) => {
    setEditing(provider || null)
    setDraft(provider ? providerDraft(provider) : newProviderDraft())
    setModels([])
    setModelStatus('')
    setDialogOpen(true)
  }
  const close = () => { if (!working) setDialogOpen(false) }
  const changeKind = (kind: AIProviderKind) => {
    const next = { ...draft, providerKind: kind }
    if (!editing) next.endpoints = [draftEndpoint(undefined, kind)]
    setDraft(next)
    setModels([])
    setModelStatus('')
  }
  const updateEndpoint = (index: number, endpoint: EndpointDraft) => setDraft(current => ({ ...current, endpoints: current.endpoints.map((item, itemIndex) => itemIndex === index ? endpoint : item) }))
  const removeEndpoint = (index: number) => setDraft(current => ({ ...current, endpoints: current.endpoints.filter((_, itemIndex) => itemIndex !== index) }))

  const endpointRequest = (endpoint: EndpointDraft) => {
    const model = endpoint.modelOverride.trim() || draft.defaultModel.trim()
    return endpointDraftRequest(editing?.id || '', draft.providerKind, endpoint, model)
  }
  const discoverModels = async () => {
    const endpoint = draft.endpoints[0]
    if (!endpoint) return
    setDiscovering(true)
    setModelStatus('')
    try {
      const payload = endpointDiscoveryRequest(editing?.id || '', draft.providerKind, endpoint)
      const result = await requestV2<{ models: string[] }>('/ai/provider-models', { method: 'POST', body: JSON.stringify(payload) })
      const values = Array.isArray(result?.models) ? result.models.filter(value => typeof value === 'string') : []
      setModels(values)
      setModelStatus(`已加载 ${values.length} 个模型`)
    } catch (error: any) {
      setModels([])
      setModelStatus('模型发现不可用，仍可手工填写模型 ID')
      notify(String(error?.message || error), 'error')
    } finally { setDiscovering(false) }
  }
  const testEndpoint = async (endpoint: EndpointDraft, provider?: AIProvider) => {
    setTestResult(null)
    setTestOpen(true)
    setTesting(true)
    try {
      const model = endpoint.modelOverride.trim() || (provider?.default_model || draft.defaultModel).trim()
      const payload = provider && endpoint.id ? { provider_id: provider.id, endpoint_id: endpoint.id, model } : endpointRequest(endpoint)
      const result = await requestV2<AIProviderTestResult>('/ai/provider-test', { method: 'POST', body: JSON.stringify(payload) })
      setTestResult(result)
      await refresh()
    } catch (error: any) { setTestResult({ ok: false, message: String(error?.message || error) }) } finally { setTesting(false) }
  }

  const save = async (event: React.FormEvent) => {
    event.preventDefault()
    const dailyTokenLimit = tokenDisplayToLimit(draft.tokenAmount, draft.tokenUnit)
    if (dailyTokenLimit === null) { notify('Token 上限必须能精确换算为整数 Token', 'error'); return }
    if (!draft.endpoints.length) { notify('至少需要一个 Endpoint', 'error'); return }
    if (draft.endpoints.some(endpoint => !endpoint.apiStyle)) { notify('Custom Provider 的每个 Endpoint 都必须选择 API Style', 'error'); return }
    try {
      const endpoints = draft.endpoints.map(endpointRequestPayload)
      setWorking(true)
      const providerPayload = { name: draft.name.trim(), provider_kind: draft.providerKind, default_model: draft.defaultModel.trim(), routing_strategy: 'ordered_failover', enabled: draft.enabled, allow_raw_audit: draft.allowRawAudit, daily_token_limit: dailyTokenLimit }
      if (!editing) {
        await requestV2('/ai/providers', { method: 'POST', body: JSON.stringify({ ...providerPayload, endpoints }) })
      } else {
        await requestV2(`/ai/providers/${editing.id}`, { method: 'PATCH', body: JSON.stringify(providerPayload) })
        const retained = new Set(draft.endpoints.map(endpoint => endpoint.id).filter(Boolean))
        for (const endpoint of editing.endpoints) if (!retained.has(endpoint.id)) await requestV2(`/ai/providers/${editing.id}/endpoints/${endpoint.id}`, { method: 'DELETE' })
        for (let index = 0; index < draft.endpoints.length; index++) {
          const endpoint = draft.endpoints[index]
          const path = endpoint.id ? `/ai/providers/${editing.id}/endpoints/${endpoint.id}` : `/ai/providers/${editing.id}/endpoints`
          await requestV2(path, { method: endpoint.id ? 'PATCH' : 'POST', body: JSON.stringify(endpoints[index]) })
        }
      }
      setDialogOpen(false)
      await refresh()
      notify(editing ? 'AI Provider 已更新' : 'AI Provider 已创建', 'success')
    } catch (error: any) { notify(String(error?.message || error), 'error') } finally { setWorking(false) }
  }
  const toggle = async (provider: AIProvider) => {
    try { await requestV2(`/ai/providers/${provider.id}`, { method: 'PATCH', body: JSON.stringify({ enabled: !provider.enabled }) }); await refresh() } catch (error: any) { notify(String(error?.message || error), 'error') }
  }
  const remove = async (provider: AIProvider) => {
    if (!await confirm({ title: `删除 ${provider.name}？`, message: '已产生审查记录的 Provider 只能停用。', confirmText: '删除', tone: 'danger' })) return
    try { await requestV2(`/ai/providers/${provider.id}`, { method: 'DELETE' }); await refresh(); notify('AI Provider 已删除', 'success') } catch (error: any) { notify(String(error?.message || error), 'error') }
  }

  const tokenLimit = tokenDisplayToLimit(draft.tokenAmount, draft.tokenUnit)
  return <>
    <section className="settings-card automation-wide">
      <div className="settings-card-head"><div><h3>AI Provider</h3><p className="muted">逻辑 Provider 按优先级路由到一个或多个独立 Endpoint。</p></div><div className="section-actions"><button type="button" className="ghost" onClick={onOpenLogs}><ClipboardList size={15} /><span>原始日志</span></button><button type="button" className="ghost" onClick={() => open()}><Plus size={15} /><span>添加 Provider</span></button></div></div>
      <div className="automation-list">{providers.length ? providers.map(provider => <div className="ai-provider-item" key={provider.id}>
        <div className="automation-row"><div><div className="automation-row-title"><strong>{provider.name}</strong><span className={`automation-state ${provider.enabled ? 'is-enabled' : ''}`}>{provider.enabled ? '已启用' : '已停用'}</span></div><span>{provider.default_model} · {provider.provider_kind === 'anthropic' ? 'Claude' : provider.provider_kind === 'openai' ? 'OpenAI' : 'Custom'}</span><small>Ordered Failover · {provider.daily_token_limit ? `每日 ${formatTokenLimit(provider.daily_token_limit)}` : '不设日限额'} · {provider.allow_raw_audit ? '原始字段已授权' : '脱敏模式'}</small></div><div>{provider.endpoints[0] ? <button className="ghost icon-button" onClick={() => void testEndpoint(draftEndpoint(provider.endpoints[0], provider.provider_kind), provider)} title="测试首选 Endpoint" aria-label={`测试 ${provider.name}`}><Send size={15} /></button> : null}<button className="ghost icon-button" onClick={() => open(provider)} title="编辑" aria-label={`编辑 ${provider.name}`}><Edit3 size={15} /></button><button className="ghost icon-button" onClick={() => void toggle(provider)} title={provider.enabled ? '停用' : '启用'} aria-label={provider.enabled ? '停用' : '启用'}>{provider.enabled ? <PauseCircle size={15} /> : <Play size={15} />}</button><button className="ghost icon-button danger-text" onClick={() => void remove(provider)} title="删除" aria-label={`删除 ${provider.name}`}><Trash2 size={15} /></button></div></div>
        <EndpointList endpoints={provider.endpoints} />
      </div>) : <div className="automation-empty"><Bot size={20} /><span>还没有 AI Provider</span><button type="button" className="ghost" onClick={() => open()}>添加 Provider</button></div>}</div>
    </section>
    <AnimatePresence>{dialogOpen ? <MotionDialogPanel onCancel={close} className="automation-dialog ai-provider-dialog">
      <header className="dialog-head"><div><h2>{editing ? '编辑 AI Provider' : '新建 AI Provider'}</h2><p className="muted">Provider 定义模型与策略，Endpoint 定义具体协议和 Credential。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={close} aria-label="关闭" title="关闭"><X size={16} /></button></header>
      <div className="dialog-body"><form id="ai-provider-v2-form" className="form automation-dialog-form" onSubmit={save}>
        <div className="two-column"><FormField label="Provider 名称" required><input autoFocus required value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} placeholder="例如：Claude Production" /></FormField><FormField label="Provider Vendor" required><Select variant="segmented" value={draft.providerKind} onChange={event => changeKind(event.target.value as AIProviderKind)}><option value="openai">OpenAI</option><option value="anthropic">Claude</option><option value="custom">Custom</option></Select></FormField></div>
        <FormField label="默认模型" required><ModelSelector value={draft.defaultModel} options={models} loading={discovering} status={modelStatus} disabled={!draft.endpoints.length} onChange={value => setDraft({ ...draft, defaultModel: value })} onDiscover={() => void discoverModels()} /></FormField>
        <div className="two-column"><FormField label="路由策略"><Select value="ordered_failover" disabled><option value="ordered_failover">Ordered Failover</option></Select></FormField><FormField label="每日 Token 上限" hint="0 表示不设日限额"><div className="token-limit-input"><input inputMode="decimal" required value={draft.tokenAmount} aria-invalid={tokenLimit === null || undefined} onChange={event => setDraft({ ...draft, tokenAmount: event.target.value })} /><Select variant="segmented" value={draft.tokenUnit} onChange={event => setDraft({ ...draft, tokenUnit: event.target.value as TokenDisplayUnit })}><option value="Token">Token</option><option value="K">K</option><option value="M">M</option></Select></div></FormField></div>
        <label className="toggle-line"><input type="checkbox" checked={draft.enabled} onChange={event => setDraft({ ...draft, enabled: event.target.checked })} /><span>启用 Provider</span></label>
        <label className="toggle-line"><input type="checkbox" checked={draft.allowRawAudit} onChange={event => setDraft({ ...draft, allowRawAudit: event.target.checked })} /><span>允许保存审计业务原始输入与输出</span></label>
        <div className="ai-endpoints-heading"><div><strong>Upstream Endpoints</strong><span>按 Priority 从小到大尝试</span></div><button type="button" className="ghost" onClick={() => setDraft(current => ({ ...current, endpoints: [...current.endpoints, { ...draftEndpoint(undefined, current.providerKind), priority: (Math.max(0, ...current.endpoints.map(endpoint => endpoint.priority)) || 0) + 10 }] }))}><Plus size={14} />添加 Endpoint</button></div>
        {draft.endpoints.map((endpoint, index) => <EndpointEditor key={endpoint.localID} endpoint={endpoint} index={index} canDelete={draft.endpoints.length > 1} testing={testing} onChange={next => updateEndpoint(index, next)} onDelete={() => removeEndpoint(index)} onTest={() => void testEndpoint(endpoint)} />)}
      </form></div>
      <footer className="dialog-actions"><button type="button" className="ghost" onClick={close}>取消</button><button type="submit" form="ai-provider-v2-form" disabled={working || !draft.name.trim() || !draft.defaultModel.trim() || !draft.endpoints.length}>{editing ? '保存修改' : '创建 Provider'}</button></footer>
    </MotionDialogPanel> : null}</AnimatePresence>
    <AnimatePresence>{testOpen ? <ProviderTest result={testResult} loading={testing} onClose={() => setTestOpen(false)} /> : null}</AnimatePresence>
  </>
}
