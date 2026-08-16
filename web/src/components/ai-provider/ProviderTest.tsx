import { AlertTriangle, Check, Loader2, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import { CapabilityBadge, capabilityOutputModeLabel } from './CapabilityBadge'
import type { AIProviderTestResult } from './types'

export function ProviderTest({ result, loading, onClose }: { result: AIProviderTestResult | null; loading: boolean; onClose: () => void }) {
  const capability = result?.capability
  const state = result?.ok ? (capability?.audit_ready ? 'ready' : 'text') : 'failed'
  const notes = capability ? [...(capability.notes || []), ...(capability.note ? [capability.note] : [])] : []
  return <MotionDialogPanel onCancel={onClose} className="notification-raw-log-dialog ai-test-result-dialog">
    <header className="dialog-head"><div><h2>Endpoint 兼容性测试</h2><p className="muted">验证连接并确定该模型实际可用的输出模式。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><X size={16} /></button></header>
    <div className="dialog-body">{loading ? <div className="ai-test-loading"><Loader2 size={18} className="spin" /><span>正在测试 Endpoint</span></div> : <>
      <div className={`ai-test-banner is-${state}`} role="status">{state === 'ready' ? <Check size={16} /> : <AlertTriangle size={16} />}<strong>{state === 'ready' ? '可用于审计' : state === 'text' ? '文本可用' : '不可用'}</strong><span>{result?.message || ''}</span></div>
      {capability ? <div className="ai-test-capability"><CapabilityBadge capability={capability} /><span>{capability.api_style}</span><span>{capability.model}</span><span>输出 {capabilityOutputModeLabel(capability)}</span><span>模型列表 {capability.models_supported ? '自动发现' : '手工填写'}</span><span>Usage {capability.usage_supported ? '支持' : '估算'} · Finish {capability.finish_reason_supported ? '支持' : '未提供'} · Streaming {capability.streaming_supported ? '支持' : '未提供'}</span><span>延迟 {capability.latency_ms} ms</span>{notes.map((note, index) => <span className="muted" key={`${index}-${note}`}>{note}</span>)}</div> : null}
    </>}</div>
    <footer className="dialog-actions"><button type="button" onClick={onClose}>关闭</button></footer>
  </MotionDialogPanel>
}
