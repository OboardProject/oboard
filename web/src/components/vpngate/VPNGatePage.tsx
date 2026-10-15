import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { ListTree, Pencil, Plus, RefreshCw, RotateCcw, Trash2 } from "lucide-react"
import { Dialog } from "../ui/dialog"
import { FormField } from "../ui/form-field"
import { Select } from "../ui/select"
import { Switch } from "../ui/switch"
import "./vpngate.css"

type Client = { request: (path: string, init?: RequestInit) => Promise<any>; requestV2?: (path: string, init?: RequestInit) => Promise<any> }
type ServerRef = { id: number; name: string }

export type VPNGatePool = {
  id: number
  name: string
  countries: string[]
  operator_asns: string[]
  network: "any" | "tcp" | "udp"
  select_mode: "latency" | "quality"
  max_rtt_ms: number
  min_quality_score: number
  hot_standby: number
  pool_size: number
  switch_policy: "on_failure" | "improve"
  refresh_minutes: number
  enabled: boolean
}

type Candidate = { node_id: number; member_id: string; ip: string; port: number; network: string; country_code: string; status: string; stage?: string; error?: string; egress_ip?: string; asn?: string; organization?: string; handshake_ms?: number; rtt_ms?: number; quality: number; eligible: boolean; reason?: string; success_rate: number; selected: boolean }
type MemberStatus = { member_id: string; state: string; active: boolean; failures: number; last_error?: string; rtt_ms?: number }
type PoolServer = { pool_id: number; server_id: number; revision: number; members: Candidate[]; candidates: Candidate[]; status?: { revision: number; active_member?: string; healthy: number; members: MemberStatus[] }; status_at?: string; last_probe_at?: string; next_refresh_at?: string; last_error?: string; history: { at: string; reason: string; from?: string; to?: string }[] }
type PoolView = VPNGatePool & { in_use: boolean; servers: PoolServer[] }
type Overview = { pools: PoolView[]; catalog: { fetched_at?: string; entries: number; skipped: number; total: number; error?: string }; providers: Record<string, boolean> }

const emptyPool: VPNGatePool = { id: 0, name: "", countries: ["JP"], operator_asns: [], network: "any", select_mode: "latency", max_rtt_ms: 0, min_quality_score: 0, hot_standby: 1, pool_size: 4, switch_policy: "on_failure", refresh_minutes: 30, enabled: true }

// Common carrier groups. ASNs are what the exit IP is matched against; the
// names are only a convenience for filling the field.
const operatorPresets: { label: string; asns: string[] }[] = [
  { label: "日本 NTT", asns: ["AS4713", "AS2914"] },
  { label: "日本 KDDI", asns: ["AS2516"] },
  { label: "日本 SoftBank", asns: ["AS17676"] },
  { label: "韩国 KT", asns: ["AS4766"] },
  { label: "韩国 SK Broadband", asns: ["AS9318"] },
  { label: "韩国 LG U+", asns: ["AS3786"] },
]

const date = (value?: string) => value && !value.startsWith("0001") ? new Date(value).toLocaleString() : "—"
const stateText: Record<string, string> = { up: "已连接", connecting: "连接中", cooldown: "冷却中", idle: "备用" }
const listText = (values: string[], empty: string) => values.length ? values.join("、") : empty

function poolSummary(pool: VPNGatePool) {
  return [
    `国家/地区：${listText(pool.countries, "不限")}`,
    `运营商：${listText(pool.operator_asns, "不限")}`,
    pool.select_mode === "quality" ? "质量优先" : "延迟优先",
    `热备 ${pool.hot_standby} 个 · 候选 ${pool.pool_size} 个`,
    pool.switch_policy === "improve" ? "发现更优时切换" : "仅失效时切换",
    `每 ${pool.refresh_minutes} 分钟复测`,
  ]
}

