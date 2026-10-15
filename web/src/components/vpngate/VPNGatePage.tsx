import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AlertTriangle, ListTree, Pencil, Plus, RefreshCw, RotateCcw, Trash2 } from "lucide-react"
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

// The ASNs are what the exit IP is matched against; the carrier names only
// fill the field faster.
const operatorPresets: { label: string; asns: string[] }[] = [
  { label: "NTT", asns: ["AS4713"] },
  { label: "KDDI", asns: ["AS2516"] },
  { label: "SoftBank", asns: ["AS17676"] },
  { label: "KT", asns: ["AS4766"] },
  { label: "SK Broadband", asns: ["AS9318"] },
  { label: "LG U+", asns: ["AS3786"] },
]

const validDate = (value?: string) => Boolean(value && !value.startsWith("0001"))
const date = (value?: string) => validDate(value) ? new Date(value!).toLocaleString() : "—"
function relative(value?: string) {
  if (!validDate(value)) return "尚未获取"
  const minutes = Math.round((Date.now() - new Date(value!).getTime()) / 60000)
  if (Math.abs(minutes) < 1) return "刚刚"
  if (minutes < 0) return `${-minutes} 分钟后`
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.round(minutes / 60)
  return hours < 48 ? `${hours} 小时前` : new Date(value!).toLocaleDateString()
}

function criteria(pool: VPNGatePool) {
  return [
    pool.countries.length ? `地区 ${pool.countries.join("、")}` : "地区不限",
    pool.operator_asns.length ? `运营商 ${pool.operator_asns.join("、")}` : "运营商不限",
    pool.select_mode === "quality" ? "质量优先" : "延迟优先",
    pool.network === "any" ? "TCP / UDP" : pool.network.toUpperCase(),
    pool.hot_standby ? `热备 ${pool.hot_standby}` : "无热备",
    pool.switch_policy === "improve" ? "有更优节点时切换" : "失效时才切换",
    `每 ${pool.refresh_minutes} 分钟复测`,
  ]
}

function exitState(pool: VPNGatePool, state: PoolServer): { tone: string; text: string; detail?: string } {
  if (!pool.enabled) return { tone: "muted", text: "暂停出站", detail: "出口池已停用" }
  if (!state.members.length) return { tone: "danger", text: "暂停出站", detail: (state.last_error || "暂无可用节点").replace(/，?当前拒绝出站$/, "") }
  if (!state.status) return { tone: "muted", text: "等待上报" }
  if (state.status.revision !== state.revision) return { tone: "muted", text: "正在下发节点" }
  if (!state.status.healthy) return { tone: "danger", text: "正在重新挑选", detail: "当前节点都不可用" }
  return { tone: "ok", text: "正常" }
}

