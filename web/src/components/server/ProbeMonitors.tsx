import { useCallback, useEffect, useRef, useState } from "react"
import { Dialog } from "../ui/dialog"
import { Select } from "../ui/select"
import type { Server } from "../proxy-path/types"

type Client = { request: (path: string, init?: RequestInit) => Promise<any>; requestV2?: (path: string, init?: RequestInit) => Promise<any> }
type Monitor = { id: number; server_id: number; revision: number; name: string; kind: "route" | "ip_quality"; target: string; mode: string; port: number; interval_seconds: number; enabled: boolean; checked_at?: string; status?: string; error?: string; result?: any }
type Geography = { address: string; asn?: string; owner?: string; isp?: string; country?: string; province?: string; city?: string; prefix?: string; router?: Record<string, string[]> }
type Route = { id: number; checked_at: string; last_seen_at: string; asn_path: string[]; trace: { provider?: string; geography_error?: string; hops: { hop: number; addresses: string[]; probes: { address?: string; rtt_ms?: number; timeout: boolean }[]; geography?: Geography[] }[] } }
const statusText: Record<string, string> = { pending: "等待执行", running: "执行中", ok: "已获取", partial: "部分数据可用", unknown: "结果不完整", offline: "服务器离线", unsupported: "需要更新 Agent", not_configured: "未配置", failed: "执行失败", unavailable: "查询失败", quota_exhausted: "额度用完" }
const date = (value?: string) => value ? new Date(value).toLocaleString() : "—"
const flag = (value?: boolean) => value === undefined ? "未知" : value ? "是" : "否"

