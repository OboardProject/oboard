import { useState } from 'react'
import { useCoalescedReadRequest } from './request-coalesce'
import { applyAuditChange, prepareAuditChange, type AuditClient } from './audit-changes'

type Scale = { start: number; full: number }
type Threshold = { start: number; full: number; unit: string }
type PolicyConfig = {
  revision: number
  source_grouping: { ipv4_prefix_bits: number; ipv6_prefix_bits: number; epoch: number }
  policy: { version: string; activity_sources: Scale; activity_minutes: Scale; exposure_sources: Scale; source_capacity: number; minimum_bytes: number; minimum_slices: number }
  resources: { request_rate: Threshold | null; connections: Threshold | null; traffic_rate: Threshold | null }
}
type PolicyRead = PolicyConfig & { source: { version: string; epoch: number; ipv4_prefix_bits: number; ipv6_prefix_bits: number }; resource_measurement: string }

// Mounted inside the audit settings dialog, independently of collection settings.
export function AuditPolicySettings({ client }: { client: AuditClient }) {
  const [config, setConfig] = useState<PolicyRead | null>(null)
  const [revision, refresh] = useState(0)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [prepared, setPrepared] = useState<{ id: string; reason: string } | null>(null)
  useCoalescedReadRequest(`audit-policy:${revision}`, signal => client.request('/audit/policy', { signal }), {
    onSuccess: (value: PolicyRead) => { setConfig(value); setPrepared(null) },
    onError: () => setError('无法读取判定规则，请检查管理员权限或稍后重试'),
  })
  const mutate = (next: PolicyRead) => { setConfig(next); setPrepared(null); setError('') }
  const prepare = async () => {
    if (!config || busy) return
    setBusy(true); setError('')
    try {
      const scales = [config.policy.activity_sources, config.policy.activity_minutes, config.policy.exposure_sources]
      if (scales.some((v, i) => !Number.isInteger(v.start) || !Number.isInteger(v.full) || v.start < 0 || v.full <= v.start || v.full > (i === 1 ? 30 : 32)) || !Number.isSafeInteger(config.policy.minimum_bytes) || config.policy.minimum_bytes < 1 || config.policy.minimum_bytes > 2 ** 40 || !Number.isInteger(config.policy.minimum_slices) || config.policy.minimum_slices < 1 || config.policy.minimum_slices > 12) throw new Error('请填写有效阈值，贡献上限必须大于起点')
      if (Object.values(config.resources).some(v => v !== null && (!Number.isSafeInteger(v.start) || !Number.isSafeInteger(v.full) || v.start < 0 || v.full <= v.start))) throw new Error('资源起点与贡献上限须为非负安全整数，贡献上限须大于起点；清空表示未配置')
      const grouping = config.source_grouping
      if (!Number.isInteger(grouping.ipv4_prefix_bits) || grouping.ipv4_prefix_bits < 0 || grouping.ipv4_prefix_bits > 24 || !Number.isInteger(grouping.ipv6_prefix_bits) || grouping.ipv6_prefix_bits < 0 || grouping.ipv6_prefix_bits > 56 || !Number.isSafeInteger(grouping.epoch) || grouping.epoch < config.source.epoch) throw new Error('来源分组须为 IPv4 /0–24、IPv6 /0–56，代次不可降低')
      if ((grouping.ipv4_prefix_bits !== config.source.ipv4_prefix_bits || grouping.ipv6_prefix_bits !== config.source.ipv6_prefix_bits) && grouping.epoch <= config.source.epoch) throw new Error('修改前缀长度时请同时递增来源代次')
      const reason = '调整账号判定及来源规则；来源分组变更后历史比较为未知，重新积累至少七天，不拼接旧代次；不改变采集、授权或配额'
      const input: PolicyConfig = { revision: config.revision, policy: config.policy, resources: config.resources, source_grouping: config.source_grouping }
      const result = await prepareAuditChange(client, 'audit.policy.update', input, reason, crypto.randomUUID())
      setPrepared({ id: result.id, reason })
    } catch (e) { setError(e instanceof Error ? e.message : '校验失败') } finally { setBusy(false) }
  }
  const apply = async () => {
    if (!prepared || busy) return
    setBusy(true); setError('')
    try { await applyAuditChange(client, prepared.id, prepared.reason); refresh(n => n + 1) }
    catch (e) { setError(e instanceof Error ? e.message : '保存失败，请重新读取并校验') } finally { setBusy(false) }
  }
  return <section className="settings-card"><h3>账号判定规则</h3>
    <p className="muted">与采集档位独立管理。阈值只用于观察与提醒，不自动封禁，不修改配额。修改后产生新策略版本，已保存的历史判断不被改写。</p>
    {error && <p role="alert" className="form-error">{error}</p>}
    {!config ? <p role="status">正在读取判定规则…</p> : <>
      <p className="muted">策略版本：{config.policy.version} · 来源容量：32</p>
      <fieldset disabled={busy}><legend>风险阈值</legend>
        {(['activity_sources', 'activity_minutes', 'exposure_sources'] as const).map((key, i) => <div key={key} className="audit-console-toolbar"><strong>{['共同活跃来源规模', '累计达到规模的分钟', '重复新来源数'][i]}</strong>{(['start', 'full'] as const).map(bound => <label key={bound}>{bound === 'start' ? '开始计分' : '贡献上限'}<input aria-label={`${['共同活跃来源规模', '累计分钟', '重复新来源数'][i]}${bound === 'start' ? '开始计分' : '贡献上限'}`} type="number" min={bound === 'start' ? 0 : 1} max={i === 1 ? 30 : 32} step="1" value={config.policy[key][bound]} onChange={e => mutate({ ...config, policy: { ...config.policy, [key]: { ...config.policy[key], [bound]: Number(e.target.value) } } })} /></label>)}</div>)}
        <label>每来源每分钟最低有效流量（字节）<input type="number" min="1" max={2 ** 40} step="1" value={config.policy.minimum_bytes} onChange={e => mutate({ ...config, policy: { ...config.policy, minimum_bytes: Number(e.target.value) } })} /></label>
        <label>每来源最低活跃片数（每片5秒）<input type="number" min="1" max="12" step="1" value={config.policy.minimum_slices} onChange={e => mutate({ ...config, policy: { ...config.policy, minimum_slices: Number(e.target.value) } })} /></label>
      </fieldset>
      <fieldset disabled={busy}><legend>资源观察阈值（可选）</legend><p className="muted">留空表示未配置。速率取最近完整一分钟的平均值；覆盖不足或连接数尚无观测时显示不可用，不按零计算。</p>
        {(['request_rate', 'connections', 'traffic_rate'] as const).map((key, i) => <div key={key} className="audit-console-toolbar"><strong>{['请求速率（请求/秒）', '同时连接数（条）', '流量速率（字节/秒）'][i]}</strong>{(['start', 'full'] as const).map(bound => <label key={bound}>{bound === 'start' ? '开始计分' : '贡献上限'}<input aria-label={`${['请求速率', '同时连接数', '流量速率'][i]}${bound === 'start' ? '开始计分' : '贡献上限'}`} type="number" min={bound === 'start' ? 0 : 1} max={Number.MAX_SAFE_INTEGER} step="1" value={config.resources[key]?.[bound] ?? ''} onChange={e => mutate({ ...config, resources: { ...config.resources, [key]: e.target.value === '' ? null : { start: config.resources[key]?.start ?? 0, full: config.resources[key]?.full ?? 0, unit: ['requests/second', 'connections', 'bytes/second'][i], [bound]: Number(e.target.value) } } })} /></label>)}</div>)}
      </fieldset>
      <fieldset disabled={busy}><legend>来源分组</legend><p className="muted">当前生效：IPv4 /{config.source.ipv4_prefix_bits}、IPv6 /{config.source.ipv6_prefix_bits} · 代次 {config.source.epoch}。仅在校验并确认后生效。来源组不代表设备或人数。只能合并采集网段，不能细分；修改前缀须同时递增代次。变更后历史比较为未知，重新积累至少七天，不拼接旧代次。</p>
        {(['ipv4_prefix_bits', 'ipv6_prefix_bits', 'epoch'] as const).map((key, i) => <label key={key}>{['IPv4 前缀长度', 'IPv6 前缀长度', '来源代次（递增以手动轮换）'][i]}<input type="number" min={i === 2 ? config.source.epoch : 0} max={[24, 56, Number.MAX_SAFE_INTEGER][i]} step="1" value={config.source_grouping[key]} onChange={e => mutate({ ...config, source_grouping: { ...config.source_grouping, [key]: Number(e.target.value) } })} /></label>)}
      </fieldset>
      {prepared ? <><p role="status">规则已校验：{prepared.reason}</p><button type="button" disabled={busy} onClick={() => void apply()}>{busy ? '正在应用…' : '确认应用判定规则'}</button></> : <button type="button" disabled={busy} onClick={() => void prepare()}>{busy ? '正在校验…' : '校验判定规则'}</button>}
      <button type="button" className="ghost" disabled={busy} onClick={() => refresh(n => n + 1)}>重新读取</button>
    </>}
  </section>
}
