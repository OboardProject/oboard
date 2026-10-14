import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react"
import { createPortal } from "react-dom"
import { Activity, ListTree, Loader2, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react"
import { Dialog } from "../ui/dialog"
import { FormField } from "../ui/form-field"
import { Select } from "../ui/select"
import { Switch } from "../ui/switch"
import type { Server } from "../proxy-path/types"

type Client = { request: (path: string, init?: RequestInit) => Promise<any>; requestV2?: (path: string, init?: RequestInit) => Promise<any> }
type Monitor = { id: number; server_id: number; revision: number; name: string; kind: "route" | "ip_quality"; target: string; mode: string; port: number; interval_seconds: number; enabled: boolean; checked_at?: string; status?: string; error?: string; result?: any }
type Geography = { address: string; asn?: string; owner?: string; isp?: string; country?: string; province?: string; city?: string; prefix?: string; router?: Record<string, string[]> }
type Trace = { provider?: string; geography_error?: string; hops: { hop: number; addresses: string[]; probes: { address?: string; rtt_ms?: number; timeout: boolean }[]; geography?: Geography[] }[] }
type Route = { id: number; checked_at: string; last_seen_at: string; asn_path: string[]; trace: Trace }
type Provider = { provider: string; status: string; error?: string; asn?: string; organization?: string; country?: string; proxy?: boolean; vpn?: boolean; tor?: boolean; hosting?: boolean; risk?: number; confidence?: number; reports?: number; checked_at?: string }

const statusText: Record<string, string> = { pending: "等待执行", running: "执行中", ok: "已获取", partial: "部分可用", unknown: "结果不完整", offline: "服务器离线", unsupported: "需要更新 Agent", not_configured: "未配置", failed: "执行失败", unavailable: "查询失败", quota_exhausted: "额度用完" }
const statusTone: Record<string, string> = { ok: "online", failed: "offline", offline: "offline", unavailable: "offline", quota_exhausted: "offline" }
const providerName: Record<string, string> = { ipinfo: "IPinfo", proxycheck: "Proxycheck", abuseipdb: "AbuseIPDB" }
const providerEnv: Record<string, string> = { ipinfo: "OBOARD_IPINFO_TOKEN", abuseipdb: "OBOARD_ABUSEIPDB_KEY" }
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

export function ProbeMonitors({ client, servers, kind, canManage, toolbarSlot }: { client: Client; servers: Server[]; kind: Monitor["kind"]; canManage: boolean; toolbarSlot?: HTMLElement | null }) {
  const [items, setItems] = useState<Monitor[]>([])
  const [providers, setProviders] = useState<Record<string, boolean>>({})
  const [error, setError] = useState("")
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
  const [historyError, setHistoryError] = useState("")
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
  const missingProviders = kind === "ip_quality" && !loading ? Object.keys(providerEnv).filter(name => providers[name] === false) : []
  const create = () => { setError(""); setNotice(""); setEditing({ id: 0, revision: 0, server_id: servers.length === 1 ? servers[0].id : 0, name: "", kind, target: "", mode: kind === "route" ? "icmp" : "", port: 0, interval_seconds: kind === "route" ? 900 : 86400, enabled: true }) }
  const dialogOpen = Boolean(editing || deleting || detail)
  const targetText = (task: Monitor) => `${task.target || "服务器公网 IPv4"}${task.mode === "tcp" ? `:${task.port} · TCP` : task.mode === "icmp" ? " · ICMP" : ""}`

  const toolbar = <>
    <button type="button" className="primary" disabled={!canManage} onClick={create}><Plus size={15} aria-hidden="true" />创建{title}任务</button>
    <button type="button" className="ghost" onClick={() => void refresh()} disabled={loading}><RefreshCw size={15} aria-hidden="true" />刷新</button>
  </>

  return <section aria-label={title} aria-busy={loading} className="return-latency-tasks probe-monitors">
    {toolbarSlot ? createPortal(toolbar, toolbarSlot) : <div className="return-latency-toolbar">{toolbar}</div>}
    {error && !dialogOpen && <p className="danger-text" role="alert">{error}</p>}
    {notice && <p className="muted" role="status">{notice}</p>}
    {missingProviders.length > 0 && <p className="muted probe-monitor-note">{missingProviders.map(name => providerName[name]).join("、")} 未配置，对应数据显示为未知。在主控环境变量中设置 {missingProviders.map(name => <code key={name}>{providerEnv[name]}</code>).reduce<ReactNode[]>((acc, node, index) => index ? [...acc, "、", node] : [node], [])} 后生效。</p>}
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
      {editing && <MonitorForm task={editing} servers={servers} busy={busy} error={error} onChange={setEditing} onCancel={() => setEditing(null)} onSubmit={() => void mutate("save", editing)} />}
    </Dialog>
    <Dialog isOpen={Boolean(deleting)} onClose={() => { if (!busy) setDeleting(null) }} title="删除监控任务" size="sm" busy={busy} footer={<>
      <button type="button" className="ghost" disabled={busy} onClick={() => setDeleting(null)}>取消</button>
      <button type="button" className="danger" disabled={busy} onClick={() => deleting && void mutate("delete", deleting)}>{busy ? "删除中…" : "确认删除"}</button>
    </>}>
      <p>删除「{deleting?.name}」{kind === "route" ? "及其全部路由历史" : "及其检测结果"}？</p>
      {error && <p role="alert" className="danger-text">{error}</p>}
    </Dialog>
    <Dialog isOpen={Boolean(detail)} onClose={() => setDetailID(null)} title={detail ? `${detail.name} · 检测结果` : "检测结果"} size="xl" className="probe-monitor-detail-dialog">
      {detail && <div className="probe-monitor-detail">
        <dl className="probe-monitor-meta">
          <div><dt>服务器</dt><dd>{serverName(detail.server_id)}</dd></div>
          <div><dt>目标</dt><dd>{targetText(detail)}</dd></div>
          <div><dt>最近结果</dt><dd><StatusBadge task={detail} /></dd></div>
          <div><dt>检测时间</dt><dd>{date(detail.checked_at)}</dd></div>
        </dl>
        {error && <p role="alert" className="danger-text">{error}</p>}
        {detail.error && <p className="muted" role="status">{detail.error}</p>}
        {detail.kind === "route" ? <RouteHistory detail={detail} versions={versions} loading={historyLoading} error={historyError} more={more} onMore={() => void loadMore()} /> : <QualityResult result={detail.result} />}
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

function MonitorForm({ task, servers, busy, error, onChange, onCancel, onSubmit }: { task: Monitor; servers: Server[]; busy: boolean; error: string; onChange: (task: Monitor) => void; onCancel: () => void; onSubmit: () => void }) {
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
      <p className="muted">{route ? "使用 NextTrace 归属数据。首次结果作为基线，之后仅在 ASN 路径变化时新增记录；有未回应跳或归属查询失败时不判定变化。" : "各数据源独立判断，未配置或查询失败的来源显示为未知。"}</p>
      {error && <p className="danger-text" role="alert">{error}</p>}
      <div className="return-latency-form-actions">
        <button type="button" className="ghost" onClick={onCancel}>取消</button>
        <button type="submit" className="primary" disabled={!canSubmit}>{busy && <Loader2 size={15} className="spin" aria-hidden="true" />}{busy ? "保存中…" : locked ? "保存任务" : "创建任务"}</button>
      </div>
    </fieldset>
  </form>
}

function RouteHistory({ detail, versions, loading, error, more, onMore }: { detail: Monitor; versions: Route[]; loading: boolean; error: string; more: boolean; onMore: () => void }) {
  const pending = detail.result && !detail.result.asn_path ? detail.result as Trace : null
  return <div className="probe-route-history" aria-busy={loading}>
    {error && <p role="alert" className="danger-text">{error}</p>}
    {pending && <details className="probe-route-version is-pending"><summary><span>最近一次未完整确认的路径</span><small className="muted">不参与变化判定</small></summary><RouteHops trace={pending} /></details>}
    <h4>ASN 路径记录</h4>
    {!versions.length ? <p className="muted">{loading ? "正在读取路由历史…" : "尚无完整的 ASN 路径记录。"}</p> : versions.map((version, index) => <details key={version.id} className="probe-route-version" open={index === 0}>
      <summary><span className="probe-route-path">{version.asn_path.join(" → ")}</span><small className="muted">{index === 0 ? "当前" : "历史"} · 首次 {date(version.checked_at)} · 最近确认 {date(version.last_seen_at)}</small></summary>
      <RouteHops trace={version.trace} />
    </details>)}
    {more && <button type="button" className="ghost" disabled={loading} onClick={onMore}>{loading ? "加载中…" : "加载更早记录"}</button>}
  </div>
}

function QualityResult({ result }: { result?: { ip?: string; providers?: Provider[] } }) {
  if (!result?.providers?.length) return <p className="muted">尚无检测结果。</p>
  return <div className="probe-quality-results">
    <p>检测 IP：<strong>{result.ip}</strong></p>
    <div className="probe-quality-grid">{result.providers.map(provider => <section key={provider.provider} className="probe-quality-card" aria-label={providerName[provider.provider] || provider.provider}>
      <header><h4>{providerName[provider.provider] || provider.provider}</h4><span className={`probe-task-badge ${statusTone[provider.status] || ""}`.trim()}>{statusText[provider.status] || provider.status}</span></header>
      {provider.error ? <p className="muted">{provider.error}</p> : <>
        {(provider.asn || provider.organization || provider.country) && <p>{[provider.asn, provider.organization, provider.country].filter(Boolean).join(" · ")}</p>}
        {provider.provider === "proxycheck" && <dl className="probe-quality-metrics">
          <div><dt>代理</dt><dd>{flag(provider.proxy)}</dd></div>
          <div><dt>VPN</dt><dd>{flag(provider.vpn)}</dd></div>
          <div><dt>Tor</dt><dd>{flag(provider.tor)}</dd></div>
          <div><dt>机房</dt><dd>{flag(provider.hosting)}</dd></div>
          <div><dt>风险分</dt><dd>{score(provider.risk)}</dd></div>
          <div><dt>置信度</dt><dd>{score(provider.confidence)}</dd></div>
        </dl>}
        {provider.provider === "abuseipdb" && <dl className="probe-quality-metrics">
          <div><dt>滥用置信度</dt><dd>{score(provider.confidence)}</dd></div>
          <div><dt>30 天举报</dt><dd>{provider.reports ?? "未知"}</dd></div>
        </dl>}
      </>}
      <small className="muted">数据时间：{date(provider.checked_at)}</small>
    </section>)}</div>
    <p className="muted">各来源独立判断；机房标记不等于滥用风险，未查询到的数据显示为未知。</p>
  </div>
}

function RouteHops({ trace }: { trace: Trace }) {
  return <>
    <p className="muted">归属来源：{trace.provider || "未知"}{trace.geography_error ? ` · ${trace.geography_error}` : ""}</p>
    <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label="路由跳数明细"><table className="network-probe-table probe-route-hops">
      <thead><tr><th scope="col">跳</th><th scope="col">IP</th><th scope="col">延迟</th><th scope="col">ASN / 运营商</th><th scope="col">位置 / 路由标注</th></tr></thead>
      <tbody>{trace.hops?.map(hop => <tr key={hop.hop}>
        <td data-label="跳">{hop.hop}</td>
        <td data-label="IP">{hop.addresses?.join(" / ") || <span className="muted">未回应</span>}</td>
        <td data-label="延迟">{hop.probes?.map(probe => probe.timeout ? "超时" : `${probe.rtt_ms ?? "—"} ms`).join(" / ")}</td>
        <td data-label="ASN / 运营商">{hop.geography?.length ? hop.geography.map(geo => <div key={geo.address}>{[geo.asn, geo.owner, geo.isp].filter(Boolean).join(" · ") || "未知"}</div>) : <span className="muted">未知</span>}</td>
        <td data-label="位置 / 路由标注">{hop.geography?.length ? hop.geography.map(geo => <div key={geo.address}>{[geo.country, geo.province, geo.city].filter(Boolean).join(" · ") || "未知"}{Object.entries(geo.router || {}).map(([key, values]) => <small className="muted" key={key}>{key}：{values.join(" · ")}</small>)}</div>) : <span className="muted">未知</span>}</td>
      </tr>)}</tbody>
    </table></div>
  </>
}