export function ProbeMonitors({ client, servers, kind, canManage }: { client: Client; servers: Server[]; kind: Monitor["kind"]; canManage: boolean }) {
  const [items, setItems] = useState<Monitor[]>([])
  const [providers, setProviders] = useState<Record<string, boolean>>({})
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [editing, setEditing] = useState<Monitor | null>(null)
  const [deleting, setDeleting] = useState<Monitor | null>(null)
  const [detail, setDetail] = useState<Monitor | null>(null)
  const [versions, setVersions] = useState<Route[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
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
      const input = action === "save" ? { id: task.id, server_id: task.server_id, revision: task.revision, name: task.name, kind: task.kind, target: task.target, mode: task.mode, port: task.port, interval_seconds: task.interval_seconds, enabled: task.enabled } : { id: task.id, server_id: task.server_id, revision: task.revision }
      await request("/probe-monitors", { method: "POST", body: JSON.stringify({ action, request_id: crypto.randomUUID(), input }) })
      setEditing(null); setDeleting(null); setNotice(action === "run" ? "已排入下一轮检测；IP 数据在缓存期内会复用。" : action === "delete" ? "任务已删除。" : "任务已保存。")
      await refresh()
    } catch (e: any) { setError(e.message || "操作失败，请重试") } finally { setBusy(false); busyRef.current = false }
  }
  useEffect(() => {
    setVersions([]); setMore(false)
    if (!detail || detail.kind !== "route") return
    const abort = new AbortController(); setHistoryLoading(true)
    void request(`/probe-monitors?id=${detail.id}&server_id=${detail.server_id}`, { signal: abort.signal }).then(result => { if (!abort.signal.aborted) { setVersions(result.versions || []); setMore(result.versions?.length === 100) } }).catch(e => { if (!abort.signal.aborted) setError(e.message || "路由历史加载失败") }).finally(() => { if (!abort.signal.aborted) setHistoryLoading(false) })
    return () => abort.abort()
  }, [detail, request])
  const loadMore = async () => {
    if (!detail || historyLoading) return
    setHistoryLoading(true)
    try { const result = await request(`/probe-monitors?id=${detail.id}&server_id=${detail.server_id}&before=${versions[versions.length - 1]?.id}`); setVersions(old => [...old, ...result.versions]); setMore(result.versions.length === 100) } catch (e: any) { setError(e.message || "路由历史加载失败") } finally { setHistoryLoading(false) }
  }
  const visible = items.filter(item => item.kind === kind)
  const title = kind === "route" ? "路由监控" : "IP 质量"
  return <section aria-label={title} aria-busy={loading}>
    <div className="return-latency-toolbar">
      <button type="button" className="primary" disabled={!canManage} onClick={() => { setError(""); setEditing({ id: 0, revision: 0, server_id: servers[0]?.id || 0, name: "", kind, target: "", mode: kind === "route" ? "icmp" : "", port: 0, interval_seconds: kind === "route" ? 900 : 86400, enabled: true }) }}>创建{title}任务</button>
      <button type="button" className="ghost" onClick={() => void refresh()} disabled={loading}>刷新</button>
    </div>
    {error && <p className="danger-text" role="alert">{error}</p>}
    {notice && <p role="status">{notice}</p>}
    {!loading && kind === "ip_quality" && !providers.ipinfo && <p className="muted">IPinfo 查询需要主控配置 <code>OBOARD_IPINFO_TOKEN</code>（IPinfo Lite）。</p>}
    {kind === "ip_quality" && <p className="muted">Proxycheck 可使用匿名免费额度。注册 Key 配置为 <code>OBOARD_PROXYCHECK_KEY</code>，可选举报数据源配置为 <code>OBOARD_ABUSEIPDB_KEY</code>。</p>}
    {!loading && !visible.length && <p className="muted">暂无{title}任务。</p>}
    {!!visible.length && <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label={`${title}列表`}><table className="network-probe-table">
      <thead><tr><th scope="col">任务 / 目标</th><th scope="col">服务器</th><th scope="col">周期</th><th scope="col">最近结果</th><th scope="col">检测时间</th><th scope="col">操作</th></tr></thead>
      <tbody>{visible.map(task => <tr key={task.id}><td data-label="任务 / 目标"><strong>{task.name}</strong><span className="probe-task-target">{task.target || "服务器公网 IPv4"}{task.mode === "tcp" ? `:${task.port} · TCP` : task.mode === "icmp" ? " · ICMP" : ""}</span></td><td data-label="服务器">{servers.find(server => server.id === task.server_id)?.name || `#${task.server_id}`}</td><td data-label="周期">{task.interval_seconds >= 3600 ? `${task.interval_seconds / 3600} 小时` : `${task.interval_seconds / 60} 分钟`}</td><td data-label="最近结果">{!task.enabled ? "已停用" : statusText[task.status || "pending"] || task.status}{task.error && <small className="muted">{task.error}</small>}</td><td data-label="检测时间">{date(task.checked_at)}</td><td data-label="操作"><div className="probe-task-card-actions">
        <button type="button" className="ghost" onClick={() => { setError(""); setDetail(task) }}>结果</button>
        <button type="button" className="ghost" disabled={!canManage || busy || !task.enabled} onClick={() => void mutate("run", task)}>立即检测</button>
        <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => { setError(""); setEditing({ ...task }) }}>编辑</button>
        <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => void mutate("save", { ...task, enabled: !task.enabled })}>{task.enabled ? "停用" : "启用"}</button>
        <button type="button" className="ghost danger" disabled={!canManage || busy} onClick={() => { setError(""); setDeleting(task) }}>删除</button>
      </div></td></tr>)}</tbody>
    </table></div>}
    <Dialog isOpen={Boolean(editing)} onClose={() => setEditing(null)} title={`${editing?.id ? "编辑" : "创建"}${title}任务`} busy={busy}>
      {editing && <form className="probe-monitor-form" aria-describedby={error ? "probe-monitor-error" : undefined} onSubmit={event => { event.preventDefault(); void mutate("save", editing) }}>
        <label>名称<input required maxLength={60} value={editing.name} onChange={e => setEditing({ ...editing, name: e.target.value })} /></label>
        <label>服务器<Select value={String(editing.server_id)} disabled={!!editing.id} onChange={e => setEditing({ ...editing, server_id: Number(e.target.value) })}><option value="0">请选择服务器</option>{servers.map(server => <option key={server.id} value={server.id}>{server.name}</option>)}</Select></label>
        <label>{kind === "route" ? "目标 IP / 域名" : "目标 IP（留空使用服务器公网 IPv4）"}<input required={kind === "route"} maxLength={253} disabled={!!editing.id} value={editing.target} onChange={e => setEditing({ ...editing, target: e.target.value })} /></label>
        {kind === "route" && <><label>路由协议<Select disabled={!!editing.id} value={editing.mode} onChange={e => setEditing({ ...editing, mode: e.target.value, port: e.target.value === "tcp" ? 443 : 0 })}><option value="icmp">ICMP traceroute</option><option value="tcp">TCP traceroute</option></Select></label>{editing.mode === "tcp" && <label>TCP 端口<input required type="number" min={1} max={65535} disabled={!!editing.id} value={editing.port} onChange={e => setEditing({ ...editing, port: Number(e.target.value) })} /></label>}</>}
        <label>检测间隔（分钟）<input type="number" required min={kind === "route" ? 5 : 60} max={10080} value={editing.interval_seconds / 60} onChange={e => setEditing({ ...editing, interval_seconds: Number(e.target.value) * 60 })} /></label>
        {kind === "route" && <p className="muted">使用 NextTrace 数据，首次保存基线，仅 ASN 路径变化新增记录。未回应跳或归属查询失败时不判定变化。</p>}
        {!!editing.id && <p className="muted">更换服务器、目标或协议请新建任务。</p>}
        {error && <p id="probe-monitor-error" className="danger-text" role="alert">{error}</p>}
        <div className="return-latency-toolbar"><button type="button" className="ghost" disabled={busy} onClick={() => setEditing(null)}>取消</button><button type="submit" className="primary" disabled={busy}>{busy ? "保存中…" : "保存"}</button></div>
      </form>}
    </Dialog>
    <Dialog isOpen={Boolean(deleting)} onClose={() => setDeleting(null)} title="删除监控任务" size="sm" busy={busy} footer={<><button type="button" className="ghost" disabled={busy} onClick={() => setDeleting(null)}>取消</button><button type="button" className="danger" disabled={busy} onClick={() => deleting && void mutate("delete", deleting)}>删除</button></>}>
      <p>删除「{deleting?.name}」及其全部路由历史？</p>{error && <p role="alert" className="danger-text">{error}</p>}
    </Dialog>
    <Dialog isOpen={Boolean(detail)} onClose={() => setDetail(null)} title={detail?.name || "检测结果"} size="xl">
      {error && <p role="alert" className="danger-text">{error}</p>}
      {detail?.error && <p role="status">{detail.error}</p>}
      {detail?.kind === "route" ? <div aria-busy={historyLoading}>
        {!versions.length && <p>{historyLoading ? "正在读取路由历史…" : "尚无完整 ASN 路径记录。"}</p>}
        {versions.map(version => <details key={version.id}><summary>{date(version.checked_at)} · {version.asn_path.join(" → ")}</summary><p className="muted">最近确认：{date(version.last_seen_at)}</p><RouteHops trace={version.trace} /></details>)}
        {more && <button type="button" className="ghost" disabled={historyLoading} onClick={() => void loadMore()}>加载更早记录</button>}
        {detail.result && !detail.result.asn_path && <details><summary>最近一次未完整确认的路径</summary><RouteHops trace={detail.result} /></details>}
      </div> : <div className="probe-quality-results">
        <p>{detail?.result?.ip || "尚无检测结果"}</p>
        {detail?.result?.providers?.map((provider: any) => <section key={provider.provider}><h4>{provider.provider} · {statusText[provider.status] || provider.status}</h4>{provider.error ? <p>{provider.error}</p> : <><p>{[provider.asn, provider.organization, provider.country].filter(Boolean).join(" · ")}</p>{provider.provider === "proxycheck" && <p>代理：{flag(provider.proxy)} · VPN：{flag(provider.vpn)} · Tor：{flag(provider.tor)} · 机房：{flag(provider.hosting)}<br />风险分：{provider.risk ?? "未知"} / 100 · 置信度：{provider.confidence ?? "未知"} / 100</p>}{provider.provider === "abuseipdb" && <p>滥用置信度：{provider.confidence ?? "未知"} / 100 · 最近 30 天举报：{provider.reports ?? "未知"}</p>}</>}<small className="muted">数据时间：{date(provider.checked_at)}</small></section>)}
        {detail?.result && <p className="muted">各来源独立判断；机房标记与滥用风险含义不同，未查询到的数据为未知。</p>}
      </div>}
    </Dialog>
  </section>
}
function RouteHops({ trace }: { trace: Route["trace"] }) {
  return <><p className="muted">归属来源：{trace.provider || "未知"}{trace.geography_error ? ` · ${trace.geography_error}` : ""}</p><div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label="路由跳数明细"><table className="network-probe-table"><thead><tr><th scope="col">跳数</th><th scope="col">IP</th><th scope="col">延迟</th><th scope="col">ASN / 运营商</th><th scope="col">位置 / 路由标注</th></tr></thead><tbody>{trace.hops?.map(hop => <tr key={hop.hop}><td data-label="跳数">{hop.hop}</td><td data-label="IP">{hop.addresses?.join(" / ") || "未回应"}</td><td data-label="延迟">{hop.probes?.map(probe => probe.timeout ? "超时" : `${probe.rtt_ms ?? "—"} ms`).join(" / ")}</td><td data-label="ASN / 运营商">{hop.geography?.length ? hop.geography.map(geo => <div key={geo.address}>{[geo.asn, geo.owner, geo.isp].filter(Boolean).join(" · ") || "未知"}</div>) : "未知"}</td><td data-label="位置 / 路由标注">{hop.geography?.length ? hop.geography.map(geo => <div key={geo.address}>{[geo.country, geo.province, geo.city].filter(Boolean).join(" · ") || "未知"}{Object.entries(geo.router || {}).map(([key, values]) => <small className="muted" key={key}>{key}：{values.join(" · ")}</small>)}</div>) : "未知"}</td></tr>)}</tbody></table></div></>
}
