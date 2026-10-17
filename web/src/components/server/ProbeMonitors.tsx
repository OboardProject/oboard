import { useErrorToast, type ToastNotify } from "../ui/use-error-toast"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { createPortal } from "react-dom"
import { Activity, ListTree, Loader2, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react"
import { Dialog } from "../ui/dialog"
import { FormField } from "../ui/form-field"
import { Select } from "../ui/select"
import { Switch } from "../ui/switch"
import type { Server } from "../proxy-path/types"
import { ipDataProviderName, ipDataStatusText, ipDataStatusTone } from "../ip-data/labels"

type Client = { request: (path: string, init?: RequestInit) => Promise<any>; requestV2?: (path: string, init?: RequestInit) => Promise<any> }
type Monitor = { id: number; server_id: number; revision: number; name: string; kind: "route" | "ip_quality"; target: string; mode: string; port: number; interval_seconds: number; enabled: boolean; checked_at?: string; status?: string; error?: string; result?: any }
type Geography = { address: string; source?: string; asn?: string; owner?: string; isp?: string; country?: string; province?: string; city?: string; prefix?: string; router?: Record<string, string[]> }
type Trace = { provider?: string; geography_error?: string; hops: { hop: number; addresses: string[]; probes: { address?: string; rtt_ms?: number; timeout: boolean }[]; geography?: Geography[] }[] }
type Route = { id: number; checked_at: string; last_seen_at: string; asn_path: string[]; trace: Trace }
type Source = { provider: string; status: string; error?: string; keyed?: boolean; asn?: string; as_name?: string; organization?: string; country_code?: string; country?: string; region?: string; city?: string; proxy?: boolean; vpn?: boolean; tor?: boolean; hosting?: boolean; abuser?: boolean; risk?: number; confidence?: number; abuse_score?: number; reports?: number; checked_at?: string }
type Agreement = { agree: number; total: number }
type Attribution = { status: string; primary?: string; asn?: string; as_name?: string; country_code?: string; country?: string; region?: string; city?: string; field_sources?: Record<string, string>; asn_agreement?: Agreement; country_agreement?: Agreement; conflicts?: { provider: string; field: string; value: string }[]; sources?: Source[] }
type Verdict = { state: string; positive: number; negative: number; flagged_by?: string[] }
type Quality = { status: string; proxy?: Verdict; vpn?: Verdict; tor?: Verdict; hosting?: Verdict; abuser?: Verdict; risk?: number; risk_source?: string; abuse_score?: number; abuse_source?: string; reports?: number; sources?: Source[] }

const statusText: Record<string, string> = { pending: "等待执行", running: "执行中", ok: "已获取", partial: "部分可用", unknown: "结果不完整", offline: "服务器离线", unsupported: "需要更新 Agent", not_configured: "未配置", failed: "执行失败", unavailable: "查询失败", quota_exhausted: "额度用完" }
const statusTone: Record<string, string> = { ok: "online", failed: "offline", offline: "offline", unavailable: "offline", quota_exhausted: "offline" }
const providerName = ipDataProviderName
const date = (value?: string) => value ? new Date(value).toLocaleString() : "—"
const flag = (value?: boolean) => value === undefined || value === null ? "未知" : value ? "是" : "否"
const score = (value?: number) => value === undefined || value === null ? "未知" : `${value} / 100`
const formatInterval = (seconds: number) => seconds % 86400 === 0 ? `${seconds / 86400} 天` : seconds % 3600 === 0 ? `${seconds / 3600} 小时` : `${Math.round(seconds / 60)} 分钟`
const presets = { route: [300, 900, 1800, 3600, 21600], ip_quality: [3600, 21600, 86400, 604800] }
const minimumMinutes = { route: 5, ip_quality: 60 }
const MAX_MINUTES = 10080

function StatusBadge({ task }: { task: Monitor }) {
  if (!task.enabled) return <span className="probe-task-badge">已停用</span>
  const status = task.status || "pending"
  return <span className={`probe-task-badge ${statusTone[status] || ""}`.trim()}>{statusText[status] || status}</span>
}

export function ProbeMonitors({ client, servers, kind, canManage, toolbarSlot, notify }: { client: Client; servers: Server[]; kind: Monitor["kind"]; canManage: boolean; toolbarSlot?: HTMLElement | null; notify?: ToastNotify }) {
  const [items, setItems] = useState<Monitor[]>([])
  const [providers, setProviders] = useState<Record<string, boolean>>({})
  const [, setError] = useErrorToast(notify)
  const [notice, setNotice] = useState("")
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [query, setQuery] = useState("")
  const [editing, setEditing] = useState<Monitor | null>(null)
  const [deleting, setDeleting] = useState<Monitor | null>(null)
  const [detailID, setDetailID] = useState<number | null>(null)
  const [versions, setVersions] = useState<Route[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const [, setHistoryError] = useErrorToast(notify)
  const [more, setMore] = useState(false)
  const request = useCallback(async (path: string, init?: RequestInit) => {
    if (client.requestV2) return client.requestV2(path, init)
    const result = await client.request(path, init); return result.data ?? result
  }, [client])
  const refresh = useCallback(async (signal?: AbortSignal) => {
    try { const result = await request("/probe-monitors", { signal }); if (!signal?.aborted) { setItems(result.monitors || []); setProviders(result.providers || {}); setError("") } }
    catch (e: any) { if (!signal?.aborted) setError(e.message || "监控任务加载失败") }
    finally { if (!signal?.aborted) setLoading(false) }
  }, [request])
  useEffect(() => { const abort = new AbortController(); void refresh(abort.signal); const timer = window.setInterval(() => { if (!document.hidden) void refresh(abort.signal) }, 15000); return () => { abort.abort(); window.clearInterval(timer) } }, [refresh])
  const mutate = async (action: string, task: Monitor) => {
    if (busyRef.current) return
    busyRef.current = true; setBusy(true); setError(""); setNotice("")
    try {
      const input = action === "save" ? { id: task.id, server_id: task.server_id, revision: task.revision, name: task.name.trim(), kind: task.kind, target: task.target.trim(), mode: task.mode, port: task.port, interval_seconds: task.interval_seconds, enabled: task.enabled } : { id: task.id, server_id: task.server_id, revision: task.revision }
      await request("/probe-monitors", { method: "POST", body: JSON.stringify({ action, request_id: crypto.randomUUID(), input }) })
      setEditing(null); setDeleting(null)
      setNotice(action === "run" ? `已安排「${task.name}」在下一轮检测${kind === "ip_quality" ? "，缓存期内的 IP 数据会复用" : ""}。` : action === "delete" ? "任务已删除。" : "任务已保存。")
      await refresh()
    } catch (e: any) { setError(e.message || "操作失败，请重试") } finally { setBusy(false); busyRef.current = false }
  }
  const detail = detailID === null ? null : items.find(item => item.id === detailID) || null
  const historyID = detail?.kind === "route" ? detail.id : 0
  const historyServer = detail?.server_id || 0
  useEffect(() => {
    setVersions([]); setMore(false); setHistoryError("")
    if (!historyID) return
    const abort = new AbortController(); setHistoryLoading(true)
    void request(`/probe-monitors?id=${historyID}&server_id=${historyServer}`, { signal: abort.signal }).then(result => { if (!abort.signal.aborted) { setVersions(result.versions || []); setMore(result.versions?.length === 100) } }).catch(e => { if (!abort.signal.aborted) setHistoryError(e.message || "路由历史加载失败") }).finally(() => { if (!abort.signal.aborted) setHistoryLoading(false) })
    return () => abort.abort()
  }, [historyID, historyServer, request])
  const loadMore = async () => {
    if (!detail || historyLoading) return
    setHistoryLoading(true); setHistoryError("")
    try { const result = await request(`/probe-monitors?id=${detail.id}&server_id=${detail.server_id}&before=${versions[versions.length - 1]?.id}`); setVersions(old => [...old, ...(result.versions || [])]); setMore(result.versions?.length === 100) } catch (e: any) { setHistoryError(e.message || "路由历史加载失败") } finally { setHistoryLoading(false) }
  }

  const serverName = (id: number) => servers.find(server => server.id === id)?.name || `#${id}`
  const title = kind === "route" ? "路由监控" : "IP 质量"
  const all = useMemo(() => items.filter(item => item.kind === kind), [items, kind])
  const needle = query.trim().toLowerCase()
  const visible = needle ? all.filter(item => `${item.name} ${item.target} ${serverName(item.server_id)}`.toLowerCase().includes(needle)) : all
  const missingProviders = kind === "ip_quality" && !loading ? ["ipinfo", "abuseipdb"].filter(name => providers[name] === false) : []
  const create = () => { setError(""); setNotice(""); setEditing({ id: 0, revision: 0, server_id: servers.length === 1 ? servers[0].id : 0, name: "", kind, target: "", mode: kind === "route" ? "icmp" : "", port: 0, interval_seconds: kind === "route" ? 900 : 86400, enabled: true }) }
  const targetText = (task: Monitor) => `${task.target || "服务器公网 IPv4"}${task.mode === "tcp" ? `:${task.port} · TCP` : task.mode === "icmp" ? " · ICMP" : ""}`

  const toolbar = <>
    <button type="button" className="primary" disabled={!canManage} onClick={create}><Plus size={15} aria-hidden="true" />创建{title}任务</button>
    <button type="button" className="ghost" onClick={() => { setError(""); void refresh() }} disabled={loading}><RefreshCw size={15} aria-hidden="true" />刷新</button>
  </>

  return <section aria-label={title} aria-busy={loading} className="return-latency-tasks probe-monitors">
    {toolbarSlot ? createPortal(toolbar, toolbarSlot) : <div className="return-latency-toolbar">{toolbar}</div>}
    {notice && <p className="muted" role="status">{notice}</p>}
    {missingProviders.length > 0 && <p className="muted probe-monitor-note">{missingProviders.map(name => providerName[name]).join("、")} 未填写 Key，已自动使用备用来源。管理员可在“设置 › 节点 › IP 与路由数据”中填写。</p>}
    {all.length > 0 && <div className="return-latency-filters">
      <div className="return-latency-search"><Search size={15} aria-hidden="true" /><input type="search" aria-label={`搜索${title}任务`} placeholder="搜索名称、目标或服务器" value={query} onChange={event => setQuery(event.target.value)} /></div>
    </div>}
    {loading && !items.length ? <p className="muted" role="status">正在加载{title}任务…</p> : !all.length ? <div className="latency-empty-state">
      <p>还没有{title}任务</p>
      <p className="muted">{kind === "route" ? "由所选服务器定期跟踪到目标的路由，ASN 路径变化时保留记录。" : "定期汇总服务器或指定公网 IP 的代理、机房与滥用风险标记。"}</p>
      <button type="button" className="primary" disabled={!canManage} onClick={create}><Plus size={15} aria-hidden="true" />创建{title}任务</button>
    </div> : !visible.length ? <div className="latency-empty-state"><p>没有匹配的任务</p><button type="button" className="ghost" onClick={() => setQuery("")}>清除筛选</button></div> : <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label={`${title}列表`}><table className="network-probe-table">
      <thead><tr><th scope="col">名称 / 目标</th><th scope="col">服务器</th><th scope="col">间隔</th><th scope="col">最近结果</th><th scope="col">检测时间</th><th scope="col" className="network-probe-actions-heading">操作</th></tr></thead>
      <tbody>{visible.map(task => <tr key={task.id} className="probe-task-card">
        <td><strong>{task.name}</strong><span className="probe-task-target">{targetText(task)}</span></td>
        <td data-label="服务器">{serverName(task.server_id)}</td>
        <td data-label="间隔">{formatInterval(task.interval_seconds)}</td>
        <td data-label="最近结果"><StatusBadge task={task} />{task.enabled && <ResultSummary task={task} />}</td>
        <td data-label="检测时间">{date(task.checked_at)}</td>
        <td><div className="probe-task-card-actions">
          <button type="button" className="ghost" aria-label={`查看 ${task.name} 的结果`} onClick={() => { setError(""); setDetailID(task.id) }}><ListTree size={14} aria-hidden="true" />结果</button>
          <button type="button" className="ghost" aria-label={`立即检测 ${task.name}`} disabled={!canManage || busy || !task.enabled} onClick={() => void mutate("run", task)}><Activity size={14} aria-hidden="true" />立即检测</button>
          <button type="button" className="ghost" aria-label={`编辑 ${task.name}`} disabled={!canManage || busy} onClick={() => { setError(""); setEditing({ ...task }) }}><Pencil size={14} aria-hidden="true" />编辑</button>
          <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => void mutate("save", { ...task, enabled: !task.enabled })}>{task.enabled ? "停用" : "启用"}</button>
          <button type="button" className="ghost danger" aria-label={`删除 ${task.name}`} disabled={!canManage || busy} onClick={() => { setError(""); setDeleting(task) }}><Trash2 size={14} aria-hidden="true" />删除</button>
        </div></td>
      </tr>)}</tbody>
    </table></div>}

    <Dialog isOpen={Boolean(editing)} onClose={() => { if (!busy) setEditing(null) }} title={`${editing?.id ? "编辑" : "创建"}${title}任务`} size="lg" className="probe-task-dialog" busy={busy}>
      {editing && <MonitorForm task={editing} servers={servers} busy={busy} onChange={setEditing} onCancel={() => setEditing(null)} onSubmit={() => void mutate("save", editing)} />}
    </Dialog>
    <Dialog isOpen={Boolean(deleting)} onClose={() => { if (!busy) setDeleting(null) }} title="删除监控任务" size="sm" busy={busy} footer={<>
      <button type="button" className="ghost" disabled={busy} onClick={() => setDeleting(null)}>取消</button>
      <button type="button" className="danger" disabled={busy} onClick={() => deleting && void mutate("delete", deleting)}>{busy ? "删除中…" : "确认删除"}</button>
    </>}>
      <p>删除「{deleting?.name}」{kind === "route" ? "及其全部路由历史" : "及其检测结果"}？</p>
    </Dialog>
    <Dialog isOpen={Boolean(detail)} onClose={() => setDetailID(null)} title={detail ? `${detail.name} · 检测结果` : "检测结果"} size="xl" className="probe-monitor-detail-dialog">
      {detail && <div className="probe-monitor-detail">
        <dl className="probe-monitor-meta">
          <div><dt>服务器</dt><dd>{serverName(detail.server_id)}</dd></div>
          <div><dt>目标</dt><dd>{targetText(detail)}</dd></div>
          <div><dt>最近结果</dt><dd><StatusBadge task={detail} /></dd></div>
          <div><dt>检测时间</dt><dd>{date(detail.checked_at)}</dd></div>
        </dl>
        {detail.error && <p className="muted" role="status">{detail.error}</p>}
        {detail.kind === "route" ? <RouteHistory detail={detail} versions={versions} loading={historyLoading} more={more} onMore={() => void loadMore()} /> : <QualityResult result={detail.result} />}
      </div>}
    </Dialog>
  </section>
}

function ResultSummary({ task }: { task: Monitor }) {
  if (task.error) return <small className="muted">{task.error}</small>
  if (task.kind === "route" && Array.isArray(task.result?.asn_path)) return <small className="muted">{task.result.asn_path.join(" → ")}</small>
  if (task.kind === "ip_quality" && task.result?.ip) return <small className="muted">{task.result.ip}</small>
  return null
}

function MonitorForm({ task, servers, busy, onChange, onCancel, onSubmit }: { task: Monitor; servers: Server[]; busy: boolean; onChange: (task: Monitor) => void; onCancel: () => void; onSubmit: () => void }) {
  const route = task.kind === "route"
  const locked = task.id > 0
  const [minutes, setMinutes] = useState<number | "">(Math.round(task.interval_seconds / 60))
  const minMinutes = minimumMinutes[task.kind]
  const intervalInvalid = minutes === "" || !Number.isInteger(minutes) || minutes < minMinutes || minutes > MAX_MINUTES
  const portInvalid = route && task.mode === "tcp" && (!Number.isInteger(task.port) || task.port < 1 || task.port > 65535)
  const canSubmit = !busy && task.server_id > 0 && task.name.trim() !== "" && (!route || task.target.trim() !== "") && !intervalInvalid && !portInvalid
  const setInterval = (value: number | "") => { setMinutes(value); if (value !== "") onChange({ ...task, interval_seconds: value * 60 }) }
  return <form className="probe-task-form probe-monitor-form" onSubmit={event => { event.preventDefault(); if (canSubmit) onSubmit() }}>
    <fieldset disabled={busy} className="return-latency-fields">
      <div className="probe-task-form-grid">
        <FormField label="任务名称" required>
          <input aria-label="任务名称" required maxLength={60} placeholder={route ? "例如：到上海电信的回程" : "例如：入口 IP 质量"} value={task.name} onChange={event => onChange({ ...task, name: event.target.value })} />
        </FormField>
        <FormField label="执行服务器" required hint={locked ? "创建后不可更换，请新建任务。" : route ? "路由从该服务器向目标发起探测，服务器需要在线。" : "未填写目标 IP 时检测该服务器的公网 IPv4。"}>
          <Select aria-label="执行服务器" value={String(task.server_id)} disabled={locked} onChange={event => onChange({ ...task, server_id: Number(event.target.value) })}>
            <option value="0">请选择服务器</option>
            {servers.map(server => <option key={server.id} value={server.id}>{server.name}</option>)}
          </Select>
        </FormField>
        {route && <FormField label="路由协议" required full>
          <Select variant="segmented" className="full-width" aria-label="路由协议" disabled={locked} value={task.mode} onChange={event => onChange({ ...task, mode: event.target.value, port: event.target.value === "tcp" ? (task.port || 443) : 0 })}>
            <option value="icmp">ICMP</option><option value="tcp">TCP</option>
          </Select>
        </FormField>}
        <FormField label={route ? "目标 IP / 域名" : "目标 IP"} required={route} full={!route || task.mode !== "tcp"}>
          <input aria-label={route ? "目标 IP / 域名" : "目标 IP"} aria-describedby="probe-monitor-target-hint" required={route} maxLength={253} disabled={locked} placeholder={route ? "例如 example.com 或 1.1.1.1" : "留空使用服务器公网 IPv4"} value={task.target} onChange={event => onChange({ ...task, target: event.target.value })} />
          <small id="probe-monitor-target-hint" className="muted">{locked ? "更换目标或协议请新建任务，以保留原有记录。" : route ? "只接受公网地址。" : "只接受公网 IP。"}</small>
        </FormField>
        {route && task.mode === "tcp" && <FormField label="TCP 端口" required>
          <input aria-label="TCP 端口" aria-invalid={portInvalid} required type="number" min={1} max={65535} disabled={locked} value={task.port || ""} onChange={event => onChange({ ...task, port: event.target.value === "" ? 0 : Number(event.target.value) })} />
        </FormField>}
        <FormField label="检测间隔（分钟）" required full hint={`${minMinutes} 分钟至 7 天。`}>
          <div className="probe-task-interval">
            <input aria-label="检测间隔（分钟）" aria-invalid={intervalInvalid} required type="number" min={minMinutes} max={MAX_MINUTES} step={1} value={minutes} onChange={event => setInterval(event.target.value === "" ? "" : Number(event.target.value))} />
            <div className="probe-task-interval-presets">
              {presets[task.kind].map(preset => <button key={preset} type="button" className={`latency-pill-btn${task.interval_seconds === preset && minutes !== "" ? " active" : ""}`} onClick={() => setInterval(preset / 60)}>{formatInterval(preset)}</button>)}
            </div>
          </div>
        </FormField>
      </div>
      {!locked && <label className="return-latency-switch-row">
        <span><strong>启用该任务</strong><small className="muted">停用后保留配置与历史，但不再执行检测。</small></span>
        <Switch checked={task.enabled} ariaLabel="启用该任务" onChange={enabled => onChange({ ...task, enabled })} />
      </label>}
      <p className="muted">{route ? "以 NextTrace 归属数据为主，缺失的跳由主控 NextTrace 与 IP 归属数据源补全。首次结果作为基线，之后仅在 ASN 路径变化时新增记录；有未回应跳或归属仍无法补全时不判定变化。" : "归属信息以 IPinfo 为准并与其他来源交叉校验；质量信息由多个来源投票。任一来源不可用时自动降级。"}</p>
      <div className="return-latency-form-actions">
        <button type="button" className="ghost" onClick={onCancel}>取消</button>
        <button type="submit" className="primary" disabled={!canSubmit}>{busy && <Loader2 size={15} className="spin" aria-hidden="true" />}{busy ? "保存中…" : locked ? "保存任务" : "创建任务"}</button>
      </div>
    </fieldset>
  </form>
}

function RouteHistory({ detail, versions, loading, more, onMore }: { detail: Monitor; versions: Route[]; loading: boolean; more: boolean; onMore: () => void }) {
  const pending = detail.result && !detail.result.asn_path ? detail.result as Trace : null
  return <div className="probe-route-history" aria-busy={loading}>
    {pending && <details className="probe-route-version is-pending"><summary><span>最近一次未完整确认的路径</span><small className="muted">不参与变化判定</small></summary><RouteHops trace={pending} /></details>}
    <h4>ASN 路径记录</h4>
    {!versions.length ? <p className="muted">{loading ? "正在读取路由历史…" : "尚无完整的 ASN 路径记录。"}</p> : versions.map((version, index) => <details key={version.id} className="probe-route-version" open={index === 0}>
      <summary><span className="probe-route-path">{version.asn_path.join(" → ")}</span><small className="muted">{index === 0 ? "当前" : "历史"} · 首次 {date(version.checked_at)} · 最近确认 {date(version.last_seen_at)}</small></summary>
      <RouteHops trace={version.trace} />
    </details>)}
    {more && <button type="button" className="ghost" disabled={loading} onClick={onMore}>{loading ? "加载中…" : "加载更早记录"}</button>}
  </div>
}

const verdictText: Record<string, string> = { yes: "是", no: "否", conflict: "来源不一致", unknown: "未知" }
const verdictTone: Record<string, string> = { yes: "offline", no: "online", conflict: "warning" }
const agreementText = (value?: Agreement) => value && value.total > 0 ? `${value.agree}/${value.total} 来源一致` : "无可比来源"

function SourceBadge({ status }: { status: string }) {
  return <span className={`probe-task-badge ${ipDataStatusTone[status] || ""}`.trim()}>{ipDataStatusText[status] || statusText[status] || status}</span>
}

function SourceList({ sources, quality }: { sources?: Source[]; quality?: boolean }) {
  if (!sources?.length) return null
  return <details className="probe-quality-sources">
    <summary>各来源原始结果</summary>
    <ul>{sources.map(source => <li key={source.provider}>
      <strong>{providerName[source.provider] || source.provider}</strong>
      <SourceBadge status={source.status} />
      <span className="muted">{source.status === "ok"
        ? quality
          ? [source.proxy !== undefined && `代理 ${flag(source.proxy)}`, source.vpn !== undefined && `VPN ${flag(source.vpn)}`, source.tor !== undefined && `Tor ${flag(source.tor)}`, source.hosting !== undefined && `机房 ${flag(source.hosting)}`, source.abuser !== undefined && `滥用 ${flag(source.abuser)}`, source.risk !== undefined && `风险 ${source.risk}`, source.abuse_score !== undefined && `滥用置信度 ${source.abuse_score}`].filter(Boolean).join(" · ") || "无质量字段"
          : [source.asn, source.as_name || source.organization, source.country_code || source.country, source.region, source.city].filter(Boolean).join(" · ")
        : source.error || ""}</span>
    </li>)}</ul>
  </details>
}

function QualityResult({ result }: { result?: { ip?: string; attribution?: Attribution; quality?: Quality } }) {
  const a = result?.attribution, q = result?.quality
  if (!a && !q) return <p className="muted">尚无检测结果。</p>
  const sourceOf = (field: string) => a?.field_sources?.[field] ? `（${providerName[a.field_sources[field]] || a.field_sources[field]}）` : ""
  return <div className="probe-quality-results">
    <p>检测 IP：<strong>{result?.ip}</strong></p>
    <div className="probe-quality-grid">
      {a && <section className="probe-quality-card" aria-label="IP 归属信息">
        <header><h4>归属信息</h4><SourceBadge status={a.status} /></header>
        <dl className="probe-quality-metrics">
          <div><dt>ASN</dt><dd>{a.asn ? `${a.asn}${sourceOf("asn")}` : "未知"}</dd></div>
          <div><dt>组织</dt><dd>{a.as_name || "未知"}</dd></div>
          <div><dt>国家 / 地区</dt><dd>{[a.country_code, a.country].filter(Boolean).join(" ") || "未知"}{a.country_code ? sourceOf("country") : ""}</dd></div>
          <div><dt>位置</dt><dd>{[a.region, a.city].filter(Boolean).join(" · ") || "未知"}{a.region || a.city ? sourceOf("region") : ""}</dd></div>
          <div><dt>ASN 校验</dt><dd>{agreementText(a.asn_agreement)}</dd></div>
          <div><dt>国家校验</dt><dd>{agreementText(a.country_agreement)}</dd></div>
        </dl>
        {a.status === "degraded" && <p className="muted">IPinfo 暂不可用，结果由其余来源投票得出。</p>}
        {a.status === "offline" && <p className="muted">在线来源均不可用，仅有离线库的国家与地区，无 ASN。</p>}
        {!!a.conflicts?.length && <p className="muted">不一致：{a.conflicts.map(c => `${providerName[c.provider] || c.provider} ${c.field === "asn" ? "ASN" : "国家"} ${c.value}`).join("；")}</p>}
        <SourceList sources={a.sources} />
      </section>}
      {q && <section className="probe-quality-card" aria-label="IP 质量信息">
        <header><h4>质量信息</h4><SourceBadge status={q.status} /></header>
        <dl className="probe-quality-metrics">
          {([["proxy", "代理"], ["vpn", "VPN"], ["tor", "Tor"], ["hosting", "机房"], ["abuser", "滥用"]] as const).map(([key, label]) => {
            const v = q[key]
            return <div key={key}><dt>{label}</dt><dd><span className={`probe-task-badge ${verdictTone[v?.state || ""] || ""}`.trim()} title={v && v.positive + v.negative > 0 ? `${v.positive} 个来源判定是，${v.negative} 个判定否` : undefined}>{verdictText[v?.state || "unknown"]}</span>{v && v.positive + v.negative > 1 ? <small className="muted"> {v.positive}/{v.positive + v.negative}</small> : null}</dd></div>
          })}
          <div><dt>风险分</dt><dd>{score(q.risk)}{q.risk_source ? <small className="muted">（{providerName[q.risk_source] || q.risk_source}）</small> : null}</dd></div>
          <div><dt>滥用置信度</dt><dd>{score(q.abuse_score)}{q.reports !== undefined ? <small className="muted">（30 天 {q.reports} 次举报）</small> : null}</dd></div>
        </dl>
        <SourceList sources={q.sources} quality />
      </section>}
    </div>
    <p className="muted">每项按各来源投票；机房不等于滥用，未查询到的数据显示为未知，不合成纯净度总分。</p>
  </div>
}

function RouteHops({ trace }: { trace: Trace }) {
  return <>
    <p className="muted">归属来源：NextTrace 为主{trace.hops?.some(hop => hop.geography?.some(geo => geo.source)) ? "，部分跳由备用来源补全" : ""}{trace.geography_error ? ` · ${trace.geography_error}` : ""}</p>
    <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label="路由跳数明细"><table className="network-probe-table probe-route-hops">
      <thead><tr><th scope="col">跳</th><th scope="col">IP</th><th scope="col">延迟</th><th scope="col">ASN / 运营商</th><th scope="col">位置 / 路由标注</th></tr></thead>
      <tbody>{trace.hops?.map(hop => <tr key={hop.hop}>
        <td data-label="跳">{hop.hop}</td>
        <td data-label="IP">{hop.addresses?.join(" / ") || <span className="muted">未回应</span>}</td>
        <td data-label="延迟">{hop.probes?.map(probe => probe.timeout ? "超时" : `${probe.rtt_ms ?? "—"} ms`).join(" / ")}</td>
        <td data-label="ASN / 运营商">{hop.geography?.length ? hop.geography.map(geo => <div key={geo.address}>{[geo.asn, geo.owner, geo.isp].filter(Boolean).join(" · ") || "未知"}{geo.source ? <small className="muted"> · {providerName[geo.source] || geo.source}</small> : null}</div>) : <span className="muted">未知</span>}</td>
        <td data-label="位置 / 路由标注">{hop.geography?.length ? hop.geography.map(geo => <div key={geo.address}>{[geo.country, geo.province, geo.city].filter(Boolean).join(" · ") || "未知"}{Object.entries(geo.router || {}).map(([key, values]) => <small className="muted" key={key}>{key}：{values.join(" · ")}</small>)}</div>) : <span className="muted">未知</span>}</td>
      </tr>)}</tbody>
    </table></div>
  </>
}