export function VPNGatePage({ client, servers, canManage, notify }: { client: Client; servers: ServerRef[]; canManage: boolean; notify?: (message: string, tone?: "success" | "error" | "warning" | "info") => void }) {
  const [overview, setOverview] = useState<Overview | null>(null)
  const [error, setError] = useState("")
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
    catch (e: any) { if (!signal?.aborted) setError(e.message || "加载失败，请稍后刷新") }
    finally { if (!signal?.aborted) setLoading(false) }
  }, [request])
  useEffect(() => { const abort = new AbortController(); void refresh(abort.signal); const timer = window.setInterval(() => { if (!document.hidden) void refresh(abort.signal) }, 15000); return () => { abort.abort(); window.clearInterval(timer) } }, [refresh])

  const mutate = async (action: "save" | "delete" | "reselect", input: any, success: string) => {
    if (busyRef.current) return
    busyRef.current = true; setBusy(true); setError("")
    try {
      await request("/vpngate", { method: "POST", body: JSON.stringify({ action, request_id: crypto.randomUUID(), input }) })
      notify?.(success, "success"); setEditing(null); setDeleting(null)
      await refresh()
    } catch (e: any) { setError(e.message || "操作失败") }
    finally { busyRef.current = false; setBusy(false) }
  }
  const serverName = useCallback((id: number) => servers.find(server => server.id === id)?.name || `服务器 #${id}`, [servers])
  const pools = overview?.pools || []
  const catalog = overview?.catalog
  const exits = pools.flatMap(pool => pool.servers.map(state => ({ pool, state })))
  const blocked = exits.filter(({ pool, state }) => exitState(pool, state).tone === "danger").length
  const ipinfoMissing = overview && !overview.providers?.ipinfo
  const detailPool = detail ? pools.find(pool => pool.id === detail.poolID) : undefined
  const detailServer = detailPool?.servers.find(item => item.server_id === detail?.serverID)
  const create = () => { setError(""); setEditing({ ...emptyPool }) }

  return <section aria-label="VPNGate" aria-busy={loading} className="vpngate-page">
    <header className="vpngate-intro">
      <p>从 VPNGate 公共节点中按地区和运营商挑选链路出口。出口服务器实测延迟，主控结合 IP 质量打分并自动维护热备；当前节点失效时秒级切换，找不到合适节点时暂停出站，不会改走其他线路。</p>
      <div className="vpngate-actions">
        <button type="button" className="ghost" onClick={() => void refresh()} disabled={loading}><RefreshCw size={15} aria-hidden="true" />刷新</button>
        <button type="button" className="primary" disabled={!canManage || busy} onClick={create}><Plus size={15} aria-hidden="true" />新建出口池</button>
      </div>
    </header>

    <div className="vpngate-stats" role="list">
      <div role="listitem" className="vpngate-stat">
        <span>节点目录</span>
        <strong>{catalog?.total ?? 0}</strong>
        <small className={catalog?.error ? "danger-text" : "muted"}>{catalog?.error ? `更新失败：${catalog.error}` : `更新于 ${relative(catalog?.fetched_at)}`}</small>
      </div>
      <div role="listitem" className="vpngate-stat">
        <span>出口池</span>
        <strong>{pools.length}</strong>
        <small className="muted">{pools.filter(pool => pool.in_use).length} 个已用于拓扑</small>
      </div>
      <div role="listitem" className="vpngate-stat">
        <span>出口服务器</span>
        <strong>{exits.length}</strong>
        <small className={blocked ? "danger-text" : "muted"}>{blocked ? `${blocked} 台暂停出站` : "全部可出站"}</small>
      </div>
    </div>

    {ipinfoMissing && <div className="vpngate-callout" role="note"><AlertTriangle size={16} aria-hidden="true" /><span>IPinfo 未填写 Key，出口运营商改用免 Key 接口与备用来源识别，额度较低；识别失败时限定了运营商的出口池会跳过该节点。可在“设置 › 节点 › IP 与路由数据”填写。</span></div>}
    {error && !editing && !deleting && <p className="danger-text" role="alert">{error}</p>}

    {loading && !overview ? <p className="muted" role="status">正在加载…</p> : !pools.length ? <div className="vpngate-empty">
      <h3>还没有出口池</h3>
      <ol>
        <li><strong>新建出口池</strong><span>选择地区、运营商和挑选方式。</span></li>
        <li><strong>接入拓扑</strong><span>在代理拓扑中添加「VPNGate 出口」，连到服务器后面。</span></li>
        <li><strong>自动维护</strong><span>部署后由出口服务器实测，自动挑选、热备和切换。</span></li>
      </ol>
      <button type="button" className="primary" disabled={!canManage} onClick={create}><Plus size={15} aria-hidden="true" />新建出口池</button>
    </div> : <div className="vpngate-pools">
      {pools.map(pool => <article key={pool.id} className={`vpngate-pool${pool.enabled ? "" : " is-disabled"}`}>
        <header>
          <div className="vpngate-pool-title">
            <h3>{pool.name}</h3>
            {!pool.enabled && <span className="probe-task-badge">已停用</span>}
            {pool.in_use && <span className="probe-task-badge online">使用中</span>}
          </div>
          <div className="vpngate-pool-actions">
            <button type="button" className="ghost" disabled={!canManage || busy || !pool.enabled} title="重新获取节点目录并立即挑选" onClick={() => void mutate("reselect", { id: pool.id, refresh_catalog: true }, `「${pool.name}」正在重新挑选节点`)}><RotateCcw size={14} aria-hidden="true" />重新挑选</button>
            <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => { setError(""); setEditing({ ...pool }) }}><Pencil size={14} aria-hidden="true" />编辑</button>
            <button type="button" className="ghost danger" disabled={!canManage || busy || pool.in_use} title={pool.in_use ? "请先在代理拓扑中移除使用它的出口" : undefined} onClick={() => { setError(""); setDeleting(pool) }}><Trash2 size={14} aria-hidden="true" />删除</button>
          </div>
        </header>
        <ul className="vpngate-chips" aria-label="挑选条件">{criteria(pool).map(item => <li key={item}>{item}</li>)}</ul>
        {!pool.servers.length ? <p className="muted vpngate-unused">还没有链路使用这个出口池。在代理拓扑中添加「VPNGate 出口」并选择它，部署后开始挑选节点。</p> : <ul className="vpngate-exits" aria-label={`${pool.name} 的出口服务器`}>
          {pool.servers.map(state => {
            const primary = state.members[0]
            const exit = exitState(pool, state)
            return <li key={state.server_id} className="vpngate-exit">
              <div className="vpngate-exit-server">
                <strong>{serverName(state.server_id)}</strong>
                <span className={`vpngate-state ${exit.tone}`}>{exit.text}</span>
                {exit.detail && <small className="muted">{exit.detail}</small>}
              </div>
              <div className="vpngate-exit-field">
                <span>当前出口</span>
                {primary ? <><strong className="vpngate-mono">{primary.egress_ip || primary.ip}</strong><small className="muted">{[primary.country_code, primary.asn || "运营商未知", primary.rtt_ms ? `${primary.rtt_ms} ms` : ""].filter(Boolean).join(" · ")}</small></> : <strong className="muted">—</strong>}
              </div>
              <div className="vpngate-exit-field">
                <span>可用节点</span>
                <strong>{state.status ? `${state.status.healthy} / ${state.members.length}` : state.members.length ? `— / ${state.members.length}` : "0"}</strong>
              </div>
              <button type="button" className="ghost" aria-label={`查看 ${serverName(state.server_id)} 的节点详情`} onClick={() => setDetail({ poolID: pool.id, serverID: state.server_id })}><ListTree size={14} aria-hidden="true" />节点详情</button>
            </li>
          })}
        </ul>}
      </article>)}
    </div>}

    <Dialog isOpen={Boolean(editing)} onClose={() => { if (!busy) setEditing(null) }} title={editing?.id ? "编辑出口池" : "新建出口池"} size="lg" className="vpngate-pool-dialog" busy={busy} footer={editing ? <>
      <button type="button" className="ghost" disabled={busy} onClick={() => setEditing(null)}>取消</button>
      <button type="submit" form="vpngate-pool-form" className="primary" disabled={!poolCanSubmit(editing, busy)}>{busy ? "保存中…" : "保存"}</button>
    </> : null}>
      {editing && <PoolForm pool={editing} busy={busy} error={error} onChange={setEditing} onSubmit={() => void mutate("save", editing, editing.id ? "已保存，将按新条件重新挑选节点" : "出口池已创建，在代理拓扑中添加「VPNGate 出口」即可使用")} />}
    </Dialog>
    <Dialog isOpen={Boolean(deleting)} onClose={() => { if (!busy) setDeleting(null) }} title="删除出口池" size="sm" busy={busy} footer={<>
      <button type="button" className="ghost" disabled={busy} onClick={() => setDeleting(null)}>取消</button>
      <button type="button" className="danger" disabled={busy} onClick={() => deleting && void mutate("delete", { id: deleting.id }, "出口池已删除")}>{busy ? "删除中…" : "删除"}</button>
    </>}>
      <p>删除「{deleting?.name}」及其挑选记录？此操作不可撤销。</p>
      {error && <p role="alert" className="danger-text">{error}</p>}
    </Dialog>
    <Dialog isOpen={Boolean(detailServer)} onClose={() => setDetail(null)} title={detailPool && detail ? `${detailPool.name} · ${serverName(detail.serverID)}` : "节点详情"} size="xl">
      {detailServer && detailPool && <PoolServerDetail pool={detailPool} state={detailServer} />}
    </Dialog>
  </section>
}

