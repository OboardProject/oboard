import React, { useEffect, useRef, useState } from 'react'
import { Select } from '../../ui/select'
import { FormField } from '../../ui/form-field'

type Check = { auto_fix?: boolean; id: string; category: string; severity: string; status: string; message: string; remedy: string; checked_at: string }
type Report = { revision: number; capabilities: string[]; desired_mode: string; actual_mode: string; state: string; checked_at: string; applied_at?: string; phase: string; error_code?: string; supported: boolean; local_policy: string; checks: Check[] }
type View = { desired: { mode: string; revision: number }; report: Report | null; queued: boolean; task_id?: number; task_status?: string; last_task_id?: number; last_task_status?: string; online: boolean }
const states: Record<string, string> = { standard: '标准模式', applying: '正在应用', enhanced: '强化模式', partial: '部分生效', unsupported: '当前环境不支持', failed: '应用失败' }
const checks: Record<string, string> = { passed: '通过', warning: '注意', failed: '存在风险', unsupported: '不支持检查', unknown: '尚未确认' }
const phases: Record<string,string> = { preflight: '环境检查', lock: '等待生命周期锁', storage: '敏感状态保护', prepare: '准备配置', verify: '运行状态验证', rollback: '回滚', confirmed: 'Agent 已确认', checked: '检查完成', permissions: '文件权限修复' }
const categories: Record<string,string> = {filesystem:'敏感文件与本地接口',process:'进程权限与信息暴露',storage:'配置存储',service:'服务隔离',binary:'二进制完整性',interface:'本地管理接口'}