export function VPNGatePage({ client, servers, canManage }: { client: Client; servers: ServerRef[]; canManage: boolean }) {
  const [overview, setOverview] = useState<Overview | null>(null)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [editing, setEditing] = useState<VPNGatePool | null>(null)
  const [deleting, setDeleting] = useState<PoolView | null>(null)
  const [detail, setDetail] = useState<{ poolID: number; serverID: number } | null>(null)
  const request = useCallback(async (path: string, init?: RequestInit) => {
    if (client.requestV2) return client.requestV2(path, init)
    const result = await client.request(path, init); return result.data ?? result
  }, [client])
  const refresh = useCallback(async (signal?: AbortSignal) => {
    try { const result = await request("/vpngate", { signal }); if (!signal?.aborted) { setOverview(result); setError("") } }
    catch (e: any) { if (!signal?.aborted) setError(e.message || "VPNGate 出口池加载失败") }
    finally { if (!signal?.aborted) setLoading(false) }
  }, [request])
  useEffect(() => { const abort = new AbortController(); void refresh(abort.signal); const timer = window.setInterval(() => { if (!document.hidden) void refresh(abort.signal) }, 15000); return () => { abort.abort(); window.clearInterval(timer) } }, [refresh])

  const mutate = async (action: "save" | "delete" | "reselect", input: any, success: string) => {
    if (busyRef.current) return
    busyRef.current = true; setBusy(true); setError(""); setNotice("")
    try {
      await request("/vpngate", { method: "POST", body: JSON.stringify({ action, request_id: crypto.randomUUID(), input }) })
      setNotice(success); setEditing(null); setDeleting(null)
      await refresh()
    } catch (e: any) { setError(e.message || "操作失败") }
    finally { busyRef.current = false; setBusy(false) }
  }
  const serverName = useCallback((id: number) => servers.find(server => server.id === id)?.name || `服务器 #${id}`, [servers])
  const pools = overview?.pools || []
  const catalog = overview?.catalog
  const ipinfoMissing = overview && !overview.providers?.ipinfo
  const detailPool = detail ? pools.find(pool => pool.id === detail.poolID) : undefined
  const detailServer = detailPool?.servers.find(item => item.server_id === detail?.serverID)

  return <section aria-label="VPNGate 出口池" aria-busy={loading} className="vpngate-page">
    <div className="return-latency-toolbar">
      <button type="button" className="primary" disabled={!canManage || busy} onClick={() => { setError(""); setEditing({ ...emptyPool }) }}><Plus size={15} aria-hidden="true" />创建出口池</button>
      <button type="button" className="ghost" onClick={() => void refresh()} disabled={loading}><RefreshCw size={15} aria-hidden="true" />刷新</button>
    </div>
    <div className="vpngate-catalog" role="status">
      <span>VPNGate 目录：已累积 <strong>{catalog?.total ?? 0}</strong> 个服务器</span>
      <span className="muted">最近获取 {date(catalog?.fetched_at)}</span>
      {catalog?.error && <span className="danger-text">获取失败：{catalog.error}</span>}
    </div>
    {ipinfoMissing && <p className="muted vpngate-note">未配置 <code>OBOARD_IPINFO_TOKEN</code>，无法确认出口运营商；设置了运营商条件的出口池将没有可用节点并拒绝出站。</p>}
    {error && !editing && !deleting && <p className="danger-text" role="alert">{error}</p>}
    {notice && <p className="muted" role="status">{notice}</p>}
    {loading && !overview ? <p className="muted" role="status">正在加载出口池…</p> : !pools.length ? <div className="latency-empty-state">
      <p>还没有 VPNGate 出口池</p>
      <p className="muted">出口池按国家/地区与运营商筛选 VPNGate 服务器，由出口服务器实测后自动选择并热备切换；没有可用节点时拒绝出站。创建后在代理拓扑中把它作为链路出口。</p>
      <button type="button" className="primary" disabled={!canManage} onClick={() => setEditing({ ...emptyPool })}><Plus size={15} aria-hidden="true" />创建出口池</button>
    </div> : <div className="vpngate-pools">
      {pools.map(pool => <article key={pool.id} className="vpngate-pool">
        <header>
          <div>
            <h3>{pool.name}</h3>
            <span className={`probe-task-badge ${pool.enabled ? "online" : ""}`}>{pool.enabled ? "启用" : "已停用（拒绝出站）"}</span>
            {pool.in_use && <span className="probe-task-badge">拓扑使用中</span>}
          </div>
          <div className="probe-task-card-actions">
            <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => void mutate("reselect", { id: pool.id, refresh_catalog: true }, `已请求「${pool.name}」重新获取目录并选择节点`)}><RotateCcw size={14} aria-hidden="true" />立即重选</button>
            <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => { setError(""); setEditing({ ...pool }) }}><Pencil size={14} aria-hidden="true" />编辑</button>
            <button type="button" className="ghost danger" disabled={!canManage || busy || pool.in_use} title={pool.in_use ? "请先在代理拓扑中移除使用该出口池的出口" : undefined} onClick={() => { setError(""); setDeleting(pool) }}><Trash2 size={14} aria-hidden="true" />删除</button>
          </div>
        </header>
        <p className="muted vpngate-summary">{poolSummary(pool).join(" · ")}</p>
        {!pool.servers.length ? <p className="muted">尚未有出口服务器使用该出口池。</p> : <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label={`${pool.name} 的出口服务器`}><table className="network-probe-table">
          <thead><tr><th scope="col">出口服务器</th><th scope="col">当前出口</th><th scope="col">健康</th><th scope="col">下次复测</th><th scope="col">状态</th><th scope="col" className="network-probe-actions-heading">详情</th></tr></thead>
          <tbody>{pool.servers.map(state => {
            const primary = state.members[0]
            const healthy = state.status ? `${state.status.healthy} / ${state.members.length}` : "等待上报"
            return <tr key={state.server_id}>
              <td><strong>{serverName(state.server_id)}</strong></td>
              <td data-label="当前出口">{primary ? <>{primary.egress_ip || primary.ip}<small className="muted vpngate-sub">{[primary.asn, primary.country_code, primary.rtt_ms ? `${primary.rtt_ms} ms` : ""].filter(Boolean).join(" · ")}</small></> : <span className="danger-text">无可用节点</span>}</td>
              <td data-label="健康">{healthy}</td>
              <td data-label="下次复测">{date(state.next_refresh_at)}</td>
              <td data-label="状态">{state.last_error ? <span className="danger-text">{state.last_error}</span> : <span className="muted">正常</span>}</td>
              <td><button type="button" className="ghost" onClick={() => setDetail({ poolID: pool.id, serverID: state.server_id })}><ListTree size={14} aria-hidden="true" />候选与记录</button></td>
            </tr>
          })}</tbody>
        </table></div>}
      </article>)}
    </div>}

    <Dialog isOpen={Boolean(editing)} onClose={() => { if (!busy) setEditing(null) }} title={editing?.id ? "编辑出口池" : "创建出口池"} size="lg" busy={busy}>
      {editing && <PoolForm pool={editing} busy={busy} error={error} onChange={setEditing} onCancel={() => setEditing(null)} onSubmit={() => void mutate("save", editing, editing.id ? "出口池已更新，将按新条件重新选择节点" : "出口池已创建")} />}
    </Dialog>
    <Dialog isOpen={Boolean(deleting)} onClose={() => { if (!busy) setDeleting(null) }} title="删除出口池" size="sm" busy={busy} footer={<>
      <button type="button" className="ghost" disabled={busy} onClick={() => setDeleting(null)}>取消</button>
      <button type="button" className="danger" disabled={busy} onClick={() => deleting && void mutate("delete", { id: deleting.id }, "出口池已删除")}>{busy ? "删除中…" : "确认删除"}</button>
    </>}>
      <p>删除出口池「{deleting?.name}」及其选优记录？</p>
      {error && <p role="alert" className="danger-text">{error}</p>}
    </Dialog>
    <Dialog isOpen={Boolean(detailServer)} onClose={() => setDetail(null)} title={detailPool && detail ? `${detailPool.name} · ${serverName(detail.serverID)}` : "候选节点"} size="xl">
      {detailServer && <PoolServerDetail state={detailServer} />}
    </Dialog>
  </section>
}

