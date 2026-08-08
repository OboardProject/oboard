import { AlertTriangle, Check, Loader2, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import { CapabilityBadge } from './CapabilityBadge'
import type { AIProviderTestResult } from './types'

export function ProviderTest({ result, loading, onClose }: { result: AIProviderTestResult | null; loading: boolean; onClose: () => void }) {
  const capability = result?.capability
  return <MotionDialogPanel onCancel={onClose} className="notification-raw-log-dialog ai-test-result-dialog">
    <header className="dialog-head"><div><h2>Endpoint 审计就绪测试</h2><p className="muted">逐项验证认证、结构化输出、Usage、停止原因、Streaming 与模型发现。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><X size={16} /></button></header>
    <div className="dialog-body">{loading ? <div className="ai-test-loading"><Loader2 size={18} className="spin" /><span>正在测试 Endpoint</span></div> : <>
      <div className={`ai-test-banner ${result?.ok ? 'is-ok' : 'is-failed'}`}>{result?.ok ? <Check size={16} /> : <AlertTriangle size={16} />}<strong>{result?.ok ? '测试完成' : '测试失败'}</strong><span>{result?.message || ''}</span></div>
      {capability ? <div className="ai-test-capability"><CapabilityBadge capability={capability} /><span>{capability.api_style}</span><span>{capability.model}</span><span>Schema {Math.round((capability.schema_success_rate || 0) * 100)}%</span><span>Usage {capability.usage_supported ? '支持' : '缺失'} · Finish {capability.finish_reason_supported ? '支持' : '缺失'} · Streaming {capability.streaming_supported ? '支持' : '缺失'}</span><span>延迟 {capability.latency_ms} ms</span>{capability.notes?.map(note => <span className="muted" key={note}>{note}</span>)}</div> : null}
    </>}</div>
    <footer className="dialog-actions"><button type="button" onClick={onClose}>关闭</button></footer>
  </MotionDialogPanel>
}