export function SystemRuntimeSecurityTab({ serverID, client, disabled }: { serverID: number; client: { requestV2: (path: string, init?: RequestInit) => Promise<any> }; disabled: boolean }) {
  const [view, setView] = useState<View | null>(null)
  const [mode, setMode] = useState('standard')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const submitAbort = useRef<AbortController | null>(null)
  useEffect(() => {
    setView(null); setError(''); setNotice(''); setBusy(false)
    return () => { submitAbort.current?.abort(); submitAbort.current = null }
  }, [serverID])
  useEffect(() => {
    const abort = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let attempts = 0
    const load = async () => {
      try {
        const value: View = await client.requestV2('/query', { method: 'POST', body: JSON.stringify({ capability: 'servers.runtime_security.read', arguments: { server_id: serverID } }), signal: abort.signal })
        if (abort.signal.aborted) return
        setView(value)
        if (!value.queued && ['succeeded', 'failed', 'rollback_failed'].includes(value.last_task_status || '')) setNotice(previous => previous ? (value.last_task_status === 'succeeded' ? '任务已完成，已刷新 Agent 检查结果。' : '任务未完成，请查看失败阶段。') : '')
        if (attempts === 0) setMode(value.desired.mode)
        if (value.online && (value.queued || (value.report?.revision ?? -1) < value.desired.revision) && attempts++ < 12) timer = setTimeout(() => void load(), 5000)
      } catch (e) { if (!abort.signal.aborted) setError(e instanceof Error ? e.message : '无法读取运行安全状态，请重试') }
    }
    void load()
    return () => { abort.abort(); clearTimeout(timer) }
  }, [serverID, client, refresh])
  const submit = async (action: 'apply' | 'check' | 'repair') => {
    if (busy || disabled || view?.queued) return
    const abort = new AbortController(); submitAbort.current?.abort(); submitAbort.current = abort
    setBusy(true); setError(''); setNotice('')
    try {
      const value: View = await client.requestV2('/servers/'+serverID+'/runtime-security', {method:'POST',body:JSON.stringify({...(action === 'check' ? {check:true} : action === 'repair' ? {repair:true} : {mode}),request_id:crypto.randomUUID()}), signal:abort.signal})
      if (abort.signal.aborted) return
      setView(value)
      setNotice(value.queued ? '任务 #'+value.task_id+' 已排队，等待 Agent 检查与确认' : '期望配置已保存，等待 Agent 上线后应用')
      setRefresh(v => v + 1)
    } catch (e) { if (!abort.signal.aborted) setError(e instanceof Error ? e.message : '提交失败，请检查操作记录后重试') }
    finally { if (!abort.signal.aborted) setBusy(false) }
  }
  const report = view?.report
  const pending = busy || !!view?.queued
  const localDenial = report?.local_policy === 'hardened' && mode === 'standard'
  const issues = (report?.checks || []).filter(item => item.status === 'failed' || item.status === 'warning') || []
  return <div className="server-system-settings-tab" aria-busy={busy}>
    <section className="server-detail-section">
      <h3>安全模式</h3>
      <div className="server-detail-grid">
        <div className="server-about-item"><span className="server-about-label">期望模式</span><span>{states[view?.desired.mode || 'standard']}</span></div>
        <div className="server-about-item"><span className="server-about-label">应用状态</span><span>{report ? states[report.state] || '尚未确认' : '尚未收到 Agent 检查结果'}</span></div>
        <div className="server-about-item"><span className="server-about-label">内核实际模式</span><span>{states[report?.actual_mode || ''] || '尚未确认'}</span></div>
        <div className="server-about-item"><span className="server-about-label">强化支持</span><span>{report ? (report.supported ? '可检测并验证' : report.state === 'unsupported' ? '当前环境不支持' : '尚未确认支持') : '等待 Agent 检查'}</span></div>
        <div className="server-about-item"><span className="server-about-label">生效时间</span><span>{report?.applied_at ? new Date(report.applied_at).toLocaleString() : '尚未确认'}</span></div>
        <div className="server-about-item"><span className="server-about-label">最近检查</span><span>{report?.checked_at ? new Date(report.checked_at).toLocaleString() : '尚未检查'}</span></div>
      </div>
      <FormField label="运行安全模式" hint="强化模式可能短暂重启内核；禁止新增权限与进程转储，并加密 Agent 私有 SSH 状态。">
        <Select aria-label="运行安全模式" value={mode} onChange={e => setMode(e.target.value)} disabled={disabled || pending}>
          <option value="standard">标准</option><option value="enhanced">强化</option>
        </Select>
      </FormField>
      <p className="muted">本地 Hardened 策略独立生效，面板不能放宽其权限。文件保护和同机加密不能阻止容器 root 或宿主机管理员访问。</p>
      {localDenial && <p role="status">本机 Hardened 策略禁止远程降级，请由主机管理员在本地处理。</p>}
      {disabled && <p className="muted">当前账号只有查看权限。</p>}
      <div className="server-workspace-actions"><button type="button" disabled={pending || disabled || localDenial || !view} onClick={() => void submit('apply')}>应用模式</button></div>
      {notice && <p role="status">{notice}</p>}
      {view?.queued && <p role="status">任务 #{view.task_id}：{view.task_status === 'running' ? 'Agent 正在处理' : '等待执行'}</p>}
      {view?.last_task_status === 'failed' && !view.queued && <p role="alert">任务 #{view.last_task_id} 执行失败。请查看任务记录中的失败阶段，处理后重新应用。</p>}
      {report?.error_code && <p role="alert">{phases[report.phase] || '安全配置'}失败：{report.error_code}。核对环境后可重新应用。</p>}
      {error && <p role="alert">{error}</p>}
    </section>
    <section className="server-detail-section">
      <h3>安全检查</h3>
      <div className="server-workspace-actions"><button type="button" className="ghost" disabled={pending || disabled || !view?.online} onClick={() => void submit('check')}>重新检查</button>{issues.some(item => item.auto_fix) && <button type="button" className="ghost" disabled={pending || disabled || !view?.online} onClick={() => void submit('repair')}>修复文件权限</button>}<button type="button" className="ghost" onClick={() => setRefresh(v => v + 1)}>刷新状态</button></div>
      {view && !view.online && <p className="muted">Agent 当前离线，展示最后一次检查结果。</p>}
      {Object.entries(categories).map(([category, label]) => {
        const items = (report?.checks || []).filter(item => item.category === category) || []
        return items.length ? <div key={category}><h4>{label}</h4><ul>{items.map(item => <li key={item.id} style={{overflowWrap:'anywhere',marginBottom:8}}><strong>{checks[item.status] || '尚未确认'}</strong> · {item.message}</li>)}</ul></div> : null
      })}
    </section>
    <section className="server-detail-section"><h3>安全问题</h3>{issues.length ? <ul>{issues.map(item => <li key={item.id} style={{overflowWrap:'anywhere',marginBottom:12}}><strong>{item.severity === 'high' ? '高风险' : '注意'}</strong> · {item.message}<p className="muted">{item.remedy}</p></li>)}</ul> : <p className="muted">{report ? '已完成的检查未发现问题；未知和不支持的项目不计为通过。' : '等待检查结果。'}</p>}</section>
  </div>
}