function PoolServerDetail({ state }: { state: PoolServer }) {
  const live = useMemo(() => new Map((state.status?.members || []).map(member => [member.member_id, member])), [state.status])
  return <div className="vpngate-detail">
    <dl className="probe-monitor-meta">
      <div><dt>最近测试</dt><dd>{date(state.last_probe_at)}</dd></div>
      <div><dt>下次复测</dt><dd>{date(state.next_refresh_at)}</dd></div>
      <div><dt>节点上报</dt><dd>{date(state.status_at)}</dd></div>
      <div><dt>成员版本</dt><dd>{state.revision}{state.status && state.status.revision !== state.revision ? "（下发中）" : ""}</dd></div>
    </dl>
    {state.last_error && <p className="danger-text" role="status">{state.last_error}</p>}
    <h4>候选节点</h4>
    {!state.candidates.length ? <p className="muted">尚未完成测试。</p> : <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label="候选节点"><table className="network-probe-table">
      <thead><tr><th scope="col">节点</th><th scope="col">出口 IP / 运营商</th><th scope="col">延迟</th><th scope="col">质量分</th><th scope="col">成功率</th><th scope="col">结果</th></tr></thead>
      <tbody>{state.candidates.map(candidate => {
        const runtime = live.get(candidate.member_id)
        return <tr key={candidate.member_id}>
          <td>{candidate.ip}:{candidate.port}<small className="muted vpngate-sub">{candidate.network.toUpperCase()} · {candidate.country_code}</small></td>
          <td data-label="出口 IP / 运营商">{candidate.egress_ip || "—"}<small className="muted vpngate-sub">{[candidate.asn, candidate.organization].filter(Boolean).join(" ") || "未知"}</small></td>
          <td data-label="延迟">{candidate.rtt_ms ? `${candidate.rtt_ms} ms` : "—"}</td>
          <td data-label="质量分">{candidate.quality}</td>
          <td data-label="成功率">{Math.round(candidate.success_rate * 100)}%</td>
          <td data-label="结果">{candidate.selected ? <span className="probe-task-badge online">{runtime?.active ? "当前出口" : runtime ? stateText[runtime.state] || runtime.state : "已选用"}</span> : candidate.eligible ? <span className="muted">备选</span> : <span className="muted">{candidate.reason || candidate.error || "不可用"}</span>}</td>
        </tr>
      })}</tbody>
    </table></div>}
    <h4>切换记录</h4>
    {!state.history.length ? <p className="muted">暂无切换。</p> : <ul className="vpngate-history">{[...state.history].reverse().map((event, index) => <li key={index}><span className="muted">{date(event.at)}</span> {event.reason}{event.from || event.to ? <span className="muted">（{event.from || "无"} → {event.to || "无"}）</span> : null}</li>)}</ul>}
  </div>
}