function candidateRole(candidate: Candidate, runtime?: MemberStatus) {
  if (candidate.selected) {
    if (runtime?.active) return { tone: "ok", text: "当前出口" }
    if (runtime?.state === "up") return { tone: "ok", text: "热备" }
    if (runtime?.state === "cooldown") return { tone: "danger", text: "暂不可用" }
    return { tone: "muted", text: "备用" }
  }
  if (candidate.eligible) return { tone: "muted", text: "候补" }
  return { tone: "muted", text: candidate.reason || candidate.error || "不可用" }
}

function PoolServerDetail({ pool, state }: { pool: VPNGatePool; state: PoolServer }) {
  const live = useMemo(() => new Map((state.status?.members || []).map(member => [member.member_id, member])), [state.status])
  const exit = exitState(pool, state)
  return <div className="vpngate-detail">
    <dl className="vpngate-detail-meta">
      <div><dt>状态</dt><dd><span className={`vpngate-state ${exit.tone}`}>{exit.text}</span>{exit.detail && <small className="muted vpngate-detail-reason">{exit.detail}</small>}</dd></div>
      <div><dt>上次测试</dt><dd>{date(state.last_probe_at)}</dd></div>
      <div><dt>下次测试</dt><dd>{date(state.next_refresh_at)}</dd></div>
      <div><dt>服务器上报</dt><dd>{relative(state.status_at)}</dd></div>
    </dl>
    <section>
      <h4>测试过的节点</h4>
      <p className="muted vpngate-detail-hint">正在使用的节点排在最前。运营商和地区以实际出口 IP 为准；质量分满分 100，综合机房属性、风险、滥用记录和历史稳定性。</p>
      {!state.candidates.length ? <p className="muted">部署后出口服务器会开始测试，通常一分钟内出结果。</p> : <ul className="vpngate-candidates">{state.candidates.map(candidate => {
        const role = candidateRole(candidate, live.get(candidate.member_id))
        return <li key={candidate.member_id} className={`vpngate-candidate${candidate.selected ? " is-selected" : ""}`}>
          <div className="vpngate-candidate-main">
            <strong className="vpngate-mono">{candidate.egress_ip || candidate.ip}</strong>
            <small className="muted">{[candidate.asn, candidate.organization].filter(Boolean).join(" ") || "运营商未知"}</small>
            <small className="muted">VPNGate {candidate.ip} · {candidate.country_code} · {candidate.network.toUpperCase()}</small>
          </div>
          <dl className="vpngate-candidate-metrics">
            <div><dt>延迟</dt><dd>{candidate.rtt_ms ? `${candidate.rtt_ms} ms` : "—"}</dd></div>
            <div><dt>质量分</dt><dd>{candidate.status === "succeeded" ? candidate.quality : "—"}</dd></div>
            <div><dt>稳定性</dt><dd>{Math.round(candidate.success_rate * 100)}%</dd></div>
          </dl>
          <span className={`vpngate-state ${role.tone}`}>{role.text}</span>
        </li>
      })}</ul>}
    </section>
    <section>
      <h4>切换记录</h4>
      {!state.history.length ? <p className="muted">还没有切换过。</p> : <ol className="vpngate-history">{[...state.history].reverse().map((event, index) => <li key={index}><time className="muted">{date(event.at)}</time><span>{event.reason}</span></li>)}</ol>}
    </section>
  </div>
}

