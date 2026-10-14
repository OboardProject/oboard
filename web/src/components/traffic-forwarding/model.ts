import type { Server } from '../proxy-path/types'

export type ForwardProtocol = 'tcp' | 'udp' | 'tcp_udp'
export type ProbeMode = 'never' | 'apply' | 'periodic'
export type TunnelType = 'wireguard' | 'ssh'
export type ForwardingKind = 'forward' | 'tunnel'

export type TrafficForward = {
  id: number
  name: string
  source_server_id: number
  target_server_id?: number
  listen_ip: string
  listen_port: number
  target_address: string
  target_port: number
  protocol: ForwardProtocol
  backend: 'realm'
  probe_mode: ProbeMode
  probe_interval_seconds: number
  priority: number
  config_json: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export type TrafficForwardProbe = {
  id: number
  port_forward_id: number
  server_id: number
  mode: string
  available: boolean
  latency_ms: number
  sample_count: number
  error: string
  result_json: string
  created_at: string
}

export type TrafficTunnel = {
  id: number
  name: string
  source_server_id: number
  target_server_id: number
  type: TunnelType
  local_address: string
  peer_address: string
  listen_port: number
  target_endpoint: string
  target_port: number
  priority: number
  config_json: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export type TrafficForwardDraft = Omit<TrafficForward, 'id' | 'target_server_id' | 'created_at' | 'updated_at'> & { target_server_id: number }

export type TunnelDraft = Omit<TrafficTunnel, 'id' | 'created_at' | 'updated_at' | 'config_json'> & {
  private_key: string
  peer_public_key: string
  allowed_ips: string
  persistent_keepalive: number
  // Config keys the form does not edit (including redacted secrets) are sent
  // back unchanged; the Controller restores redacted values on update.
  extra_config: Record<string, unknown>
}

export type HealthTone = 'success' | 'danger' | 'neutral' | 'off'
export type ForwardingHealth = { tone: HealthTone; label: string; detail: string }

export type ForwardingItem =
  | { kind: 'forward'; key: string; item: TrafficForward; probe?: TrafficForwardProbe; health: ForwardingHealth }
  | { kind: 'tunnel'; key: string; item: TrafficTunnel; health: ForwardingHealth }

export const REDACTED = '<redacted>'

export const protocolOptions: Array<{ value: ForwardProtocol; label: string }> = [
  { value: 'tcp', label: 'TCP' },
  { value: 'udp', label: 'UDP' },
  { value: 'tcp_udp', label: 'TCP + UDP' },
]

export const probeOptions: Array<{ value: ProbeMode; label: string; description: string }> = [
  { value: 'periodic', label: '周期检查', description: '部署时检查，并按间隔持续复检。' },
  { value: 'apply', label: '部署时检查', description: '仅在配置下发后检查一次。' },
  { value: 'never', label: '不自动检查', description: '仍可在列表中手动检查。' },
]

export function protocolLabel(value: ForwardProtocol) {
  return protocolOptions.find(option => option.value === value)?.label || value
}

export function tunnelTypeLabel(value: TunnelType) {
  return value === 'wireguard' ? 'WireGuard' : 'SSH'
}

export function serverAddress(server?: Server) {
  if (!server) return ''
  return server.entry_address || server.public_ipv4 || server.public_ipv6 || server.interface_ipv6 || ''
}

export function listenLabel(value: string) {
  const trimmed = value.trim()
  return trimmed && trimmed !== '0.0.0.0' ? trimmed : '*'
}

function parseObject(raw: string): Record<string, unknown> {
  try {
    const value = JSON.parse(raw || '{}')
    return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function timestamp(value?: string) {
  const parsed = value ? new Date(value).getTime() : 0
  return Number.isFinite(parsed) ? parsed : 0
}

export function parseProbeDetails(probe?: TrafficForwardProbe) {
  if (!probe) return { successCount: 0, p95: 0, jitter: 0, forwardUpdatedAt: '' }
  const raw = parseObject(probe.result_json)
  return {
    successCount: Number(raw.success_count ?? (probe.available ? probe.sample_count : 0)),
    p95: Number(raw.p95_latency_ms || 0),
    jitter: Number(raw.jitter_ms || 0),
    forwardUpdatedAt: String(raw.forward_updated_at || ''),
  }
}

// latestProbes keeps only the newest probe that still describes the current
// forward revision, so a late result from before an edit is never shown.
export function latestProbes(probes: TrafficForwardProbe[], forwards: TrafficForward[]) {
  const result = new Map<number, TrafficForwardProbe>()
  const forwardByID = new Map(forwards.map(forward => [forward.id, forward]))
  probes.forEach(probe => {
    const forward = forwardByID.get(probe.port_forward_id)
    if (!forward) return
    const probeAt = timestamp(probe.created_at)
    const forwardUpdatedAt = timestamp(forward.updated_at)
    const reportedUpdatedAt = timestamp(parseProbeDetails(probe).forwardUpdatedAt)
    if (reportedUpdatedAt && forwardUpdatedAt && reportedUpdatedAt !== forwardUpdatedAt) return
    if (forwardUpdatedAt && (!probeAt || probeAt < forwardUpdatedAt)) return
    const current = result.get(probe.port_forward_id)
    if (!current || probeAt > timestamp(current.created_at)) result.set(probe.port_forward_id, probe)
  })
  return result
}

export function forwardHealth(forward: TrafficForward, probe?: TrafficForwardProbe): ForwardingHealth {
  if (!forward.enabled) return { tone: 'off', label: '已停用', detail: '不再监听端口' }
  if (!probe) return { tone: 'neutral', label: '等待检查', detail: forward.probe_mode === 'never' ? '可手动检查' : '同步后自动检查' }
  if (!probe.available) return { tone: 'danger', label: '不可达', detail: probe.error || '目标没有响应' }
  const details = parseProbeDetails(probe)
  return { tone: 'success', label: `${probe.latency_ms || 0} ms`, detail: details.p95 ? `P95 ${details.p95} ms · 抖动 ${details.jitter} ms` : '最近检查正常' }
}

export function tunnelHealth(tunnel: TrafficTunnel): ForwardingHealth {
  if (!tunnel.enabled) return { tone: 'off', label: '已停用', detail: '链路已断开' }
  const keepalive = Number(parseObject(tunnel.config_json).persistent_keepalive || 0)
  return { tone: 'neutral', label: '已启用', detail: keepalive ? `保活 ${keepalive} 秒` : '未开启保活' }
}

export function buildForwardingItems(forwards: TrafficForward[], probes: TrafficForwardProbe[], tunnels: TrafficTunnel[]): ForwardingItem[] {
  const probeByForward = latestProbes(probes, forwards)
  const items: ForwardingItem[] = [
    ...forwards.map(forward => {
      const probe = probeByForward.get(forward.id)
      return { kind: 'forward' as const, key: `forward-${forward.id}`, item: forward, probe, health: forwardHealth(forward, probe) }
    }),
    ...tunnels.map(tunnel => ({ kind: 'tunnel' as const, key: `tunnel-${tunnel.id}`, item: tunnel, health: tunnelHealth(tunnel) })),
  ]
  return items.sort((left, right) => (left.item.priority - right.item.priority) || left.item.name.localeCompare(right.item.name, 'zh-CN'))
}

export function itemTargetServerID(entry: ForwardingItem) {
  return Number(entry.item.target_server_id || 0)
}

export function itemSearchText(entry: ForwardingItem, servers: Server[]) {
  const source = servers.find(server => server.id === entry.item.source_server_id)
  const target = servers.find(server => server.id === itemTargetServerID(entry))
  const parts = entry.kind === 'forward'
    ? [entry.item.name, source?.name, target?.name, entry.item.target_address, entry.item.listen_port, entry.item.target_port, protocolLabel(entry.item.protocol), '端口转发']
    : [entry.item.name, source?.name, target?.name, entry.item.target_endpoint, entry.item.listen_port, entry.item.target_port, entry.item.local_address, entry.item.peer_address, tunnelTypeLabel(entry.item.type), '隧道']
  return parts.map(value => String(value ?? '')).join(' ').toLocaleLowerCase('zh-CN')
}

function validPort(value: number) {
  return Number.isInteger(value) && value >= 1 && value <= 65535
}

function validOptionalPort(value: number) {
  return value === 0 || validPort(value)
}

function nextListenPort(server: Server | undefined, forwards: TrafficForward[]) {
  if (!server) return 10000
  const used = new Set(forwards.filter(forward => forward.source_server_id === server.id).map(forward => forward.listen_port))
  const preferred = server.port_range_start > 0 ? server.port_range_start : 10000
  for (let port = Math.max(1, preferred); port <= 65535; port++) if (!used.has(port)) return port
  for (let port = 10000; port < preferred; port++) if (!used.has(port)) return port
  return 10000
}

export function emptyTrafficForwardDraft(servers: Server[], forwards: TrafficForward[], sourceServerID?: number): TrafficForwardDraft {
  const source = servers.find(server => server.id === sourceServerID) || servers[0]
  return {
    name: '',
    source_server_id: Number(source?.id || 0),
    target_server_id: 0,
    listen_ip: '',
    listen_port: nextListenPort(source, forwards),
    target_address: '',
    target_port: 443,
    protocol: 'tcp',
    backend: 'realm',
    probe_mode: 'periodic',
    probe_interval_seconds: 300,
    priority: 100,
    config_json: '{}',
    enabled: true,
  }
}

export function trafficForwardDraftFromForward(forward: TrafficForward): TrafficForwardDraft {
  const { id: _id, created_at: _createdAt, updated_at: _updatedAt, ...draft } = forward
  return { ...draft, target_server_id: Number(draft.target_server_id || 0) }
}

export function suggestedForwardName(draft: TrafficForwardDraft, servers: Server[]) {
  const source = servers.find(server => server.id === draft.source_server_id)
  const target = servers.find(server => server.id === draft.target_server_id)
  const to = target?.name || draft.target_address.trim() || '目标'
  return `${source?.name || '入口'}:${draft.listen_port || ''} → ${to}`
}

export function trafficForwardPayload(draft: TrafficForwardDraft, servers: Server[] = []): TrafficForwardDraft {
  return {
    ...draft,
    name: draft.name.trim() || suggestedForwardName(draft, servers),
    listen_ip: draft.listen_ip.trim(),
    target_address: draft.target_address.trim(),
    config_json: draft.config_json.trim() || '{}',
  }
}

export type TrafficForwardField = 'name' | 'source_server_id' | 'target_server_id' | 'listen_port' | 'target_address' | 'target_port' | 'priority' | 'probe_interval_seconds' | 'config_json'

export function trafficForwardFieldErrors(draft: TrafficForwardDraft): Partial<Record<TrafficForwardField, string>> {
  const errors: Partial<Record<TrafficForwardField, string>> = {}
  if (draft.name.trim().length > 128) errors.name = '名称不能超过 128 个字符。'
  if (!draft.source_server_id) errors.source_server_id = '请选择入口服务器。'
  if (draft.target_server_id && draft.source_server_id === draft.target_server_id) errors.target_server_id = '入口和目标不能是同一台服务器。'
  if (!validPort(draft.listen_port)) errors.listen_port = '监听端口必须是 1–65535。'
  if (!draft.target_server_id && !draft.target_address.trim()) errors.target_address = '请填写目标地址，或选择一台目标服务器。'
  else if (/\s/.test(draft.target_address.trim())) errors.target_address = '目标地址不能包含空格。'
  if (!validPort(draft.target_port)) errors.target_port = '目标端口必须是 1–65535。'
  if (!Number.isInteger(draft.priority) || draft.priority < 1) errors.priority = '优先级必须是大于 0 的整数。'
  if (draft.probe_mode === 'periodic' && (!Number.isInteger(draft.probe_interval_seconds) || draft.probe_interval_seconds < 300)) errors.probe_interval_seconds = '检查间隔不能少于 300 秒。'
  const parsed = (() => { try { return JSON.parse(draft.config_json.trim() || '{}') } catch { return undefined } })()
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') errors.config_json = '高级配置必须是 JSON 对象。'
  return errors
}

export function emptyTunnelDraft(servers: Server[], sourceServerID?: number): TunnelDraft {
  const source = servers.find(server => server.id === sourceServerID) || servers[0]
  const target = servers.find(server => server.id !== source?.id)
  return {
    name: '',
    source_server_id: Number(source?.id || 0),
    target_server_id: Number(target?.id || 0),
    type: 'wireguard',
    local_address: '',
    peer_address: '',
    listen_port: 0,
    target_endpoint: '',
    target_port: 0,
    priority: 100,
    enabled: true,
    private_key: '',
    peer_public_key: '',
    allowed_ips: '',
    persistent_keepalive: 25,
    extra_config: {},
  }
}

export function tunnelDraftFromTunnel(tunnel: TrafficTunnel): TunnelDraft {
  const { private_key, peer_public_key, allowed_ips, persistent_keepalive, ...extra } = parseObject(tunnel.config_json)
  return {
    name: tunnel.name,
    source_server_id: tunnel.source_server_id,
    target_server_id: tunnel.target_server_id,
    type: tunnel.type,
    local_address: tunnel.local_address || '',
    peer_address: tunnel.peer_address || '',
    listen_port: Number(tunnel.listen_port || 0),
    target_endpoint: tunnel.target_endpoint || '',
    target_port: Number(tunnel.target_port || 0),
    priority: Number(tunnel.priority || 100),
    enabled: tunnel.enabled,
    private_key: String(private_key ?? ''),
    peer_public_key: String(peer_public_key ?? ''),
    allowed_ips: Array.isArray(allowed_ips) ? allowed_ips.join(', ') : '',
    persistent_keepalive: Number(persistent_keepalive ?? 0),
    extra_config: extra,
  }
}

export function suggestedTunnelName(draft: TunnelDraft, servers: Server[]) {
  const source = servers.find(server => server.id === draft.source_server_id)
  const target = servers.find(server => server.id === draft.target_server_id)
  return `${source?.name || '源'} ⇄ ${target?.name || '目标'}`
}

export function tunnelPayload(draft: TunnelDraft, servers: Server[] = []) {
  const { private_key, peer_public_key, allowed_ips, persistent_keepalive, extra_config, ...rest } = draft
  const config: Record<string, unknown> = { ...extra_config }
  if (draft.type === 'wireguard') {
    config.private_key = private_key.trim()
    config.peer_public_key = peer_public_key.trim()
    config.allowed_ips = allowed_ips.split(/[\n,]/).map(value => value.trim()).filter(Boolean)
    config.persistent_keepalive = persistent_keepalive
  }
  return {
    ...rest,
    name: draft.name.trim() || suggestedTunnelName(draft, servers),
    local_address: draft.local_address.trim(),
    peer_address: draft.peer_address.trim(),
    target_endpoint: draft.target_endpoint.trim(),
    config_json: JSON.stringify(config),
  }
}

export type TunnelField = 'name' | 'source_server_id' | 'target_server_id' | 'private_key' | 'peer_public_key' | 'persistent_keepalive' | 'listen_port' | 'target_port' | 'priority'

export function tunnelFieldErrors(draft: TunnelDraft): Partial<Record<TunnelField, string>> {
  const errors: Partial<Record<TunnelField, string>> = {}
  if (draft.name.trim().length > 128) errors.name = '名称不能超过 128 个字符。'
  if (!draft.source_server_id) errors.source_server_id = '请选择源服务器。'
  if (!draft.target_server_id) errors.target_server_id = '请选择目标服务器。'
  else if (draft.source_server_id === draft.target_server_id) errors.target_server_id = '源和目标不能是同一台服务器。'
  if (draft.type === 'wireguard') {
    if (!draft.private_key.trim()) errors.private_key = '请填写本机私钥。'
    if (!draft.peer_public_key.trim()) errors.peer_public_key = '请填写对端公钥。'
    if (!Number.isInteger(draft.persistent_keepalive) || draft.persistent_keepalive < 0 || draft.persistent_keepalive > 65535) errors.persistent_keepalive = '保活间隔必须是 0–65535 秒。'
  }
  if (!validOptionalPort(draft.listen_port)) errors.listen_port = '监听端口必须是 0（自动）或 1–65535。'
  if (!validOptionalPort(draft.target_port)) errors.target_port = '目标端口必须是 0（自动）或 1–65535。'
  if (!Number.isInteger(draft.priority) || draft.priority < 1) errors.priority = '优先级必须是大于 0 的整数。'
  return errors
}

export function firstError(errors: Partial<Record<string, string>>) {
  return Object.values(errors).find(Boolean) || ''
}