function PoolForm({ pool, busy, error, onChange, onCancel, onSubmit }: { pool: VPNGatePool; busy: boolean; error: string; onChange: (pool: VPNGatePool) => void; onCancel: () => void; onSubmit: () => void }) {
  const [countries, setCountries] = useState(pool.countries.join(", "))
  const [asns, setASNs] = useState(pool.operator_asns.join(", "))
  const split = (value: string) => value.split(/[\s,，、]+/).map(item => item.trim()).filter(Boolean)
  const update = (patch: Partial<VPNGatePool>) => onChange({ ...pool, ...patch })
  const canSubmit = !busy && pool.name.trim() !== "" && pool.pool_size >= pool.hot_standby + 1
  const addPreset = (values: string[]) => { const next = Array.from(new Set([...split(asns).map(item => item.toUpperCase()), ...values])); setASNs(next.join(", ")); update({ operator_asns: next }) }
  return <form className="probe-task-form" onSubmit={event => { event.preventDefault(); if (canSubmit) onSubmit() }}>
    <fieldset disabled={busy} className="return-latency-fields">
      <div className="probe-task-form-grid">
        <FormField label="名称" required full>
          <input aria-label="名称" required maxLength={64} placeholder="例如：日本家宽" value={pool.name} onChange={event => update({ name: event.target.value })} />
        </FormField>
        <FormField label="国家/地区" hint="两位代码，多个用逗号分隔，例如 JP, KR；留空不限。" full>
          <input aria-label="国家/地区" value={countries} onChange={event => { setCountries(event.target.value); update({ countries: split(event.target.value).map(item => item.toUpperCase()) }) }} />
        </FormField>
        <FormField label="运营商 ASN" hint="按实际出口 IP 的 ASN 匹配；留空不限。设置后无法确认运营商的节点不会被使用。" full>
          <input aria-label="运营商 ASN" placeholder="例如 AS4713, AS2516" value={asns} onChange={event => { setASNs(event.target.value); update({ operator_asns: split(event.target.value).map(item => item.toUpperCase()) }) }} />
          <div className="vpngate-presets">{operatorPresets.map(preset => <button key={preset.label} type="button" className="ghost" onClick={() => addPreset(preset.asns)}>{preset.label}</button>)}</div>
        </FormField>
        <FormField label="选优策略" required>
          <Select variant="segmented" aria-label="选优策略" value={pool.select_mode} onChange={event => update({ select_mode: event.target.value as VPNGatePool["select_mode"] })}>
            <option value="latency">延迟优先</option><option value="quality">质量优先</option>
          </Select>
        </FormField>
        <FormField label="协议">
          <Select aria-label="协议" value={pool.network} onChange={event => update({ network: event.target.value as VPNGatePool["network"] })}>
            <option value="any">TCP 与 UDP</option><option value="tcp">仅 TCP</option><option value="udp">仅 UDP</option>
          </Select>
        </FormField>
        <FormField label="延迟上限（毫秒）" hint="0 表示不限制。">
          <input aria-label="延迟上限" type="number" min={0} max={5000} value={pool.max_rtt_ms} onChange={event => update({ max_rtt_ms: Number(event.target.value) || 0 })} />
        </FormField>
        <FormField label="质量分下限" hint="0-100，0 表示不限制。">
          <input aria-label="质量分下限" type="number" min={0} max={100} value={pool.min_quality_score} onChange={event => update({ min_quality_score: Number(event.target.value) || 0 })} />
        </FormField>
        <FormField label="热备数量" hint="与当前出口同时保持连接、可秒级切换的节点数。">
          <Select aria-label="热备数量" value={String(pool.hot_standby)} onChange={event => update({ hot_standby: Number(event.target.value) })}>
            <option value="0">0</option><option value="1">1</option><option value="2">2</option>
          </Select>
        </FormField>
        <FormField label="候选节点数" hint="下发到出口服务器的有序节点数，1-8。">
          <input aria-label="候选节点数" type="number" min={1} max={8} value={pool.pool_size} onChange={event => update({ pool_size: Number(event.target.value) || 1 })} />
        </FormField>
        <FormField label="切换策略">
          <Select aria-label="切换策略" value={pool.switch_policy} onChange={event => update({ switch_policy: event.target.value as VPNGatePool["switch_policy"] })}>
            <option value="on_failure">仅失效时切换（出口 IP 稳定）</option><option value="improve">发现明显更优时切换</option>
          </Select>
        </FormField>
        <FormField label="复测周期（分钟）" hint="15 分钟至 1 天。">
          <input aria-label="复测周期" type="number" min={15} max={1440} value={pool.refresh_minutes} onChange={event => update({ refresh_minutes: Number(event.target.value) || 30 })} />
        </FormField>
        <FormField label="启用" hint="停用后使用该出口池的链路拒绝出站，不会改走其他出口。" full>
          <Switch aria-label="启用" checked={pool.enabled} onChange={checked => update({ enabled: checked })} />
        </FormField>
      </div>
    </fieldset>
    {error && <p role="alert" className="danger-text">{error}</p>}
    <div className="dialog-actions">
      <button type="button" className="ghost" disabled={busy} onClick={onCancel}>取消</button>
      <button type="submit" className="primary" disabled={!canSubmit}>{busy ? "保存中…" : "保存"}</button>
    </div>
  </form>
}