function poolCanSubmit(pool: VPNGatePool, busy: boolean) {
  return !busy && pool.name.trim() !== "" && pool.pool_size >= pool.hot_standby + 1
}

function PoolForm({ pool, busy, error, onChange, onSubmit }: { pool: VPNGatePool; busy: boolean; error: string; onChange: (pool: VPNGatePool) => void; onSubmit: () => void }) {
  const [countries, setCountries] = useState(pool.countries.join(", "))
  const [asns, setASNs] = useState(pool.operator_asns.join(", "))
  const split = (value: string) => value.split(/[\s,，、]+/).map(item => item.trim()).filter(Boolean)
  const update = (patch: Partial<VPNGatePool>) => onChange({ ...pool, ...patch })
  const sizeInvalid = pool.pool_size < pool.hot_standby + 1
  const canSubmit = poolCanSubmit(pool, busy)
  const togglePreset = (values: string[]) => {
    const current = split(asns).map(item => item.toUpperCase())
    const active = values.every(value => current.includes(value))
    const next = active ? current.filter(value => !values.includes(value)) : Array.from(new Set([...current, ...values]))
    setASNs(next.join(", ")); update({ operator_asns: next })
  }
  const selectedASNs = split(asns).map(item => item.toUpperCase())
  return <form id="vpngate-pool-form" className="probe-task-form vpngate-form" onSubmit={event => { event.preventDefault(); if (canSubmit) onSubmit() }}>
    <fieldset disabled={busy} className="return-latency-fields">
      <div className="probe-task-form-grid">
        <FormField label="名称" required full>
          <input aria-label="名称" required maxLength={64} placeholder="例如：日本家宽" value={pool.name} onChange={event => update({ name: event.target.value })} />
        </FormField>

        <h4 className="form-section-title">节点范围</h4>
        <FormField label="地区" hint="两位国家/地区代码，多个用逗号分隔。留空表示不限。" full>
          <input aria-label="地区" placeholder="例如 JP, KR" value={countries} onChange={event => { setCountries(event.target.value); update({ countries: split(event.target.value).map(item => item.toUpperCase()) }) }} />
        </FormField>
        <FormField label="运营商" hint="按实际出口 IP 的 ASN 匹配，留空表示不限。无法识别运营商的节点不会被使用。" full>
          <input aria-label="运营商 ASN" placeholder="例如 AS4713, AS2516" value={asns} onChange={event => { setASNs(event.target.value); update({ operator_asns: split(event.target.value).map(item => item.toUpperCase()) }) }} />
          <div className="vpngate-presets" role="group" aria-label="常用运营商">{operatorPresets.map(preset => {
            const active = preset.asns.every(asn => selectedASNs.includes(asn))
            return <button key={preset.label} type="button" className={`ghost${active ? " is-active" : ""}`} aria-pressed={active} onClick={() => togglePreset(preset.asns)}>{preset.label}</button>
          })}</div>
        </FormField>
        <FormField label="协议">
          <Select aria-label="协议" value={pool.network} onChange={event => update({ network: event.target.value as VPNGatePool["network"] })}>
            <option value="any">TCP 和 UDP</option><option value="tcp">仅 TCP</option><option value="udp">仅 UDP</option>
          </Select>
        </FormField>

        <h4 className="form-section-title">挑选方式</h4>
        <FormField label="优先考虑" required full>
          <Select variant="segmented" className="full-width" aria-label="优先考虑" value={pool.select_mode} onChange={event => update({ select_mode: event.target.value as VPNGatePool["select_mode"] })}>
            <option value="latency">延迟最低</option><option value="quality">IP 质量最好</option>
          </Select>
        </FormField>
        <FormField label="最高延迟（毫秒）" hint="超过的节点不使用。0 表示不限。">
          <input aria-label="最高延迟" type="number" min={0} max={5000} value={pool.max_rtt_ms} onChange={event => update({ max_rtt_ms: Number(event.target.value) || 0 })} />
        </FormField>
        <FormField label="最低质量分" hint="0–100，综合机房属性、风险评分、滥用记录和历史稳定性。0 表示不限。">
          <input aria-label="最低质量分" type="number" min={0} max={100} value={pool.min_quality_score} onChange={event => update({ min_quality_score: Number(event.target.value) || 0 })} />
        </FormField>

        <h4 className="form-section-title">切换</h4>
        <FormField label="热备节点" hint="与当前出口同时保持连接，失效时秒级接替。">
          <Select aria-label="热备节点" value={String(pool.hot_standby)} onChange={event => update({ hot_standby: Number(event.target.value) })}>
            <option value="0">不保留</option><option value="1">1 个</option><option value="2">2 个</option>
          </Select>
        </FormField>
        <FormField label="候选节点" hint="交给出口服务器按顺序使用的节点数，1–8，不少于热备数加 1。">
          <input aria-label="候选节点" aria-invalid={sizeInvalid} type="number" min={1} max={8} value={pool.pool_size} onChange={event => update({ pool_size: Number(event.target.value) || 1 })} />
        </FormField>
        <FormField label="何时更换出口">
          <Select aria-label="何时更换出口" value={pool.switch_policy} onChange={event => update({ switch_policy: event.target.value as VPNGatePool["switch_policy"] })}>
            <option value="on_failure">当前节点失效时（出口 IP 更稳定）</option><option value="improve">出现明显更好的节点时</option>
          </Select>
        </FormField>
        <FormField label="复测间隔（分钟）" hint="15 分钟到 1 天。节点异常时会提前复测。">
          <input aria-label="复测间隔" type="number" min={15} max={1440} value={pool.refresh_minutes} onChange={event => update({ refresh_minutes: Number(event.target.value) || 30 })} />
        </FormField>

        <FormField label="启用" hint="停用后，使用它的链路暂停出站，不会改走其他线路。" full>
          <Switch aria-label="启用" checked={pool.enabled} onChange={checked => update({ enabled: checked })} />
        </FormField>
      </div>
    </fieldset>
    {sizeInvalid && <p role="alert" className="danger-text">候选节点数不能少于热备数加 1。</p>}
    {error && <p role="alert" className="danger-text">{error}</p>}
  </form>
}
