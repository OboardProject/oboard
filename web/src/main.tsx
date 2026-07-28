import React, { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { createRoot } from 'react-dom/client'
import {
  type ThemeName,
  applyThemeToDocument,
  normalizeTheme,
  toggleThemeWithTransition,
} from './theme'
import ReactFlow, { Background, BackgroundVariant, BaseEdge, Connection, Controls, Edge, EdgeChange, EdgeLabelRenderer, Handle, MarkerType, Node, NodeChange, PanOnScrollMode, Position, applyEdgeChanges, applyNodeChanges, getNodesBounds, getStraightPath, getViewportForBounds } from 'reactflow'
import type { EdgeProps, ReactFlowInstance } from 'reactflow'
import 'reactflow/dist/style.css'
import type {
  CertificateMode,
  DNSRecordTypes,
  EntryIPMode,
  ExternalOutbound,
  ExternalProtocol,
  Inbound,
  Protocol,
  ProxyPath,
  ProxyPathNamePart,
  ProxyPathStep,
  ProxyPathTransportMode,
  RegionMode,
  Server,
} from './components/proxy-path/types'
import { TransportDialog } from './components/proxy-path/TransportDialog'
import {
  GRAPH_ENTRY_NODE_WIDTH,
  defaultEntryGraphPosition,
  defaultImportedGraphPosition,
  defaultServerGraphPosition,
  graphEntryHandleLeft,
  graphEntryHandleRatio,
  graphPathHandleLeft,
  graphServerNodeWidth,
  loadGraphPositions,
  loadGraphToolboxPosition,
  saveGraphPositions,
  saveGraphToolboxPosition,
  snapGraphPosition,
} from './components/proxy-path/layout'
import type { GraphPosition } from './components/proxy-path/layout'
import type { TransportDialogTarget, TransportSelection } from './components/proxy-path/TransportDialog'
import './style.css'
import logo from './assets/logo.svg'
import { 
  LayoutDashboard, Server as ServerIcon, Workflow, Users as UsersIcon, Link as LinkIcon, 
  Bell, CheckSquare, ClipboardList, Settings as SettingsIcon, LogOut, Shield,
  Settings2, Activity, ArrowLeftRight, HelpCircle, HardDrive, 
  Zap, Sliders, Menu, X, Sun, Moon, RefreshCw, ChevronDown, ChevronRight, Check, Info,
  User, Lock, Globe, Copy, Edit3, Trash2, Plus, UserPlus, Gauge, Database, CalendarDays,
  Eye, EyeOff, FileText, Download, Search, Eraser, ArrowDown, ArrowUp, MoreHorizontal,
  KeyRound, ExternalLink, CalendarSync, BadgeCheck, Fingerprint, Smartphone, ShieldCheck
} from 'lucide-react'

// Import shadcn/ui style components
import { Input } from './components/ui/input'
import { Select } from './components/ui/select'
import { Toast } from './components/ui/toast'
import { Dialog } from './components/ui/dialog'
import { TableSkeleton, CardSkeleton, DashboardSkeleton } from './components/ui/skeleton'
import { AnimatePresence, LazyMotion, domAnimation, m, motion } from 'motion/react'
import { MotionPage, MotionDialogPanel, MotionList, MotionCard } from './components/ui/motion'
import { CustomSelect } from './components/ui/CustomSelect'
import singBoxClientIcon from './assets/subscription-clients/sing-box.svg'
import clashMetaClientIcon from './assets/subscription-clients/clash-meta.png'
import stashClientIcon from './assets/subscription-clients/stash.jpg'
import surgeClientIcon from './assets/subscription-clients/surge.jpg'
import shadowrocketClientIcon from './assets/subscription-clients/shadowrocket.jpg'
import quantumultXClientIcon from './assets/subscription-clients/quantumult-x.jpg'
import loonClientIcon from './assets/subscription-clients/loon.jpg'
import surfboardClientIcon from './assets/subscription-clients/surfboard.png'
import egernClientIcon from './assets/subscription-clients/egern.jpg'
import v2rayNClientIcon from './assets/subscription-clients/v2rayn.png'
import clashClassicClientIcon from './assets/subscription-clients/clash-classic.png'

const appBasePath = (() => {
  const href = document.querySelector('base')?.getAttribute('href') || '/'
  const pathname = new URL(href, window.location.origin).pathname.replace(/\/+$/, '')
  return pathname === '/' ? '' : pathname
})()

function appPath(path: string) {
  const suffix = path.startsWith('/') ? path : `/${path}`
  return `${appBasePath}${suffix}` || '/'
}

function appControllerURL() {
  return `${window.location.origin}${appBasePath}`
}

function stripAppBasePath(pathname: string) {
  if (!appBasePath) return pathname
  if (pathname === appBasePath) return '/'
  return pathname.startsWith(`${appBasePath}/`) ? pathname.slice(appBasePath.length) : pathname
}

type Role = 'admin' | 'operator' | 'viewer'
type ControllerUpdateStatus = {
  channel: 'stable' | 'dev' | 'pinned' | ''
  current: { version: string; build: string; commit: string; date: string }
  available: { version: string; build: string; commit: string; date: string }
  update_available: boolean
  auto_update_enabled: boolean
  can_cancel: boolean
  status: string
  last_checked_at?: string
  last_error?: string
  backup_path?: string
  manual_command?: string
}
type BackupDestination = { provider: 's3' | 'webdav' | ''; endpoint: string; bucket?: string; prefix?: string; region?: string; force_path_style?: boolean; enabled: boolean }
type ControllerBackup = { id: string; name: string; origin: 'manual' | 'automatic' | 'uploaded' | 'pre_restore' | string; local_status: string; remote_status: string; remote_error?: string; remote_retrievable: boolean; size_bytes: number; source_version: string; format_version: number; protected: boolean; created_at: string }
type ControllerBackupSettings = { enabled: boolean; schedule: 'daily' | 'weekly'; time: string; weekday: number; local_retention: number; remote_retention: number; destination: BackupDestination; password_configured: boolean; destination_configured: boolean; last_success_at?: string; last_error?: string }
type ControllerBackupSnapshot = { settings: ControllerBackupSettings; backups: ControllerBackup[] }
type ServerMetricSample = { id: number; server_id: number; cpu_usage_percent: number; memory_used_bytes: number; memory_total_bytes: number; network_upload_bps: number; network_download_bps: number; traffic_upload_bytes: number; traffic_download_bytes: number; connectivity_available?: boolean; connectivity_latency_ms: number; sampled_at: string }
type DNSProvider = 'cloudflare' | 'alidns' | 'tencent_dns' | 'tencent_esa' | 'huawei_cloud'
type DNSCredentialZone = { id: number; credential_id: number; zone_name: string; provider_zone_id?: string; server_id?: number }
type DNSCredential = { id: number; name: string; provider: DNSProvider; zones: DNSCredentialZone[]; configured: boolean; enabled: boolean; verified_at?: string; last_error?: string }
type DNSRecord = { id: string; credential_id: number; dns_zone_id?: number; zone_id?: string; zone_name: string; type: string; name: string; content: string; comment?: string; server_id?: number; inbound_id?: number; proxied?: boolean; ttl: number; enabled: boolean }
type GoogleEABCredential = { id: number; key_id: string; remark: string; usage_count: number; created_at: string }
type Certificate = { id: number; name: string; primary_domain: string; domains: string[]; wildcard: boolean; challenge_type: 'http01' | 'dns01' | 'dns01_manual' | 'imported'; dns_credential_id?: number; issuance_server_id?: number; acme_ca: string; account_email: string; google_eab_credential_id?: number; eab_key_id?: string; eab_configured?: boolean; status: string; revision?: string; not_before?: string; not_after?: string; auto_renew: boolean; validation_records?: DNSRecord[]; last_error?: string; last_issued_at?: string; last_renewal_attempt_at?: string }
type InboundUser = { id: number; inbound_id: number; user_id: number; enabled: boolean }
type SSHUserKey = { id: number; user_id: number; name: string; public_key: string; fingerprint: string; enabled: boolean }
type SSHAccess = { inbound_id: number; name: string; address: string; port: number; username: string }
type AccessSubjectType = 'user' | 'group'
type AccessScopeType = 'global' | 'server' | 'inbound'
type UserGroup = { id: number; name: string; description: string; role: Role; system_key?: string; enabled: boolean; speed_limit_mbps: number; traffic_limit_bytes: number; traffic_reset_mode: string; traffic_reset_day: number }
type UserGroupMember = { id: number; group_id: number; user_id: number; enabled: boolean }
type InboundAccessGrant = { id: number; subject_type: AccessSubjectType; subject_id: number; scope_type: AccessScopeType; server_id?: number; inbound_id?: number; enabled: boolean }
type Outbound = { id: number; server_id: number; next_server_id?: number; name: string; protocol: Protocol; target_address: string; target_port: number; config_json: string; enabled: boolean }
type User = { id: number; username: string; nickname: string; role: Role; status: string; protected?: boolean; proxy_uuid: string; proxy_password: string; speed_limit_mbps: number; traffic_limit_bytes: number; traffic_used_bytes: number; traffic_reset_mode: string; traffic_reset_day: number; traffic_period_key?: string; traffic_period_end?: string; traffic_quota_state?: string; subscription_token: string; subscription_burn_after_read: boolean; subscription_burned_at?: string; subscription_age_enabled: boolean; subscription_age_public_key?: string; subscription_age_policy?: 'optional' | 'required'; totp_enabled?: boolean; passkey_count?: number }
type PasskeyCredential = { id: number; name: string; created_at: string; last_used_at?: string }
type AuthenticationStatus = { totp_enabled: boolean; recovery_codes_remaining: number; passkeys: PasskeyCredential[]; passkey_supported: boolean }
type DNSTransport = 'udp' | 'tcp' | 'dot' | 'doh' | 'doq'
type DNSListKind = 'encrypted' | 'bootstrap'
type DNSCandidate = { tag: string; transport: DNSTransport; server: string; port: number; path?: string; tls_name?: string }
type DNSList = { id: number; name: string; kind: DNSListKind; revision: number; candidates: DNSCandidate[]; enabled: boolean; protected: boolean; usage_count: number; created_at?: string; updated_at?: string }
type DNSListDraft = { name: string; kind: DNSListKind; enabled: boolean; candidates: string }
type ServerDNSPolicy = { server_id: number; encrypted_list_id: number; bootstrap_list_id: number; revision: number; strategy: string; auto_test: 'never' | 'first_apply' | 'periodic'; test_interval_seconds: number; encrypted_selected: DNSCandidate[]; bootstrap_selected: DNSCandidate[]; encrypted_selection_revision: number; bootstrap_selection_revision: number; last_attempt_at?: string; last_success_at?: string; last_error: string; needs_benchmark: boolean; updated_at?: string }
type DNSBenchmarkItem = { tag: string; latency_ms: number; error?: string }
type DNSBenchmarkGroup = { items: DNSBenchmarkItem[]; best_tags: string[] }
type DNSBenchmarkResult = { id: number; report_id: string; request_id?: string; server_id: number; policy_revision: number; encrypted_list_id: number; encrypted_list_revision: number; bootstrap_list_id: number; bootstrap_list_revision: number; encrypted: DNSBenchmarkGroup; bootstrap: DNSBenchmarkGroup; status: string; error: string; created_at: string }
type ForwardBackend = 'auto' | 'realm' | 'nft' | 'builtin'
type ForwardProtocol = 'tcp' | 'udp' | 'tcp_udp'
type ProbeMode = 'never' | 'apply' | 'periodic' | 'sampled' | 'periodic_sampled'
type PortForward = { id: number; name: string; source_server_id: number; target_server_id: number; listen_ip: string; listen_port: number; target_address: string; target_port: number; protocol: ForwardProtocol; backend: ForwardBackend; probe_mode: ProbeMode; probe_interval_seconds: number; sample_rate: number; priority: number; config_json: string; enabled: boolean }
type InboundProbeResult = { id: number; inbound_id: number; server_id: number; config_version: number; mode: string; transport: string; endpoint: string; available: boolean; confirmed: boolean; latency_ms: number; min_latency_ms: number; p95_latency_ms: number; jitter_ms: number; sample_count: number; success_count: number; error: string; result_json: string; created_at: string }
type PortForwardProbeResult = { id: number; port_forward_id: number; server_id: number; mode: string; available: boolean; latency_ms: number; sample_count: number; error: string; result_json: string; created_at: string }
type TunnelType = 'wireguard' | 'ssh'
type Tunnel = { id: number; name: string; source_server_id: number; target_server_id: number; type: TunnelType; local_address: string; peer_address: string; listen_port: number; target_endpoint: string; target_port: number; priority: number; config_json: string; enabled: boolean }
type NotificationTemplate = { title: string; body: string }
type NotificationEventDefinition = { value: string; label: string; description: string; variables: string[] }
type NotificationChannel = { id: number; owner_user_id: number; owner_username?: string; name: string; type: 'telegram' | 'bark'; enabled: boolean; events: string; config_json: string; templates_json: string; user_ids: number[] }
type NotificationAnnouncement = { id: number; actor_user_id: number; actor_name: string; title: string; body: string; user_ids: number[]; queued_count: number; created_at: string }
type RouteAction = 'direct' | 'block' | 'outbound' | 'external' | 'warp' | 'interface'
type RoutingRule = { id: number; server_id: number; name: string; priority: number; match_json: string; action: RouteAction; outbound_id?: number; external_outbound_id?: number; target_server_id?: number; warp_profile_id?: number; outbound_tag: string; interface_name?: string; enabled: boolean }
type ExternalOutboundAccessGrant = { id: number; external_outbound_id: number; subject_type: AccessSubjectType; subject_id: number; enabled: boolean }
type WARPProfile = { id: number; server_id: number; name: string; status: 'needed' | 'requested' | 'ready' | 'failed'; config_json: string; mtu: number; dns_strategy: string; error: string; enabled: boolean }
type SubscriptionFormat = 'plain-json' | 'stash' | 'clash-meta' | 'mihomo' | 'surfboard' | 'surge' | 'surge-mac' | 'loon' | 'egern' | 'shadowrocket' | 'qx' | 'sing-box' | 'v2ray' | 'v2ray-uri' | 'clash'
type SubscriptionProfile = { id: number; name: string; group_name: string; description: string; config_json: string; enabled: boolean; created_at?: string; updated_at?: string }
type SubscriptionAssignment = { id: number; profile_id: number; user_id: number; server_id?: number; inbound_id?: number; group_name: string; enabled: boolean }
type AuditLog = { id: number; actor_id?: number; action: string; target: string; detail: string; ip: string; created_at: string }
type AuditTone = 'success' | 'warning' | 'danger' | 'neutral'
type AuditRiskLevel = 'low' | 'medium' | 'high' | 'critical'
type ConnectionAuditUser = { user_id: number; username: string; nickname: string; risk_level: AuditRiskLevel; risk_score: number; risk_signals: string[]; source_ip_count: number; source_subnet_count: number; shared_source_ip_count: number; server_count: number; connection_count: number; active_peak: number; report_count: number; last_seen_at: string }
type ConnectionAuditOverview = { window_hours: number; generated_at: string; enabled_server_count: number; reporting_user_count: number; elevated_risk_count: number; total_connections: number; unique_source_ips: number; users: ConnectionAuditUser[] }
type ConnectionAuditDimension = { key: string; label: string; secondary?: string; connection_count: number; active_peak: number; last_seen_at: string }
type ConnectionAuditReport = { report_id: string; server_id: number; user_id: number; inbound_id?: number; path_id?: number; source_ip: string; source_geo_code?: string; network: string; destination?: string; destination_port?: number; outbound_tag?: string; outbound_type?: string; connection_count: number; active_peak: number; active_at_end: number; started_at: string; ended_at: string }
type ConnectionAuditUserDetail = { summary: ConnectionAuditUser; sources: ConnectionAuditDimension[]; destinations: ConnectionAuditDimension[]; outbounds: ConnectionAuditDimension[]; servers: ConnectionAuditDimension[]; recent: ConnectionAuditReport[] }
type LimitMode = 'inherit' | 'custom'
type SessionUser = Pick<User, 'id' | 'username' | 'nickname' | 'role' | 'status' | 'totp_enabled' | 'passkey_count'>
type UserDraft = { username: string; nickname: string; password?: string; role: Role; status: string; speed_limit_mbps: number; traffic_limit_bytes: number; traffic_reset_mode: string; traffic_reset_day: number; limit_mode: LimitMode }
type UserGroupDraft = { name: string; description: string; role: Role; enabled: boolean; speed_limit_mbps: number; traffic_limit_bytes: number; traffic_reset_mode: string; traffic_reset_day: number }

const protocols: Protocol[] = ['vless', 'hy2', 'anytls', 'shadowsocks', 'ssh']
const proxyProtocols: Exclude<Protocol, 'ssh'>[] = ['vless', 'hy2', 'anytls', 'shadowsocks']
const externalProtocols: ExternalProtocol[] = ['vless', 'hy2', 'anytls', 'shadowsocks', 'socks']
const forwardProtocols: ForwardProtocol[] = ['tcp', 'udp', 'tcp_udp']
const forwardBackends: ForwardBackend[] = ['auto', 'realm', 'nft', 'builtin']
const probeModes: ProbeMode[] = ['never', 'apply', 'periodic', 'sampled', 'periodic_sampled']
const tunnelTypes: TunnelType[] = ['wireguard', 'ssh']
const proxyPathChainMethods = [
  { value: '2022-blake3-aes-128-gcm', label: 'SS 2022-128' },
  { value: '2022-blake3-aes-256-gcm', label: 'SS 2022-256' },
  { value: '2022-blake3-chacha20-poly1305', label: 'SS 2022-ChaCha20' },
]
const ipStacks = ['auto', 'ipv4_only', 'ipv6_only', 'dual_stack', 'prefer_ipv4', 'prefer_ipv6']
const entryIPModes: EntryIPMode[] = ['auto', 'ipv4', 'ipv6', 'custom']
const dnsRecordTypes: DNSRecordTypes[] = ['a', 'aaaa', 'both']
const udpModes = ['allow', 'block', 'uot']
const mtuModes = ['disabled', 'detect', 'apply']
const trafficTimezones = [
  'Asia/Shanghai', 'UTC', 'Asia/Hong_Kong', 'Asia/Taipei', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Singapore', 'Asia/Bangkok', 'Asia/Jakarta', 'Asia/Kolkata', 'Asia/Dubai',
  'Australia/Sydney', 'Pacific/Auckland',
  'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Moscow',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'America/Toronto', 'America/Vancouver', 'America/Mexico_City', 'America/Sao_Paulo',
  'Africa/Johannesburg',
]
const trafficTimezoneNames: Record<string, string> = {
  UTC: '协调世界时',
  'Asia/Shanghai': '北京时间',
  'Asia/Hong_Kong': '香港时间',
  'Asia/Taipei': '台北时间',
  'Asia/Tokyo': '东京时间',
  'Asia/Seoul': '首尔时间',
  'Asia/Singapore': '新加坡时间',
  'Asia/Bangkok': '曼谷时间',
  'Asia/Jakarta': '雅加达时间',
  'Asia/Kolkata': '印度时间',
  'Asia/Dubai': '迪拜时间',
  'Australia/Sydney': '悉尼时间',
  'Pacific/Auckland': '奥克兰时间',
  'Europe/London': '伦敦时间',
  'Europe/Paris': '巴黎时间',
  'Europe/Berlin': '柏林时间',
  'Europe/Moscow': '莫斯科时间',
  'America/New_York': '美国东部时间',
  'America/Chicago': '美国中部时间',
  'America/Denver': '美国山地时间',
  'America/Los_Angeles': '美国西部时间',
  'America/Toronto': '多伦多时间',
  'America/Vancouver': '温哥华时间',
  'America/Mexico_City': '墨西哥城时间',
  'America/Sao_Paulo': '圣保罗时间',
  'Africa/Johannesburg': '约翰内斯堡时间',
}

function trafficTimezoneLabel(timezone: string) {
  try {
    const offset = new Intl.DateTimeFormat('zh-CN', { timeZone: timezone, timeZoneName: 'longOffset' as any })
      .formatToParts(new Date())
      .find(part => part.type === 'timeZoneName')?.value
      ?.replace('GMT', 'UTC')
    return [trafficTimezoneNames[timezone], timezone, offset].filter(Boolean).join(' · ')
  } catch {
    return timezone
  }
}
const routeActions: RouteAction[] = ['direct', 'block', 'outbound', 'external', 'warp', 'interface']
const outboundScopes = ['global', 'server']
const warpStatuses = ['needed', 'requested', 'ready', 'failed']

const qureRegionFlags: Record<string, string> = {
  AR: 'Argentina.png', AU: 'Australia.png', BR: 'Brazil.png', CA: 'Canada.png', CN: 'China.png',
  DE: 'Germany.png', EG: 'Egypt.png', FI: 'Finland.png', FR: 'France.png', GB: 'United_Kingdom.png',
  HK: 'Hong_Kong.png', IN: 'India.png', JP: 'Japan.png', KR: 'Korea.png', MO: 'Macao.png',
  MY: 'Malaysia.png', PH: 'Philippines.png', RU: 'Russia.png', SG: 'Singapore.png', TH: 'Thailand.png',
  TR: 'Turkey.png', TW: 'Taiwan.png', UA: 'Ukraine.png', US: 'United_States.png',
}
const isoRegionCodes = new Set('AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CP CR CU CV CW CX CY CZ DE DG DJ DK DM DO DZ EC EE EG EH ER ES ET EU FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU IC ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PC PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM UN US UY UZ VA VC VE VG VI VN VU WF WS XK XX YE YT ZA ZM ZW'.split(' '))
const regionLabelOverrides: Record<string, string> = {
  CN: '中国',
  HK: '香港',
  MO: '澳门',
  TW: '台湾',
}
const regionDisplayNames = typeof (Intl as any).DisplayNames === 'function'
  ? new (Intl as any).DisplayNames(['zh-CN'], { type: 'region' })
  : null

function normalizeRegionCode(code?: string) {
  const value = String(code || '').trim().toUpperCase()
  return /^[A-Z]{2}$/.test(value) ? value : ''
}

function regionLabel(code?: string) {
  const value = normalizeRegionCode(code)
  if (!value) return '地区待检测'
  if (regionLabelOverrides[value]) return regionLabelOverrides[value]
  const localized = regionDisplayNames?.of(value)
  return localized && localized !== value ? localized : value
}

function regionFlagEmoji(code: string) {
  return Array.from(code).map(char => String.fromCodePoint(127397 + char.charCodeAt(0))).join('')
}

const regionOptions = (() => {
  const values: { code: string; label: string }[] = []
  for (let first = 65; first <= 90; first++) {
    for (let second = 65; second <= 90; second++) {
      const code = String.fromCharCode(first, second)
      const localized = regionDisplayNames?.of(code)
      if (isoRegionCodes.has(code) && ((localized && localized !== code) || regionLabelOverrides[code])) values.push({ code, label: regionLabel(code) })
    }
  }
  return values.sort((a, b) => a.label.localeCompare(b.label, 'zh-CN'))
})()
const popularRegionCodes = ['CN', 'TW', 'HK', 'MO', 'US', 'SG', 'JP', 'KR', 'CA', 'DE', 'GB']
const popularRegions = popularRegionCodes.map(code => ({ code, label: regionLabel(code) }))
const otherRegions = regionOptions.filter(region => !popularRegionCodes.includes(region.code))

function serverRegionCode(server?: Pick<Server, 'region_mode' | 'region_code' | 'detected_region_code'>) {
  if (!server) return ''
  return normalizeRegionCode(server.region_mode === 'manual' ? server.region_code : server.detected_region_code)
}

function RegionFlag({ code, size = 22 }: { code?: string; size?: number }) {
  const value = normalizeRegionCode(code)
  const label = regionLabel(value)
  const qureFilename = value ? qureRegionFlags[value] : 'World_Map.png'
  if (qureFilename) return <img className="region-flag" src={appPath(`/region-flags/${qureFilename}`)} width={size} height={size} alt={label} title={label} />
  if (isoRegionCodes.has(value)) return <img className="region-flag" src={appPath(`/region-flags/iso/${value.toLowerCase()}.svg`)} width={size} height={size} alt={label} title={label} />
  return <span className="region-flag region-flag-emoji" style={{ width: size, height: size, fontSize: Math.max(15, size * 0.72) }} role="img" aria-label={label} title={label}>{regionFlagEmoji(value)}</span>
}

function RegionPicker({ value, onChange }: { value: string; onChange: (code: string) => void }) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [position, setPosition] = useState<{ top: number; left: number; width: number; maxHeight: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const selectedCode = normalizeRegionCode(value) || 'CN'
  const normalizedQuery = query.trim().toLocaleLowerCase('zh-CN')
  const matchesQuery = (region: { code: string; label: string }) => !normalizedQuery
    || region.code.toLowerCase().includes(normalizedQuery)
    || region.label.toLocaleLowerCase('zh-CN').includes(normalizedQuery)
  const filteredPopular = popularRegions.filter(matchesQuery)
  const filteredOthers = otherRegions.filter(matchesQuery)

  const updatePosition = () => {
    const trigger = triggerRef.current
    if (!trigger) return
    const rect = trigger.getBoundingClientRect()
    const viewportPadding = 10
    const gutter = 8
    const width = Math.min(620, window.innerWidth - viewportPadding * 2)
    const left = Math.min(Math.max(viewportPadding, rect.left), window.innerWidth - viewportPadding - width)
    const below = window.innerHeight - rect.bottom - viewportPadding - gutter
    const above = rect.top - viewportPadding - gutter
    const maxHeight = Math.min(480, Math.max(240, Math.max(below, above)))
    const top = below >= 300 || below >= above
      ? rect.bottom + gutter
      : Math.max(viewportPadding, rect.top - gutter - maxHeight)
    setPosition({ top, left, width, maxHeight })
  }

  useEffect(() => {
    if (!open) return
    updatePosition()
    const reposition = () => updatePosition()
    const closeOnOutsideClick = (event: MouseEvent) => {
      const target = event.target as HTMLElement
      if (!triggerRef.current?.contains(target) && !panelRef.current?.contains(target)) setOpen(false)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false)
        triggerRef.current?.focus()
      }
    }
    window.addEventListener('resize', reposition)
    window.addEventListener('scroll', reposition, true)
    document.addEventListener('mousedown', closeOnOutsideClick)
    document.addEventListener('keydown', closeOnEscape)
    window.requestAnimationFrame(() => searchRef.current?.focus())
    return () => {
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
      document.removeEventListener('mousedown', closeOnOutsideClick)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [open])

  const chooseRegion = (code: string) => {
    onChange(code)
    setOpen(false)
    setQuery('')
    triggerRef.current?.focus()
  }

  const renderRegion = (region: { code: string; label: string }) => (
    <button
      key={region.code}
      type="button"
      className={`region-picker-option${region.code === selectedCode ? ' selected' : ''}`}
      role="option"
      aria-selected={region.code === selectedCode}
      onClick={() => chooseRegion(region.code)}
    >
      <RegionFlag code={region.code} size={28} />
      <span>{region.label}</span>
      <small>{region.code}</small>
      {region.code === selectedCode ? <Check size={15} aria-hidden="true" /> : null}
    </button>
  )

  return <div className="region-picker">
    <button
      ref={triggerRef}
      type="button"
      className="region-picker-trigger"
      aria-haspopup="listbox"
      aria-expanded={open}
      onClick={() => { setQuery(''); setOpen(current => !current) }}
    >
      <RegionFlag code={selectedCode} size={28} />
      <span>{regionLabel(selectedCode)}</span>
      <small>{selectedCode}</small>
      <ChevronDown size={16} aria-hidden="true" />
    </button>
    {open && position && createPortal(
      <div
        ref={panelRef}
        className="region-picker-panel"
        style={{ top: position.top, left: position.left, width: position.width, maxHeight: position.maxHeight }}
      >
        <div className="region-picker-search">
          <Search size={18} aria-hidden="true" />
          <input
            ref={searchRef}
            value={query}
            onChange={event => setQuery(event.target.value)}
            placeholder="搜索国家、地区或代码"
            aria-label="搜索服务器地区"
          />
          {query ? <button type="button" className="icon-button ghost" onClick={() => setQuery('')} aria-label="清空搜索" title="清空搜索"><X size={16} /></button> : null}
        </div>
        <div className="region-picker-results" role="listbox" aria-label="服务器地区">
          {filteredPopular.length > 0 ? <section>
            <h4>常用地区</h4>
            <div className="region-picker-grid popular">{filteredPopular.map(renderRegion)}</div>
          </section> : null}
          {filteredOthers.length > 0 ? <section>
            <h4>{normalizedQuery ? '搜索结果' : '全部地区'}</h4>
            <div className="region-picker-grid">{filteredOthers.map(renderRegion)}</div>
          </section> : null}
          {filteredPopular.length === 0 && filteredOthers.length === 0 ? <div className="region-picker-empty">没有匹配的地区</div> : null}
        </div>
      </div>,
      document.body,
    )}
  </div>
}

function ServerRegionField({ draft, update }: { draft: any; update: (patch: any) => void }) {
  const mode: RegionMode = draft.region_mode === 'manual' ? 'manual' : 'auto'
  const detectedCode = normalizeRegionCode(draft.detected_region_code)

  return (
    <FormField label="服务器地区" hint="自动识别出口地区，也可手动指定。">
      <div className="server-region-editor">
        <Select
          variant="segmented"
          value={mode}
          onChange={e =>
            update({
              region_mode: e.target.value as RegionMode,
              region_code: e.target.value === 'auto' ? '' : (draft.region_code || draft.detected_region_code || 'CN'),
            })
          }
        >
          <option value="auto">自动识别</option>
          <option value="manual">手动指定</option>
        </Select>

        {mode === 'manual' ? (
          <RegionPicker
            value={normalizeRegionCode(draft.region_code) || 'CN'}
            onChange={region_code => update({ region_code })}
          />
        ) : (
          <div className="server-region-auto-box">
            <RegionFlag code={detectedCode} size={28} />
            <div className="server-region-auto-info">
              <span className="server-region-auto-title">
                {detectedCode ? regionLabel(detectedCode) : '等待 Agent 出口探测'}
              </span>
              {detectedCode ? <small className="server-region-auto-code">{detectedCode}</small> : null}
            </div>
            <span className="server-region-tag">{detectedCode ? '已自动探测' : '待探测'}</span>
          </div>
        )}
      </div>
    </FormField>
  )
}
const subscriptionFormats: { value: SubscriptionFormat; label: string }[] = [
  { value: 'sing-box', label: 'sing-box' },
  { value: 'clash-meta', label: 'Clash.Meta' },
  { value: 'mihomo', label: 'Mihomo' },
  { value: 'stash', label: 'Stash' },
  { value: 'plain-json', label: 'Plain JSON' },
  { value: 'surfboard', label: 'Surfboard' },
  { value: 'surge', label: 'Surge' },
  { value: 'surge-mac', label: 'Surge Mac' },
  { value: 'loon', label: 'Loon' },
  { value: 'egern', label: 'Egern' },
  { value: 'shadowrocket', label: 'Shadowrocket' },
  { value: 'qx', label: 'Quantumult X' },
  { value: 'v2ray', label: 'V2Ray' },
  { value: 'v2ray-uri', label: 'V2Ray URI' },
  { value: 'clash', label: 'Clash' }
]
const tabMeta: Record<string, { label: string; desc: string; group: string }> = {
  dashboard: { label: '总览', desc: '全局健康、版本、部署状态和关键指标。', group: '总览' },
  account: { label: '我的账户', desc: '查看个人订阅并维护昵称和登录密码。', group: '账户' },
  servers: { label: '服务器管理', desc: '管理服务器、Agent、IP 栈、UDP 入站和端口范围。', group: '基础设施' },
  'proxy-paths': { label: '代理链路', desc: '管理入口、服务器跳点、第三方出口和传递路径。', group: '代理编排' },
  inbounds: { label: '入口', desc: '统一编排 sing-box 入站监听、协议和端口。', group: '代理' },
  outbounds: { label: '出口', desc: '配置服务器出口、下一跳和协议认证参数。', group: '代理' },
  routing: { label: '分流规则', desc: '为任意服务器配置分流规则、直连、链路、WARP 或导入节点。', group: '流量' },
  'external-outbounds': { label: '导入节点', desc: '导入第三方 SS、SOCKS、VLESS 等节点。', group: '流量' },
  warp: { label: 'WARP', desc: '按服务器申请和管理独立 WARP 配置。', group: '流量' },
  users: { label: '用户', desc: '多用户、凭据、限速、流量额度和订阅令牌。', group: '访问控制' },
  dns: { label: 'DNS 设置', desc: '为服务器选择解析服务并检查解析速度。', group: '网络' },
  'dns-records': { label: '域名解析', desc: '管理云服务商账号和域名解析记录。', group: '网络' },
  mtu: { label: 'MTU', desc: '检测路径 MTU、给出建议并可由 Agent 应用。', group: '网络' },
  'port-forwards': { label: '端口转发', desc: '配置 Realm、nft 或内置端口转发与延迟探测。', group: '拓扑' },
  tunnels: { label: '隧道', desc: '配置 WireGuard / SSH 服务器间隧道。', group: '拓扑' },
  notifications: { label: '通知', desc: '配置消息通道、接收提醒并管理通知模板。', group: '通知' },
  subscriptions: { label: '订阅', desc: '管理订阅配置、节点分组和用户分配。', group: '访问控制' },
  tasks: { label: '任务', desc: '查询配置下发、Agent 任务和部署回执。', group: '运维' },
  audit: { label: '审计台', desc: '分析连接来源、出口行为和操作记录。', group: '运维' },
  settings: { label: '设置', desc: '管理面板设置。', group: '系统' }
}
const navGroups = [
  { label: '', tabs: ['dashboard'] },
  { label: '基础设施', tabs: ['servers'] },
  { label: '代理编排', tabs: ['proxy-paths'] },
  { label: '网络', tabs: ['dns', 'dns-records'] },
  { label: '访问控制', tabs: ['users', 'subscriptions'] },
  { label: '通知', tabs: ['notifications'] },
  { label: '运维审计', tabs: ['tasks', 'audit'] },
  { label: '系统', tabs: ['settings'] },
  { label: '账户', tabs: ['account'] },
]

const roleRanks: Record<Role, number> = { viewer: 0, operator: 1, admin: 2 }
const tabMinimumRole: Record<string, Role> = {
  account: 'viewer', dashboard: 'operator', tasks: 'operator', audit: 'operator',
  servers: 'operator', 'proxy-paths': 'operator',
  users: 'admin', subscriptions: 'admin', notifications: 'viewer', settings: 'admin',
  dns: 'admin', 'dns-records': 'admin', mtu: 'operator',
}

const preloadTabsByRole: Record<Role, string[]> = {
  viewer: ['account', 'notifications'],
  operator: ['servers', 'proxy-paths', 'tasks', 'audit', 'mtu'],
  admin: ['servers', 'proxy-paths', 'users', 'dns', 'dns-records', 'tasks', 'audit', 'settings'],
}

function tabAllowedForRole(tab: string, role: Role) {
  return roleRanks[role] >= roleRanks[tabMinimumRole[tab] || 'viewer']
}

function sessionRoleLabel(role?: Role) {
  if (role === 'admin') return '系统管理员'
  if (role === 'operator') return '操作员'
  return '只读用户'
}

function sessionInitials(username?: string) {
  const text = String(username || 'U').trim()
  return Array.from(text).slice(0, 2).join('').toUpperCase()
}

function storedSessionUser(): SessionUser | null {
  try {
    const value = JSON.parse(sessionStorage.getItem('oboard.user') || 'null')
    return value && value.username && value.role ? value as SessionUser : null
  } catch {
    return null
  }
}

const tabPaths: Record<string, string> = Object.keys(tabMeta).reduce((acc, tab) => {
  acc[tab] = '/' + tab
  return acc
}, {} as Record<string, string>)
tabPaths.dashboard = '/dashboard'

const pathTabs: Record<string, string> = Object.entries(tabPaths).reduce((acc, [tab, path]) => {
  acc[path] = tab
  return acc
}, { '/': 'dashboard' } as Record<string, string>)

function tabFromPath(pathname: string) {
  const path = stripAppBasePath(pathname).replace(/\/+$/, '') || '/'
  return pathTabs[path] || 'dashboard'
}

function pathForTab(tab: string) {
  return appPath(tabPaths[tab] || '/dashboard')
}

function goTab(tab: string) {
  const path = pathForTab(tab)
  if (window.location.pathname !== path) window.history.pushState({ tab }, '', path)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

function formatDashDate(d = new Date()) {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}.${m}.${day}`
}

function getTabIcon(x: string) {
  if (x === 'account') return <User size={18} />
  if (x === 'dashboard') return <LayoutDashboard size={18} />
  if (x === 'servers') return <ServerIcon size={18} />
  if (x === 'proxy-paths') return <Workflow size={18} />
  if (x === 'users') return <UsersIcon size={18} />
  if (x === 'subscriptions') return <LinkIcon size={18} />
  if (x === 'notifications') return <Bell size={18} />
  if (x === 'tasks') return <CheckSquare size={18} />
  if (x === 'audit') return <ClipboardList size={18} />
  if (x === 'dns') return <Globe size={18} />
  if (x === 'dns-records') return <Database size={18} />
  if (x === 'settings') return <SettingsIcon size={18} />
  return <Sliders size={18} />
}

const fieldLabels: Record<string, string> = {
  id: 'ID', server_id: '服务器', source_server_id: '源服务器', target_server_id: '目标服务器', next_server_id: '下一跳服务器', user_id: '用户', group_id: '用户组', subject_id: '授权对象', profile_id: '订阅配置', inbound_id: '入口', inbound_users: '入口用户', outbound_id: '出口', external_outbound_id: '导入节点', external_outbound_access_grants: '导入节点授权', warp_profile_id: 'WARP 配置',
  name: '名称', region: '地区', region_code: '地区代码', region_mode: '地区来源', username: '用户名', nickname: '昵称', password: '密码', role: '角色', status: '状态', enabled: '启用', expose_to_users: '显示到订阅', protocol: '协议', type: '类型', scope: '作用域', action: '动作', priority: '优先级',
  entry_address: '入口地址', public_ipv4: '检测 IPv4', public_ipv6: '检测 IPv6', entry_ip_mode: '入口地址策略', external_ip: '自定义入口地址', listen_ip: '监听 IP', listen_port: '监听端口', port: '端口', port_range: '端口范围', port_range_start: '端口范围起点', port_range_end: '端口范围终点', target_address: '目标地址', target_port: '目标端口', target_endpoint: '目标端点',
  dns_sync_enabled: '域名解析', dns_credential_id: '域名服务账号', dns_domain: '解析域名', dns_proxy_enabled: '代理访问', dns_record_types: '解析记录', ddns_enabled: '自动更新地址', ddns_interval_seconds: '更新间隔', dns_sync_status: '同步状态', dns_sync_error: '同步错误', dns_last_synced_at: '同步时间',
  subject_type: '授权类型', scope_type: '授权范围',
  ip_stack: 'IP 栈', udp_inbound_mode: 'UDP 入站', mtu_mode: 'MTU 模式', mtu_value: 'MTU 值', mtu_probe_host: 'MTU 探测主机', mtu_probe_port: 'MTU 探测端口', mtu_overhead_bytes: 'MTU 额外开销', bbr_enabled: 'BBR + FQ',
  os: '系统', system: '系统', distro_id: '发行版 ID', distro_version: '发行版版本', distro_name: '发行版', libc: 'libc', service_manager: '服务管理器', package_manager: '包管理器', arch: '架构', cpu: 'CPU', cpu_usage: 'CPU', cpu_usage_percent: 'CPU 使用率', memory: '内存', memory_used_bytes: '已用内存', memory_total_bytes: '总内存', agent_memory: 'Agent 内存', agent_memory_bytes: 'Agent 内存', agent_version: 'Agent 版本', agent_build: 'Agent 构建', sing_box_version: 'sing-box 版本', download_rate: '下载速率', upload_rate: '上传速率', period_traffic: '周期流量', monitoring_mode: '回报模式',
  tls: 'TLS', certificate_mode: '证书模式', certificate_id: '证书', certificate_domain: '证书域名', config_json: 'JSON 配置', match_json: '匹配规则 JSON', result_json: '结果 JSON', events: '事件',
  proxy_uuid: '代理 UUID', proxy_password: '代理密码', speed_limit_mbps: '限速 Mbps', traffic_limit_bytes: '流量额度', traffic_used_bytes: '已用流量', subscription_token: '订阅令牌',
  limit_policy: '限速策略', effective_speed_limit: '实际限速', effective_traffic_limit: '实际流量额度',
  servers_total: '服务器总数', servers_online: '在线服务器', servers_offline: '离线服务器', servers_degraded: '异常服务器', online_servers: '在线服务器', online_agents: '在线 Agent', server_count: '服务器数量',
  users_total: '用户总数', users_active: '活跃用户', traffic_upload_bytes: '上传流量', traffic_download_bytes: '下载流量', total_traffic_bytes: '总流量', upload_bytes: '上传流量', download_bytes: '下载流量',
  pending_tasks: '等待任务', running_tasks: '执行中任务', failed_tasks: '失败任务', last_config_version: '最新配置版本',
  final: '最终策略', transport: '传输', preset: '预设', address: '地址', encrypted: '加密解析', bootstrap: '基础解析', strategy: 'IP 类型', auto_test: '自动检查', test_interval_seconds: '检查间隔', last_egress_ip: '上次出口 IP',
  encrypted_list: '加密解析服务', bootstrap_list: '基础解析服务', encrypted_selected: '加密解析结果', bootstrap_selected: '基础解析结果',
  backend: '后端', probe_mode: '探测模式', probe_interval_seconds: '探测间隔秒', sample_rate: '采样率',
  local_address: '本地地址', peer_address: '对端地址', interface_name: '网卡', current_mtu: '当前 MTU', path_mtu: '路径 MTU', recommended_mtu: '建议 MTU', applied_mtu: '已应用 MTU', confidence: '可信度', error: '错误',
  format: '格式', group_name: '分组名', description: '描述', subscription_format: '订阅格式', subscription_url: '订阅链接', outbound_tag: '出口标签',
  created_at: '创建时间', updated_at: '更新时间', completed_at: '完成时间', config_version: '配置版本', task_id: '任务 ID', payload_json: '任务内容 JSON', nonce: '随机数', result: '结果',
  total: '总数', pending: '等待中', running: '执行中', succeeded: '成功', failed: '失败', partial_failed: '部分失败', timeout: '超时', latency_ms: '平均延迟', min_latency_ms: '最低延迟', p95_latency_ms: 'P95 延迟', jitter_ms: '抖动', success_count: '成功次数', sample_count: '样本数', endpoint: '探测端点', probe_status: '端口状态', probe_detail: '探测明细', checked_at: '检测时间', message: '消息'
}

const valueLabels: Record<string, string> = {
  admin: '管理员', operator: '操作员', viewer: '只读', active: '活跃', online: '在线', offline: '离线', unknown: '未知', healthy: '健康', unhealthy: '异常',
  enabled: '已启用', disabled: '已禁用', true: '启用', false: '禁用', succeeded: '成功', success: '成功', skipped: '已跳过', warning: '需关注', failed: '失败', partial_failed: '部分失败', timeout: '超时', error: '错误', pending: '等待中', running: '执行中', requested: '已请求', needed: '需要申请', ready: '就绪',
  rollback_failed: '回滚失败', direct: '直连', block: '阻断', outbound: '出口', external: '导入节点', chain: '链式代理', warp: 'WARP', interface: '指定网卡', socks: 'SOCKS',
  global: '全局', server: '服务器', auto: '自动（IPv4 优先）', ipv4: 'IPv4', ipv6: 'IPv6', custom: '自定义', ipv4_only: '仅 IPv4', ipv6_only: '仅 IPv6', dual_stack: '双栈', prefer_ipv4: '优先 IPv4', prefer_ipv6: '优先 IPv6',
  a: 'A', aaaa: 'AAAA', both: 'A + AAAA',
  allow: '允许', uot: 'UoT', never: '从不', first_apply: '首次下发', periodic: '定期', always: '每次', sampled: '实际连接采样', periodic_sampled: '定期+采样',
  detect: '仅检测', apply: '检测并应用', tcp_udp: 'TCP+UDP', builtin: '内置', wireguard: 'WireGuard', ssh: 'SSH', doh: 'DoH', dot: 'DoT', udp: 'UDP', tcp: 'TCP',
  cloudflare: 'Cloudflare', google: 'Google', quad9: 'Quad9', alidns: '阿里 DNS', dnspod: 'DNSPod', remote: '远程', local: '本地',
  apply_deployment: '应用部署', apply_core_config: '下发核心配置', probe_inbounds: '检查入口监听', probe_inbounds_external: '检查公网端口', probe_port_forwards: '探测端口转发',
  detect_mtu: 'MTU 检测', update_agent_config: '同步 Agent 配置', diagnose_network: '网络诊断',
  collect_logs: '拉取日志', manage_logs: '管理日志',
  install_agent: '安装 Agent', update_agent: '更新 Agent', uninstall_agent: '卸载 Agent',
}

type ToastKind = 'error' | 'success' | 'warning' | 'info'
type ToastState = { id: number; kind: ToastKind; message: string } | null
type DialogTone = 'default' | 'danger'
type DialogChoice = { value: string; label: string }
type DialogBase = { title: string; message?: React.ReactNode; confirmText?: string; cancelText?: string; tone?: DialogTone }
type PromptDialogOptions = DialogBase & { defaultValue?: string; placeholder?: string; inputType?: string; choices?: DialogChoice[] }
type DialogState =
  | ({ id: number; kind: 'alert'; resolve: () => void } & DialogBase)
  | ({ id: number; kind: 'confirm'; resolve: (value: boolean) => void } & DialogBase)
  | ({ id: number; kind: 'prompt'; resolve: (value: string | null) => void } & PromptDialogOptions)
type DialogApi = {
  alert: (options: DialogBase) => Promise<void>
  confirm: (options: DialogBase) => Promise<boolean>
  prompt: (options: PromptDialogOptions) => Promise<string | null>
}

const DialogContext = React.createContext<DialogApi | null>(null)
const LoadingContext = React.createContext<boolean>(false)

const errorMessages: Record<string, string> = {
  'invalid credentials': '用户名或密码错误',
  'invalid session': '登录状态已失效，请重新登录',
  'invalid token': '登录状态已失效，请重新登录',
  'token is expired': '登录状态已失效，请重新登录',
  'invalid signature': '登录状态已失效，请重新登录',
  'missing or malformed jwt': '登录状态已失效，请重新登录',
  'current password is incorrect': '当前密码不正确',
  'new password must be at least 8 characters': '新密码至少需要 8 位',
  'new password must be at least 10 characters': '新密码至少需要 10 位',
  'rate limit exceeded': '操作过于频繁，请稍后再试',
  forbidden: '权限不足，无法执行该操作',
  'method not allowed': '请求方法不允许',
  'only the latest deployment failure can be dismissed': '只能忽略当前最新一次下发的失败提醒',
  'deployment is still running': '下发仍在进行中，暂时不能忽略',
  'deployment has no failure': '当前下发没有失败项',
  'age public key is required': '需要先配置 Age 公钥',
  'age encryption is not enabled for this user': '该用户尚未开启 Age 加密',
  'age encryption is only supported for Mihomo subscriptions': 'Age 加密仅支持 Mihomo、Clash.Meta 和 Clash 格式',
  'do not upload an age secret key; provide the public key': '请填写 Age 公钥，不要上传私钥',
  'subscription_age_policy must be optional or required': '订阅加密策略无效',
  '验证码或恢复码错误': '验证码或恢复码错误',
  '六位验证码错误': '六位验证码错误',
  '该账号无法使用通行密钥': '该账号尚未添加通行密钥，或当前环境不支持',
  '通行密钥验证失败': '通行密钥验证失败，请重试',
  '通行密钥登录请求已失效': '通行密钥登录已超时，请重试',
  '用户名或密码错误': '用户名或密码错误'
}

function localizeErrorMessage(message: unknown) {
  const raw = String(message || '').trim()
  if (!raw) return '操作失败，请稍后重试'
  if (raw.startsWith('invalid age public key:')) return 'Age 公钥格式无效'
  return errorMessages[raw] || errorMessages[raw.toLowerCase()] || raw
}

function showToast(setToast: React.Dispatch<React.SetStateAction<ToastState>>, message: unknown, kind: ToastKind = 'error') {
  setToast({ id: Date.now(), kind, message: kind === 'error' ? localizeErrorMessage(message) : String(message) })
}

function TopToast({ toast, onClose }: { toast: ToastState; onClose: () => void }) {
  if (!toast) return null
  return <Toast message={toast.message} kind={toast.kind} onClose={onClose} />
}

function IconButton({ label, onClick, children, className = '', busy = false }: { label: string; onClick: () => void; children: React.ReactNode; className?: string; busy?: boolean }) {
  return <button className={['ghost icon-button', className].filter(Boolean).join(' ')} onClick={onClick} aria-label={label} title={label} aria-busy={busy}>{children}</button>
}

function XIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 6 6 18" /><path d="m6 6 12 12" /></svg>
}

function ChevronDownIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 9 6 6 6-6" /></svg>
}

function RefreshIcon() {
  return <RefreshCw size={17} strokeWidth={2} aria-hidden="true" />
}

function useDialogs() {
  const dialogs = React.useContext(DialogContext)
  if (!dialogs) throw new Error('DialogContext is not mounted')
  return dialogs
}

function useDialogController() {
  const [dialog, setDialog] = useState<DialogState | null>(null)
  const dialogs = useMemo<DialogApi>(() => ({
    alert: options => new Promise<void>(resolve => {
      setDialog({ ...options, id: Date.now(), kind: 'alert', resolve })
    }),
    confirm: options => new Promise<boolean>(resolve => {
      setDialog({ ...options, id: Date.now(), kind: 'confirm', resolve })
    }),
    prompt: options => new Promise<string | null>(resolve => {
      setDialog({ ...options, id: Date.now(), kind: 'prompt', resolve })
    }),
  }), [])
  return { dialogs, dialog, setDialog }
}

function DialogHost({ dialog, onClose }: { dialog: DialogState | null; onClose: () => void }) {
  const [value, setValue] = useState('')
  useEffect(() => {
    if (dialog?.kind === 'prompt') setValue(dialog.defaultValue || '')
  }, [dialog?.id])
  if (!dialog) return null
  const close = (result?: string | boolean | null) => {
    if (dialog.kind === 'alert') dialog.resolve()
    if (dialog.kind === 'confirm') dialog.resolve(Boolean(result))
    if (dialog.kind === 'prompt') dialog.resolve(typeof result === 'string' ? result : null)
    onClose()
  }
  const confirmText = dialog.confirmText || (dialog.kind === 'alert' ? '知道了' : '确认')
  const isPrompt = dialog.kind === 'prompt'
  const hasMessage = Boolean(dialog.message)

  return (
    <Dialog
      isOpen={!!dialog}
      onClose={() => close(dialog.kind === 'confirm' ? false : null)}
      title={dialog.title}
      size="sm"
      className={[
        'dialog-host',
        isPrompt ? 'dialog-host-prompt' : 'dialog-host-compact',
        dialog.tone === 'danger' ? 'dialog-host-danger' : '',
      ].filter(Boolean).join(' ')}
    >
      {(hasMessage || isPrompt) && (
        <div className="dialog-host-body">
          {hasMessage && <div className="dialog-host-message">{dialog.message}</div>}
          {isPrompt && (
            <div className="dialog-host-field">
              {dialog.choices?.length ? (
                <Select value={value} onChange={e => setValue(e.target.value)} autoFocus>
                  {dialog.choices.map(x => <option value={x.value} key={x.value}>{x.label}</option>)}
                </Select>
              ) : (
                <Input
                  value={value}
                  onChange={e => setValue(e.target.value)}
                  placeholder={dialog.placeholder}
                  type={dialog.inputType || 'text'}
                  autoFocus
                  onKeyDown={e => { if (e.key === 'Enter') close(value) }}
                />
              )}
            </div>
          )}
        </div>
      )}
      <div className="dialog-actions dialog-host-actions">
        {dialog.kind !== 'alert' && (
          <button type="button" className="ghost" onClick={() => close(dialog.kind === 'confirm' ? false : null)}>
            {dialog.cancelText || '取消'}
          </button>
        )}
        <button
          type="button"
          className={dialog.tone === 'danger' ? 'danger-button' : ''}
          onClick={() => close(dialog.kind === 'prompt' ? value : true)}
        >
          {confirmText}
        </button>
      </div>
    </Dialog>
  )
}

function CopyBlock({ value }: { value: string }) {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const copy = async () => {
    const ok = await copyText(value)
    setCopyState(ok ? 'copied' : 'failed')
    window.setTimeout(() => setCopyState('idle'), 1400)
  }
  return <div className="copy-block">
    <code>{value || '—'}</code>
    <button type="button" className="copy-block-button" onClick={copy}>{copyState === 'copied' ? '已复制' : copyState === 'failed' ? '复制失败' : '复制'}</button>
  </div>
}

async function copyText(value: string) {
  const text = String(value || '')
  if (!text) return false
  try {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // HTTP 测试站或浏览器权限拒绝时，继续使用 textarea fallback。
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.top = '0'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  textarea.focus()
  textarea.select()
  textarea.setSelectionRange(0, textarea.value.length)
  try {
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(textarea)
  }
}

function passkeyAvailable() {
  return window.isSecureContext && 'PublicKeyCredential' in window && Boolean(navigator.credentials)
}

function base64URLToBytes(value: string) {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4)
  const binary = window.atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return bytes
}

function bytesToBase64URL(value: ArrayBuffer | ArrayBufferView | null) {
  if (!value) return ''
  const bytes = value instanceof ArrayBuffer
    ? new Uint8Array(value)
    : new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, Math.min(offset + 0x8000, bytes.length)))
  }
  return window.btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function decodePasskeyCreationOptions(options: any): PublicKeyCredentialCreationOptions {
  return {
    ...options,
    challenge: base64URLToBytes(options.challenge),
    user: { ...options.user, id: base64URLToBytes(options.user.id) },
    excludeCredentials: (options.excludeCredentials || []).map((credential: any) => ({ ...credential, id: base64URLToBytes(credential.id) })),
  }
}

function decodePasskeyRequestOptions(options: any): PublicKeyCredentialRequestOptions {
  return {
    ...options,
    challenge: base64URLToBytes(options.challenge),
    allowCredentials: (options.allowCredentials || []).map((credential: any) => ({ ...credential, id: base64URLToBytes(credential.id) })),
  }
}

function passkeyCredentialJSON(credential: PublicKeyCredential) {
  const response: any = credential.response
  const payload: any = {
    id: credential.id,
    rawId: bytesToBase64URL(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment || undefined,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bytesToBase64URL(response.clientDataJSON),
    },
  }
  if (response.attestationObject) {
    payload.response.attestationObject = bytesToBase64URL(response.attestationObject)
    payload.response.authenticatorData = typeof response.getAuthenticatorData === 'function' ? bytesToBase64URL(response.getAuthenticatorData()) : undefined
    payload.response.publicKey = typeof response.getPublicKey === 'function' ? bytesToBase64URL(response.getPublicKey()) : undefined
    payload.response.publicKeyAlgorithm = typeof response.getPublicKeyAlgorithm === 'function' ? response.getPublicKeyAlgorithm() : 0
    payload.response.transports = typeof response.getTransports === 'function' ? response.getTransports() : []
  } else {
    payload.response.authenticatorData = bytesToBase64URL(response.authenticatorData)
    payload.response.signature = bytesToBase64URL(response.signature)
    payload.response.userHandle = response.userHandle ? bytesToBase64URL(response.userHandle) : undefined
  }
  return payload
}

async function createPasskeyCredential(options: any) {
  if (!passkeyAvailable()) throw new Error('当前浏览器或访问方式不支持通行密钥')
  const credential = await navigator.credentials.create({ publicKey: decodePasskeyCreationOptions(options.publicKey) }) as PublicKeyCredential | null
  if (!credential) throw new Error('没有创建通行密钥')
  return passkeyCredentialJSON(credential)
}

async function getPasskeyCredential(options: any) {
  if (!passkeyAvailable()) throw new Error('当前浏览器或访问方式不支持通行密钥')
  const credential = await navigator.credentials.get({ publicKey: decodePasskeyRequestOptions(options.publicKey) }) as PublicKeyCredential | null
  if (!credential) throw new Error('没有选择通行密钥')
  return passkeyCredentialJSON(credential)
}

function sleep(ms: number) {
  return new Promise(resolve => window.setTimeout(resolve, ms))
}

function DeploymentSummary({ data }: { data: any }) {
  const items = [
    ['服务器', data.servers?.length || 0],
    ['入口', data.inbounds?.length || 0],
    ['出口', data.outbounds?.length || 0],
    ['分流规则', data.routing_rules?.length || 0],
    ['端口转发', data.port_forwards?.length || 0],
    ['隧道', data.tunnels?.length || 0],
    ['解析服务列表', data.dns_lists?.length || 0],
    ['WARP', data.warp_profiles?.length || 0],
  ]
  return <div className="deployment-summary">
    <p>下发后，相关服务器会按当前链路配置更新。</p>
    <div>{items.map(([label, count]) => <span key={label}>{label}<strong>{count}</strong></span>)}</div>
    <small>进度可以在“任务”里查看。</small>
  </div>
}

type DashboardAttention = {
  parts: string[]
  fingerprint: string
}

function getDashboardAttention(data: any): DashboardAttention {
  const summary = data.summary || {}
  const servers = data.servers || []
  const totalServers = Number(summary.servers_total ?? summary.servers ?? summary.server_count ?? servers.length ?? 0)
  const onlineServers = Number(summary.servers_online ?? summary.online_agents ?? summary.online_servers ?? servers.filter((server: any) => server.status === 'online').length ?? 0)
  const offlineServers = Math.max(0, totalServers - onlineServers)
  const failedTasks = Math.max(0, Number(summary.failed_tasks || 0))
  const offlineSnapshot = servers
    .filter((server: any) => server.status !== 'online')
    .map((server: any) => `${server.id}:${server.status || 'offline'}:${server.last_seen_at || server.updated_at || ''}`)
    .sort()
  const latestFailedTask = (data.agent_tasks || [])
    .filter((task: any) => task.status === 'failed' || task.status === 'rollback_failed')
    .reduce((latest: any, task: any) => Number(task.id || 0) > Number(latest?.id || 0) ? task : latest, null)

  const parts: string[] = []
  if (offlineServers > 0) parts.push(`${offlineServers} 台服务器离线`)
  if (failedTasks > 0) parts.push(`${failedTasks} 个任务失败`)

  return {
    parts,
    fingerprint: parts.length > 0
      ? JSON.stringify({
          offlineServers,
          offlineSnapshot,
          failedTasks,
          latestFailedTask: latestFailedTask
            ? `${latestFailedTask.id}:${latestFailedTask.updated_at || latestFailedTask.finished_at || latestFailedTask.created_at || ''}`
            : '',
        })
      : '',
  }
}

function DashboardAttentionNotice({ parts, className = '', onDismiss }: { parts: string[]; className?: string; onDismiss: () => void }) {
  return (
    <button
      type="button"
      className={`page-announce dismissable ${className}`.trim()}
      onClick={onDismiss}
      title="点击忽略此前问题"
      aria-label={`需要关注：${parts.join('，')}，点击忽略此前问题`}
    >
      <Info size={16} />
      <span className="page-announce-copy">
        <strong>需要关注</strong>
        <span> · {parts.join('，')}</span>
      </span>
    </button>
  )
}

class SupersededAuthRequestError extends Error {
  constructor() {
    super('superseded authentication request')
    this.name = 'SupersededAuthRequestError'
  }
}

function api(token: string, onUnauthorized?: (failedToken: string) => boolean) {
  const csrf = token === 'cookie' ? sessionStorage.getItem('oboard.csrf') || '' : ''
  const authHeaders: Record<string, string> = token && token !== 'cookie' ? { authorization: `Bearer ${token}` } : {}
  const csrfHeaders: Record<string, string> = token === 'cookie' && csrf ? { 'x-oboard-csrf': csrf } : {}
  async function request<T = any>(path: string, init: RequestInit = {}): Promise<T> {
    const res = await fetch(appPath('/api/v1' + path), {
      ...init,
      credentials: 'same-origin',
      headers: {
        'content-type': 'application/json',
        ...authHeaders,
        ...csrfHeaders,
        ...(init.headers || {})
      }
    })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) {
      if (res.status === 401 && token && onUnauthorized) {
        if (!onUnauthorized(token)) throw new SupersededAuthRequestError()
        throw new Error('登录已过期，请重新登录')
      }
      throw new Error(localizeErrorMessage(data.error || res.statusText))
    }
    return data
  }
  async function download(path: string): Promise<{ blob: Blob; filename: string }> {
    const res = await fetch(appPath('/api/v1' + path), { credentials: 'same-origin', headers: authHeaders })
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      throw new Error(localizeErrorMessage(data.error || res.statusText))
    }
    const disposition = res.headers.get('content-disposition') || ''
    const filename = disposition.match(/filename="?([^";]+)"?/i)?.[1] || 'oboard-logs.zip'
    return { blob: await res.blob(), filename }
  }
  async function upload<T = any>(path: string, body: FormData): Promise<T> {
    const res = await fetch(appPath('/api/v1' + path), { method: 'POST', body, credentials: 'same-origin', headers: { ...authHeaders, ...csrfHeaders } })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(localizeErrorMessage(data.error || res.statusText))
    return data
  }
  return { request, download, upload }
}

function App() {
  const [token, setToken] = useState(sessionStorage.getItem('oboard.token') || '')
  const activeTokenRef = useRef(token)
  activeTokenRef.current = token
  const [sessionUser, setSessionUser] = useState<SessionUser | null>(() => storedSessionUser())
  const [tab, setTab] = useState(() => tabFromPath(window.location.pathname))
  const [theme, setTheme] = useState<ThemeName>(() => normalizeTheme(localStorage.getItem('oboard.theme')))
  const [toast, setToast] = useState<ToastState>(null)
  const [loading, setLoading] = useState(false)
  const [data, setData] = useState<any>({})
  const [, setAttentionDismissRevision] = useState(0)
  const loadSeq = useRef(0)
  // Per-tab page-data cache so tab switches can crossfade into last-known content
  // instead of blanking the stage while the network request is in flight.
  const pageCacheRef = useRef<Record<string, any>>({})
  const pageRequestRef = useRef<Record<string, Promise<any>>>({})
  const preloadedTabsRef = useRef(new Set<string>())
  const { dialogs, dialog, setDialog } = useDialogController()
  const client = useMemo(() => api(token, failedToken => {
    if (failedToken !== activeTokenRef.current) return false
    activeTokenRef.current = ''
    loadSeq.current++
    sessionStorage.removeItem('oboard.token')
    sessionStorage.removeItem('oboard.user')
    sessionStorage.removeItem('oboard.csrf')
    setToken('')
    setSessionUser(null)
    setData({})
    pageCacheRef.current = {}
    pageRequestRef.current = {}
    preloadedTabsRef.current.clear()
    showToast(setToast, '登录已过期，请重新登录')
    return true
  }), [token])

  const [showPortalLoader, setShowPortalLoader] = useState(Boolean(token))

  const [isSidebarOpen, setIsSidebarOpen] = useState(false)
  const [isMobile, setIsMobile] = useState(false)

  useEffect(() => {
    const checkMobile = () => {
      const mobile = window.innerWidth <= 900
      setIsMobile(mobile)
      if (!mobile) {
        setIsSidebarOpen(false)
      }
    }
    checkMobile()
    window.addEventListener('resize', checkMobile)
    return () => window.removeEventListener('resize', checkMobile)
  }, [])

  // Lock body scroll when mobile menu is open
  useEffect(() => {
    if (isSidebarOpen && isMobile) {
      document.body.style.overflow = "hidden"
    } else {
      document.body.style.overflow = ""
    }
    return () => {
      document.body.style.overflow = ""
    }
  }, [isSidebarOpen, isMobile])

  // Handle Escape key to close mobile menu
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && isMobile && isSidebarOpen) {
        setIsSidebarOpen(false)
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [isMobile, isSidebarOpen])

  const requestPageData = (page: string) => {
    const pending = pageRequestRef.current[page]
    if (pending) return pending
    const request = client.request(`/page-data?page=${encodeURIComponent(page)}`)
      .finally(() => { delete pageRequestRef.current[page] })
    pageRequestRef.current[page] = request
    return request
  }

  const load = async (targetTab?: string, opts?: { background?: boolean }) => {
    if (!token) return
    const page = typeof targetTab === 'string' && targetTab ? targetTab : tab
    const seq = ++loadSeq.current
    const requestToken = token
    const background = Boolean(opts?.background)
    // Only show the global loading flag when this tab has no cached payload yet.
    // Background revalidation must not flash skeletons during a crossfade.
    if (!background) setLoading(true)
    try {
      const next = await requestPageData(page)
      if (requestToken !== activeTokenRef.current) return
      if (next.current_user && seq === loadSeq.current) {
        setSessionUser(next.current_user)
        sessionStorage.setItem('oboard.user', JSON.stringify(next.current_user))
      }
      const merged = { ...next, load_errors: [] as string[] }
      // Always warm the per-tab cache, even if the user has already navigated away.
      // That way a quick A→B→A return crossfades into fresh content without a blank stage.
      pageCacheRef.current[page] = merged
      if (seq !== loadSeq.current) return
      setData((old: any) => ({ ...old, ...merged }))
    } catch (e: any) {
      if (e instanceof SupersededAuthRequestError) return
      if (seq !== loadSeq.current) return
      const message = localizeErrorMessage(e?.message || e)
      setData((old: any) => ({ ...old, load_errors: [`${tabMeta[page]?.label || page}: ${message}`] }))
      showToast(setToast, message)
    } finally {
      if (seq === loadSeq.current) {
        setLoading(false)
        setShowPortalLoader(false)
      }
    }
  }

  useEffect(() => {
    if (!token || !sessionUser || showPortalLoader) return
    const connection = (navigator as Navigator & { connection?: { saveData?: boolean; effectiveType?: string } }).connection
    if (connection?.saveData || connection?.effectiveType === 'slow-2g' || connection?.effectiveType === '2g') return

    const pages = preloadTabsByRole[sessionUser.role].filter(page => page !== tab && !pageCacheRef.current[page] && !preloadedTabsRef.current.has(page))
    if (!pages.length) return

    let cancelled = false
    const requestToken = token
    const warmCache = async () => {
      for (const page of pages) {
        if (cancelled || requestToken !== activeTokenRef.current) return
        preloadedTabsRef.current.add(page)
        try {
          const next = await requestPageData(page)
          if (cancelled || requestToken !== activeTokenRef.current) return
          pageCacheRef.current[page] = { ...next, load_errors: [] as string[] }
        } catch {
          // Foreground navigation will retry and surface errors when needed.
        }
      }
    }
    const start = () => { void warmCache() }
    const idleWindow = window as unknown as {
      requestIdleCallback?: (callback: () => void, options?: { timeout: number }) => number
      cancelIdleCallback?: (handle: number) => void
    }
    const idleHandle = idleWindow.requestIdleCallback
      ? idleWindow.requestIdleCallback(start, { timeout: 1500 })
      : window.setTimeout(start, 600)

    return () => {
      cancelled = true
      if (idleWindow.cancelIdleCallback && idleWindow.requestIdleCallback) idleWindow.cancelIdleCallback(idleHandle)
      else window.clearTimeout(idleHandle)
    }
  }, [token, sessionUser?.role, showPortalLoader, tab])

  useEffect(() => {
    if (!token) return
    const cached = pageCacheRef.current[tab]
    if (cached) {
      // Instant paint from cache, then silent revalidate.
      setData((old: any) => ({ ...old, ...cached, load_errors: [] }))
      setLoading(false)
      void load(tab, { background: true })
      return
    }
    void load(tab)
  }, [token, tab])
  useEffect(() => {
    // Keep document tokens in sync with React state (e.g. first paint / external restore).
    // Animated toggles already apply inside the View Transition callback.
    if (normalizeTheme(document.documentElement.dataset.theme) !== theme) {
      applyThemeToDocument(theme)
    }
  }, [theme])
  useEffect(() => {
    const onPopState = () => {
      const next = tabFromPath(window.location.pathname)
      if (next === tab) return
      const cached = pageCacheRef.current[next]
      if (cached) {
        setData((old: any) => ({ ...old, ...cached, load_errors: [] }))
        setLoading(false)
      } else {
        setData((old: any) => ({
          version: old.version,
          settings: old.settings,
          current_user: old.current_user,
          last_deployment: old.last_deployment,
          deployment_status: old.deployment_status,
        }))
        setLoading(true)
      }
      setTab(next)
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [tab])

  const toggleTheme = (event?: React.MouseEvent<HTMLElement> | null) => {
    toggleThemeWithTransition(event, next => setTheme(next))
  }
  const navigateTab = (next: string) => {
    if (next === tab) {
      setIsSidebarOpen(false)
      return
    }
    // Seed the visible data from cache before React commits the new tab, so the
    // entering MotionPage paints with content rather than an empty shell.
    const cached = pageCacheRef.current[next]
    if (cached) {
      setData((old: any) => ({ ...old, ...cached, load_errors: [] }))
      setLoading(false)
    } else {
      // Keep shared chrome (version/settings/current_user) but drop list payloads
      // so the entering page does not briefly render the previous tab's rows.
      setData((old: any) => ({
        version: old.version,
        settings: old.settings,
        current_user: old.current_user,
        last_deployment: old.last_deployment,
        deployment_status: old.deployment_status,
      }))
      setLoading(true)
    }
    setTab(next)
    const path = pathForTab(next)
    if (window.location.pathname !== path) window.history.pushState({ tab: next }, '', path)
    setIsSidebarOpen(false)
  }

  useEffect(() => {
    if (token && sessionUser && !tabAllowedForRole(tab, sessionUser.role)) {
      navigateTab(sessionUser.role === 'viewer' ? 'account' : 'dashboard')
    }
  }, [token, sessionUser?.role, tab])

  const handleLogout = async () => {
    const ok = await dialogs.confirm({
      title: '确认退出登录？',
      confirmText: '退出',
      cancelText: '取消',
      tone: 'danger'
    })
    if (ok) {
      try {
        await client.request('/auth/logout', { method: 'POST', body: '{}' })
      } catch (e: any) {
        showToast(setToast, `未能撤销服务端会话：${localizeErrorMessage(e?.message || e)}`, 'warning')
      }
      document.body.style.overflow = ""
      activeTokenRef.current = ''
      loadSeq.current++
      sessionStorage.removeItem('oboard.token')
      sessionStorage.removeItem('oboard.user')
      sessionStorage.removeItem('oboard.csrf')
      setToken('')
      setSessionUser(null)
      setData({})
      pageCacheRef.current = {}
      pageRequestRef.current = {}
      preloadedTabsRef.current.clear()
      setIsSidebarOpen(false)
    }
  }

  if (!token) return <Login theme={theme} toggleTheme={(e) => toggleTheme(e)} onToken={(v, user, csrfToken) => {
    activeTokenRef.current = v
    loadSeq.current++
    sessionStorage.setItem('oboard.token', v)
    sessionStorage.setItem('oboard.user', JSON.stringify(user))
    sessionStorage.setItem('oboard.csrf', csrfToken)
    setSessionUser(user)
    setData({})
    pageCacheRef.current = {}
    pageRequestRef.current = {}
    preloadedTabsRef.current.clear()
    setToast(null)
    setShowPortalLoader(true)
    setToken(v)
  }} />

  const rememberDeploymentStatus = (status: any) => {
    Object.keys(pageCacheRef.current).forEach(page => {
      pageCacheRef.current[page] = { ...pageCacheRef.current[page], deployment_status: status }
    })
  }

  const apply = async () => {
    try {
      const deploymentData = tab === 'proxy-paths'
        ? data
        : await client.request('/page-data?page=proxy-paths')
      const conflicts = deploymentConflicts(deploymentData)
      if (conflicts.length) {
        showToast(setToast, `下发已阻止：${conflicts.join('；')}`, 'warning')
        return
      }
      const confirmed = await dialogs.confirm({
        title: '确认下发配置',
        tone: 'danger',
        confirmText: '下发配置',
        message: <DeploymentSummary data={deploymentData} />,
      })
      if (!confirmed) return
      const deployment = await client.request('/deployments/apply', { method: 'POST', body: '{}' })
      const deploymentStatus = { config_version: deployment.config_version, summary: deployment.summary, failure_dismissed: false }
      rememberDeploymentStatus(deploymentStatus)
      setData((old: any) => ({ ...old, last_deployment: deployment, deployment_status: deploymentStatus }))
      await load()
      const version = deployment.config_version ? `版本 ${deployment.config_version}` : '配置'
      const summary = deployment.summary || {}
      const total = Number(summary.total || 0)
      showToast(setToast, total > 0 ? `${version} 已下发 · ${total} 个子任务，请在任务中心查看进度` : `${version} 已下发，请在任务中心查看进度`, 'success')
    } catch (e: any) {
      showToast(setToast, e.message)
    }
  }

  const tabTitles: { [key: string]: string } = {
    dashboard: '系统总览',
    servers: '服务器管理',
    'proxy-paths': '代理编排链路',
    users: '用户与分组管理',
    dns: 'DNS 设置',
    'dns-records': '域名解析',
    subscriptions: '节点订阅分发',
    notifications: '通知中心',
    tasks: '任务部署中心',
    audit: '审计日志时间线',
    settings: '面板系统设置',
    account: '我的账户',
  }

  const deploymentSummary = data.deployment_status?.summary || {}
  const deploymentTotal = Number(deploymentSummary.total || 0)
  const deploymentPending = Number(deploymentSummary.pending || 0) + Number(deploymentSummary.running || 0)
  const deploymentFailed = Number(deploymentSummary.failed || 0)
  const deploymentSucceeded = Number(deploymentSummary.succeeded || 0)
  const isDeploying = deploymentPending > 0
  const isSynced = deploymentTotal > 0 && deploymentSucceeded === deploymentTotal
  const deploymentHasFailure = !isDeploying && deploymentFailed > 0
  const deploymentStatusLabel = isDeploying
    ? '正在下发配置...'
    : isSynced
      ? '配置已同步'
      : deploymentHasFailure
        ? deploymentFailed === deploymentTotal ? '下发失败' : '部分下发失败'
        : '有待部署修改'
  const deploymentVersion = Number(data.deployment_status?.config_version || data.last_deployment?.config_version || 0)
  const deployStatusDismissable = deploymentHasFailure
  const showDeployStatus = !deployStatusDismissable || !Boolean(data.deployment_status?.failure_dismissed)
  const dashboardAttention = getDashboardAttention(data)
  const dashboardAttentionStorageKey = `oboard.dashboard-attention.${sessionUser?.id || data.current_user?.id || sessionUser?.username || data.current_user?.username || 'anonymous'}`
  const dismissedDashboardAttention = localStorage.getItem(dashboardAttentionStorageKey) || ''
  const showDashboardAttention = tab === 'dashboard' && dashboardAttention.parts.length > 0 && dismissedDashboardAttention !== dashboardAttention.fingerprint
  const dismissDashboardAttention = () => {
    if (!dashboardAttention.fingerprint) return
    localStorage.setItem(dashboardAttentionStorageKey, dashboardAttention.fingerprint)
    setAttentionDismissRevision(value => value + 1)
  }
  const dismissDeployStatus = async () => {
    if (!deployStatusDismissable || !deploymentVersion) return
    try {
      const result = await client.request(`/deployments/${deploymentVersion}/dismiss-failure`, { method: 'POST', body: '{}' })
      const status = result.deployment_status || { ...data.deployment_status, failure_dismissed: true }
      rememberDeploymentStatus(status)
      setData((old: any) => ({ ...old, deployment_status: status }))
    } catch (error: any) {
      showToast(setToast, localizeErrorMessage(error?.message || error), 'error')
    }
  }

  const current = tabMeta[tab] || { label: tab, desc: '', group: 'OBoard' }
  const currentRole = sessionUser?.role || 'viewer'
  const canOperate = roleRanks[currentRole] >= roleRanks.operator
  const visibleNavGroups = navGroups
    .map(group => ({ ...group, tabs: group.tabs.filter(item => tabAllowedForRole(item, currentRole)) }))
    .filter(group => group.tabs.length > 0)

  return (
    <DialogContext.Provider value={dialogs}>
      <LoadingContext.Provider value={loading}>
        <AnimatePresence>
          {showPortalLoader && (
            <motion.div
              key="portal-loader"
              className="portal-loader"
              initial={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.28, ease: [0.22, 1, 0.36, 1] }}
            >
              <div className="portal-loader-mark" aria-hidden="true">
                <span className="portal-loader-ring portal-loader-ring-outer" />
                <span className="portal-loader-ring portal-loader-ring-inner" />
                <span className="portal-loader-badge">O</span>
              </div>
              <div className="portal-loader-copy">
                <h2>OBoard 控制台</h2>
                <AnimatePresence mode="wait">
                  <motion.span
                    key={loading ? 'loading' : 'preparing'}
                    initial={{ opacity: 0, y: 4 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={{ opacity: 0, y: -4 }}
                    transition={{ duration: 0.18, ease: [0.22, 1, 0.36, 1] }}
                  >
                    {loading ? '正在加载当前页面...' : '正在准备控制台...'}
                  </motion.span>
                </AnimatePresence>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        <div className="app">
          <TopToast toast={toast} onClose={() => setToast(null)} />
          <DialogHost dialog={dialog} onClose={() => setDialog(null)} />
          {isMobile && (
            <div
              className={`sidebar-backdrop ${isSidebarOpen ? 'open' : ''}`}
              onClick={() => setIsSidebarOpen(false)}
              aria-hidden="true"
            />
          )}
          <aside
            id="sidebar"
            className={`sidebar ${isSidebarOpen ? 'open' : ''}`}
            role={isMobile ? 'dialog' : 'navigation'}
            aria-label="系统菜单"
            aria-modal={isMobile ? 'true' : undefined}
            aria-hidden={isMobile ? !isSidebarOpen : undefined}
          >
            <div className="brand">
              <img className="brand-mark" src={logo} alt="OBoard" width={34} height={34} />
              <div className="brand-text">
                <h1>OBoard</h1>
              </div>
              {isMobile && (
                <button
                  className="ghost icon-button sidebar-close"
                  onClick={() => setIsSidebarOpen(false)}
                  aria-label="关闭菜单"
                >
                  <X size={16} />
                </button>
              )}
            </div>
            <nav className="nav-list" aria-label="主导航">
              {visibleNavGroups.map(group => <div className="nav-section" key={group.label || group.tabs.join('-')}>
                {group.label && <p>{group.label}</p>}
                {group.tabs.map(x => <button
                  className={tab === x ? 'nav-item active' : 'nav-item'}
                  onClick={() => navigateTab(x)}
                  key={x}
                >
                  <span>{getTabIcon(x)}</span>
                  <span>{tabMeta[x]?.label || x}</span>
                </button>)}
              </div>)}
            </nav>
            <div className="sidebar-footer">
              <button className="sidebar-footer-btn" onClick={(e) => toggleTheme(e)} type="button" aria-label="切换主题">
                {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
                <span>{theme === 'dark' ? '浅色主题' : '深色主题'}</span>
              </button>
              <button className="sidebar-footer-btn danger" onClick={handleLogout} type="button">
                <LogOut size={16} />
                <span>退出登录</span>
              </button>
            </div>
          </aside>
          <main className={`main${tab === 'proxy-paths' ? ' proxy-page-main' : ''}`}>
            <header className={`topbar${tab === 'dashboard' ? ' topbar-quiet' : ''}`}>
              <div className="topbar-title-container">
                {isMobile && (
                  <button
                    className="ghost icon-button mobile-menu-toggle"
                    onClick={() => setIsSidebarOpen(true)}
                    aria-label="打开系统菜单"
                    aria-expanded={isSidebarOpen}
                    aria-controls="sidebar"
                  >
                    <svg viewBox="0 0 24 24"><line x1="3" y1="12" x2="21" y2="12" /><line x1="3" y1="6" x2="21" y2="6" /><line x1="3" y1="18" x2="21" y2="18" /></svg>
                  </button>
                )}
                <div className="topbar-title-stage" aria-live="polite">
                  <AnimatePresence initial={false} mode="sync">
                    <m.div
                      key={tab}
                      className="topbar-title-layer"
                      initial={{ opacity: 0 }}
                      animate={{ opacity: 1, transition: { duration: 0.2, ease: [0.22, 1, 0.36, 1] } }}
                      exit={{ opacity: 0, transition: { duration: 0.16, ease: 'easeIn' } }}
                    >
                      <p className="eyebrow">{current.group}</p>
                      <h1>{tabTitles[tab] || current.label}</h1>
                      <p>{current.desc}</p>
                    </m.div>
                  </AnimatePresence>
                </div>
              </div>
              <div className="topbar-actions">
                {showDashboardAttention && (
                  <DashboardAttentionNotice
                    parts={dashboardAttention.parts}
                    className="topbar-attention"
                    onDismiss={dismissDashboardAttention}
                  />
                )}
                {canOperate && <>
                {showDeployStatus && (
                  deployStatusDismissable ? (
                    <button
                      type="button"
                      className={`deploy-status-pill dismissable danger`}
                      onClick={() => void dismissDeployStatus()}
                      title="点击忽略"
                      aria-label={`${deploymentStatusLabel}，点击忽略`}
                    >
                      <Info size={16} />
                      <span>{deploymentStatusLabel}</span>
                    </button>
                  ) : (
                    <div className={`deploy-status-pill ${isDeploying ? 'info' : isSynced ? 'ok' : 'warn'}`}>
                      {isDeploying ? (
                        <>
                          <motion.div
                            animate={{ rotate: 360 }}
                            transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
                            style={{
                              width: '14px',
                              height: '14px',
                              borderWidth: '2px',
                              borderStyle: 'solid',
                              borderColor: 'currentColor',
                              borderTopColor: 'transparent',
                              borderRadius: '50%',
                              display: 'inline-block'
                            }}
                          />
                          <span>{deploymentStatusLabel}</span>
                        </>
                      ) : isSynced ? (
                        <>
                          <Check size={16} />
                          <span>{deploymentStatusLabel}</span>
                        </>
                      ) : (
                        <>
                          <Info size={16} />
                          <span>{deploymentStatusLabel}</span>
                        </>
                      )}
                    </div>
                  )
                )}

                <button className="topbar-apply" onClick={apply} disabled={isDeploying}>
                  <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/></svg>
                  <span>下发配置</span>
                </button>
                </>}

                <IconButton label={loading ? "正在刷新" : "刷新"} onClick={() => void load(tab)} className={`topbar-refresh${loading ? " refreshing" : ""}`} busy={loading}><RefreshIcon /></IconButton>
              </div>
            </header>
            <div className="page-stage">
              <AnimatePresence initial={false} mode="popLayout">
                <MotionPage key={tab}>
                  {renderTab(tab, data, client, load, apply, loading, (message, tone) => showToast(setToast, message, tone), sessionUser, showDashboardAttention ? dashboardAttention : null, dismissDashboardAttention)}
                </MotionPage>
              </AnimatePresence>
            </div>
          </main>
        </div>
      </LoadingContext.Provider>
    </DialogContext.Provider>
  )
}

function Login({ theme, toggleTheme, onToken }: { theme: string; toggleTheme: (event?: React.MouseEvent<HTMLElement>) => void; onToken: (token: string, user: SessionUser, csrfToken: string) => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [challengeToken, setChallengeToken] = useState('')
  const [loginStep, setLoginStep] = useState<'password' | 'totp'>('password')
  const [secondFactorPasskey, setSecondFactorPasskey] = useState(false)
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState('')
  const [isLoading, setIsLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setIsLoading(true)
    setError('')
    try {
      if (loginStep === 'totp') {
        const res = await api('').request<{ csrf_token: string; user: SessionUser }>('/auth/totp/verify', { method: 'POST', body: JSON.stringify({ challenge_token: challengeToken, code }) })
        onToken('cookie', res.user, res.csrf_token)
        return
      }
      const res = await api('').request<{ csrf_token?: string; user?: SessionUser; two_factor_required?: boolean; challenge_token?: string; passkey_available?: boolean }>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) })
      if (res.two_factor_required && res.challenge_token) {
        setChallengeToken(res.challenge_token)
        setSecondFactorPasskey(Boolean(res.passkey_available))
        setLoginStep('totp')
        setPassword('')
        setCode('')
        return
      }
      if (!res.user || !res.csrf_token) throw new Error('登录响应无效')
      onToken('cookie', res.user, res.csrf_token)
    } catch (e: any) {
      setError(localizeErrorMessage(e?.message || '用户名或密码错误'))
    } finally {
      setIsLoading(false)
    }
  }

  const loginWithPasskey = async () => {
    if (!username.trim() || isLoading) return
    setIsLoading(true)
    setError('')
    try {
      const begin = await api('').request<{ options: any; challenge_token: string }>('/auth/passkey/login/begin', { method: 'POST', body: JSON.stringify({ username: username.trim() }) })
      const credential = await getPasskeyCredential(begin.options)
      const result = await api('').request<{ csrf_token: string; user: SessionUser }>('/auth/passkey/login/finish', { method: 'POST', body: JSON.stringify({ challenge_token: begin.challenge_token, credential }) })
      onToken('cookie', result.user, result.csrf_token)
    } catch (e: any) {
      const name = String(e?.name || '')
      setError(name === 'NotAllowedError' ? '未完成通行密钥验证' : localizeErrorMessage(e?.message || e))
    } finally {
      setIsLoading(false)
    }
  }

  const backToPassword = () => {
    setLoginStep('password')
    setChallengeToken('')
    setSecondFactorPasskey(false)
    setCode('')
    setError('')
  }

  return (
    <div className="login-screen">
      <section className="login-hero" aria-label="产品介绍">
        <div className="login-hero-grid" />
        <div className="login-hero-topline">
          <span>OBOARD</span>
          <span className="login-hero-dot">·</span>
          <span>控制台</span>
        </div>

        <div className="login-hero-network">
          <svg className="login-hero-svg" viewBox="0 0 640 640" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
            <path d="M120 180 L320 280 L520 160" stroke="rgba(255,255,255,0.14)" strokeWidth="1.2" strokeDasharray="5 7" />
            <path d="M160 460 L320 280 L480 470" stroke="rgba(255,255,255,0.12)" strokeWidth="1.2" strokeDasharray="5 7" />
            <path d="M120 180 L160 460" stroke="rgba(255,255,255,0.1)" strokeWidth="1.2" strokeDasharray="5 7" />
            <path d="M520 160 L480 470" stroke="rgba(255,255,255,0.1)" strokeWidth="1.2" strokeDasharray="5 7" />
            <circle cx="120" cy="180" r="18" stroke="rgba(255,255,255,0.28)" strokeWidth="1.2" />
            <circle cx="120" cy="180" r="4" fill="rgba(255,255,255,0.85)" />
            <circle cx="320" cy="280" r="28" stroke="rgba(255,255,255,0.22)" strokeWidth="1.2" />
            <circle cx="320" cy="280" r="8" fill="rgba(255,255,255,0.9)" />
            <circle cx="520" cy="160" r="16" stroke="rgba(255,255,255,0.24)" strokeWidth="1.2" />
            <circle cx="520" cy="160" r="4" fill="rgba(255,255,255,0.8)" />
            <circle cx="160" cy="460" r="14" stroke="rgba(255,255,255,0.2)" strokeWidth="1.2" />
            <circle cx="160" cy="460" r="3.5" fill="rgba(255,255,255,0.75)" />
            <circle cx="480" cy="470" r="14" stroke="rgba(255,255,255,0.2)" strokeWidth="1.2" />
            <circle cx="480" cy="470" r="3.5" fill="rgba(255,255,255,0.75)" />
          </svg>

          <div className="login-float-card login-float-server">
            <div className="login-float-head">
              <span>服务器</span>
              <span className="login-online"><i />在线</span>
            </div>
            <strong>oboard-node-01</strong>
            <div className="login-meter"><span>CPU</span><div className="login-meter-bar"><i style={{ width: '42%' }} /></div><em>42%</em></div>
            <div className="login-meter"><span>内存</span><div className="login-meter-bar"><i style={{ width: '68%' }} /></div><em>68%</em></div>
            <div className="login-meter"><span>磁盘</span><div className="login-meter-bar"><i style={{ width: '21%' }} /></div><em>21%</em></div>
          </div>

          <div className="login-float-card login-float-regions">
            <div className="login-float-head"><span>节点</span><i className="login-pulse" /></div>
            <strong>12 个地区</strong>
            <div className="login-region-chips">
              {['HK', 'SG', 'TYO', 'NRT', 'LAX', 'FRA'].map(x => <span key={x}>{x}</span>)}
            </div>
          </div>

          <div className="login-float-card login-float-term">
            <div className="login-float-head">
              <span>终端</span>
              <span className="login-term-dots"><i /><i /><i /></span>
            </div>
            <pre>{`$ ssh root@oboard-node-01
$ uptime
已运行 42 天，负载 0.18
$ _`}</pre>
          </div>
        </div>

        <div className="login-hero-copy">
          <h1>OBOARD</h1>
          <p>高性能代理基础设施，一键编排、按需部署。</p>
          <div className="login-hero-meta">
            <span>账户访问</span>
          </div>
        </div>

        <div className="login-hero-footer">
          <span>© {new Date().getFullYear()} OBoard</span>
          <button type="button" className="login-ghost-link" onClick={(e) => toggleTheme(e)}>
            <Globe size={14} />
            <span>{theme === 'dark' ? '浅色' : '深色'}</span>
          </button>
        </div>
      </section>

      <section className="login-panel">
        <motion.div
          className="login-panel-card"
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.45, ease: [0.16, 1, 0.3, 1] }}
        >
          <div className="login-panel-kicker">{loginStep === 'totp' ? '双重认证' : '登录'}</div>
          <h2>{loginStep === 'totp' ? '确认是你本人' : '欢迎回来'}</h2>
          <p className="login-panel-desc">{loginStep === 'totp' ? '输入认证器中的六位验证码，也可以使用一枚恢复码。' : '请输入账号信息以访问控制台。'}</p>

          <form className="login-form-hyvps" onSubmit={handleSubmit}>
            {loginStep === 'password' ? <><label className="login-field">
              <span className="sr-only">用户名</span>
              <div className="login-input-wrap">
                <User size={16} className="login-input-leading" aria-hidden="true" />
                <input
                  value={username}
                  onChange={e => setUsername(e.target.value)}
                  placeholder="用户名"
                  autoComplete="username"
                  required
                  aria-label="用户名"
                />
              </div>
            </label>

            <label className="login-field">
              <span className="sr-only">密码</span>
              <div className="login-input-wrap">
                <Lock size={16} className="login-input-leading" aria-hidden="true" />
                <input
                  type={showPassword ? 'text' : 'password'}
                  value={password}
                  onChange={e => setPassword(e.target.value)}
                  placeholder="密码"
                  autoComplete="current-password"
                  required
                  aria-label="密码"
                />
                <button
                  type="button"
                  className="login-eye"
                  onClick={() => setShowPassword(v => !v)}
                  aria-label={showPassword ? '隐藏密码' : '显示密码'}
                >
                  {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </div>
            </label></> : <label className="login-field">
              <span className="sr-only">验证码或恢复码</span>
              <div className="login-input-wrap">
                <Smartphone size={16} className="login-input-leading" aria-hidden="true" />
                <input
                  value={code}
                  onChange={e => setCode(e.target.value)}
                  placeholder="六位验证码或恢复码"
                  autoComplete="one-time-code"
                  autoFocus
                  required
                  aria-label="验证码或恢复码"
                />
              </div>
            </label>}

            {error && <div className="login-error">{error}</div>}

            <button type="submit" className="login-submit" disabled={isLoading}>
              {isLoading ? '验证中…' : loginStep === 'totp' ? '验证并登录' : '登录'}
            </button>

            {passkeyAvailable() && (loginStep === 'password' || secondFactorPasskey) && <>
              <div className="login-divider"><span>或者</span></div>
              <button type="button" className="login-passkey" onClick={() => void loginWithPasskey()} disabled={isLoading || !username.trim()}><Fingerprint size={17} />使用通行密钥</button>
            </>}
            {loginStep === 'totp' && <button type="button" className="login-back" onClick={backToPassword} disabled={isLoading}>返回密码登录</button>}
          </form>

          {/* Shown when the left hero (and its theme control) is hidden on narrow screens. */}
          <button
            type="button"
            className="login-theme-inline"
            onClick={(e) => toggleTheme(e)}
            aria-label="切换主题"
          >
            {theme === 'dark' ? <Sun size={14} /> : <Moon size={14} />}
            <span>{theme === 'dark' ? '浅色主题' : '深色主题'}</span>
          </button>
        </motion.div>
      </section>
    </div>
  )
}

function renderTab(tab: string, data: any, client: ReturnType<typeof api>, load: () => Promise<void>, apply?: () => Promise<void>, loading?: boolean, notify?: (message: string, tone?: ToastKind) => void, sessionUser?: SessionUser | null, dashboardAttention?: DashboardAttention | null, dismissDashboardAttention?: () => void) {
  if (tab === 'account') return <AccountPage data={data} client={client} load={load} notify={notify} />
  if (tab === 'dashboard') return <Dashboard data={data} loading={loading} displayName={sessionUser?.nickname || data.current_user?.nickname || sessionUser?.username || data.current_user?.username || 'Admin'} attention={dashboardAttention} dismissAttention={dismissDashboardAttention} />
  if (tab === 'servers') return <Servers data={data} client={client} load={load} loading={loading} notify={notify} />
  if (tab === 'proxy-paths') return <ProxyPathsWorkspace data={data} client={client} load={load} apply={apply} loading={loading} />
  if (tab === 'inbounds') return <Inbounds data={data} client={client} load={load} />
  if (tab === 'outbounds') return <Outbounds data={data} client={client} load={load} />
  if (tab === 'routing') return <RoutingRules data={data} client={client} load={load} />
  if (tab === 'external-outbounds') return <ExternalOutbounds data={data} client={client} load={load} />
  if (tab === 'warp') return <WARPProfiles data={data} client={client} load={load} />
  if (tab === 'users') return <UserManagement data={data} client={client} load={load} />
  if (tab === 'dns') return <DNS data={data} client={client} load={load} notify={notify} />
  if (tab === 'dns-records') return <ManagedDNSSettings data={data} client={client} load={load} notify={notify} />
  if (tab === 'mtu') return <MTU data={data} client={client} load={load} notify={notify} />
  if (tab === 'port-forwards') return <PortForwards data={data} client={client} load={load} notify={notify} />
  if (tab === 'tunnels') return <Tunnels data={data} client={client} load={load} />
  if (tab === 'notifications') return <Notifications data={data} client={client} load={load} notify={notify} sessionUser={sessionUser} />
  if (tab === 'subscriptions') return <Subscriptions data={data} client={client} load={load} notify={notify} />
  if (tab === 'tasks') return <Tasks data={data} client={client} loading={loading} />
  if (tab === 'audit') return <AuditConsole data={data} client={client} loading={loading} />
  if (tab === 'settings') return <SettingsPage data={data} client={client} load={load} notify={notify} />
  return null
}

function AccountPage({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
  const user: User | undefined = data.account_user || data.current_user
  const sshUserKeys: SSHUserKey[] = data.ssh_user_keys || []
  const sshAccesses: SSHAccess[] = data.ssh_accesses || []
  const [nickname, setNickname] = useState(user?.nickname || '')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [format, setFormat] = useState<SubscriptionFormat>('sing-box')
  const [iconsReady, setIconsReady] = useState(false)
  const [ageEnabled, setAgeEnabled] = useState(Boolean(user?.subscription_age_enabled))
  const [agePublicKey, setAgePublicKey] = useState(user?.subscription_age_public_key || '')
  const [authentication, setAuthentication] = useState<AuthenticationStatus>({
    totp_enabled: Boolean(user?.totp_enabled),
    recovery_codes_remaining: 0,
    passkeys: data.passkeys || [],
    passkey_supported: passkeyAvailable(),
  })
  const [totpSetup, setTOTPSetup] = useState<{ secret: string; qr_data_url: string } | null>(null)
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null)
  const [securityWorking, setSecurityWorking] = useState('')

  useEffect(() => setNickname(user?.nickname || ''), [user?.nickname])
  useEffect(() => {
    setAgeEnabled(Boolean(user?.subscription_age_enabled))
    setAgePublicKey(user?.subscription_age_public_key || '')
  }, [user?.subscription_age_enabled, user?.subscription_age_public_key])
  useEffect(() => {
    let active = true
    preloadSubscriptionClientIcons().then(() => { if (active) setIconsReady(true) })
    return () => { active = false }
  }, [])

  const refreshAuthentication = async () => {
    const result = await client.request('/me/authentication') as AuthenticationStatus
    setAuthentication(result)
  }

  useEffect(() => {
    let active = true
    client.request('/me/authentication').then((result: AuthenticationStatus) => { if (active) setAuthentication(result) }).catch(() => undefined)
    return () => { active = false }
  }, [user?.id])

  const formats: Array<{ id: SubscriptionFormat; name: string }> = [
    { id: 'sing-box', name: 'sing-box' },
    { id: 'clash-meta', name: 'Clash.Meta' },
    { id: 'mihomo', name: 'Mihomo' },
    { id: 'stash', name: 'Stash' },
    { id: 'shadowrocket', name: 'Shadowrocket' },
    { id: 'qx', name: 'Quantumult X' },
    { id: 'loon', name: 'Loon' },
    { id: 'surge', name: 'Surge' },
  ]

  const agePolicy = user?.subscription_age_policy || 'optional'
  const ageRequired = agePolicy === 'required'
  const ageCapable = isAgeSubscriptionFormat(format)
  const ageReady = Boolean(user?.subscription_age_public_key) && (ageRequired || Boolean(user?.subscription_age_enabled))

  const copySubscription = async (encrypted = false) => {
    if (!user) return
    const useAge = ageCapable && (encrypted || ageRequired)
    if (useAge && !ageReady) {
      notify?.('请先保存有效的 Age 公钥', 'warning')
      return
    }
    const ok = await copyText(subscriptionURLForUser(user, format, useAge))
    notify?.(ok ? useAge ? 'Age 加密订阅链接已复制' : '普通订阅链接已复制' : '复制失败，请手动复制', ok ? 'success' : 'error')
  }

  const saveProfile = async () => {
    await client.request('/me', { method: 'PATCH', body: JSON.stringify({ nickname }) })
    await load()
    notify?.('个人信息已更新', 'success')
  }

  const saveAge = async () => {
    if ((ageEnabled || ageRequired) && !agePublicKey.trim()) {
      notify?.('请填写 Age 公钥；私钥只保留在客户端', 'warning')
      return
    }
    await client.request('/me/subscription-age', { method: 'PATCH', body: JSON.stringify({ enabled: ageRequired || ageEnabled, public_key: agePublicKey.trim() }) })
    await load()
    notify?.('订阅加密设置已保存', 'success')
  }

  const savePassword = async () => {
    if (newPassword.length < 10) {
      notify?.('新密码至少需要 10 个字符', 'warning')
      return
    }
    await client.request('/auth/password', { method: 'POST', body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }) })
    setCurrentPassword('')
    setNewPassword('')
    notify?.('密码已修改', 'success')
  }

  const beginTOTPSetup = async () => {
    const currentPassword = await dialogs.prompt({ title: '开启双重认证', message: '先验证当前登录密码，再绑定认证器。', placeholder: '当前密码', inputType: 'password', confirmText: '继续' })
    if (currentPassword === null) return
    setSecurityWorking('totp-setup')
    try {
      const result = await client.request('/me/totp/setup/begin', { method: 'POST', body: JSON.stringify({ current_password: currentPassword }) }) as { secret: string; qr_data_url: string }
      setTOTPSetup(result)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const completeTOTPSetup = async (result: { recovery_codes: string[]; csrf_token?: string }) => {
    if (result.csrf_token) sessionStorage.setItem('oboard.csrf', result.csrf_token)
    setTOTPSetup(null)
    setRecoveryCodes(result.recovery_codes)
    await refreshAuthentication()
    await load()
    notify?.('双重认证已开启', 'success')
  }

  const disableTOTP = async () => {
    const confirmed = await dialogs.confirm({ title: '停用双重认证？', message: '停用后，账号只需要密码或通行密钥即可登录。', confirmText: '继续停用', tone: 'danger' })
    if (!confirmed) return
    const currentPassword = await dialogs.prompt({ title: '验证登录密码', placeholder: '当前密码', inputType: 'password', confirmText: '下一步' })
    if (currentPassword === null) return
    const code = await dialogs.prompt({ title: '验证认证器', message: '输入六位验证码，也可以使用一枚恢复码。', placeholder: '验证码或恢复码', inputType: 'text', confirmText: '停用' })
    if (code === null) return
    setSecurityWorking('totp-disable')
    try {
      const result = await client.request('/me/totp/disable', { method: 'POST', body: JSON.stringify({ current_password: currentPassword, code }) }) as { csrf_token?: string }
      if (result.csrf_token) sessionStorage.setItem('oboard.csrf', result.csrf_token)
      await refreshAuthentication()
      await load()
      notify?.('双重认证已停用', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const regenerateRecoveryCodes = async () => {
    const currentPassword = await dialogs.prompt({ title: '生成新的恢复码', message: '生成后，之前的恢复码会立即失效。', placeholder: '当前密码', inputType: 'password', confirmText: '下一步' })
    if (currentPassword === null) return
    const code = await dialogs.prompt({ title: '验证认证器', placeholder: '六位验证码或恢复码', inputType: 'text', confirmText: '生成' })
    if (code === null) return
    setSecurityWorking('totp-recovery')
    try {
      const result = await client.request('/me/totp/recovery-codes', { method: 'POST', body: JSON.stringify({ current_password: currentPassword, code }) }) as { recovery_codes: string[] }
      setRecoveryCodes(result.recovery_codes)
      await refreshAuthentication()
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const addPasskey = async () => {
    const name = await dialogs.prompt({ title: '添加通行密钥', message: '名称用于区分这台设备或密码管理器。', defaultValue: '我的通行密钥', placeholder: '例如：MacBook', confirmText: '下一步' })
    if (name === null) return
    const currentPassword = await dialogs.prompt({ title: '验证登录密码', placeholder: '当前密码', inputType: 'password', confirmText: '添加' })
    if (currentPassword === null) return
    const code = authentication.totp_enabled ? await dialogs.prompt({ title: '验证认证器', message: '输入六位验证码，也可以使用一枚恢复码。', placeholder: '验证码或恢复码', confirmText: '添加' }) : ''
    if (code === null) return
    setSecurityWorking('passkey-add')
    try {
      const begin = await client.request('/me/passkeys/register/begin', { method: 'POST', body: JSON.stringify({ name, current_password: currentPassword, code }) }) as { options: any; challenge_token: string }
      const credential = await createPasskeyCredential(begin.options)
      await client.request('/me/passkeys/register/finish', { method: 'POST', body: JSON.stringify({ challenge_token: begin.challenge_token, credential }) })
      await refreshAuthentication()
      await load()
      notify?.('通行密钥已添加', 'success')
    } catch (error: any) {
      const message = String(error?.name || '') === 'NotAllowedError' ? '未完成通行密钥创建' : localizeErrorMessage(error?.message || error)
      notify?.(message, 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const removePasskey = async (passkey: PasskeyCredential) => {
    const confirmed = await dialogs.confirm({ title: '移除通行密钥？', message: `将移除“${passkey.name}”，这台设备之后不能再用它登录。`, confirmText: '移除', tone: 'danger' })
    if (!confirmed) return
    const currentPassword = await dialogs.prompt({ title: '验证登录密码', placeholder: '当前密码', inputType: 'password', confirmText: '移除' })
    if (currentPassword === null) return
    const code = authentication.totp_enabled ? await dialogs.prompt({ title: '验证认证器', message: '输入六位验证码，也可以使用一枚恢复码。', placeholder: '验证码或恢复码', confirmText: '移除' }) : ''
    if (code === null) return
    setSecurityWorking(`passkey-${passkey.id}`)
    try {
      await client.request(`/me/passkeys/${passkey.id}`, { method: 'DELETE', body: JSON.stringify({ current_password: currentPassword, code }) })
      await refreshAuthentication()
      await load()
      notify?.('通行密钥已移除', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const addSSHUserKey = async () => {
    const publicKey = await dialogs.prompt({ title: '添加 SSH 公钥', message: '只粘贴公钥（ssh-ed25519、ssh-rsa 等），私钥只保留在你的设备上。', placeholder: 'ssh-ed25519 AAAA...' })
    if (publicKey === null || !publicKey.trim()) return
    const name = await dialogs.prompt({ title: '密钥名称', message: '用于区分你的设备。', defaultValue: '默认密钥', placeholder: '例如：MacBook' })
    if (name === null) return
    try {
      await client.request('/me/ssh-user-keys', { method: 'POST', body: JSON.stringify({ name: name.trim() || '默认密钥', public_key: publicKey.trim(), enabled: true }) })
      await load()
      notify?.('SSH 公钥已添加', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    }
  }

  const deleteSSHUserKey = async (key: SSHUserKey) => {
    const confirmed = await dialogs.confirm({ title: '移除 SSH 公钥？', message: `将移除“${key.name}”。使用此密钥的 SSH 受限代理连接会被拒绝。`, confirmText: '移除', tone: 'danger' })
    if (!confirmed) return
    try {
      await client.request(`/me/ssh-user-keys/${key.id}`, { method: 'DELETE' })
      await load()
      notify?.('SSH 公钥已移除', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    }
  }

  const copySSHCommand = async (access: SSHAccess) => {
    const host = access.address.includes(':') && !access.address.startsWith('[') ? `[${access.address}]` : access.address
    const command = `ssh -N -D 127.0.0.1:1080 -p ${access.port} ${access.username}@${host}`
    const ok = await copyText(command)
    notify?.(ok ? 'SSH 动态代理命令已复制' : '复制失败，请手动复制', ok ? 'success' : 'error')
  }

  return <><Panel title="我的账户" className="account-panel">
    <div className="account-layout">
      <section className="sub-section account-subscription-section">
        <div className="sub-section-head"><div><h3><LinkIcon size={16} />我的订阅</h3><p className="muted">选择客户端格式后复制自己的订阅链接。</p></div></div>
        {!iconsReady ? <div className="subscription-page-loading compact"><RefreshCw /><span>正在加载客户端图标</span></div> : <div className="sub-format-grid account-format-grid">
          {formats.map(item => <button key={item.id} type="button" className={`sub-format-card ${format === item.id ? 'active' : ''}`} onClick={() => setFormat(item.id)}>
            <div className="subscription-client-icon-shell">{renderFormatIcon(item.id)}</div><strong>{item.name}</strong>
          </button>)}
        </div>}
        <div className="account-subscription-action">
          <span>当前格式：{formats.find(item => item.id === format)?.name}</span>
          <div className="account-subscription-buttons">
            {(!ageCapable || !ageRequired) && <button onClick={() => void copySubscription(false)} disabled={!user?.subscription_token}><Copy size={15} />复制普通订阅</button>}
            {ageCapable && <button onClick={() => void copySubscription(true)} disabled={!user?.subscription_token || !ageReady}><Shield size={15} />复制 Age 订阅</button>}
          </div>
        </div>
      </section>

      <div className="account-settings-grid">
        <section className="sub-section account-login-security">
          <div className="sub-section-head"><div><h3><ShieldCheck size={16} />登录安全</h3><p className="muted">管理双重认证、恢复码和通行密钥。</p></div></div>
          <div className="account-security-list">
            <div className="account-security-item">
              <div className="account-security-icon"><Smartphone size={19} /></div>
              <div className="account-security-copy"><strong>认证器验证码</strong><span>{authentication.totp_enabled ? `已开启 · 剩余 ${authentication.recovery_codes_remaining} 枚恢复码` : '未开启'}</span></div>
              <div className="account-security-actions">
                {authentication.totp_enabled ? <><button type="button" className="ghost" onClick={() => void regenerateRecoveryCodes()} disabled={Boolean(securityWorking)}>生成新恢复码</button><button type="button" className="ghost danger-text" onClick={() => void disableTOTP()} disabled={Boolean(securityWorking)}>停用</button></> : <button type="button" onClick={() => void beginTOTPSetup()} disabled={Boolean(securityWorking)}>{securityWorking === 'totp-setup' ? '准备中…' : '开启'}</button>}
              </div>
            </div>
            <div className="account-security-item passkeys">
              <div className="account-security-icon"><Fingerprint size={19} /></div>
              <div className="account-security-copy"><strong>通行密钥</strong><span>{authentication.passkeys.length ? `已添加 ${authentication.passkeys.length} 个` : '使用设备解锁直接登录'}</span></div>
              <div className="account-security-actions"><button type="button" className="ghost" onClick={() => void addPasskey()} disabled={Boolean(securityWorking) || !authentication.passkey_supported || !passkeyAvailable()}><Plus size={15} />添加</button></div>
              {!authentication.passkey_supported || !passkeyAvailable() ? <p className="account-security-note">通行密钥需要通过 HTTPS 访问面板。</p> : null}
              {authentication.passkeys.length > 0 && <div className="account-passkey-list">{authentication.passkeys.map(passkey => <div key={passkey.id}><span><strong>{passkey.name}</strong><small>{passkey.last_used_at ? `最近使用 ${formatDate(passkey.last_used_at)}` : `添加于 ${formatDate(passkey.created_at)}`}</small></span><button type="button" className="ghost danger-text" onClick={() => void removePasskey(passkey)} disabled={Boolean(securityWorking)}>移除</button></div>)}</div>}
            </div>
          </div>
        </section>
        <section className="sub-section account-age-section">
          <div className="sub-section-head"><div><h3><Shield size={16} />订阅加密</h3><p className="muted">只填写客户端生成的公钥，私钥不要上传到面板。</p></div><span className={`sub-pill ${ageRequired ? 'warn' : ageReady ? 'ok' : ''}`}>{ageRequired ? '管理员强制' : ageReady ? '已开启' : '未开启'}</span></div>
          <div className="form account-form">
            <label className="subscription-burn-toggle">
              <input type="checkbox" checked={ageRequired || ageEnabled} disabled={ageRequired} onChange={event => setAgeEnabled(event.target.checked)} />
              <span className="setting-switch" aria-hidden="true"><span /></span>
              <span>{ageRequired ? '必须使用 Age 加密' : '为 Mihomo 开启 Age 加密'}</span>
            </label>
            <FormField label="Age 公钥" hint="只填写公钥，私钥留在客户端。">
              <textarea value={agePublicKey} onChange={event => setAgePublicKey(event.target.value)} rows={4} spellCheck={false} placeholder="age1..." />
            </FormField>
            <button onClick={saveAge}>保存订阅加密</button>
          </div>
        </section>
        <section className="sub-section">
          <div className="sub-section-head"><div><h3><User size={16} />个人信息</h3><p className="muted">登录用户名不可自行修改，昵称可随时更新。</p></div></div>
          <div className="form account-form">
            <FormField label="登录用户名"><input value={user?.username || ''} disabled /></FormField>
            <FormField label="昵称"><input value={nickname} onChange={event => setNickname(event.target.value)} maxLength={40} placeholder="设置一个昵称" /></FormField>
            <button onClick={saveProfile}>保存个人信息</button>
          </div>
        </section>
        <section className="sub-section">
          <div className="sub-section-head"><div><h3><Lock size={16} />修改密码</h3><p className="muted">修改后下次登录使用新密码。</p></div></div>
          <div className="form account-form">
            <FormField label="当前密码"><input type="password" autoComplete="current-password" value={currentPassword} onChange={event => setCurrentPassword(event.target.value)} /></FormField>
            <FormField label="新密码" hint="至少 10 个字符"><input type="password" autoComplete="new-password" value={newPassword} onChange={event => setNewPassword(event.target.value)} /></FormField>
            <button onClick={savePassword} disabled={!currentPassword || !newPassword}>修改密码</button>
          </div>
        </section>
        <section className="sub-section">
          <div className="sub-section-head"><div><h3><Lock size={16} />SSH 公钥</h3><p className="muted">用于 SSH 受限代理。只支持公钥认证；私钥始终保留在你的设备上。</p></div><button type="button" className="ghost" onClick={() => void addSSHUserKey()}>添加公钥</button></div>
          <div className="sub-user-actions">
            {sshUserKeys.length ? sshUserKeys.map(key => <div className="sub-user-actions" key={key.id}><span className={`sub-pill ${key.enabled !== false ? 'ok' : 'warn'}`} title={key.fingerprint}>{key.name} · {key.fingerprint}</span><button type="button" className="ghost danger-text" onClick={() => void deleteSSHUserKey(key)}>移除</button></div>) : <span className="muted">尚未添加 SSH 公钥</span>}
          </div>
          {sshAccesses.length > 0 && <div className="sub-user-actions"><span className="muted">已授权入口</span>{sshAccesses.map(access => <button type="button" className="ghost" key={access.inbound_id} disabled={!sshUserKeys.some(key => key.enabled !== false)} onClick={() => void copySSHCommand(access)}>复制 {access.name} 命令</button>)}</div>}
        </section>
      </div>
    </div>
  </Panel>
  <AnimatePresence>{totpSetup && <TOTPSetupDialog setup={totpSetup} client={client} onCancel={() => setTOTPSetup(null)} onComplete={completeTOTPSetup} />}</AnimatePresence>
  <AnimatePresence>{recoveryCodes && <RecoveryCodesDialog codes={recoveryCodes} onClose={() => setRecoveryCodes(null)} />}</AnimatePresence>
  </>
}

function TOTPSetupDialog({ setup, client, onCancel, onComplete }: { setup: { secret: string; qr_data_url: string }; client: ReturnType<typeof api>; onCancel: () => void; onComplete: (result: { recovery_codes: string[]; csrf_token?: string }) => Promise<void> }) {
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [working, setWorking] = useState(false)
  const confirm = async () => {
    if (!/^\d{6}$/.test(code.trim()) || working) {
      setError('请输入认证器显示的六位验证码')
      return
    }
    setWorking(true)
    setError('')
    try {
      const result = await client.request('/me/totp/setup/confirm', { method: 'POST', body: JSON.stringify({ code: code.trim() }) }) as { recovery_codes: string[]; csrf_token?: string }
      await onComplete(result)
    } catch (requestError: any) {
      setError(localizeErrorMessage(requestError?.message || requestError))
    } finally {
      setWorking(false)
    }
  }
  return <MotionDialogPanel onCancel={onCancel} className="totp-setup-dialog">
    <header className="dialog-head"><div><h2>绑定认证器</h2><p className="muted">使用任意支持六位动态验证码的认证器扫描二维码。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body totp-setup-body">
      <div className="totp-qr"><img src={setup.qr_data_url} alt="OBoard 双重认证二维码" /></div>
      <div className="totp-manual"><span>无法扫描时，手动输入密钥</span><CopyBlock value={setup.secret} /></div>
      <FormField label="六位验证码" hint="输入当前验证码以完成开启。"><input value={code} onChange={event => setCode(event.target.value.replace(/\D/g, '').slice(0, 6))} inputMode="numeric" autoComplete="one-time-code" placeholder="000000" autoFocus /></FormField>
      {error && <div className="login-error">{error}</div>}
    </div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={onCancel} disabled={working}>取消</button><button type="button" onClick={() => void confirm()} disabled={working || code.length !== 6}>{working ? '验证中…' : '验证并开启'}</button></footer>
  </MotionDialogPanel>
}

function RecoveryCodesDialog({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const copyAll = async () => {
    const ok = await copyText(codes.join('\n'))
    setCopied(ok)
  }
  return <MotionDialogPanel onCancel={onClose} className="recovery-codes-dialog">
    <header className="dialog-head"><div><h2>保存恢复码</h2><p className="muted">每枚恢复码只能使用一次，关闭后不会再次显示。</p></div></header>
    <div className="dialog-body recovery-codes-body">
      <div className="controller-update-install-notice"><strong>请存放在安全的位置</strong><span>手机无法使用时，可以用其中一枚恢复码完成登录。</span></div>
      <div className="recovery-code-grid">{codes.map(code => <code key={code}>{code}</code>)}</div>
    </div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={() => void copyAll()}><Copy size={15} />{copied ? '已复制' : '复制全部'}</button><button type="button" onClick={onClose}>我已保存</button></footer>
  </MotionDialogPanel>
}


function SettingsPage({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
  const [activeSection, setActiveSection] = useState<'connection' | 'servers' | 'certificates' | 'subscriptions' | 'traffic' | 'backups' | 'updates' | 'logs'>('connection')
  const currentOrigin = appControllerURL()
  const savedURL = data.settings?.controller_url || ''
  const currentBasePath = String(data.settings?.base_path || '')
  const migration = data.settings?.base_path_migration || { active: false, current_path: currentBasePath, agents: [] }
  const [controllerURL, setControllerURL] = useState(savedURL || currentOrigin)
  const [basePath, setBasePath] = useState(currentBasePath)
  const [subscriptionAgePolicy, setSubscriptionAgePolicy] = useState<'optional' | 'required'>(data.settings?.subscription_age_policy === 'required' ? 'required' : 'optional')
  const [trafficTimezone, setTrafficTimezone] = useState(data.settings?.traffic_timezone || 'Asia/Shanghai')
  const [trafficMode, setTrafficMode] = useState(data.settings?.traffic_enforcement_mode || 'disconnect_and_reject')
  const [controllerLogMaxMB, setControllerLogMaxMB] = useState(Number(data.settings?.controller_log_max_mb || 32))
  const [controllerLogBackups, setControllerLogBackups] = useState(Number(data.settings?.controller_log_backups || 5))
  const [serverDefaultMTUMode, setServerDefaultMTUMode] = useState(String(data.settings?.server_default_mtu_mode || 'detect'))
  const [serverDefaultBBREnabled, setServerDefaultBBREnabled] = useState(String(data.settings?.server_default_bbr_enabled || 'false') === 'true')
  const [saving, setSaving] = useState('')
  useEffect(() => { setControllerURL(savedURL || currentOrigin) }, [savedURL, currentOrigin])
  useEffect(() => { setBasePath(currentBasePath) }, [currentBasePath])
  useEffect(() => {
    if (!migration.active) return
    const timer = window.setInterval(() => { void load('settings', { background: true }) }, 3000)
    return () => window.clearInterval(timer)
  }, [migration.active, migration.config_version])
  useEffect(() => { setSubscriptionAgePolicy(data.settings?.subscription_age_policy === 'required' ? 'required' : 'optional') }, [data.settings?.subscription_age_policy])
  useEffect(() => { setTrafficTimezone(data.settings?.traffic_timezone || 'Asia/Shanghai'); setTrafficMode(data.settings?.traffic_enforcement_mode || 'disconnect_and_reject') }, [data.settings?.traffic_timezone, data.settings?.traffic_enforcement_mode])
  useEffect(() => {
    setControllerLogMaxMB(Number(data.settings?.controller_log_max_mb || 32))
    setControllerLogBackups(Number(data.settings?.controller_log_backups || 5))
  }, [data.settings?.controller_log_max_mb, data.settings?.controller_log_backups])
  useEffect(() => {
    setServerDefaultMTUMode(String(data.settings?.server_default_mtu_mode || 'detect'))
    setServerDefaultBBREnabled(String(data.settings?.server_default_bbr_enabled || 'false') === 'true')
  }, [data.settings?.server_default_mtu_mode, data.settings?.server_default_bbr_enabled])
  const runSave = async (key: string, action: () => Promise<void>, success: string) => {
    if (saving) return
    setSaving(key)
    try {
      await action()
      await load()
      notify?.(success, 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSaving('')
    }
  }
  const save = async () => {
    await runSave('controller', async () => {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ controller_url: controllerURL.trim() }) })
    }, '节点连接地址已保存')
  }
  const useCurrent = () => setControllerURL(currentOrigin)
  const resetAuto = async () => {
    await runSave('controller', async () => {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ controller_url: '' }) })
      setControllerURL(currentOrigin)
    }, '已恢复自动使用当前面板地址')
  }
  const normalizedBasePathDraft = (() => {
    const value = basePath.trim().replace(/\/+$/, '')
    return value === '/' ? '' : value
  })()
  const startBasePathMigration = async () => {
    if (saving || migration.active || normalizedBasePathDraft === currentBasePath) return
    const confirmed = await dialogs.confirm({
      title: '修改面板路径？',
      message: `路径将从 ${currentBasePath || '/'} 迁移到 ${normalizedBasePathDraft || '/'}。`,
      confirmText: '开始迁移',
    })
    if (!confirmed) return
    setSaving('base-path')
    try {
      const result = await client.request('/settings', { method: 'POST', body: JSON.stringify({ base_path: normalizedBasePathDraft }) }) as { redirect_path?: string }
      notify?.('面板路径迁移已开始', 'success')
      window.location.assign(result.redirect_path || `${normalizedBasePathDraft}/settings` || '/settings')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
      setSaving('')
    }
  }
  const retryBasePathMigration = async () => {
    if (saving || !migration.active) return
    setSaving('base-path-retry')
    try {
      await client.request('/settings/base-path/retry', { method: 'POST', body: '{}' })
      await load('settings', { background: true })
      notify?.('失败的 Agent 已重新加入迁移队列', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSaving('')
    }
  }
  const migrationStatusLabel = (status: string) => ({
    pending: '等待中', running: '更新中', succeeded: '已更新', failed: '失败', removed: '已移除',
  } as Record<string, string>)[status] || status
  const saveTraffic = async () => {
    await runSave('traffic', async () => {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ traffic_timezone: trafficTimezone.trim() || 'Asia/Shanghai', traffic_enforcement_mode: trafficMode }) })
    }, '流量控制设置已保存')
  }
  const saveSubscriptionSecurity = async () => {
    await runSave('subscription-security', async () => {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ subscription_age_policy: subscriptionAgePolicy }) })
    }, '订阅加密策略已保存')
  }
  const saveControllerLogs = async () => {
    await runSave('controller-logs', async () => {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ controller_log_max_mb: controllerLogMaxMB, controller_log_backups: controllerLogBackups }) })
    }, '主控日志保留策略已保存')
  }
  const saveServerDefaults = async () => {
    await runSave('server-defaults', async () => {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ server_default_mtu_mode: serverDefaultMTUMode, server_default_bbr_enabled: serverDefaultBBREnabled }) })
    }, '新服务器默认设置已保存')
  }
  return <section className="settings-shell">
    <nav className="settings-tabs" role="tablist" aria-label="设置分类">
      <button className={activeSection === 'connection' ? 'active' : ''} role="tab" aria-selected={activeSection === 'connection'} onClick={() => setActiveSection('connection')}><LinkIcon size={15} />基础设置</button>
      <button className={activeSection === 'servers' ? 'active' : ''} role="tab" aria-selected={activeSection === 'servers'} onClick={() => setActiveSection('servers')}><ServerIcon size={15} />服务器默认值</button>
      <button className={activeSection === 'certificates' ? 'active' : ''} role="tab" aria-selected={activeSection === 'certificates'} onClick={() => setActiveSection('certificates')}><Lock size={15} />证书</button>
      <button className={activeSection === 'subscriptions' ? 'active' : ''} role="tab" aria-selected={activeSection === 'subscriptions'} onClick={() => setActiveSection('subscriptions')}><Shield size={15} />订阅安全</button>
      <button className={activeSection === 'traffic' ? 'active' : ''} role="tab" aria-selected={activeSection === 'traffic'} onClick={() => setActiveSection('traffic')}><Gauge size={15} />流量控制</button>
      <button className={activeSection === 'backups' ? 'active' : ''} role="tab" aria-selected={activeSection === 'backups'} onClick={() => setActiveSection('backups')}><Database size={15} />数据备份</button>
      <button className={activeSection === 'updates' ? 'active' : ''} role="tab" aria-selected={activeSection === 'updates'} onClick={() => setActiveSection('updates')}><Download size={15} />主控更新</button>
      <button className={activeSection === 'logs' ? 'active' : ''} role="tab" aria-selected={activeSection === 'logs'} onClick={() => setActiveSection('logs')}><FileText size={15} />运行日志</button>
    </nav>
    <div className="settings-grid">
      {activeSection === 'connection' && <section className="settings-card">
        <div className="settings-card-head"><div><h3>节点连接地址</h3><p className="muted">填写服务器访问地址</p></div></div>
        <div className="form settings-form single-field">
          <FormField label="面板访问地址" hint="Agent 连接面板的地址，留空自动使用当前地址。">
            <input value={controllerURL} onChange={e => setControllerURL(e.target.value)} placeholder={currentOrigin} disabled={migration.active} />
          </FormField>
          <div className="settings-actions">
            <button onClick={save} disabled={Boolean(saving) || migration.active}>{saving === 'controller' ? '保存中...' : '保存'}</button>
            <button className="ghost" onClick={useCurrent} disabled={Boolean(saving) || migration.active}>使用当前地址</button>
            <button className="ghost" onClick={resetAuto} disabled={Boolean(saving) || migration.active}>恢复自动</button>
          </div>
        </div>
        <div className="base-path-settings">
          <div className="base-path-settings-head">
            <div><h3>面板路径</h3><p className="muted">当前路径：{currentBasePath || '/'}</p></div>
            <span className={`status-pill ${migration.active ? 'warning' : 'ok'}`}>{migration.active ? '迁移中' : '已生效'}</span>
          </div>
          <div className="form settings-form single-field">
            <FormField label="路径前缀" hint="以 / 开头；留空表示根路径。">
              <input value={basePath} onChange={event => setBasePath(event.target.value)} placeholder="/private-panel" disabled={migration.active} />
            </FormField>
            <div className="settings-actions">
              <button onClick={startBasePathMigration} disabled={Boolean(saving) || migration.active || normalizedBasePathDraft === currentBasePath}>
                <ArrowLeftRight size={14} />{saving === 'base-path' ? '迁移中...' : '修改路径'}
              </button>
            </div>
          </div>
          {migration.active && <div className="base-path-migration" aria-live="polite">
            <div className="base-path-route-row">
              <span><small>旧路径</small><code>{migration.previous_path || '/'}</code></span>
              <ArrowLeftRight size={16} />
              <span><small>新路径</small><code>{migration.current_path || '/'}</code></span>
            </div>
            <div className="base-path-progress-head">
              <strong>{Number(migration.percentage || 0)}%</strong>
              <span>{Number(migration.succeeded || 0)} / {Number(migration.total || 0)} Agent</span>
            </div>
            <div className="base-path-progress-track" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Number(migration.percentage || 0)}>
              <span style={{ width: `${Math.max(0, Math.min(100, Number(migration.percentage || 0)))}%` }} />
            </div>
            <div className="base-path-progress-stats">
              <span><strong>{Number(migration.pending || 0)}</strong>等待</span>
              <span><strong>{Number(migration.running || 0)}</strong>更新中</span>
              <span><strong>{Number(migration.failed || 0)}</strong>失败</span>
            </div>
            <div className="base-path-agent-list">
              {(migration.agents || []).map((agent: any) => <div className="base-path-agent-row" key={agent.server_id}>
                <div><strong>{agent.server_name || `Agent ${agent.server_id}`}</strong>{agent.error && <small title={agent.error}>{agent.error}</small>}</div>
                <span className={`status-pill ${agent.status === 'succeeded' || agent.status === 'removed' ? 'ok' : agent.status === 'failed' ? 'danger' : 'warning'}`}>{migrationStatusLabel(agent.status)}</span>
              </div>)}
            </div>
            {Number(migration.failed || 0) > 0 && <button className="ghost base-path-retry" onClick={retryBasePathMigration} disabled={Boolean(saving)}>
              <RefreshCw size={14} className={saving === 'base-path-retry' ? 'spin' : ''} />{saving === 'base-path-retry' ? '重试中...' : '重试失败 Agent'}
            </button>}
          </div>}
        </div>
      </section>}
      {activeSection === 'servers' && <section className="settings-card">
        <div className="settings-card-head"><div><h3>新服务器默认值</h3><p className="muted">创建服务器时自动带入，可在创建窗口中单独修改。</p></div></div>
        <div className="form settings-form single-field">
          <FormField label="MTU" hint="首次部署或设置变化时执行。">
            <Select variant="segmented" value={serverDefaultMTUMode} onChange={event => setServerDefaultMTUMode(event.target.value)}>
              {mtuModes.map(mode => <option value={mode} key={mode}>{labelValue(mode)}</option>)}
            </Select>
          </FormField>
          <FormField label="BBR + FQ" hint="首次安装 Agent 时尝试启用，失败不影响安装。">
            <label className="notification-enable-row"><input type="checkbox" checked={serverDefaultBBREnabled} onChange={event => setServerDefaultBBREnabled(event.target.checked)} aria-label="新服务器默认启用 BBR + FQ" /></label>
          </FormField>
          <div className="settings-actions"><button onClick={() => void saveServerDefaults()} disabled={Boolean(saving)}>{saving === 'server-defaults' ? '保存中...' : '保存默认值'}</button></div>
        </div>
      </section>}
      {activeSection === 'certificates' && <CertificateSettings data={data} client={client} load={load} notify={notify} />}
      {activeSection === 'subscriptions' && <section className="settings-card">
        <div className="settings-card-head subscription-security-head">
          <div><h3>Mihomo Age 加密</h3><p className="muted">服务端只保存用户公钥，私钥始终留在客户端。</p></div>
          <div className="subscription-security-summary">
            <span className={`status-pill ${subscriptionAgePolicy === 'required' ? 'warning' : 'ok'}`}>{subscriptionAgePolicy === 'required' ? '强制开启' : '用户可选'}</span>
            <div className="subscription-security-note">
              <Shield size={18} />
              <div><strong>{subscriptionAgePolicy === 'required' ? 'Mihomo 订阅必须加密' : '普通订阅与加密订阅并存'}</strong><span>{subscriptionAgePolicy === 'required' ? '没有配置 Age 公钥的用户将无法获取 Mihomo 格式，直到保存公钥。' : '用户可在自己的账户页面开启，已有普通订阅链接不会失效。'}</span></div>
            </div>
          </div>
        </div>
        <div className="form settings-form single-field">
          <FormField label="加密策略" hint="仅影响 Mihomo 和 Clash 格式。">
            <Select variant="segmented" value={subscriptionAgePolicy} onChange={e => setSubscriptionAgePolicy(e.target.value as 'optional' | 'required')} aria-label="Age 加密策略">
              <option value="optional">用户可选</option>
              <option value="required">强制开启</option>
            </Select>
          </FormField>
          <div className="settings-actions"><button onClick={saveSubscriptionSecurity} disabled={Boolean(saving)}>{saving === 'subscription-security' ? '保存中...' : '保存订阅策略'}</button></div>
        </div>
      </section>}
      {activeSection === 'traffic' && <section className="settings-card">
        <div className="settings-card-head"><div><h3>流量控制</h3><p className="muted">用于计算用户当前周期流量，并在达量后暂停节点使用。</p></div></div>
        <div className="form settings-form two-column">
          <FormField label="统计时区" hint="用于计算流量重置时间。">
            <Select value={trafficTimezone} onChange={e => setTrafficTimezone(e.target.value)} aria-label="统计时区">
              {!trafficTimezones.includes(trafficTimezone) && <option value={trafficTimezone}>{trafficTimezoneLabel(trafficTimezone)}</option>}
              {trafficTimezones.map(timezone => <option key={timezone} value={timezone}>{trafficTimezoneLabel(timezone)}</option>)}
            </Select>
          </FormField>
          <FormField label="达量后处理">
            <Select variant="segmented" value={trafficMode} onChange={e => setTrafficMode(e.target.value)}>
              <option value="disconnect_and_reject">断开并拒绝</option>
              <option value="reject_new">仅拒绝新连接</option>
            </Select>
          </FormField>
          <div className="settings-actions"><button onClick={saveTraffic} disabled={Boolean(saving)}>{saving === 'traffic' ? '保存中...' : '保存流量设置'}</button></div>
          <p className="muted">Agent 会保留本地可用额度；面板暂时不可达时，节点仍会按已下发额度暂停超量用户。</p>
        </div>
      </section>}
      {activeSection === 'backups' && <ControllerBackupPanel client={client} notify={notify} dialogs={dialogs} />}
      {activeSection === 'updates' && <ControllerUpdatePanel data={data} client={client} load={load} notify={notify} dialogs={dialogs} />}
      {activeSection === 'logs' && <ControllerLogsPanel
        client={client}
        dialogs={dialogs}
        notify={notify}
        maxMB={controllerLogMaxMB}
        backups={controllerLogBackups}
        setMaxMB={setControllerLogMaxMB}
        setBackups={setControllerLogBackups}
        saving={saving === 'controller-logs'}
        onSave={saveControllerLogs}
      />}
    </div>
  </section>
}

function ControllerUpdatePanel({ data, client, load, notify, dialogs }: any) {
  const emptyStatus: ControllerUpdateStatus = {
    channel: '', current: { version: data.version?.version || '', build: data.version?.build || '', commit: data.version?.commit || '', date: data.version?.built_at || '' },
    available: { version: '', build: '', commit: '', date: '' }, update_available: false, auto_update_enabled: false, can_cancel: false, status: 'loading',
  }
  const [snapshot, setSnapshot] = useState<ControllerUpdateStatus>(emptyStatus)
  const [working, setWorking] = useState('')
  const [installExpected, setInstallExpected] = useState(false)
  const [installDialogOpen, setInstallDialogOpen] = useState(false)
  const [installPhase, setInstallPhase] = useState<ControllerUpdateInstallPhase>('confirm')
  const [installConnectionInterrupted, setInstallConnectionInterrupted] = useState(false)
  const [installFailure, setInstallFailure] = useState('')
  const installExpectedRef = useRef(false)
  const cancelExpectedRef = useRef(false)
  const installRequestPendingRef = useRef(false)
  const installTargetBuildRef = useRef('')
  const updateInstallExpected = (value: boolean) => {
    installExpectedRef.current = value
    setInstallExpected(value)
  }
  const applyInstallStatus = (result: ControllerUpdateStatus) => {
    if (!installExpectedRef.current) return
    setInstallConnectionInterrupted(false)
    const targetReached = Boolean(installTargetBuildRef.current) && result.current?.build === installTargetBuildRef.current
    if (result.status === 'cancelled') {
      cancelExpectedRef.current = false
      updateInstallExpected(false)
      setInstallPhase('cancelled')
      setInstallDialogOpen(true)
      notify?.('更新已中断', 'success')
      return
    }
    const alreadyCurrent = result.status === 'current' && !result.update_available
    if (result.status === 'installed' || alreadyCurrent || (targetReached && !result.update_available)) {
      cancelExpectedRef.current = false
      updateInstallExpected(false)
      setInstallPhase('complete')
      setInstallDialogOpen(true)
      return
    }
    if (result.status === 'failed' || result.status === 'unavailable') {
      cancelExpectedRef.current = false
      updateInstallExpected(false)
      setInstallFailure(result.last_error || '主控更新未能完成，请检查更新状态。')
      setInstallPhase('failed')
      setInstallDialogOpen(true)
      return
    }
    const taskExpired = result.status === 'idle' || result.status === 'pinned' || (result.status === 'available' && !installRequestPendingRef.current)
    if (taskExpired) {
      cancelExpectedRef.current = false
      updateInstallExpected(false)
      setInstallPhase('stopped')
      setInstallDialogOpen(true)
      return
    }
    if (result.status === 'downloading') setInstallPhase('downloading')
    else if (result.status === 'ready') setInstallPhase('ready')
    else if (result.status === 'installing') setInstallPhase('installing')
    else if (result.status === 'cancelling') setInstallPhase('cancelling')
    else setInstallPhase('starting')
  }
  const refresh = async (quiet = false) => {
    if (!quiet) setWorking('load')
    try {
      const result = await client.request('/controller-update') as ControllerUpdateStatus
      setSnapshot(result)
      applyInstallStatus(result)
    } catch (error: any) {
      if (quiet && (installExpectedRef.current || ['downloading', 'ready', 'installing', 'cancelling'].includes(snapshot.status)) && isExpectedControllerUpdateDisconnect(error)) {
        setInstallConnectionInterrupted(true)
      } else {
        notify?.(localizeErrorMessage(error?.message || error), 'error')
      }
    } finally {
      if (!quiet) setWorking('')
    }
  }
  useEffect(() => { void refresh() }, [])
  useEffect(() => {
    if (!installExpected && !['downloading', 'ready', 'installing', 'cancelling', 'checking'].includes(snapshot.status)) return
    const timer = window.setInterval(() => { void refresh(true) }, 3000)
    return () => window.clearInterval(timer)
  }, [snapshot.status, installExpected])
  useEffect(() => {
    if (!['downloading', 'ready', 'installing', 'cancelling'].includes(snapshot.status) || installExpected) return
    installTargetBuildRef.current = snapshot.available?.build || ''
    updateInstallExpected(true)
    setInstallPhase(snapshot.status as ControllerUpdateInstallPhase)
    setInstallDialogOpen(true)
  }, [snapshot.status, installExpected])
  const check = async () => {
    if (working) return
    setWorking('check')
    try {
      const result = await client.request('/controller-update/check', { method: 'POST' }) as ControllerUpdateStatus
      setSnapshot(result)
      notify?.(result.update_available ? '发现主控更新' : '当前已是最新版本', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const openInstall = () => {
    if (installExpected || ['downloading', 'ready', 'installing', 'cancelling'].includes(snapshot.status)) {
      setInstallPhase(snapshot.status === 'cancelling' ? 'cancelling' : snapshot.status === 'installing' ? 'installing' : snapshot.status === 'ready' ? 'ready' : 'downloading')
      setInstallDialogOpen(true)
      return
    }
    if (working || snapshot.channel === 'pinned' || !snapshot.update_available) return
    setInstallFailure('')
    setInstallConnectionInterrupted(false)
    setInstallPhase('confirm')
    setInstallDialogOpen(true)
  }
  const install = async () => {
    if (working || snapshot.channel === 'pinned' || !snapshot.update_available) return
    installTargetBuildRef.current = snapshot.available?.build || ''
    cancelExpectedRef.current = false
    updateInstallExpected(true)
    setInstallPhase('starting')
    setWorking('install')
    installRequestPendingRef.current = true
    try {
      const result = await client.request('/controller-update/install', { method: 'POST' }) as ControllerUpdateStatus
      setSnapshot(result)
      applyInstallStatus(result)
      notify?.('更新已开始，主控将自动重启', 'success')
    } catch (error: any) {
      if (isExpectedControllerUpdateDisconnect(error)) {
        setSnapshot(previous => ({ ...previous, status: 'installing' }))
        setInstallConnectionInterrupted(true)
        setInstallPhase('installing')
      } else {
        updateInstallExpected(false)
        setInstallFailure(localizeErrorMessage(error?.message || error))
        setInstallPhase('failed')
      }
    } finally {
      installRequestPendingRef.current = false
      setWorking('')
    }
  }
  const cancelInstall = async () => {
    if (working || !snapshot.can_cancel) return
    setWorking('cancel')
    try {
      cancelExpectedRef.current = true
      const result = await client.request('/controller-update/cancel', { method: 'POST' }) as ControllerUpdateStatus
      setSnapshot(result)
      setInstallPhase('cancelling')
      notify?.('正在中断更新', 'success')
    } catch (error: any) {
      cancelExpectedRef.current = false
      notify?.(localizeErrorMessage(error?.message || error), 'error')
      await refresh(true)
    } finally {
      setWorking('')
    }
  }
  const setAutoUpdate = async (enabled: boolean) => {
    if (working || snapshot.channel === 'pinned') return
    if (enabled && snapshot.channel === 'dev') {
      const confirmed = await dialogs.confirm({
        title: '启用开发版自动更新？',
        message: '开发版更新频繁，可能包含尚未稳定的功能。',
        confirmText: '确认启用',
      })
      if (!confirmed) return
    }
    setWorking('auto')
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify({ controller_auto_update_enabled: enabled }) })
      setSnapshot(previous => ({ ...previous, auto_update_enabled: enabled }))
      await load('settings', { background: true })
      notify?.(enabled ? '主控自动更新已开启' : '主控自动更新已关闭', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const copyManualCommand = async () => {
    if (!snapshot.manual_command) return
    await navigator.clipboard.writeText(snapshot.manual_command)
    notify?.('切换命令已复制', 'success')
  }
  const labels: Record<string, string> = {
    loading: '读取中', idle: '等待检查', checking: '检查中', current: '已是最新', available: '可更新', downloading: '下载中', ready: '文件已准备好', installing: '安装中', cancelling: '正在中断', cancelled: '已中断', installed: '已安装', failed: '失败', unavailable: '更新器不可用', pinned: '固定版本',
  }
  const channelLabel = snapshot.channel === 'dev' ? '开发版' : snapshot.channel === 'stable' ? '正式版' : snapshot.channel === 'pinned' ? '固定版本' : '未知'
  const statusTone = snapshot.status === 'failed' || snapshot.status === 'unavailable' ? 'danger' : snapshot.update_available || ['downloading', 'ready', 'installing', 'cancelling'].includes(snapshot.status) ? 'warning' : 'ok'
  const updateInProgress = installExpected || ['downloading', 'ready', 'installing', 'cancelling'].includes(snapshot.status)
  return <section className="settings-card controller-update-card">
    <div className="settings-card-head">
      <div><h3>主控更新</h3><p className="muted">更新通道 · {channelLabel}</p></div>
      <span className={`status-pill ${statusTone}`}>{labels[snapshot.status] || snapshot.status}</span>
    </div>
    {snapshot.channel === 'dev' && <div className="controller-update-warning"><Info size={17} /><span><strong>开发版更新频繁</strong><small>可能包含尚未稳定的功能。</small></span></div>}
    <div className="controller-update-versions">
      <div><span>当前版本</span><strong>{snapshot.current?.version || '-'}</strong><small>{snapshot.current?.build ? `构建 ${snapshot.current.build}` : '暂无构建信息'}</small></div>
      <ArrowLeftRight size={18} />
      <div><span>最新版本</span><strong>{snapshot.available?.version || '尚未检查'}</strong><small>{snapshot.available?.build ? `构建 ${snapshot.available.build}` : '点击检查更新'}</small></div>
    </div>
    <div className="controller-update-meta">
      <span>上次检查<strong>{snapshot.last_checked_at ? formatDate(snapshot.last_checked_at) : '尚未检查'}</strong></span>
      {snapshot.backup_path && <span>最近备份<strong title={snapshot.backup_path}>{snapshot.backup_path}</strong></span>}
    </div>
    {snapshot.last_error && <div className="controller-update-error" role="alert">{snapshot.last_error}</div>}
    {snapshot.channel === 'pinned' ? <div className="controller-update-pinned">
      <span>当前版本保持锁定。切换到正式版通道后，才能在面板内更新。</span>
      <div><code>{snapshot.manual_command}</code><button type="button" className="ghost icon-button" onClick={() => void copyManualCommand()} title="复制切换命令" aria-label="复制切换命令"><Copy size={15} /></button></div>
    </div> : <label className="check-row controller-update-toggle">
      <input type="checkbox" checked={snapshot.auto_update_enabled} disabled={Boolean(working) || snapshot.status === 'unavailable' || updateInProgress} onChange={event => void setAutoUpdate(event.target.checked)} />
      <span><strong>自动安装主控更新</strong><small>发现当前通道的新版本后，先备份数据库再安装。</small></span>
    </label>}
    <div className="settings-actions controller-update-actions">
      <button type="button" className="ghost" onClick={() => void check()} disabled={Boolean(working) || snapshot.channel === 'pinned' || updateInProgress}><RefreshCw size={14} className={working === 'check' ? 'spin' : ''} />{working === 'check' ? '检查中...' : '检查更新'}</button>
      <button type="button" onClick={openInstall} disabled={Boolean(working) || snapshot.channel === 'pinned' || (!snapshot.update_available && !updateInProgress)}><Download size={14} />{working === 'install' ? '准备中...' : updateInProgress ? '查看安装进度' : '备份并安装'}</button>
    </div>
    <AnimatePresence>{installDialogOpen && <ControllerUpdateInstallDialog
      phase={installPhase}
      targetVersion={snapshot.available?.version || ''}
      connectionInterrupted={installConnectionInterrupted}
      failure={installFailure}
      canCancel={Boolean(snapshot.can_cancel) && ['downloading', 'ready'].includes(installPhase)}
      cancelling={working === 'cancel' || installPhase === 'cancelling'}
      onCancel={() => setInstallDialogOpen(false)}
      onInstall={() => void install()}
      onInterrupt={() => void cancelInstall()}
      onHide={() => setInstallDialogOpen(false)}
      onReload={() => window.location.reload()}
    />}</AnimatePresence>
  </section>
}

type ControllerUpdateInstallPhase = 'confirm' | 'starting' | 'downloading' | 'ready' | 'installing' | 'cancelling' | 'cancelled' | 'stopped' | 'complete' | 'failed'

function isExpectedControllerUpdateDisconnect(error: unknown) {
  if (error instanceof TypeError) return true
  const message = String((error as any)?.message || error || '').trim().toLowerCase()
  return ['failed to fetch', 'networkerror', 'load failed', 'bad gateway', 'service unavailable', 'gateway timeout'].some(value => message.includes(value))
}

function ControllerUpdateInstallDialog({ phase, targetVersion, connectionInterrupted, failure, canCancel, cancelling, onCancel, onInstall, onInterrupt, onHide, onReload }: { phase: ControllerUpdateInstallPhase; targetVersion: string; connectionInterrupted: boolean; failure: string; canCancel: boolean; cancelling: boolean; onCancel: () => void; onInstall: () => void; onInterrupt: () => void; onHide: () => void; onReload: () => void }) {
  const waiting = ['starting', 'downloading', 'ready', 'installing', 'cancelling'].includes(phase)
  const title = phase === 'confirm' ? '更新期间面板会暂时离线' : phase === 'starting' ? '正在准备更新' : phase === 'downloading' ? '正在下载更新' : phase === 'ready' ? '更新文件已准备好' : phase === 'installing' ? '正在安装更新' : phase === 'cancelling' ? '正在中断更新' : phase === 'cancelled' ? '更新已中断' : phase === 'stopped' ? '本次更新已停止' : phase === 'complete' ? '主控更新已完成' : '主控更新未完成'
  const activeTitle = phase === 'starting' ? '正在备份数据' : phase === 'downloading' ? '正在下载并检查更新文件' : phase === 'ready' ? '更新文件已经检查完成' : phase === 'cancelling' ? '正在停止更新' : connectionInterrupted ? '正在重新启动主控' : '正在安装新版本'
  const activeDescription = phase === 'starting' ? '备份完成后会自动开始下载。' : phase === 'downloading' ? '此阶段可以安全中断，不会改动当前程序。' : phase === 'ready' ? '安装即将开始，此时仍可安全中断。' : phase === 'cancelling' ? '当前程序不会被替换，请稍候。' : connectionInterrupted ? '面板会自动尝试重新连接。' : '安装完成后主控会自动重新启动。'
  const downloadDone = phase === 'ready' || phase === 'installing'
  return <MotionDialogPanel onCancel={waiting ? onHide : onCancel} className="controller-update-install-dialog" system>
    <header className="dialog-head"><div><h2>{title}</h2><p className="muted">{targetVersion ? `目标版本 ${targetVersion}` : '主控更新'}</p></div>{!waiting && <button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>}</header>
    <div className="dialog-body controller-update-install-body">
      {phase === 'confirm' && <>
        <div className="controller-update-install-lead"><Info size={20} /><div><strong>整个过程通常需要几分钟</strong><p>面板会先备份数据库，再下载并检查更新文件，然后安装新版本，最后重新启动主控。</p></div></div>
        <div className="controller-update-install-notice"><strong>更新期间暂时无法访问面板是正常现象</strong><span>主控停止和重新启动期间，连接可能短暂中断，刷新时也可能看到 502 或“页面暂时无法访问”的提示。这不代表更新失败。</span></div>
        <p className="muted controller-update-install-advice">请不要重复点击安装或手动重启服务，等待几分钟后再重新打开面板。</p>
      </>}
      {waiting && <>
        <div className="controller-update-install-state" aria-live="polite"><RefreshCw size={24} className="spin" /><div><strong>{activeTitle}</strong><p>{activeDescription}</p></div></div>
        <div className="controller-update-stages" aria-label="更新进度">
          <div className={`controller-update-stage ${phase === 'downloading' || phase === 'cancelling' ? 'active' : downloadDone ? 'done' : ''}`}><span>{downloadDone ? <Check size={14} /> : '1'}</span><div><strong>下载更新</strong><small>{downloadDone ? '更新文件已经准备好' : phase === 'cancelling' ? '正在停止更新' : phase === 'downloading' ? '正在下载并检查文件' : '等待开始'}</small></div></div>
          <div className={`controller-update-stage ${phase === 'ready' || phase === 'installing' ? 'active' : ''}`}><span>2</span><div><strong>安装更新</strong><small>{phase === 'ready' ? '即将开始安装' : phase === 'installing' ? connectionInterrupted ? '正在重新启动主控' : '正在安装新版本' : '等待下载完成'}</small></div></div>
        </div>
        <div className="controller-update-install-progress" role="progressbar" aria-label="主控更新进行中"><span /></div>
        <div className="controller-update-install-notice compact"><span>期间出现连接中断、短暂白屏或 502 提示都是正常现象。</span></div>
      </>}
      {phase === 'cancelled' && <div className="controller-update-install-result cancelled"><Info size={24} /><div><strong>更新已安全中断</strong><p>当前版本没有被改动，可以稍后重新开始更新。</p></div></div>}
      {phase === 'stopped' && <div className="controller-update-install-result cancelled"><Info size={24} /><div><strong>本次更新不会继续进行</strong><p>请重新检查当前版本，再决定是否重新更新。</p></div></div>}
      {phase === 'complete' && <div className="controller-update-install-result success"><Check size={24} /><div><strong>新版本已经安装完成</strong><p>主控服务已恢复，可以重新加载面板并继续使用。</p></div></div>}
      {phase === 'failed' && <div className="controller-update-install-result failed"><Info size={24} /><div><strong>更新没有完成</strong><p>{failure || '请检查主控更新状态后重试。'}</p></div></div>}
    </div>
    <footer className="dialog-actions">
      {phase === 'confirm' && <><button type="button" className="ghost" onClick={onCancel}>取消</button><button type="button" onClick={onInstall}>我知道了，开始更新</button></>}
      {waiting && <>{canCancel && <button type="button" className="ghost danger-text" onClick={onInterrupt} disabled={cancelling}><X size={14} />{cancelling ? '正在中断...' : '中断更新'}</button>}<button type="button" className="ghost" onClick={onHide}>在后台继续</button></>}
      {phase === 'cancelled' && <button type="button" onClick={onCancel}>关闭</button>}
      {phase === 'stopped' && <button type="button" onClick={onCancel}>关闭</button>}
      {phase === 'complete' && <button type="button" onClick={onReload}>重新加载面板</button>}
      {phase === 'failed' && <button type="button" onClick={onCancel}>关闭</button>}
    </footer>
  </MotionDialogPanel>
}

function ControllerBackupPanel({ client, notify, dialogs }: any) {
  const emptySettings: ControllerBackupSettings = {
    enabled: false, schedule: 'daily', time: '03:00', weekday: 0, local_retention: 7, remote_retention: 30,
    destination: { provider: '', endpoint: '', bucket: '', prefix: '', region: 'us-east-1', force_path_style: false, enabled: false },
    password_configured: false, destination_configured: true,
  }
  const [snapshot, setSnapshot] = useState<ControllerBackupSnapshot>({ settings: emptySettings, backups: [] })
  const [draft, setDraft] = useState<ControllerBackupSettings>(emptySettings)
  const [recoveryPassword, setRecoveryPassword] = useState('')
  const [recoveryPasswordConfirm, setRecoveryPasswordConfirm] = useState('')
  const [s3AccessKey, setS3AccessKey] = useState('')
  const [s3SecretKey, setS3SecretKey] = useState('')
  const [webdavUsername, setWebdavUsername] = useState('')
  const [webdavPassword, setWebdavPassword] = useState('')
  const [uploadPassword, setUploadPassword] = useState('')
  const [working, setWorking] = useState('')
  const uploadRef = useRef<HTMLInputElement>(null)
  const refresh = async (quiet = false) => {
    if (!quiet) setWorking('load')
    try {
      const result = await client.request('/backups') as ControllerBackupSnapshot
      setSnapshot(result)
      setDraft(result.settings || emptySettings)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      if (!quiet) setWorking('')
    }
  }
  useEffect(() => { void refresh() }, [])
  const saveSettings = async () => {
    if (working) return
    if (recoveryPassword && recoveryPassword !== recoveryPasswordConfirm) {
      notify?.('两次输入的恢复密码不一致', 'error')
      return
    }
    if (!draft.password_configured && !recoveryPassword) {
      notify?.('请先设置恢复密码', 'error')
      return
    }
    setWorking('save')
    try {
      const result = await client.request('/backups/settings', {
        method: 'PUT',
        body: JSON.stringify({
          enabled: draft.enabled,
          schedule: draft.schedule,
          time: draft.time,
          weekday: draft.weekday,
          local_retention: draft.local_retention,
          remote_retention: draft.remote_retention,
          destination: draft.destination,
          recovery_password: recoveryPassword,
          s3_access_key: s3AccessKey,
          s3_secret_key: s3SecretKey,
          webdav_username: webdavUsername,
          webdav_password: webdavPassword,
        }),
      }) as { settings: ControllerBackupSettings }
      setSnapshot(previous => ({ ...previous, settings: result.settings }))
      setDraft(result.settings)
      setRecoveryPassword('')
      setRecoveryPasswordConfirm('')
      setS3AccessKey('')
      setS3SecretKey('')
      setWebdavUsername('')
      setWebdavPassword('')
      notify?.('备份设置已保存', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const testDestination = async () => {
    setWorking('test')
    try {
      await client.request('/backups/settings/test', {
        method: 'POST',
        body: JSON.stringify({
          destination: draft.destination,
          s3_access_key: s3AccessKey,
          s3_secret_key: s3SecretKey,
          webdav_username: webdavUsername,
          webdav_password: webdavPassword,
        }),
      })
      notify?.('第三方备份目标连接成功', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const createBackup = async () => {
    if (working) return
    setWorking('create')
    try {
      const result = await client.request('/backups', { method: 'POST', body: JSON.stringify({ upload_remote: true }) }) as { backup: ControllerBackup }
      notify?.(result.backup?.remote_status === 'failed' ? '本地备份已创建，但第三方上传失败' : '备份已创建', result.backup?.remote_status === 'failed' ? 'error' : 'success')
      await refresh(true)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const downloadBackup = async (item: ControllerBackup) => {
    setWorking(`download-${item.id}`)
    try {
      const file = await client.download(`/backups/${item.id}/download`)
      const url = URL.createObjectURL(file.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = file.filename
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const restoreBackup = async (item: ControllerBackup, password: string, alreadyConfirmed = false) => {
    if (!password) {
      notify?.('请输入该备份的恢复密码', 'error')
      return
    }
    if (!alreadyConfirmed) {
      const confirmed = await dialogs.confirm({
        title: '恢复主控数据？',
        message: '恢复前会创建保护备份。主控将重启，当前登录会话失效，并重新下发恢复后的节点配置。',
        confirmText: '备份并恢复',
        tone: 'danger',
      })
      if (!confirmed) return
    }
    setWorking(`restore-${item.id}`)
    try {
      await client.request(`/backups/${item.id}/restore`, { method: 'POST', body: JSON.stringify({ recovery_password: password }) })
      notify?.('备份已验证，主控正在重启恢复数据', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
      setWorking('')
    }
  }
  const removeBackup = async (item: ControllerBackup) => {
    const remoteDeleteMessage = item.remote_status === 'available' && !item.remote_retrievable
      ? '当前第三方目标与这条记录不一致。这里只会删除本地记录，远端文件需要到旧存储中手动删除。'
      : item.remote_status === 'available' ? '本地副本和第三方副本都会删除。' : '本地副本和备份记录都会删除。'
    const deleteMessage = item.protected ? `这是恢复前创建的保护备份。${remoteDeleteMessage}` : remoteDeleteMessage
    const confirmed = await dialogs.confirm({ title: '删除备份？', message: deleteMessage, confirmText: '删除', tone: 'danger' })
    if (!confirmed) return
    setWorking(`delete-${item.id}`)
    try {
      const result = await client.request(`/backups/${item.id}`, { method: 'DELETE' }) as { message?: string }
      notify?.(result.message || '备份已删除', 'success')
      await refresh(true)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const uploadBackup = async (file?: File) => {
    if (!file) return
    if (!uploadPassword) {
      notify?.('请输入该备份的恢复密码', 'error')
      return
    }
    setWorking('upload')
    let restoreStarted = false
    try {
      const form = new FormData()
      form.set('backup', file)
      form.set('recovery_password', uploadPassword)
      const result = await client.upload('/backups/upload', form) as { backup: ControllerBackup; inspection: { manifest: { source_version: string; created_at: string } } }
      await refresh(true)
      const confirmed = await dialogs.confirm({
        title: '立即恢复上传的备份？',
        message: `该备份来自 ${result.inspection?.manifest?.source_version || '未知版本'}。选择稍后恢复会保留文件，之后可从备份列表恢复。`,
        confirmText: '立即恢复',
        cancelText: '只保存',
        tone: 'danger',
      })
      if (confirmed) {
        restoreStarted = true
        await restoreBackup(result.backup, uploadPassword, true)
      } else {
        notify?.('备份已保存，尚未恢复', 'success')
      }
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      if (!restoreStarted) setWorking('')
    }
  }
  const updateDestination = (patch: Partial<BackupDestination>) => setDraft(current => ({ ...current, destination: { ...current.destination, ...patch } }))
  const destination = draft.destination || emptySettings.destination
  const weekdayNames = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
  const backupStatus = (item: ControllerBackup) => item.remote_status === 'failed'
    ? (item.local_status === 'available' ? '本地可用，远端失败' : '副本不可用')
    : item.local_status === 'available' && item.remote_status === 'available' ? '本地和远端可用'
      : item.local_status === 'available' ? '本地可用'
        : item.remote_retrievable ? '可从第三方取回'
          : item.remote_status === 'available' ? '保留在旧目标' : '副本不可用'
  return <section className="settings-card controller-backup-card">
    <div className="settings-card-head">
      <div><h3>主控数据备份</h3><p className="muted">备份数据库、证书续期状态和受保护配置；日志、下载缓存和程序文件不包含在内。</p></div>
      <span className={`status-pill ${snapshot.settings?.last_error ? 'danger' : 'ok'}`}>{snapshot.settings?.last_error ? '需要处理' : '已就绪'}</span>
    </div>
    {snapshot.settings?.last_error && <div className="controller-update-error" role="alert">{snapshot.settings.last_error}</div>}
    <div className="backup-policy-grid">
      <label className="check-row backup-enabled"><input type="checkbox" checked={draft.enabled} onChange={event => setDraft(current => ({ ...current, enabled: event.target.checked }))} /><span><strong>自动备份</strong><small>按设定时间创建加密备份，并在启用第三方目标后自动上传。</small></span></label>
      <FormField label="备份频率"><Select value={draft.schedule} onChange={event => setDraft(current => ({ ...current, schedule: event.target.value as 'daily' | 'weekly' }))}><option value="daily">每天</option><option value="weekly">每周</option></Select></FormField>
      {draft.schedule === 'weekly' && <FormField label="每周日期"><Select value={draft.weekday} onChange={event => setDraft(current => ({ ...current, weekday: Number(event.target.value) }))}>{weekdayNames.map((label, index) => <option key={label} value={index}>{label}</option>)}</Select></FormField>}
      <FormField label="执行时间" hint="使用流量控制中设置的时区"><input type="time" value={draft.time} onChange={event => setDraft(current => ({ ...current, time: event.target.value || '03:00' }))} /></FormField>
      <FormField label="本地保留数量" hint="手动、自动和上传备份共用此数量"><input type="number" min={1} max={100} value={draft.local_retention} onChange={event => setDraft(current => ({ ...current, local_retention: Math.max(1, Math.min(100, Number(event.target.value) || 1)) }))} /></FormField>
      <FormField label="远端保留数量" hint="远端副本独立滚动"><input type="number" min={1} max={365} value={draft.remote_retention} onChange={event => setDraft(current => ({ ...current, remote_retention: Math.max(1, Math.min(365, Number(event.target.value) || 1)) }))} /></FormField>
      <FormField label={draft.password_configured ? '更换恢复密码' : '恢复密码'} hint={draft.password_configured ? '留空表示保持当前密码；旧备份仍使用创建时的原密码。' : '用于加密备份并在新主控恢复数据。至少 12 个字符。'}><input type="password" autoComplete="new-password" value={recoveryPassword} onChange={event => setRecoveryPassword(event.target.value)} /></FormField>
      {(recoveryPassword || !draft.password_configured) && <FormField label="确认恢复密码"><input type="password" autoComplete="new-password" value={recoveryPasswordConfirm} onChange={event => setRecoveryPasswordConfirm(event.target.value)} /></FormField>}
    </div>
    <section className="backup-destination">
      <div className="backup-destination-head"><div><h3>第三方备份目标</h3><p className="muted">一次可启用一个 S3 兼容存储或 WebDAV 目标。第三方副本同样使用恢复密码加密；更换目标后，旧目标中的文件不会被自动删除。</p></div><label className="check-row"><input type="checkbox" checked={destination.enabled} onChange={event => updateDestination({ enabled: event.target.checked })} /><span>启用</span></label></div>
      <div className="backup-policy-grid">
        <FormField label="类型"><Select value={destination.provider} onChange={event => updateDestination({ provider: event.target.value as BackupDestination['provider'] })} disabled={!destination.enabled}><option value="">选择目标</option><option value="s3">S3 兼容存储</option><option value="webdav">WebDAV</option></Select></FormField>
        <FormField label={destination.provider === 'webdav' ? 'WebDAV 地址' : '终端地址'} hint="生产环境请使用 HTTPS。"><input value={destination.endpoint || ''} disabled={!destination.enabled} onChange={event => updateDestination({ endpoint: event.target.value })} placeholder={destination.provider === 'webdav' ? 'https://dav.example.com/oboard' : 'https://s3.example.com'} /></FormField>
        <FormField label="目录前缀" hint="只会管理该前缀下由主控创建的备份。"><input value={destination.prefix || ''} disabled={!destination.enabled} onChange={event => updateDestination({ prefix: event.target.value })} placeholder="oboard-backups" /></FormField>
        {destination.provider === 's3' && <><FormField label="存储桶"><input value={destination.bucket || ''} disabled={!destination.enabled} onChange={event => updateDestination({ bucket: event.target.value })} /></FormField><FormField label="区域"><input value={destination.region || ''} disabled={!destination.enabled} onChange={event => updateDestination({ region: event.target.value })} placeholder="us-east-1" /></FormField><label className="check-row"><input type="checkbox" checked={Boolean(destination.force_path_style)} disabled={!destination.enabled} onChange={event => updateDestination({ force_path_style: event.target.checked })} /><span>使用路径风格地址</span></label><FormField label="访问密钥"><input type="password" value={s3AccessKey} disabled={!destination.enabled} onChange={event => setS3AccessKey(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField><FormField label="访问密钥密码"><input type="password" value={s3SecretKey} disabled={!destination.enabled} onChange={event => setS3SecretKey(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField></>}
        {destination.provider === 'webdav' && <><FormField label="用户名"><input value={webdavUsername} disabled={!destination.enabled} onChange={event => setWebdavUsername(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField><FormField label="密码"><input type="password" value={webdavPassword} disabled={!destination.enabled} onChange={event => setWebdavPassword(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField></>}
      </div>
      <div className="settings-actions"><button onClick={() => void saveSettings()} disabled={Boolean(working)}>{working === 'save' ? '保存中...' : '保存备份设置'}</button><button className="ghost" onClick={() => void testDestination()} disabled={Boolean(working) || !destination.enabled}>{working === 'test' ? '测试中...' : '测试连接'}</button></div>
    </section>
    <div className="backup-actions"><div><strong>立即备份</strong><span>本地备份完成后，会上传到已启用的第三方目标。</span></div><button onClick={() => void createBackup()} disabled={Boolean(working) || !draft.password_configured}><Database size={15} />{working === 'create' ? '备份中...' : '创建备份'}</button></div>
    <section className="backup-import">
      <div><h3>导入与恢复密码</h3><p className="muted">这里的密码也用于恢复列表中的备份。上传时会先验证密码和完整性。</p></div>
      <div className="backup-import-actions"><input ref={uploadRef} type="file" accept=".obk,application/octet-stream" onChange={event => { void uploadBackup(event.target.files?.[0]); event.currentTarget.value = '' }} /><input type="password" value={uploadPassword} onChange={event => setUploadPassword(event.target.value)} placeholder="该备份的恢复密码" /><button className="ghost" onClick={() => uploadRef.current?.click()} disabled={Boolean(working)}><ArrowUp size={15} />{working === 'upload' ? '上传中...' : '上传备份'}</button></div>
    </section>
    <section className="backup-records">
      <div className="settings-card-head"><div><h3>备份记录</h3><p className="muted">恢复会先创建保护备份。受保护备份不会被自动滚动删除。</p></div><button className="ghost icon-button" onClick={() => void refresh()} disabled={Boolean(working)} title="刷新备份记录" aria-label="刷新备份记录"><RefreshCw size={15} className={working === 'load' ? 'spin' : ''} /></button></div>
      {snapshot.backups?.length ? <div className="backup-record-list">{snapshot.backups.map(item => <div className="backup-record" key={item.id}><div className="backup-record-main"><strong>{item.origin === 'automatic' ? '自动备份' : item.origin === 'uploaded' ? '上传备份' : item.origin === 'pre_restore' ? '恢复前保护备份' : '手动备份'}</strong><span>{formatDate(item.created_at)} · {formatBytes(Number(item.size_bytes || 0))} · 来源 {item.source_version || '-'}</span>{item.remote_error && <small>{item.remote_error}</small>}</div><span className={`status-pill ${item.remote_status === 'failed' || (item.local_status !== 'available' && !item.remote_retrievable) ? 'danger' : item.protected ? 'warning' : 'ok'}`}>{backupStatus(item)}</span><div className="backup-record-actions">{(item.local_status === 'available' || item.remote_retrievable) && <button type="button" className="ghost icon-button" title={item.local_status === 'available' ? '下载备份' : '从第三方取回并下载'} aria-label={item.local_status === 'available' ? '下载备份' : '从第三方取回并下载'} onClick={() => void downloadBackup(item)} disabled={Boolean(working)}><Download size={15} /></button>}{(item.local_status === 'available' || item.remote_retrievable) && <button type="button" className="ghost" onClick={() => void restoreBackup(item, uploadPassword)} disabled={Boolean(working)}>{item.local_status === 'available' ? '恢复' : '取回并恢复'}</button>}<button type="button" className="ghost icon-button danger-text" title="删除备份" aria-label="删除备份" onClick={() => void removeBackup(item)} disabled={Boolean(working)}><Trash2 size={15} /></button></div></div>)}</div> : <p className="muted backup-empty">尚未创建备份。</p>}
    </section>
  </section>
}

const dnsProviderLabels: Record<DNSProvider, string> = {
  cloudflare: 'Cloudflare',
  alidns: '阿里云 DNS',
  tencent_dns: '腾讯云 DNSPod',
  tencent_esa: '腾讯云 ESA',
  huawei_cloud: '华为云 DNS',
}

const certificateValueLabels: Record<string, string> = {
  issuing: '签发中',
  awaiting_dns: '等待 DNS 解析',
  http01: 'Agent HTTP-01',
  dns01: '面板 DNS-01',
  dns01_manual: '手动 DNS-01',
  imported: '导入证书',
}

function certificateLabelValue(value: string) {
  return certificateValueLabels[value] || labelValue(value)
}

const dnsProviderFields: Record<DNSProvider, Array<{ key: string; label: string; optional?: boolean }>> = {
  cloudflare: [{ key: 'api_token', label: 'API Token' }, { key: 'account_id', label: 'Account ID', optional: true }],
  alidns: [{ key: 'access_key_id', label: 'AccessKey ID' }, { key: 'access_key_secret', label: 'AccessKey Secret' }],
  tencent_dns: [{ key: 'secret_id', label: 'SecretId' }, { key: 'secret_key', label: 'SecretKey' }],
  tencent_esa: [{ key: 'secret_id', label: 'SecretId' }, { key: 'secret_key', label: 'SecretKey' }],
  huawei_cloud: [{ key: 'username', label: 'IAM 用户名' }, { key: 'password', label: 'IAM 密码' }, { key: 'domain_name', label: '账号名' }, { key: 'region', label: '区域' }],
}

function emptyDNSCredentialDraft() {
  return { name: '', provider: 'cloudflare' as DNSProvider, zones: [emptyDNSZoneDraft()], enabled: true, config: {} as Record<string, string> }
}

function emptyDNSZoneDraft() {
  return { id: 0, zone_name: '', provider_zone_id: '', server_id: 0 }
}

function emptyDNSRecordDraft() {
  return { type: 'A', name: '', content: '', comment: '', ttl: 300, proxied: false }
}

function ManagedDNSSettings({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
  const credentials: DNSCredential[] = data.dns_credentials || []
  const servers: Server[] = data.servers || []
  const [activeTab, setActiveTab] = useState<'records' | 'settings'>('records')
  const [editingID, setEditingID] = useState(0)
  const [credentialDialogOpen, setCredentialDialogOpen] = useState(false)
  const [recordDialogOpen, setRecordDialogOpen] = useState(false)
  const [draft, setDraft] = useState<any>(emptyDNSCredentialDraft())
  const zoneOptions = useMemo(() => credentials.flatMap(credential => (credential.zones || []).map(zone => ({ credential, zone }))), [credentials])
  const [selectedZoneID, setSelectedZoneID] = useState(Number(zoneOptions[0]?.zone.id || 0))
  const [recordDialogZoneID, setRecordDialogZoneID] = useState(Number(zoneOptions[0]?.zone.id || 0))
  const [records, setRecords] = useState<DNSRecord[]>([])
  const [recordDraft, setRecordDraft] = useState(emptyDNSRecordDraft())
  const [working, setWorking] = useState('')
  useEffect(() => {
    if (!zoneOptions.some(item => item.zone.id === selectedZoneID)) setSelectedZoneID(Number(zoneOptions[0]?.zone.id || 0))
  }, [zoneOptions, selectedZoneID])
  const resetDraft = () => { setEditingID(0); setDraft(emptyDNSCredentialDraft()) }
  const openCreateCredential = () => {
    resetDraft()
    setCredentialDialogOpen(true)
  }
  const closeCredentialDialog = () => {
    resetDraft()
    setCredentialDialogOpen(false)
  }
  const openCreateRecord = () => {
    setRecordDialogZoneID(selectedZoneID || Number(zoneOptions[0]?.zone.id || 0))
    setRecordDraft(emptyDNSRecordDraft())
    setRecordDialogOpen(true)
  }
  const closeRecordDialog = () => {
    setRecordDraft(emptyDNSRecordDraft())
    setRecordDialogOpen(false)
  }
  const editCredential = (credential: DNSCredential) => {
    setEditingID(credential.id)
    setDraft({ name: credential.name, provider: credential.provider, zones: (credential.zones || []).map(zone => ({ id: zone.id, zone_name: zone.zone_name, provider_zone_id: zone.provider_zone_id || '', server_id: Number(zone.server_id || 0) })), enabled: credential.enabled, config: {} })
    setCredentialDialogOpen(true)
  }
  const saveCredential = async () => {
    const provider = draft.provider as DNSProvider
    const config = Object.fromEntries(Object.entries(draft.config || {}).map(([key, value]) => [key, String(value || '').trim()]).filter(([, value]) => value))
    const zones = (draft.zones || []).map((zone: any) => ({ id: Number(zone.id || 0), zone_name: String(zone.zone_name || '').trim(), provider_zone_id: String(zone.provider_zone_id || '').trim(), server_id: Number(zone.server_id || 0) || undefined }))
    if (!draft.name.trim() || !zones.length || zones.some((zone: any) => !zone.zone_name)) return dialogs.alert({ title: '信息不完整', message: '请填写账号名称和至少一个域名。' })
    if (provider === 'tencent_esa' && zones.some((zone: any) => !zone.provider_zone_id)) return dialogs.alert({ title: '信息不完整', message: '腾讯云 ESA 的每个域名都需要 Zone ID。' })
    if (!editingID && dnsProviderFields[provider].some(field => !field.optional && !config[field.key])) return dialogs.alert({ title: '信息不完整', message: '请填写当前服务商要求的全部账号信息。' })
    setWorking('credential-save')
    try {
      const payload: any = { name: draft.name.trim(), provider, zones, enabled: Boolean(draft.enabled) }
      if (Object.keys(config).length) payload.config = config
      await client.request(editingID ? `/dns-credentials/${editingID}` : '/dns-credentials', { method: editingID ? 'PATCH' : 'POST', body: JSON.stringify(payload) })
      closeCredentialDialog()
      await load()
      notify?.(editingID ? '域名服务账号已更新' : '域名服务账号已创建', 'success')
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const verifyCredential = async (credential: DNSCredential) => {
    setWorking(`verify-${credential.id}`)
    try {
      await client.request(`/dns-credentials/${credential.id}/verify`, { method: 'POST', body: '{}' })
      await load()
      notify?.(`${credential.name} 验证成功`, 'success')
    } catch (error: any) { await load(); notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const deleteCredential = async (credential: DNSCredential) => {
    const ok = await dialogs.confirm({ title: '删除域名服务账号', message: `确认删除 ${credential.name}？`, confirmText: '删除', tone: 'danger' })
    if (!ok) return
    try { await client.request(`/dns-credentials/${credential.id}`, { method: 'DELETE' }); await load(); notify?.('域名服务账号已删除', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const loadRecords = async (zoneID = selectedZoneID) => {
    if (!zoneID) { setRecords([]); return }
    setWorking('records-load')
    try {
      const result = await client.request(`/dns-records?dns_zone_id=${zoneID}`) as { dns_records?: DNSRecord[] }
      setRecords(result.dns_records || [])
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  useEffect(() => { if (selectedZoneID) void loadRecords(); else setRecords([]) }, [selectedZoneID])
  const createRecord = async () => {
    const zoneID = recordDialogZoneID
    if (!zoneID || !recordDraft.name.trim() || !recordDraft.content.trim()) return
    setWorking('record-save')
    try {
      await client.request(`/dns-records?dns_zone_id=${zoneID}`, { method: 'POST', body: JSON.stringify({ ...recordDraft, enabled: true }) })
      closeRecordDialog()
      if (selectedZoneID === zoneID) await loadRecords(zoneID)
      else setSelectedZoneID(zoneID)
      notify?.('解析记录已创建', 'success')
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const editRecord = async (record: DNSRecord) => {
    const name = await dialogs.prompt({ title: '解析名称', defaultValue: record.name })
    if (name === null) return
    const content = await dialogs.prompt({ title: '解析内容', defaultValue: record.content })
    if (content === null) return
    const comment = await dialogs.prompt({ title: '解析备注', defaultValue: record.comment || '' })
    if (comment === null) return
    try { await client.request(`/dns-records?dns_zone_id=${selectedZoneID}`, { method: 'PATCH', body: JSON.stringify({ ...record, name, content, comment }) }); await loadRecords(); notify?.('解析记录已更新', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const deleteRecord = async (record: DNSRecord) => {
    const ok = await dialogs.confirm({ title: '删除解析记录', message: `${record.type} ${record.name}`, confirmText: '删除', tone: 'danger' })
    if (!ok) return
    try { await client.request(`/dns-records?dns_zone_id=${selectedZoneID}&id=${encodeURIComponent(record.id)}`, { method: 'DELETE' }); await loadRecords(); notify?.('解析记录已删除', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const selectedOption = zoneOptions.find(item => item.zone.id === selectedZoneID)
  const serverName = (id?: number) => servers.find(server => server.id === id)?.name || ''
  return <div className="settings-grid dns-management">
    <div className="settings-tabs dns-management-tabs" role="tablist" aria-label="域名解析视图">
      <button type="button" className={activeTab === 'records' ? 'active' : ''} onClick={() => setActiveTab('records')} role="tab" aria-selected={activeTab === 'records'}><Globe size={15} />域名记录</button>
      <button type="button" className={activeTab === 'settings' ? 'active' : ''} onClick={() => setActiveTab('settings')} role="tab" aria-selected={activeTab === 'settings'}><Settings2 size={15} />解析设置</button>
    </div>
    {activeTab === 'records' ? <section className="settings-card dns-management-card">
      <div className="settings-card-head"><div><h3>域名当前记录</h3><p className="muted">{selectedOption ? `${dnsProviderLabels[selectedOption.credential.provider]} · ${selectedOption.credential.name}` : '先在解析设置中添加域名'}</p></div><div className="settings-card-actions"><button type="button" onClick={openCreateRecord} disabled={!zoneOptions.length}><Plus size={14} />添加记录</button><button type="button" className="ghost icon-button" onClick={() => void loadRecords()} disabled={!selectedZoneID || working === 'records-load'} aria-label="刷新记录" title="刷新记录"><RefreshCw size={15} className={working === 'records-load' ? 'spin' : ''} /></button></div></div>
      <div className="form settings-form">
        <FormField label="域名"><Select value={selectedZoneID} onChange={e => setSelectedZoneID(Number(e.target.value))}><option value={0}>选择域名</option>{zoneOptions.map(({ credential, zone }) => <option key={zone.id} value={zone.id}>{zone.zone_name} · {credential.name}{zone.server_id ? ` · ${serverName(zone.server_id)}` : ''}</option>)}</Select></FormField>
      </div>
      {records.length ? <div className="dns-record-list">{records.map(record => <div className="dns-record-row" key={record.id}><span className="record-type">{record.type}</span><div className="record-main"><strong>{record.name}</strong><span>{record.content}</span><small>{record.comment || `TTL ${record.ttl}`}</small></div><span className={`status-pill ${record.proxied ? 'warning' : ''}`}>{record.proxied ? '已开启代理' : '仅域名解析'}</span><div className="record-actions"><button className="ghost icon-button" onClick={() => editRecord(record)} title="编辑"><Edit3 size={14} /></button><button className="ghost icon-button danger-text" onClick={() => deleteRecord(record)} title="删除"><Trash2 size={14} /></button></div></div>)}</div> : <div className="dns-credential-empty">{selectedZoneID ? '该域名当前没有解析记录。' : '请先选择一个域名。'}</div>}
    </section> : <section className="settings-card dns-management-card">
      <div className="settings-card-head"><div><h3>域名与解析服务商</h3><p className="muted">一个账号可以管理多个域名，并分别关联服务器。</p></div><button className="ghost" onClick={openCreateCredential}><Plus size={14} />新建账号</button></div>
      {credentials.length ? <div className="dns-record-list">{credentials.map(credential => <div className="dns-record-row" key={credential.id}><span className="record-type">{dnsProviderLabels[credential.provider]}</span><div className="record-main"><strong>{credential.name}</strong><span>{(credential.zones || []).map(zone => `${zone.zone_name}${zone.server_id ? `（${serverName(zone.server_id)}）` : ''}`).join(' · ')}</span><small>{credential.last_error || (credential.verified_at ? `${credential.zones.length} 个域名 · 已验证 ${formatTableTime(credential.verified_at)}` : `${credential.zones.length} 个域名 · 待验证`)}</small></div><span className={`status-pill ${credential.verified_at ? 'ok' : credential.last_error ? 'warning' : ''}`}>{credential.verified_at ? '可用' : '待验证'}</span><div className="record-actions"><button className="ghost icon-button" onClick={() => verifyCredential(credential)} title="验证"><RefreshCw size={14} className={working === `verify-${credential.id}` ? 'spin' : ''} /></button><button className="ghost icon-button" onClick={() => editCredential(credential)} title="编辑"><Edit3 size={14} /></button><button className="ghost icon-button danger-text" onClick={() => deleteCredential(credential)} title="删除"><Trash2 size={14} /></button></div></div>)}</div> : <div className="dns-credential-empty">还没有解析服务账号。</div>}
    </section>}
    <AnimatePresence>{recordDialogOpen && <DNSRecordDialog zoneOptions={zoneOptions} zoneID={recordDialogZoneID} setZoneID={setRecordDialogZoneID} draft={recordDraft} setDraft={setRecordDraft} serverName={serverName} saving={working === 'record-save'} onCancel={closeRecordDialog} onSubmit={createRecord} />}</AnimatePresence>
    <AnimatePresence>{credentialDialogOpen && <DNSCredentialDialog draft={draft} setDraft={setDraft} servers={servers} editing={editingID > 0} saving={working === 'credential-save'} onCancel={closeCredentialDialog} onSubmit={saveCredential} />}</AnimatePresence>
  </div>
}

function DNSRecordDialog({ zoneOptions, zoneID, setZoneID, draft, setDraft, serverName, saving, onCancel, onSubmit }: { zoneOptions: { credential: DNSCredential; zone: DNSCredentialZone }[]; zoneID: number; setZoneID: (zoneID: number) => void; draft: ReturnType<typeof emptyDNSRecordDraft>; setDraft: React.Dispatch<React.SetStateAction<ReturnType<typeof emptyDNSRecordDraft>>>; serverName: (serverID?: number) => string; saving: boolean; onCancel: () => void; onSubmit: () => Promise<void> }) {
  const selectedOption = zoneOptions.find(item => item.zone.id === zoneID)
  const update = (patch: Partial<typeof draft>) => setDraft(current => ({ ...current, ...patch }))
  return <MotionDialogPanel onCancel={onCancel} className="dns-record-dialog">
    <header className="dialog-head"><div><h2>添加解析记录</h2><p className="muted">为指定域名创建一条子域名解析。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body"><div className="form server-dialog-form labeled-form">
      <FormField label="域名" required><Select value={zoneID} onChange={e => setZoneID(Number(e.target.value))}><option value={0}>选择域名</option>{zoneOptions.map(({ credential, zone }) => <option key={zone.id} value={zone.id}>{zone.zone_name} · {credential.name}{zone.server_id ? ` · ${serverName(zone.server_id)}` : ''}</option>)}</Select></FormField>
      <FormField label="记录类型" required><Select value={draft.type} onChange={e => update({ type: e.target.value })}>{['A', 'AAAA', 'CNAME', 'TXT'].map(type => <option key={type}>{type}</option>)}</Select></FormField>
      <FormField label="主机记录" required><input value={draft.name} onChange={e => update({ name: e.target.value })} placeholder="entry.example.com" autoCapitalize="none" /></FormField>
      <FormField label="记录值" required><input value={draft.content} onChange={e => update({ content: e.target.value })} autoCapitalize="none" /></FormField>
      <FormField label="解析备注" hint="备注属于这条子域名解析；自动创建时会写入入口和服务器信息。"><input value={draft.comment} maxLength={100} onChange={e => update({ comment: e.target.value })} placeholder="例如：东京入口" /></FormField>
      {selectedOption?.credential.provider === 'cloudflare' && <label className="check-row"><input type="checkbox" checked={draft.proxied} onChange={e => update({ proxied: e.target.checked })} /><span>Cloudflare 代理</span></label>}
    </div></div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={onCancel}>取消</button><button type="button" onClick={() => void onSubmit()} disabled={saving || !zoneID || !draft.name.trim() || !draft.content.trim()}>{saving ? '创建中...' : '添加记录'}</button></footer>
  </MotionDialogPanel>
}

function DNSCredentialDialog({ draft, setDraft, servers, editing, saving, onCancel, onSubmit }: { draft: ReturnType<typeof emptyDNSCredentialDraft>; setDraft: React.Dispatch<React.SetStateAction<any>>; servers: Server[]; editing: boolean; saving: boolean; onCancel: () => void; onSubmit: () => Promise<void> }) {
  const provider = draft.provider as DNSProvider
  const update = (patch: Partial<typeof draft>) => setDraft((current: typeof draft) => ({ ...current, ...patch }))
  const updateZone = (index: number, patch: Record<string, unknown>) => update({ zones: draft.zones.map((zone: any, zoneIndex: number) => zoneIndex === index ? { ...zone, ...patch } : zone) })
  const removeZone = (index: number) => update({ zones: draft.zones.filter((_: any, zoneIndex: number) => zoneIndex !== index) })
  return <MotionDialogPanel onCancel={onCancel} className="dns-credential-dialog">
    <header className="dialog-head"><div><h2>{editing ? '编辑解析设置' : '新建解析设置'}</h2><p className="muted">同一份授权信息可以管理多个域名。</p></div><button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body"><div className="form server-dialog-form labeled-form">
      <div className="form-section-title">账号信息</div>
      <FormField label="账号名称" required hint="用于在面板中识别。"><input value={draft.name} onChange={e => update({ name: e.target.value })} placeholder="例如：生产域名" /></FormField>
      <FormField label="域名服务商" required><Select value={provider} onChange={e => update({ provider: e.target.value as DNSProvider, config: {} })}>{(Object.keys(dnsProviderLabels) as DNSProvider[]).map(item => <option key={item} value={item}>{dnsProviderLabels[item]}</option>)}</Select></FormField>
      <div className="form-section-title">授权信息</div>
      {dnsProviderFields[provider].map(field => <FormField key={field.key} label={field.label} required={!editing && !field.optional} hint={editing ? '留空则不修改已有信息。' : field.optional ? '可选。' : undefined}><input type={field.key === 'region' || field.key.endsWith('_id') || field.key === 'username' || field.key === 'domain_name' ? 'text' : 'password'} autoComplete="off" value={draft.config?.[field.key] || ''} onChange={e => update({ config: { ...draft.config, [field.key]: e.target.value } })} /></FormField>)}
      <div className="form-section-title dns-zone-section-title"><span>域名绑定</span><button className="ghost" type="button" onClick={() => update({ zones: [...draft.zones, emptyDNSZoneDraft()] })}><Plus size={14} />添加域名</button></div>
      <div className="dns-zone-editor">{draft.zones.map((zone: any, index: number) => <div className="dns-zone-editor-row" key={zone.id || `new-${index}`}>
        <div className="dns-zone-editor-head"><strong>域名 {index + 1}</strong>{draft.zones.length > 1 && <button className="ghost icon-button danger-text" type="button" onClick={() => removeZone(index)} title="移除域名"><Trash2 size={14} /></button>}</div>
        <FormField label="主域名" required hint="例如 oboard.proxy"><input value={zone.zone_name} onChange={e => updateZone(index, { zone_name: e.target.value })} placeholder="example.com" autoCapitalize="none" /></FormField>
        {(provider === 'cloudflare' || provider === 'tencent_esa' || provider === 'huawei_cloud') && <FormField label="Zone ID" required={provider === 'tencent_esa'} hint={provider === 'tencent_esa' ? '服务商要求填写。' : '留空时自动查找。'}><input value={zone.provider_zone_id} onChange={e => updateZone(index, { provider_zone_id: e.target.value })} autoComplete="off" /></FormField>}
        <FormField className="dns-zone-server-field" label="关联服务器" hint="用于维护识别和自动解析匹配。"><Select value={Number(zone.server_id || 0)} onChange={e => updateZone(index, { server_id: Number(e.target.value) })}><option value={0}>不指定服务器</option>{servers.map(server => <option key={server.id} value={server.id}>{server.name}</option>)}</Select></FormField>
      </div>)}</div>
    </div></div>
    <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={() => void onSubmit()} disabled={saving}>{saving ? '保存中...' : editing ? '保存修改' : '创建账号'}</button></footer>
  </MotionDialogPanel>
}

function defaultCertificateAccountEmail(rawDomains: unknown) {
  const domain = String(rawDomains || '').split(/[\s,]+/).map(item => item.trim().toLowerCase().replace(/\.$/, '')).find(Boolean)?.replace(/^\*\./, '') || ''
  return domain ? `admin@${domain}` : ''
}

function CertificateLogDialog({ certificate, onClose }: { certificate: Certificate; onClose: () => void }) {
  const log = String(certificate.last_error || '').trim()
  return <MotionDialogPanel onCancel={onClose} className="certificate-log-dialog">
    <header className="dialog-head"><div><h2>签发日志</h2><p className="muted">{certificate.name} · {certificate.domains.join(' · ')}</p></div><button type="button" className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body certificate-log-body">
      <div className="certificate-log-meta"><span className={`status-pill ${certificate.status === 'ready' ? 'ok' : certificate.status === 'failed' ? 'warning' : ''}`}>{certificateLabelValue(certificate.status)}</span><span>{certificate.last_renewal_attempt_at ? `最近尝试：${formatTableTime(certificate.last_renewal_attempt_at)}` : '尚未开始签发'}</span></div>
      {log ? <div className="raw-log-copy"><CopyBlock value={log} /></div> : <div className="certificate-log-empty"><FileText size={20} /><span>最近一次签发没有可查看的错误日志。</span></div>}
    </div>
    <footer className="dialog-actions"><button type="button" onClick={onClose}>关闭</button></footer>
  </MotionDialogPanel>
}

function CertificateEABDialog({ keyID, hmacKey, remark, retain, configured, secretRequired, credentials, saving, deletingID, onChange, onSelectCredential, onDeleteCredential, onCancel, onSubmit }: { keyID: string; hmacKey: string; remark: string; retain: boolean; configured: boolean; secretRequired: boolean; credentials: GoogleEABCredential[]; saving: boolean; deletingID: number; onChange: (patch: { keyID?: string; hmacKey?: string; remark?: string; retain?: boolean }) => void; onSelectCredential: (credential: GoogleEABCredential) => void; onDeleteCredential: (credential: GoogleEABCredential) => void; onCancel: () => void; onSubmit: () => void }) {
  return <MotionDialogPanel onCancel={onCancel} className="certificate-eab-dialog">
    <header className="dialog-head"><div><h2>填写 Google EAB</h2><p className="muted">连接您的 Google Cloud 公共 CA 账号</p></div><button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body">
      <div className="certificate-eab-guide"><KeyRound size={20} /><div><strong>需要从 Google 获取两项信息</strong><p>Google Trust Services 要求先完成外部账号绑定。请打开 Google 官方页面，按页面指引获取 Key ID 和 HMAC Key。</p><a href="https://cloud.google.com/certificate-manager/docs/public-ca-tutorial?hl=zh-cn#request-key-hmac" target="_blank" rel="noreferrer">打开 Google 官方获取页面<ExternalLink size={14} /></a></div></div>
      <div className="certificate-eab-secret-note">HMAC Key 是敏感信息，请在获取后尽快使用。提交后 OBoard 不会再次展示密钥。</div>
      <div className="form server-dialog-form labeled-form">
        <FormField label="Key ID（密钥编号）" required hint="粘贴 Google 返回的 keyId。"><input value={keyID} onChange={event => onChange({ keyID: event.target.value })} autoComplete="off" spellCheck={false} /></FormField>
        <FormField label="HMAC Key（绑定密钥）" required={secretRequired} hint={retain && configured ? '长期保存需要重新填写 HMAC Key，原密钥无法再次查看。' : configured && !secretRequired ? '已保存。留空则保持当前值。' : configured ? '更换 Key ID 时，需要同时填写新的 HMAC Key。' : '粘贴 Google 返回的 b64MacKey。'}><input type="password" value={hmacKey} onChange={event => onChange({ hmacKey: event.target.value })} autoComplete="new-password" spellCheck={false} placeholder={configured && !secretRequired ? '留空保持当前值' : ''} /></FormField>
        <label className="check-row"><input type="checkbox" checked={retain} onChange={event => onChange({ retain: event.target.checked })} /><span>保存到 EAB 列表，供以后签发使用</span></label>
        {retain && <FormField label="备注" hint="只用于区分不同 EAB。"><input value={remark} maxLength={120} onChange={event => onChange({ remark: event.target.value })} placeholder="例如：生产账号" /></FormField>}
      </div>
      <div className="certificate-eab-saved">
        <div className="certificate-eab-saved-head"><strong>已保存的 EAB</strong><span>{credentials.length} 项</span></div>
        {credentials.length ? <div className="certificate-eab-saved-list">{credentials.map(credential => <div className="certificate-eab-saved-row" key={credential.id}>
          <div><strong>{credential.key_id}</strong><span>{credential.remark || '无备注'} · 创建于 {formatTableTime(credential.created_at)}</span></div>
          <div className="record-actions"><button type="button" className="ghost" onClick={() => onSelectCredential(credential)} disabled={saving}>使用</button><button type="button" className="ghost icon-button danger-text" onClick={() => onDeleteCredential(credential)} disabled={deletingID === credential.id} title="删除已保存的 EAB" aria-label="删除已保存的 EAB"><Trash2 size={14} /></button></div>
        </div>)}</div> : <div className="certificate-eab-saved-empty">还没有保存的 EAB</div>}
      </div>
    </div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={onCancel}>取消</button><button type="button" onClick={onSubmit} disabled={saving || !keyID || (secretRequired && !hmacKey)}>{saving ? '保存中...' : retain ? '保存并使用' : configured ? '保存 EAB' : '仅用于此证书'}</button></footer>
  </MotionDialogPanel>
}

function CertificateSettings({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
  const certificates: Certificate[] = data.certificates || []
  const credentials: DNSCredential[] = data.dns_credentials || []
  const servers: Server[] = data.servers || []
  const [draft, setDraft] = useState<any>({ name: '', domains: '', challenge_type: 'dns01', dns_credential_id: 0, issuance_server_id: 0, acme_ca: 'letsencrypt', account_email: '', google_eab_credential_id: 0, eab_key_id: '', eab_hmac_key: '', auto_renew: true })
  const [eabCredentials, setEABCredentials] = useState<GoogleEABCredential[]>(data.google_eab_credentials || [])
  const [autoMatch, setAutoMatch] = useState(data.settings?.certificate_auto_match_enabled !== false && data.settings?.certificate_auto_match_enabled !== 'false')
  const [preference, setPreference] = useState(data.settings?.certificate_default_preference === 'wildcard' ? 'wildcard' : 'subdomain')
  const [working, setWorking] = useState('')
  const [importDraft, setImportDraft] = useState({ name: '', certificate_pem: '', fullchain_pem: '', private_key_pem: '' })
  const [eabTarget, setEABTarget] = useState<'draft' | Certificate | null>(null)
  const [eabDraft, setEABDraft] = useState({ keyID: '', hmacKey: '', remark: '', retain: false })
  const [logCertificate, setLogCertificate] = useState<Certificate | null>(null)
  useEffect(() => { setAutoMatch(data.settings?.certificate_auto_match_enabled !== false && data.settings?.certificate_auto_match_enabled !== 'false'); setPreference(data.settings?.certificate_default_preference === 'wildcard' ? 'wildcard' : 'subdomain') }, [data.settings])
  useEffect(() => { setEABCredentials(data.google_eab_credentials || []) }, [data.google_eab_credentials])
  const createCertificate = async () => {
    const domains = String(draft.domains).split(/[\s,]+/).map(item => item.trim()).filter(Boolean)
    if (!draft.name.trim() || !domains.length) return
    const payload: any = { ...draft, domains, dns_credential_id: draft.challenge_type === 'dns01' ? Number(draft.dns_credential_id || 0) : undefined, issuance_server_id: draft.challenge_type === 'http01' ? Number(draft.issuance_server_id || 0) : undefined }
    setWorking('create')
    try { await client.request('/certificates', { method: 'POST', body: JSON.stringify(payload) }); setDraft({ ...draft, name: '', domains: '', account_email: '', google_eab_credential_id: 0, eab_key_id: '', eab_hmac_key: '' }); await load(); notify?.('证书申请已创建', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const certificateAction = async (certificate: Certificate, action: 'issue' | 'renew' | 'confirm-dns') => {
    setWorking(`${action}-${certificate.id}`)
    try { await client.request(`/certificates/${certificate.id}/${action}`, { method: 'POST', body: '{}' }); await load(); notify?.(action === 'confirm-dns' ? 'DNS 验证已继续' : action === 'renew' ? '续签任务已提交' : '签发任务已提交', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const refreshCertificateStatus = async (certificate: Certificate) => {
    setWorking(`refresh-${certificate.id}`)
    try {
      await client.request(`/certificates/${certificate.id}`)
      await load(undefined, { background: true })
      notify?.(`${certificate.name} 状态已刷新`, 'success')
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const deleteCertificate = async (certificate: Certificate) => {
    const ok = await dialogs.confirm({ title: '删除证书', message: `确认删除 ${certificate.name}？`, confirmText: '删除', tone: 'danger' })
    if (!ok) return
    try { await client.request(`/certificates/${certificate.id}`, { method: 'DELETE' }); await load(); notify?.('证书已删除', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const saveMatching = async () => {
    try { await client.request('/settings', { method: 'POST', body: JSON.stringify({ certificate_auto_match_enabled: autoMatch, certificate_default_preference: preference }) }); await load(); notify?.('证书匹配设置已保存', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const importCertificate = async () => {
    if (!importDraft.certificate_pem.trim() || !importDraft.private_key_pem.trim()) return
    try { await client.request('/certificates/import', { method: 'POST', body: JSON.stringify(importDraft) }); setImportDraft({ name: '', certificate_pem: '', fullchain_pem: '', private_key_pem: '' }); await load(); notify?.('证书已导入', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const openDraftEAB = () => {
    setEABDraft({ keyID: String(draft.eab_key_id || ''), hmacKey: String(draft.eab_hmac_key || ''), remark: '', retain: false })
    setEABTarget('draft')
  }
  const openCertificateEAB = (certificate: Certificate) => {
    setEABDraft({ keyID: certificate.google_eab_credential_id ? '' : certificate.eab_key_id || '', hmacKey: '', remark: '', retain: false })
    setEABTarget(certificate)
  }
  const closeEAB = () => {
    setEABTarget(null)
    setEABDraft({ keyID: '', hmacKey: '', remark: '', retain: false })
  }
  const existingDirectEAB = Boolean(eabTarget && eabTarget !== 'draft' && !eabTarget.google_eab_credential_id && eabTarget.eab_configured)
  const eabSecretRequired = eabDraft.retain || eabTarget === 'draft' || Boolean(eabTarget && (!existingDirectEAB || eabDraft.keyID !== (eabTarget.eab_key_id || '')))
  const selectSavedEAB = async (credential: GoogleEABCredential) => {
    if (eabTarget === 'draft') {
      setDraft((current: any) => ({ ...current, google_eab_credential_id: credential.id, eab_key_id: '', eab_hmac_key: '' }))
      closeEAB()
      return
    }
    if (!eabTarget) return
    setWorking(`eab-select-${eabTarget.id}`)
    try {
      await client.request(`/certificates/${eabTarget.id}`, { method: 'PATCH', body: JSON.stringify({ google_eab_credential_id: credential.id }) })
      closeEAB()
      await load()
      notify?.(`已使用 EAB ${credential.key_id}`, 'success')
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const saveEAB = async () => {
    if (!eabDraft.keyID || (eabSecretRequired && !eabDraft.hmacKey)) return
    setWorking(eabTarget === 'draft' ? 'eab-save-draft' : `eab-save-${eabTarget?.id || 0}`)
    try {
      let credentialID = 0
      if (eabDraft.retain) {
        const result = await client.request('/google-eab-credentials', { method: 'POST', body: JSON.stringify({ key_id: eabDraft.keyID, hmac_key: eabDraft.hmacKey, remark: eabDraft.remark }) })
        const credential = result.google_eab_credential as GoogleEABCredential
        credentialID = credential.id
        setEABCredentials(current => [credential, ...current.filter(item => item.id !== credential.id)])
      }
      if (eabTarget === 'draft') {
        setDraft((current: any) => credentialID
          ? { ...current, google_eab_credential_id: credentialID, eab_key_id: '', eab_hmac_key: '' }
          : { ...current, google_eab_credential_id: 0, eab_key_id: eabDraft.keyID, eab_hmac_key: eabDraft.hmacKey })
        closeEAB()
        if (credentialID) await load(undefined, { background: true })
        return
      }
      if (!eabTarget) return
      const payload: Record<string, string | number> = credentialID
        ? { google_eab_credential_id: credentialID }
        : { google_eab_credential_id: 0, eab_key_id: eabDraft.keyID }
      if (!credentialID && eabDraft.hmacKey) payload.eab_hmac_key = eabDraft.hmacKey
      await client.request(`/certificates/${eabTarget.id}`, { method: 'PATCH', body: JSON.stringify(payload) })
      closeEAB()
      await load()
      notify?.(credentialID ? 'Google EAB 已保存并应用' : 'Google EAB 已应用', 'success')
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const deleteSavedEAB = async (credential: GoogleEABCredential) => {
    const ok = await dialogs.confirm({ title: '删除已保存的 EAB', message: `确认删除 ${credential.key_id}？HMAC Key 无法找回。`, confirmText: '删除', tone: 'danger' })
    if (!ok) return
    setWorking(`eab-delete-${credential.id}`)
    try {
      await client.request(`/google-eab-credentials/${credential.id}`, { method: 'DELETE' })
      setEABCredentials(current => current.filter(item => item.id !== credential.id))
      setDraft((current: any) => Number(current.google_eab_credential_id || 0) === credential.id ? { ...current, google_eab_credential_id: 0 } : current)
      await load(undefined, { background: true })
      notify?.('已删除保存的 Google EAB', 'success')
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const draftDirectEABConfigured = Boolean(draft.eab_key_id && draft.eab_hmac_key)
  const draftEABConfigured = Boolean(Number(draft.google_eab_credential_id || 0) > 0 || draftDirectEABConfigured)
  const draftEABSelection = Number(draft.google_eab_credential_id || 0) > 0 ? String(draft.google_eab_credential_id) : draftDirectEABConfigured ? 'direct' : ''
  const googleHTTPUnsupported = draft.acme_ca === 'google' && draft.challenge_type === 'http01'
  return <div className="settings-grid">
    <section className="settings-card">
      <div className="settings-card-head"><div><h3>申请证书</h3><p className="muted">HTTP-01、面板 DNS 或手动 DNS</p></div></div>
      <div className="form settings-form">
        <FormField label="名称"><input value={draft.name} onChange={e => setDraft({ ...draft, name: e.target.value })} /></FormField>
        <FormField label="域名" hint="多个域名用逗号分隔"><input value={draft.domains} onChange={e => { const domains = e.target.value; const previousDefault = defaultCertificateAccountEmail(draft.domains); setDraft({ ...draft, domains, account_email: !draft.account_email || draft.account_email === previousDefault ? defaultCertificateAccountEmail(domains) : draft.account_email }) }} placeholder="example.com, *.example.com" /></FormField>
        <FormField label="验证方式"><Select value={draft.challenge_type} onChange={e => setDraft({ ...draft, challenge_type: e.target.value })}><option value="dns01">面板 DNS-01</option><option value="dns01_manual">手动 DNS-01</option><option value="http01">Agent HTTP-01</option></Select></FormField>
        {draft.challenge_type === 'dns01' && <FormField label="域名服务账号"><Select value={draft.dns_credential_id} onChange={e => setDraft({ ...draft, dns_credential_id: Number(e.target.value) })}><option value={0}>选择账号</option>{credentials.filter(item => item.verified_at).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</Select></FormField>}
        {draft.challenge_type === 'http01' && <FormField label="签发服务器"><Select value={draft.issuance_server_id} onChange={e => setDraft({ ...draft, issuance_server_id: Number(e.target.value) })}><option value={0}>选择服务器</option>{servers.map(server => <option key={server.id} value={server.id}>{server.name}</option>)}</Select></FormField>}
        <FormField label="ACME CA"><Select value={draft.acme_ca} onChange={e => { const acmeCA = e.target.value; setDraft({ ...draft, acme_ca: acmeCA, ...(acmeCA === 'google' ? {} : { google_eab_credential_id: 0, eab_key_id: '', eab_hmac_key: '' }) }) }}><option value="letsencrypt">Let's Encrypt</option><option value="zerossl">ZeroSSL</option><option value="buypass">Buypass</option><option value="google">Google Trust Services</option></Select></FormField>
        {draft.acme_ca === 'google' && <div className="certificate-eab-row"><div className="certificate-eab-state"><KeyRound size={16} /><span><strong>Google EAB</strong><small>{draftEABConfigured ? '已配置，可用于本次签发' : '请选择已保存的 EAB，或填写新的 EAB'}</small></span></div><div className="certificate-eab-controls"><Select value={draftEABSelection} onChange={event => {
          const value = event.target.value
          if (value === 'direct') setDraft({ ...draft, google_eab_credential_id: 0 })
          else if (Number(value) > 0) setDraft({ ...draft, google_eab_credential_id: Number(value), eab_key_id: '', eab_hmac_key: '' })
          else setDraft({ ...draft, google_eab_credential_id: 0, eab_key_id: '', eab_hmac_key: '' })
        }}><option value="">选择已保存的 EAB</option>{draftDirectEABConfigured && <option value="direct">{draft.eab_key_id} · 仅本次使用</option>}{eabCredentials.map(credential => <option key={credential.id} value={credential.id}>{credential.key_id}{credential.remark ? ` · ${credential.remark}` : ''}</option>)}</Select><button type="button" className="ghost" onClick={openDraftEAB}><Plus size={14} />新增 EAB</button></div></div>}
        {googleHTTPUnsupported && <div className="certificate-eab-warning">Google EAB 暂不支持 Agent HTTP-01，请选择面板 DNS-01 或手动 DNS-01。</div>}
        <FormField label="账户邮箱"><input type="email" value={draft.account_email} onChange={e => setDraft({ ...draft, account_email: e.target.value })} placeholder={defaultCertificateAccountEmail(draft.domains) || 'admin@example.com'} /></FormField>
        <label className="check-row"><input type="checkbox" checked={draft.auto_renew} onChange={e => setDraft({ ...draft, auto_renew: e.target.checked })} /><span>自动续期</span></label>
        <button onClick={createCertificate} disabled={working === 'create' || googleHTTPUnsupported || (draft.acme_ca === 'google' && !draftEABConfigured)}>{working === 'create' ? '创建中...' : '创建申请'}</button>
      </div>
      <details className="advanced-config"><summary>导入现有证书</summary><div className="form settings-form"><FormField label="名称"><input value={importDraft.name} onChange={e => setImportDraft({ ...importDraft, name: e.target.value })} /></FormField><FormField label="证书 PEM"><textarea rows={4} value={importDraft.certificate_pem} onChange={e => setImportDraft({ ...importDraft, certificate_pem: e.target.value })} /></FormField><FormField label="完整链 PEM"><textarea rows={4} value={importDraft.fullchain_pem} onChange={e => setImportDraft({ ...importDraft, fullchain_pem: e.target.value })} /></FormField><FormField label="私钥 PEM"><textarea rows={4} value={importDraft.private_key_pem} onChange={e => setImportDraft({ ...importDraft, private_key_pem: e.target.value })} /></FormField><button onClick={importCertificate}>导入</button></div></details>
    </section>
    <section className="settings-card">
      <div className="settings-card-head"><div><h3>自动匹配</h3><p className="muted">入口域名的全局默认策略</p></div><button onClick={saveMatching}>保存</button></div>
      <div className="form settings-form"><label className="check-row"><input type="checkbox" checked={autoMatch} onChange={e => setAutoMatch(e.target.checked)} /><span>启用自动匹配</span></label><FormField label="默认优先级"><Select variant="segmented" value={preference} onChange={e => setPreference(e.target.value)}><option value="subdomain">精确子域证书</option><option value="wildcard">泛域名证书</option></Select></FormField></div>
      <div className="dns-record-list">{certificates.map(certificate => {
        const ready = certificate.status === 'ready'
        const issueAction = ready ? 'renew' : 'issue'
        const issueLabel = ready ? '续签证书' : '签发证书'
        const issueWorking = working === `${issueAction}-${certificate.id}`
        const refreshWorking = working === `refresh-${certificate.id}`
        return <div className="dns-record-row" key={certificate.id}>
          <span className="record-type">{certificate.wildcard ? 'WILD' : 'TLS'}</span>
          <div className="record-main"><strong>{certificate.name}</strong><span>{certificate.domains.join(' · ')}</span><small>{certificate.last_error || (certificate.not_after ? `有效至 ${formatTableTime(certificate.not_after)}` : certificateLabelValue(certificate.challenge_type))}</small>{certificate.status === 'awaiting_dns' && (certificate.validation_records || []).map(record => <code key={`${record.name}-${record.content}`}>{record.name} TXT {record.content}</code>)}</div>
          <span className={`status-pill ${ready ? 'ok' : certificate.status === 'failed' ? 'warning' : ''}`}>{certificateLabelValue(certificate.status)}</span>
          <div className="record-actions">
            <button type="button" className="ghost icon-button" onClick={() => void refreshCertificateStatus(certificate)} disabled={refreshWorking} title="刷新证书状态" aria-label="刷新证书状态"><RefreshCw size={14} className={refreshWorking ? 'spin' : ''} /></button>
            <button type="button" className="ghost icon-button" onClick={() => setLogCertificate(certificate)} title="查看签发日志" aria-label="查看签发日志"><FileText size={14} /></button>
            {certificate.acme_ca === 'google' && <button type="button" className="ghost icon-button" onClick={() => openCertificateEAB(certificate)} title={certificate.eab_configured ? '更新 Google EAB' : '填写 Google EAB'} aria-label={certificate.eab_configured ? '更新 Google EAB' : '填写 Google EAB'}><KeyRound size={14} /></button>}
            {certificate.status === 'awaiting_dns' ? <button type="button" className="ghost" onClick={() => void certificateAction(certificate, 'confirm-dns')} disabled={working === `confirm-dns-${certificate.id}`}>已解析</button> : certificate.challenge_type !== 'imported' ? <button type="button" className="ghost icon-button" onClick={() => void certificateAction(certificate, issueAction)} disabled={issueWorking || certificate.status === 'issuing'} title={issueLabel} aria-label={issueLabel}>{ready ? <CalendarSync size={14} className={issueWorking ? 'spin' : ''} /> : <BadgeCheck size={14} />}</button> : null}
            <button type="button" className="ghost icon-button danger-text" onClick={() => void deleteCertificate(certificate)} title="删除证书" aria-label="删除证书"><Trash2 size={14} /></button>
          </div>
        </div>
      })}</div>
    </section>
    <AnimatePresence>{logCertificate && <CertificateLogDialog certificate={logCertificate} onClose={() => setLogCertificate(null)} />}</AnimatePresence>
    <AnimatePresence>{eabTarget && <CertificateEABDialog keyID={eabDraft.keyID} hmacKey={eabDraft.hmacKey} remark={eabDraft.remark} retain={eabDraft.retain} configured={existingDirectEAB} secretRequired={eabSecretRequired} credentials={eabCredentials} saving={working.startsWith('eab-') && !working.startsWith('eab-delete-')} deletingID={working.startsWith('eab-delete-') ? Number(working.slice('eab-delete-'.length)) : 0} onChange={patch => setEABDraft(current => ({ ...current, ...patch }))} onSelectCredential={credential => void selectSavedEAB(credential)} onDeleteCredential={credential => void deleteSavedEAB(credential)} onCancel={closeEAB} onSubmit={() => void saveEAB()} />}</AnimatePresence>
  </div>
}

function ControllerLogsPanel({ client, dialogs, notify, maxMB, backups, setMaxMB, setBackups, saving, onSave }: any) {
  const [lines, setLines] = useState(500)
  const [query, setQuery] = useState('')
  const [snapshot, setSnapshot] = useState<any>(null)
  const [loading, setLoading] = useState(false)
  const [working, setWorking] = useState('')
  const loadLogs = async () => {
    setLoading(true)
    try {
      const params = new URLSearchParams({ lines: String(lines) })
      if (query.trim()) params.set('q', query.trim())
      const result = await client.request(`/system-logs?${params.toString()}`)
      setSnapshot(result.logs || null)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { void loadLogs() }, [])
  const operate = async (action: 'rotate' | 'clear') => {
    if (action === 'clear') {
      const ok = await dialogs.confirm({ title: '清空主控日志', message: '当前日志和所有轮转备份都会被删除，此操作不能撤销。', confirmText: '清空日志', tone: 'danger' })
      if (!ok) return
    }
    setWorking(action)
    try {
      await client.request('/system-logs', action === 'clear' ? { method: 'DELETE' } : { method: 'POST', body: JSON.stringify({ action: 'rotate' }) })
      notify?.(action === 'clear' ? '主控日志已清空' : '主控日志已轮转', 'success')
      await loadLogs()
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const downloadLogs = async () => {
    setWorking('download')
    try {
      const file = await client.download('/system-logs/download')
      const url = URL.createObjectURL(file.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = file.filename
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const content = String(snapshot?.content || '')
  return <section className="settings-card controller-logs-card">
    <div className="settings-card-head"><div><h3>主控运行日志</h3><p className="muted">查看 API、后台任务和运行错误。日志会自动脱敏并按大小轮转。</p></div><span className="status-pill">{formatBytes(Number(snapshot?.total_size_bytes || 0))}</span></div>
    <div className="controller-log-policy">
      <FormField label="单个日志上限" hint="1-1024 MB"><input type="number" min={1} max={1024} value={maxMB} onChange={e => setMaxMB(Math.max(1, Math.min(1024, Number(e.target.value) || 1)))} /></FormField>
      <FormField label="保留备份数" hint="0-20 个"><input type="number" min={0} max={20} value={backups} onChange={e => setBackups(Math.max(0, Math.min(20, Number(e.target.value) || 0)))} /></FormField>
      <button onClick={onSave} disabled={saving}>{saving ? '保存中...' : '保存策略'}</button>
    </div>
    <div className="controller-log-toolbar">
      <label className="log-search"><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') void loadLogs() }} placeholder="搜索日志内容" /></label>
      <Select value={lines} onChange={e => setLines(Number(e.target.value))}><option value={200}>最近 200 行</option><option value={500}>最近 500 行</option><option value={1000}>最近 1000 行</option><option value={2000}>最近 2000 行</option><option value={5000}>最近 5000 行</option></Select>
      <button className="ghost" onClick={loadLogs} disabled={loading}><RefreshCw size={15} className={loading ? 'spin' : ''} />刷新</button>
      <button className="ghost" onClick={() => copyText(content)} disabled={!content}><Copy size={15} />复制</button>
      <button className="ghost" onClick={downloadLogs} disabled={Boolean(working)}><Download size={15} />下载</button>
      <button className="ghost" onClick={() => operate('rotate')} disabled={Boolean(working)}><RefreshCw size={15} />轮转</button>
      <button className="ghost danger-text" onClick={() => operate('clear')} disabled={Boolean(working)}><Eraser size={15} />清空</button>
    </div>
    <div className="controller-log-meta"><span>{snapshot?.line_count || 0} 行</span><span>{snapshot?.files?.length || 0} 个文件</span><span>上限 {snapshot?.max_size_bytes ? formatBytes(snapshot.max_size_bytes) : `${maxMB} MB`}</span></div>
    <pre className="controller-log-output">{loading && !snapshot ? '正在读取日志...' : content || '暂无日志'}</pre>
  </section>
}

function AuditConsole({ data, client, loading }: any) {
  const [view, setView] = useState<'connections' | 'operations'>('connections')
  const [windowHours, setWindowHours] = useState(24)
  const [risk, setRisk] = useState<'all' | AuditRiskLevel>('all')
  const [query, setQuery] = useState('')
  const [overview, setOverview] = useState<ConnectionAuditOverview | null>(data.connection_audit || null)
  const [refreshing, setRefreshing] = useState(false)
  const [refreshRevision, setRefreshRevision] = useState(0)
  const [loadError, setLoadError] = useState('')
  const [detail, setDetail] = useState<ConnectionAuditUserDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)

  useEffect(() => {
    let cancelled = false
    setRefreshing(true)
    setLoadError('')
    client.request(`/audit/overview?window_hours=${windowHours}`).then((res: any) => {
      if (!cancelled) setOverview(res.connection_audit || null)
    }).catch((error: any) => {
      if (!cancelled) setLoadError(localizeErrorMessage(error?.message || error))
    }).finally(() => { if (!cancelled) setRefreshing(false) })
    return () => { cancelled = true }
  }, [client, windowHours, refreshRevision])

  const users = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return (overview?.users || []).filter(item => {
      if (risk !== 'all' && item.risk_level !== risk) return false
      return !needle || item.username.toLowerCase().includes(needle) || String(item.nickname || '').toLowerCase().includes(needle)
    })
  }, [overview, query, risk])
  const openUser = async (user: ConnectionAuditUser) => {
    setDetailLoading(true)
    setLoadError('')
    try {
      const res = await client.request(`/audit/users/${user.user_id}?window_hours=${windowHours}`)
      setDetail(res.connection_audit_user || null)
    } catch (error: any) {
      setLoadError(localizeErrorMessage(error?.message || error))
    } finally {
      setDetailLoading(false)
    }
  }
  const enabled = Number(overview?.enabled_server_count || 0)
  return <Panel title="审计台" className="audit-console-panel">
    <div className="audit-console-tabs" role="tablist" aria-label="审计视图">
      <button type="button" role="tab" aria-selected={view === 'connections'} className={view === 'connections' ? 'active' : ''} onClick={() => setView('connections')}><Shield size={15} />连接风险</button>
      <button type="button" role="tab" aria-selected={view === 'operations'} className={view === 'operations' ? 'active' : ''} onClick={() => setView('operations')}><ClipboardList size={15} />操作日志</button>
    </div>
    {view === 'operations' ? <AuditLogs data={data} loading={loading} embedded /> : <>
      <div className="audit-overview-grid">
        <div><span>启用服务器</span><strong>{enabled}</strong><small>{enabled ? '正在接收摘要' : '全部关闭'}</small></div>
        <div><span>活跃用户</span><strong>{overview?.reporting_user_count || 0}</strong><small>{windowHours} 小时窗口</small></div>
        <div><span>高风险用户</span><strong>{overview?.elevated_risk_count || 0}</strong><small>高风险与严重</small></div>
        <div><span>来源 IP</span><strong>{overview?.unique_source_ips || 0}</strong><small>{formatCompactAuditNumber(overview?.total_connections || 0)} 次连接</small></div>
      </div>
      <div className="audit-console-toolbar">
        <label className="log-search"><Search size={15} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索用户" /></label>
        <Select value={String(windowHours)} onChange={event => setWindowHours(Number(event.target.value))} aria-label="审计时间窗口"><option value={1}>最近 1 小时</option><option value={24}>最近 24 小时</option><option value={168}>最近 7 天</option><option value={720}>最近 30 天</option></Select>
        <Select value={risk} onChange={event => setRisk(event.target.value as 'all' | AuditRiskLevel)} aria-label="风险等级"><option value="all">全部风险</option><option value="critical">严重</option><option value="high">高风险</option><option value="medium">中风险</option><option value="low">低风险</option></Select>
        <button type="button" className="ghost icon-button" onClick={() => setRefreshRevision(value => value + 1)} disabled={refreshing} aria-label="刷新审计数据" title="刷新"><RefreshCw size={15} className={refreshing ? 'spin' : ''} /></button>
      </div>
      {loadError ? <p className="form-error">{loadError}</p> : null}
	  {!enabled && (overview?.reporting_user_count || 0) > 0 ? <div className="audit-paused-notice"><Shield size={16} /><span>当前没有服务器继续采集，以下为已保存的历史摘要。</span></div> : null}
	  {refreshing && !overview ? <TableSkeleton /> : !enabled && !(overview?.reporting_user_count || 0) ? <div className="audit-disabled-state"><Shield size={24} /><strong>连接审计未启用</strong><span>可在创建或编辑服务器时开启。</span></div> : !users.length ? <p className="muted">当前筛选条件下暂无连接审计数据</p> : <div className="audit-user-table-wrap">
        <table className="audit-user-table"><thead><tr><th>用户</th><th>风险</th><th>来源</th><th>服务器</th><th>连接 / 峰值</th><th>最后活动</th><th aria-label="操作" /></tr></thead><tbody>
          {users.map(user => <tr key={user.user_id}>
            <td><strong>{user.nickname || user.username}</strong><span>{user.nickname ? user.username : `用户 #${user.user_id}`}</span></td>
            <td><span className={`audit-risk-pill ${user.risk_level}`}>{auditRiskLabel(user.risk_level)} · {user.risk_score}</span>{user.risk_signals?.[0] ? <small>{user.risk_signals[0]}</small> : null}</td>
            <td><strong>{user.source_ip_count}</strong><span>{user.source_subnet_count} 个网段</span>{user.shared_source_ip_count ? <small>{user.shared_source_ip_count} 个共享出口 IP</small> : null}</td>
            <td><strong>{user.server_count}</strong><span>台</span></td>
            <td><strong>{formatCompactAuditNumber(user.connection_count)}</strong><span>峰值 {user.active_peak}</span></td>
            <td>{formatTableTime(user.last_seen_at)}</td>
            <td><button type="button" className="ghost" onClick={() => void openUser(user)} disabled={detailLoading}>查看</button></td>
          </tr>)}
        </tbody></table>
      </div>}
    </>}
    <AnimatePresence>{detail && <ConnectionAuditUserDialog detail={detail} onClose={() => setDetail(null)} />}</AnimatePresence>
  </Panel>
}

function auditRiskLabel(level: AuditRiskLevel) {
  return ({ low: '低风险', medium: '中风险', high: '高风险', critical: '严重' } as Record<AuditRiskLevel, string>)[level] || level
}

function formatCompactAuditNumber(value: number) {
  return new Intl.NumberFormat('zh-CN', { notation: value >= 10000 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(Number(value || 0))
}

function ConnectionAuditUserDialog({ detail, onClose }: { detail: ConnectionAuditUserDetail; onClose: () => void }) {
  const user = detail.summary
  return <MotionDialogPanel onCancel={onClose} className="audit-detail-dialog">
    <header className="dialog-head"><div><h2>{user.nickname || user.username}</h2><p className="muted">{user.username} · {user.connection_count} 次连接 · 并发峰值 {user.active_peak}</p></div><button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body audit-detail-body">
      <div className="audit-detail-risk"><span className={`audit-risk-pill ${user.risk_level}`}>{auditRiskLabel(user.risk_level)} · {user.risk_score}</span><div>{user.risk_signals?.length ? user.risk_signals.map(signal => <span key={signal}>{signal}</span>) : <span>当前窗口未发现显著共享特征</span>}</div></div>
      <div className="audit-dimension-grid">
        <AuditDimensionList title="来源 IP" items={detail.sources} />
        <AuditDimensionList title="目标" items={detail.destinations} />
        <AuditDimensionList title="出口" items={detail.outbounds} />
        <AuditDimensionList title="服务器" items={detail.servers} />
      </div>
      <div className="audit-recent-head"><h3>最近连接摘要</h3><span>{detail.recent?.length || 0} 条</span></div>
      <div className="audit-recent-list">{(detail.recent || []).map(item => <div key={item.report_id}><code>{item.source_ip}</code><span>{item.network.toUpperCase()}</span><strong>{item.destination || '未知目标'}{item.destination_port ? `:${item.destination_port}` : ''}</strong><span>{item.outbound_tag || item.outbound_type || 'direct'}</span><span>{item.connection_count} 次 · 峰值 {item.active_peak}</span><time>{formatTableTime(item.ended_at)}</time></div>)}</div>
    </div>
  </MotionDialogPanel>
}

function AuditDimensionList({ title, items }: { title: string; items: ConnectionAuditDimension[] }) {
  return <section><h3>{title}</h3>{!items?.length ? <p className="muted">暂无数据</p> : <div>{items.map(item => <div key={item.key}><span><strong>{item.label || '未标记'}</strong>{item.secondary ? <small>{item.secondary}</small> : null}</span><span>{formatCompactAuditNumber(item.connection_count)} 次</span></div>)}</div>}</section>
}

function AuditLogs({ data, loading, embedded = false }: any) {
  const dialogs = useDialogs()
  const rows: AuditLog[] = data.audit_logs || []
  const showRaw = (log: AuditLog) => dialogs.alert({
    title: `原始日志 #${log.id}`,
    message: <div className="raw-log-copy"><CopyBlock value={JSON.stringify(log, null, 2)} /></div>,
  })
  const content = <>
    {loading && !rows.length ? <TableSkeleton /> : !rows.length ? <p className="muted">暂无数据</p> : <MotionList className="audit-timeline">
      {rows.map(log => {
        const item = describeAuditLog(log, data)
        return <MotionCard tag="article" hoverEffect={false} className={`audit-card ${item.tone}`} key={log.id}>
          <div className="audit-line-dot" aria-hidden="true" />
          <div className="audit-card-content">
            <div className="audit-card-head">
              <div>
                <div className="audit-title">
                  <span className={`badge ${item.tone}`}>{item.actionLabel}</span>
                  <strong>{item.title}</strong>
                </div>
                <div className="audit-meta">
                  <span>{formatTableTime(String(log.created_at || ''))}</span>
                  <span>操作者：{item.actor}</span>
                  <span>来源：{item.ip}</span>
                  <span>{item.targetType}：{item.targetLabel}</span>
                  {item.detail && <span>详情：{item.detail}</span>}
                </div>
              </div>
              <button className="ghost" onClick={() => showRaw(log)}>查看原始日志</button>
            </div>
          </div>
        </MotionCard>
      })}
    </MotionList>}
  </>
  return embedded ? content : <Panel title="审计日志">{content}</Panel>
}

function Dashboard({ data, loading, displayName: preferredDisplayName, attention, dismissAttention }: any) {
  const summary = data.summary || {}
  const servers = data.servers || []
  const displayName = String(preferredDisplayName || data.current_user?.nickname || data.current_user?.username || 'Admin')

  const totalServers = summary.servers_total ?? summary.servers ?? summary.server_count ?? servers.length ?? 0
  const serverCountWatermark = String(Math.max(0, Number(totalServers) || 0)).padStart(2, '0')
  const onlineServers = summary.servers_online ?? summary.online_agents ?? summary.online_servers ?? servers.filter((s: any) => s.status === 'online').length ?? 0
  const offlineServers = Math.max(0, Number(totalServers) - Number(onlineServers))
  const auditOverview = data.connection_audit || {}
  const elevatedRiskCount = Number(auditOverview.elevated_risk_count || 0)
  const auditWindowHours = Number(auditOverview.window_hours || 24)
  const activeUsers = summary.users_active ?? summary.users_total ?? (data.users || []).length ?? 0
  const totalTraffic = formatBytes(Number(summary.traffic_upload_bytes || 0) + Number(summary.traffic_download_bytes || 0))

  const groupedTasks = groupTasksForTimeline(data.agent_tasks || [])
  const recentTasks = groupedTasks.slice(0, 6).map((g: TaskGroup) => {
    const summaryStatus = taskStatusSummary(g.tasks)
    const status = deploymentStatusFromSummary(summaryStatus)
    const isSuccess = status === 'succeeded'
    const isRunning = status === 'running' || status === 'pending'
    const serverCount = new Set(g.tasks.map((t: any) => t.server_id)).size
    const createdAt = String(g.tasks.map((t: any) => t.created_at).filter(Boolean).sort()[0] || '')
    return {
      id: g.id,
      title: g.title,
      subtitle: g.kind === 'single'
        ? (g.tasks[0]?.server_id ? taskServerLabel(data, g.tasks[0].server_id) : 'Agent 任务')
        : `${serverCount} 台服务器 · ${g.tasks.length} 项`,
      status: isRunning ? 'running' : isSuccess ? 'success' : 'failed',
      createdAt,
    }
  })

  const quickActions = [
    { key: 'servers', title: '管理服务器', desc: '查看状态、安装 Agent 与端口策略', icon: <ServerIcon size={18} /> },
    { key: 'proxy-paths', title: '代理链路', desc: '编排入口、跳点与第三方出口', icon: <Workflow size={18} /> },
    { key: 'users', title: '用户与分组', desc: '凭据、限速与订阅令牌', icon: <UsersIcon size={18} /> },
    { key: 'tasks', title: '任务记录', desc: '查看配置下发与执行回执', icon: <CheckSquare size={18} /> },
  ]

  return (
    <div className="dashboard-page">
      {attention?.parts?.length > 0 && (
        <DashboardAttentionNotice
          parts={attention.parts}
          className="dashboard-attention"
          onDismiss={dismissAttention}
        />
      )}

      <section className="dash-welcome">
        <div className="dash-welcome-copy">
          <div className="dash-welcome-kicker">
            <span>总览</span>
            <span className="dot" />
            <span>{formatDashDate()}</span>
          </div>
          <h1>欢迎回来,{displayName}</h1>
          <p>以下是您的服务器、任务和近期活动概览。在几秒内部署配置或管理您的服务器集群。</p>
        </div>
        <div className="dash-welcome-actions">
          <button type="button" onClick={() => goTab('servers')}>
            <HardDrive size={15} />
            <span>管理服务器</span>
          </button>
        </div>
        <div className="dash-watermark" aria-hidden="true">{serverCountWatermark}</div>
      </section>

      <section className="stat-row">
        <div className="stat-cell">
          <div className="stat-cell-head">
            <span>我的服务器</span>
            <ServerIcon size={16} />
          </div>
          <strong>{totalServers}</strong>
          <small>{onlineServers} 台运行中，{offlineServers} 台已停止</small>
        </div>
        <div className="stat-cell">
          <div className="stat-cell-head">
            <span>高风险事件</span>
            <Shield size={16} />
          </div>
          <strong>{elevatedRiskCount}</strong>
          <small>{elevatedRiskCount === 0 ? `最近 ${auditWindowHours} 小时暂无高风险` : `最近 ${auditWindowHours} 小时 · 高风险与严重`}</small>
        </div>
        <div className="stat-cell">
          <div className="stat-cell-head">
            <span>活跃流量</span>
            <Zap size={16} />
          </div>
          <strong>{totalTraffic}</strong>
          <small>{activeUsers} 位活跃用户 · 当前账期累计</small>
        </div>
      </section>

      <section className="dash-lower">
        <div>
          <div className="dash-section-head">
            <div>
              <h2>快捷操作</h2>
              <p>直达最常用的功能</p>
            </div>
          </div>
          <div className="quick-grid">
            {quickActions.map(item => (
              <button key={item.key} type="button" className="quick-card" onClick={() => goTab(item.key)}>
                <div className="quick-card-icon">{item.icon}</div>
                <strong>{item.title}</strong>
                <span>{item.desc}</span>
                <em>打开 →</em>
              </button>
            ))}
          </div>
        </div>

        <div>
          <div className="dash-section-head">
            <div>
              <h2>最近活动</h2>
              <p>您账户上的最新事件</p>
            </div>
            <button type="button" className="linkish" onClick={() => goTab('tasks')}>查看全部 →</button>
          </div>
          <div className="activity-list">
            {recentTasks.length === 0 ? (
              <div className="activity-empty">{loading ? '正在加载活动…' : '暂无最近活动'}</div>
            ) : (
              recentTasks.map((tsk: any) => (
                <div className="activity-item" key={tsk.id}>
                  <div className="activity-icon">
                    {tsk.status === 'running' ? <RefreshCw size={14} /> : tsk.status === 'succeeded' ? <Check size={14} /> : <Info size={14} />}
                  </div>
                  <div>
                    <strong>{tsk.title}</strong>
                    <span>{tsk.subtitle}</span>
                  </div>
                  <time>{formatTableTime(tsk.createdAt)}</time>
                </div>
              ))
            )}
          </div>
        </div>
      </section>
    </div>
  )
}

function defaultServerDraft(defaults?: { mtu_mode?: string; bbr_enabled?: boolean }) {
  return { name: 'server-1', entry_address: '', public_ipv4: '', public_ipv6: '', region_code: '', detected_region_code: '', region_mode: 'auto' as RegionMode, entry_ip_mode: 'auto' as EntryIPMode, listen_ip: '0.0.0.0', ip_stack: 'auto', udp_inbound_mode: 'allow', mtu_mode: defaults?.mtu_mode || 'detect', mtu_value: 0, mtu_probe_host: '1.1.1.1', mtu_probe_port: 443, mtu_overhead_bytes: 0, bbr_enabled: Boolean(defaults?.bbr_enabled), port_range_start: 100, port_range_end: 65535, ssh_port: 0, status: 'unknown', monitoring_mode: 'lightweight' as 'lightweight' | 'standard', traffic_reset_mode: 'monthly', traffic_reset_day: 1, connectivity_probe_enabled: true, connection_audit_enabled: true }
}

function GridViewIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="4" width="7" height="7" rx="1.5" /><rect x="13" y="4" width="7" height="7" rx="1.5" /><rect x="4" y="13" width="7" height="7" rx="1.5" /><rect x="13" y="13" width="7" height="7" rx="1.5" /></svg>
}

function ListViewIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 6h12" /><path d="M8 12h12" /><path d="M8 18h12" /><circle cx="4" cy="6" r="1.2" /><circle cx="4" cy="12" r="1.2" /><circle cx="4" cy="18" r="1.2" /></svg>
}

function Servers({ data, client, load, loading, notify }: any) {
  const dialogs = useDialogs()
  const creationDefaults = data.server_creation_defaults || {}
  const [draft, setDraft] = useState(() => defaultServerDraft(creationDefaults))
  const [createOpen, setCreateOpen] = useState(false)
  const [editServer, setEditServer] = useState<Server | null>(null)
  const [agentConfigServer, setAgentConfigServer] = useState<Server | null>(null)
  const [installTarget, setInstallTarget] = useState<{ server: Server; token: string } | null>(null)
  const [logServer, setLogServer] = useState<Server | null>(null)
  const [mtuServer, setMtuServer] = useState<Server | null>(null)
  const [dnsServer, setDNSServer] = useState<Server | null>(null)
  const [detailServer, setDetailServer] = useState<Server | null>(null)
  const [view, setView] = useState<'grid' | 'list'>('grid')
  const [servers, setServers] = useState<Server[]>(data.servers || [])
  const [serverMetrics, setServerMetrics] = useState<ServerMetricSample[]>(data.server_metrics || [])
  const [serverRefreshing, setServerRefreshing] = useState(false)
  const [serverRefreshFailed, setServerRefreshFailed] = useState(false)
  const [serverRefreshedAt, setServerRefreshedAt] = useState<Date | null>(null)
  const serverRequestInFlightRef = useRef(false)
  const serversMountedRef = useRef(false)

  useEffect(() => { setServers(data.servers || []) }, [data.servers])
  useEffect(() => { setServerMetrics(data.server_metrics || []) }, [data.server_metrics])

  useEffect(() => {
    serversMountedRef.current = true
    return () => { serversMountedRef.current = false }
  }, [])

  const refreshServers = React.useCallback(async () => {
    if (serverRequestInFlightRef.current) return
    serverRequestInFlightRef.current = true
    setServerRefreshing(true)
    try {
      const res = await client.request('/servers')
      if (!serversMountedRef.current) return
      const nextServers: Server[] = res.servers || []
      setServers(nextServers)
      setServerMetrics(current => appendLiveServerMetrics(current, nextServers))
      setServerRefreshedAt(new Date())
      setServerRefreshFailed(false)
    } catch (error) {
      if (serversMountedRef.current) setServerRefreshFailed(true)
      console.warn('Server refresh failed:', error)
    } finally {
      serverRequestInFlightRef.current = false
      if (serversMountedRef.current) setServerRefreshing(false)
    }
  }, [client])

  useEffect(() => {
    let cancelled = false
    let timer: number | undefined

    const scheduleNext = () => {
      if (cancelled || document.visibilityState !== 'visible') return
      timer = window.setTimeout(runRefresh, 5000)
    }
    const runRefresh = async () => {
      if (cancelled || document.visibilityState !== 'visible') return
      await refreshServers()
      scheduleNext()
    }
    const handleVisibilityChange = () => {
      if (timer !== undefined) window.clearTimeout(timer)
      timer = undefined
      if (document.visibilityState === 'visible') void runRefresh()
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    scheduleNext()
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [refreshServers])

  const serverRefreshedTime = serverRefreshedAt?.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
  const metricsByServer = useMemo(() => {
    const grouped = new Map<number, ServerMetricSample[]>()
    serverMetrics.forEach(sample => grouped.set(Number(sample.server_id), [...(grouped.get(Number(sample.server_id)) || []), sample]))
    return grouped
  }, [serverMetrics])
  const enroll = async (s: Server) => {
    const res = await client.request(`/servers/${s.id}/enroll-token`, { method: 'POST', body: '{}' })
    setInstallTarget({ server: s, token: res.enrollment_token })
  }
  const tasks = async (s: Server) => {
    notify?.(`已打开任务中心，可查看 ${s.name || '服务器'} 相关任务`, 'info')
    goTab('tasks')
  }
  const diagnose = async (s: Server) => {
    if (agentBuildMismatch(s, data)) {
      const expected = data.version?.agent_expected_build || data.version?.build || ''
      const command = agentUpdateCommand(effectiveControllerURL(data))
      await dialogs.alert({
        title: 'Agent 需要先更新',
        message: <div>
          <p>{s.name || '这台服务器'} 的 Agent 构建号是 <strong>{s.agent_build || '未知'}</strong>，当前主控需要 <strong>{expected}</strong>。</p>
          <p className="muted">旧 Agent 不支持网络诊断任务。请先在服务器上执行更新命令，然后再重新诊断。</p>
          <CommandCopyBlock value={command} buttonText="复制更新命令" />
        </div>,
      })
      return
    }
    await client.request(`/servers/${s.id}/diagnose`, { method: 'POST', body: '{}' })
    await load()
    notify?.(`已创建 ${s.name || '服务器'} 的网络诊断，请在任务中心查看结果`, 'success')
  }
  const createServer = async () => {
    await client.request('/servers', { method: 'POST', body: JSON.stringify(draft) })
    setCreateOpen(false)
    setDraft(defaultServerDraft(creationDefaults))
    await load()
  }
  const updateServer = async (next: any) => {
    await client.request(`/servers/${next.id}`, { method: 'PATCH', body: JSON.stringify(next) })
    setEditServer(null)
    await load()
  }
  const syncAgentConfig = async (server: Server, cfg: any) => {
    await client.request(`/servers/${server.id}/agent-config`, { method: 'POST', body: JSON.stringify(cfg) })
    setAgentConfigServer(null)
    await load()
    notify?.(`已创建 ${server.name || '服务器'} 的 Agent 配置同步，请在任务中心查看`, 'success')
  }
  const updateAgent = async (s: Server) => {
    try {
      const res = await client.request(`/servers/${s.id}/agent-update`, { method: 'POST', body: '{}' })
      await load()
      notify?.(
        res.existing
          ? `${s.name || '服务器'} 已有更新任务进行中，未重复创建`
          : `已创建 ${s.name || '服务器'} 的 Agent 更新，请在任务中心查看`,
        res.existing ? 'info' : 'success',
      )
    } catch (err: any) {
      const command = agentUpdateCommand(effectiveControllerURL(data))
      await dialogs.alert({
        title: '需要命令行更新一次',
        message: <div>
          <p>{err?.message || '当前 Agent 版本不支持面板自更新。'}</p>
          <p className="muted">在服务器上执行下面命令，更新完成后再使用面板更新。</p>
          <CommandCopyBlock value={command} buttonText="复制更新命令" />
        </div>,
      })
    }
  }
  const updateAllAgents = async () => {
    const enrolled = servers.filter(s => String(s.agent_id || '').trim())
    if (!enrolled.length) {
      notify?.('没有已接入的 Agent，无法更新', 'warning')
      return
    }
    const online = enrolled.filter(s => String(s.status || '').toLowerCase() === 'online').length
    const offline = enrolled.length - online
    const ok = await dialogs.confirm({
      title: '一键更新所有 Agent',
      confirmText: '开始更新',
      message: <div>
        <p>将为 <strong>{enrolled.length}</strong> 台已接入服务器创建 Agent 更新任务。</p>
        <p className="muted">在线 {online} 台{offline > 0 ? `，离线 ${offline} 台会立即标记失败` : ''}。进度请在任务中心查看。</p>
      </div>,
    })
    if (!ok) return
    try {
      const res = await client.request('/agents/update-all', { method: 'POST', body: '{}' })
      await load()
      const summary = res.summary || {}
      const created = Number(summary.created || 0)
      const existing = Number(summary.existing || 0)
      const failed = Number(summary.failed || 0)
      const skipped = Number(summary.skipped || 0)
      const parts = [
        created ? `新建 ${created}` : '',
        existing ? `已有进行中 ${existing}` : '',
        failed ? `失败 ${failed}` : '',
        skipped ? `跳过 ${skipped}` : '',
      ].filter(Boolean)
      notify?.(
        parts.length ? `Agent 批量更新已提交：${parts.join(' · ')}` : 'Agent 批量更新已提交',
        failed && !created && !existing ? 'warning' : 'success',
      )
    } catch (err: any) {
      notify?.(localizeErrorMessage(err?.message || err), 'error')
    }
  }
  const handleServerAction = async (type: string, s: Server) => {
    if (type === 'details') setDetailServer(s)
    else if (type === 'edit') setEditServer(s)
    else if (type === 'mtu') setMtuServer(s)
    else if (type === 'dns') setDNSServer(s)
    else if (type === 'agent-config') setAgentConfigServer(s)
    else if (type === 'update-agent') updateAgent(s)
    else if (type === 'enroll') enroll(s)
    else if (type === 'logs') setLogServer(s)
    else if (type === 'diagnose') diagnose(s)
    else if (type === 'tasks') tasks(s)
    else if (type === 'delete') remove(client, `/servers/${s.id}`, load, dialogs, s)
  }
  const role: Role = data.session?.role || 'viewer'
  const actions = (s: Server) => <ServerActionsDropdown server={s} role={role} onAction={handleServerAction} />
  const enrolledCount = servers.filter(s => String(s.agent_id || '').trim()).length
  return <section className="panel">
    <div className="panel-body">
    <div className="section-toolbar">
      <div>
        <div className={`live-refresh-status ${serverRefreshFailed ? 'is-error' : 'is-active'}`} title="服务器状态和资源数据每 5 秒更新">
          <span className="live-refresh-dot" aria-hidden="true" />
          <span>{serverRefreshFailed ? '自动刷新暂时失败' : serverRefreshing ? '正在更新服务器' : '服务器数据自动刷新'}</span>
          {serverRefreshedTime ? <time dateTime={serverRefreshedAt?.toISOString()}>更新于 {serverRefreshedTime}</time> : null}
        </div>
      </div>
      <div className="section-actions">
        {role === 'admin' && (
          <button type="button" className="ghost" onClick={() => void updateAllAgents()} disabled={!enrolledCount} title={enrolledCount ? `为 ${enrolledCount} 台已接入 Agent 创建更新任务` : '没有已接入的 Agent'}>
            <RefreshCw size={14} />
            <span>一键更新 Agent</span>
          </button>
        )}
        <button onClick={() => { setDraft(defaultServerDraft(creationDefaults)); setCreateOpen(true) }}>添加服务器</button>
        <div className="view-mode-toggle" role="radiogroup" aria-label="显示方式">
          <button type="button" role="radio" aria-checked={view === 'grid'} className={view === 'grid' ? 'active' : ''} onClick={() => setView('grid')} aria-label="平铺模式" title="平铺模式"><GridViewIcon /></button>
          <button type="button" role="radio" aria-checked={view === 'list'} className={view === 'list' ? 'active' : ''} onClick={() => setView('list')} aria-label="列表模式" title="列表模式"><ListViewIcon /></button>
        </div>
      </div>
    </div>
    {loading && !servers.length
      ? <CardSkeleton />
      : !servers.length
      ? <p className="muted server-empty">暂无服务器</p>
      : view === 'grid'
		  ? <MotionList className="server-grid">{servers.map(s => <ServerCard key={s.id} server={s} samples={metricsByServer.get(Number(s.id)) || []} role={role} expectedBuild={data.version?.agent_expected_build || data.version?.build || ''} onAction={handleServerAction} />)}</MotionList>
      : <Table rows={compactServerRows(servers)} actions={(r: any) => actions(r._server)} loading={loading} />}
    <AnimatePresence>{createOpen && <ServerCreateDialog draft={draft} setDraft={setDraft} onCancel={() => setCreateOpen(false)} onSubmit={createServer} />}</AnimatePresence>
    <AnimatePresence>{editServer && <ServerEditDialog server={editServer} onCancel={() => setEditServer(null)} onSubmit={updateServer} />}</AnimatePresence>
    <AnimatePresence>{detailServer && <ServerDetailDialog server={detailServer} onClose={() => setDetailServer(null)} />}</AnimatePresence>
    <AnimatePresence>{agentConfigServer && <AgentConfigDialog server={agentConfigServer} controllerURL={effectiveControllerURL(data)} onCancel={() => setAgentConfigServer(null)} onSubmit={cfg => syncAgentConfig(agentConfigServer, cfg)} />}</AnimatePresence>
    <AnimatePresence>{installTarget && <AgentInstallDialog server={installTarget.server} token={installTarget.token} controllerURL={effectiveControllerURL(data)} onClose={() => setInstallTarget(null)} />}</AnimatePresence>
    <AnimatePresence>{logServer && <AgentLogsDialog server={logServer} data={data} client={client} onClose={() => setLogServer(null)} />}</AnimatePresence>
    <AnimatePresence>{mtuServer && <MTUSettingsDialog draft={serverToDraft(mtuServer)} nested={false} onCancel={() => setMtuServer(null)} onSave={async (patch) => {
      await client.request(`/servers/${mtuServer.id}`, { method: 'PATCH', body: JSON.stringify({ ...mtuServer, ...patch }) })
      setMtuServer(null)
      await load()
    }} />}</AnimatePresence>
    <AnimatePresence>{dnsServer && <DNSSettingsDialog
      server={dnsServer}
      policy={(data.server_dns_policies || []).find((policy: ServerDNSPolicy) => Number(policy.server_id) === Number(dnsServer.id))}
      lists={data.dns_lists || []}
      benchmarks={(data.dns_benchmarks || []).filter((item: DNSBenchmarkResult) => Number(item.server_id) === Number(dnsServer.id))}
      client={client}
      onClose={() => setDNSServer(null)}
      onChanged={load}
      notify={notify}
    />}</AnimatePresence>
    </div>
  </section>
}

function appendLiveServerMetrics(current: ServerMetricSample[], servers: Server[]) {
  const grouped = new Map<number, ServerMetricSample[]>()
  current.forEach(sample => grouped.set(Number(sample.server_id), [...(grouped.get(Number(sample.server_id)) || []), sample]))
  servers.forEach(server => {
    const sampledAt = String(server.telemetry_updated_at || '')
    if (!sampledAt) return
    const list = grouped.get(Number(server.id)) || []
    if (list.some(sample => String(sample.sampled_at) === sampledAt)) return
    list.push({
      id: -Date.parse(sampledAt),
      server_id: server.id,
      cpu_usage_percent: server.cpu_usage_percent || 0,
      memory_used_bytes: server.memory_used_bytes || 0,
      memory_total_bytes: server.memory_total_bytes || 0,
      network_upload_bps: server.network_upload_bps || 0,
      network_download_bps: server.network_download_bps || 0,
      traffic_upload_bytes: server.traffic_upload_bytes || 0,
      traffic_download_bytes: server.traffic_download_bytes || 0,
      connectivity_available: server.connectivity_probe_enabled && server.connectivity_status !== 'pending' ? server.connectivity_status === 'available' : undefined,
      connectivity_latency_ms: server.connectivity_latency_ms || 0,
      sampled_at: sampledAt,
    })
    grouped.set(Number(server.id), list.slice(-60))
  })
  return Array.from(grouped.values()).flat()
}

function effectiveControllerURL(data: any) {
  return String(data.settings?.controller_url || appControllerURL()).replace(/\/+$/, '')
}

function shellQuote(value: string) {
  return `'${String(value).replace(/'/g, `'\\''`)}'`
}

function agentScriptCommand(controllerURL: string, action: 'install' | 'update' | 'uninstall', token = '', server?: Pick<Server, 'bbr_enabled'>) {
  const base = controllerURL.replace(/\/+$/, '')
  const download = `curl -fsSL ${shellQuote(`${base}/install/agent.sh`)}`
  if (action === 'install') return `${download} | OBOARD_ENROLL_TOKEN=${shellQuote(token)} OBOARD_INSTALL_BBR=${server?.bbr_enabled ? '1' : '0'} sh`
  return `${download} | sh -s -- ${action}`
}

function agentUpdateCommand(controllerURL: string) {
  return agentScriptCommand(controllerURL, 'update')
}

function agentBuildMismatch(server: Server, data: any) {
  const expected = String(data.version?.agent_expected_build || data.version?.build || '').trim()
  const actual = String(server.agent_build || '').trim()
  return Boolean(expected && actual && expected !== actual)
}

function AgentInstallDialog({ server, token, controllerURL, onClose }: { server: Server; token: string; controllerURL: string; onClose: () => void }) {
  const isOnline = String(server.status || '').toLowerCase() === 'online'
  const [action, setAction] = useState<'install' | 'update' | 'uninstall'>(isOnline ? 'update' : 'install')
  const actionTitle = action === 'install' ? '安装' : action === 'update' ? '更新' : '卸载'
  const actionDescription = action === 'install'
    ? `安装 Agent 和内核并连接当前面板${server.bbr_enabled ? '，同时尝试启用 BBR + FQ' : ''}。`
    : action === 'update'
      ? '从当前面板更新 Agent 和内核，保留配置。'
      : '移除 Agent、内核和本机配置。'
  const command = agentScriptCommand(controllerURL, action, token, server)
  return <MotionDialogPanel onCancel={onClose} className="install-dialog">
      <header className="dialog-head">
        <div><h2 id="agent-install-title">Agent 和内核</h2><p className="muted">{server.name || '这台服务器'} · {isOnline ? '在线' : '离线'}</p></div>
        <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <Select variant="segmented" className="install-action-select" value={action} onChange={e => setAction(e.target.value as 'install' | 'update' | 'uninstall')} aria-label="Agent 操作">
          <option value="install">安装</option>
          <option value="update">更新</option>
          <option value="uninstall">卸载</option>
        </Select>
        <p className="install-root-note">请在目标服务器的 root SSH 中执行。安装包由当前面板提供并经过签名校验。</p>
        <div className="install-command-current">
          <InstallCommandCard title={actionTitle} desc={actionDescription} command={command} tone={action === 'uninstall' ? 'danger' : 'default'} />
        </div>
      </div>
      <footer className="dialog-actions"><button onClick={onClose}>完成</button></footer>
  </MotionDialogPanel>
}

function InstallCommandCard({ title, desc, command, tone = 'default' }: { title: string; desc: string; command: string; tone?: 'default' | 'danger' }) {
  return <section className={tone === 'danger' ? 'install-command-card danger' : 'install-command-card'}>
    <div><h3>{title}</h3><p className="muted">{desc}</p></div>
    <CommandCopyBlock value={command} buttonText={`复制${title}命令`} />
  </section>
}

function AgentLogsDialog({ server, data, client, onClose }: { server: Server; data: any; client: ReturnType<typeof api>; onClose: () => void }) {
  const dialogs = useDialogs()
  const [lines, setLines] = useState(120)
  const [services, setServices] = useState('all')
  const [loading, setLoading] = useState(false)
  const [task, setTask] = useState<any>(null)
  const [result, setResult] = useState<any>(null)
  const [operation, setOperation] = useState('')
  const mismatch = agentBuildMismatch(server, data)
  const updateCommand = agentUpdateCommand(effectiveControllerURL(data))
  const pull = async () => {
    setLoading(true)
    setTask(null)
    setResult(null)
    try {
      const created = await client.request(`/servers/${server.id}/logs`, { method: 'POST', body: JSON.stringify({ lines, services }) })
      const taskID = created.task?.id
      setTask(created.task)
      if (!taskID) return
      for (let i = 0; i < 30; i++) {
        await sleep(1000)
        const res = await client.request(`/servers/${server.id}/tasks?limit=30`)
        const next = (res.tasks || []).find((x: any) => Number(x.id) === Number(taskID))
        if (next) {
          setTask(next)
          if (['succeeded', 'failed', 'rollback_failed'].includes(String(next.status))) {
            setResult(parseJSONLoose(next.result_json))
            return
          }
        }
      }
    } finally {
      setLoading(false)
    }
  }
  const control = async (action: 'rotate' | 'clear') => {
    if (action === 'clear') {
      const ok = await dialogs.confirm({ title: '清空服务器日志', message: `${services === 'all' ? 'Agent 和内核' : services === 'agent' ? 'Agent' : '内核'}日志及轮转备份将被清空。`, confirmText: '清空日志', tone: 'danger' })
      if (!ok) return
    }
    setOperation(action)
    try {
      const created = await client.request(`/servers/${server.id}/logs/control`, { method: 'POST', body: JSON.stringify({ action, services }) })
      const taskID = created.task?.id
      if (!taskID) return
      for (let i = 0; i < 30; i++) {
        await sleep(1000)
        const res = await client.request(`/servers/${server.id}/tasks?limit=30`)
        const next = (res.tasks || []).find((item: any) => Number(item.id) === Number(taskID))
        if (next && ['succeeded', 'failed', 'rollback_failed'].includes(String(next.status))) {
          if (next.status !== 'succeeded') throw new Error(parseJSONLoose(next.result_json)?.error || '日志操作失败')
          await pull()
          return
        }
      }
      throw new Error('Agent 日志操作超时')
    } catch (error: any) {
      await dialogs.alert({ title: '日志操作失败', message: localizeErrorMessage(error?.message || error) })
    } finally {
      setOperation('')
    }
  }
  return <MotionDialogPanel onCancel={onClose} className="logs-dialog">
      <header className="dialog-head">
        <div><h2 id="agent-logs-title">{server.name || '服务器'} 日志</h2><p className="muted">按需拉取 Agent 和内核最近日志。内容会自动脱敏。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        {mismatch
          ? <div className="install-command-card danger">
            <div><h3>Agent 需要先更新</h3><p className="muted">当前构建 {server.agent_build || '未知'}，主控需要 {data.version?.agent_expected_build || data.version?.build || '最新构建'}。旧 Agent 不支持日志拉取任务。</p></div>
            <CommandCopyBlock value={updateCommand} buttonText="复制更新命令" />
          </div>
          : <div className="logs-dialog-grid">
            <div className="form logs-form">
              <FormField label="服务">
                <Select variant="segmented" value={services} onChange={e => setServices(e.target.value)}>
                  <option value="all">Agent + 内核</option>
                  <option value="agent">仅 Agent</option>
                  <option value="core">仅内核</option>
                </Select>
              </FormField>
              <FormField label="行数">
                <input type="number" min={20} max={2000} value={lines} onChange={e => setLines(Math.max(20, Math.min(2000, Number(e.target.value) || 120)))} />
              </FormField>
              <button onClick={pull} disabled={loading}>{loading ? '拉取中…' : '拉取日志'}</button>
            </div>
            <div className="agent-log-actions">
              <button className="ghost" onClick={() => control('rotate')} disabled={Boolean(operation) || loading}><RefreshCw size={15} />{operation === 'rotate' ? '轮转中...' : '立即轮转'}</button>
              <button className="ghost danger-text" onClick={() => control('clear')} disabled={Boolean(operation) || loading}><Eraser size={15} />{operation === 'clear' ? '清空中...' : '清空日志'}</button>
            </div>
            {task && <p className="muted">状态：{labelValue(task.status)}{task.completed_at ? ` · 完成于 ${formatTableTime(String(task.completed_at))}` : ' · 执行中'}</p>}
            {result ? <AgentLogResult result={result} /> : <p className="muted">点击“拉取日志”后，Agent 在线时会返回最近日志。</p>}
          </div>}
      </div>
      <footer className="dialog-actions"><button onClick={onClose}>关闭</button></footer>
  </MotionDialogPanel>
}

function AgentLogResult({ result }: { result: any }) {
  const raw = JSON.stringify(result || {}, null, 2)
  const logs = result?.logs || {}
  const policy = result?.policy || {}
  return <div className="agent-log-result">
    <div className="log-summary">
      <span>Agent：{result?.agent_version || '—'} #{result?.agent_build || '—'}</span>
      <span>时间：{result?.time ? formatTableTime(String(result.time)) : '—'}</span>
      <span>行数：{result?.lines || '—'}</span>
      {policy.agent && <span>Agent 日志：{policy.agent.max_mb} MB · {policy.agent.backups} 个备份</span>}
      {policy.core && <span>内核日志：{policy.core.max_mb} MB · {policy.core.backups} 个备份</span>}
    </div>
    {logs.agent && <ServiceLogBlock title="Agent 日志" value={logs.agent} />}
    {logs.core && <ServiceLogBlock title="内核日志" value={logs.core} />}
    <details className="task-details"><summary>原始日志 JSON</summary><CopyBlock value={raw} /></details>
  </div>
}

function ServiceLogBlock({ title, value }: { title: string; value: any }) {
  const status = value?.status?.output || value?.active?.output || ''
  const journal = value?.log_file?.content || value?.journal?.output || value?.system_log?.content || value?.log_file?.error || value?.journal?.error || value?.system_log?.error || ''
  return <section className="service-log-block">
    <h3>{title}{value?.log_file?.files?.length ? <small> · {value.log_file.files.length} 个日志文件</small> : null}</h3>
    {status && <pre>{status}</pre>}
    <pre>{journal || '暂无日志'}</pre>
  </section>
}

function CommandCopyBlock({ value, buttonText = '复制命令' }: { value: string; buttonText?: string }) {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const copy = async () => {
    const ok = await copyText(value)
    setCopyState(ok ? 'copied' : 'failed')
    window.setTimeout(() => setCopyState('idle'), 1400)
  }
  return <div className="command-copy">
    <pre>{value}</pre>
    <button onClick={copy}>{copyState === 'copied' ? '已复制' : copyState === 'failed' ? '复制失败' : buttonText}</button>
  </div>
}

function ServerCreateDialog({ draft, setDraft, onCancel, onSubmit }: { draft: ReturnType<typeof defaultServerDraft>; setDraft: React.Dispatch<React.SetStateAction<ReturnType<typeof defaultServerDraft>>>; onCancel: () => void; onSubmit: () => Promise<void> }) {
  const update = (patch: Partial<ReturnType<typeof defaultServerDraft>>) => setDraft(old => ({ ...old, ...patch }))
  const [mtuDialogOpen, setMtuDialogOpen] = useState(false)
  const [portRangeValid, setPortRangeValid] = useState(true)
  return <MotionDialogPanel onCancel={onCancel} className="server-dialog">
      <header className="dialog-head">
        <div><h2 id="server-dialog-title">添加服务器</h2><p className="muted">设置名称、入口地址和网络策略。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form server-dialog-form labeled-form">
          <div className="form-section-title">基础信息</div>
          <FormField label="服务器名称" required hint="用于面板识别。">
            <input value={draft.name} onChange={e => update({ name: e.target.value })} placeholder="例如：server-1" />
          </FormField>
          <ServerRegionField draft={draft} update={update} />
          <FormField label="默认入口地址策略" hint="订阅默认使用的服务器地址。">
            <Select value={draft.entry_ip_mode} onChange={e => update({ entry_ip_mode: e.target.value as EntryIPMode })}>{entryIPModes.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select>
          </FormField>
          <FormField label="自定义入口地址" hint="可填写域名、IPv4 或 IPv6。">
            <input value={draft.entry_address} onChange={e => update({ entry_address: e.target.value })} placeholder="例如 1.2.3.4 或 example.com" />
            <DetectedEntryAddressNote ipv4={draft.public_ipv4} ipv6={draft.public_ipv6} />
          </FormField>
          <FormField label="监听 IP" hint="通常保持 0.0.0.0。">
            <input value={draft.listen_ip} onChange={e => update({ listen_ip: e.target.value })} placeholder="0.0.0.0" />
          </FormField>
		  <FormField label="SSH 端口（可选）" hint="用于 SSH 隧道，留空时再询问。">
			<input type="number" min={1} max={65535} value={draft.ssh_port || ''} onChange={e => update({ ssh_port: e.target.value ? Number(e.target.value) : 0 })} placeholder="例如：22" />
		  </FormField>

          <div className="form-section-title">网络策略</div>
          <FormField label="出口解析策略" hint="选择出口优先使用的 IP 类型。">
            <Select value={draft.ip_stack} onChange={e => update({ ip_stack: e.target.value })}>{ipStacks.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select>
          </FormField>
          <FormField label="UDP 入站" hint="选择 UDP 的处理方式。">
            <UDPModeSelector value={draft.udp_inbound_mode} onChange={value => update({ udp_inbound_mode: value })} />
          </FormField>
          <FormField label="BBR + FQ" hint="首次安装 Agent 时尝试启用，失败不影响安装。">
            <label className="notification-enable-row"><input type="checkbox" checked={Boolean(draft.bbr_enabled)} onChange={e => update({ bbr_enabled: e.target.checked })} aria-label="安装时启用 BBR + FQ" /></label>
          </FormField>
          <FormField label="端口范围" hint="100-65535" full>
            <PortRangeInput start={draft.port_range_start} end={draft.port_range_end} onChange={(port_range_start, port_range_end) => update({ port_range_start, port_range_end })} onValidityChange={setPortRangeValid} />
          </FormField>

          <div className="form-section-title">监控与流量</div>
          <FormField label="回报模式" hint="轻量 20 秒，标准 10 秒。">
            <Select variant="segmented" value={draft.monitoring_mode || 'lightweight'} onChange={e => update({ monitoring_mode: e.target.value as 'lightweight' | 'standard' })}>
              <option value="lightweight">轻量</option>
              <option value="standard">标准</option>
            </Select>
          </FormField>
          <TrafficResetFields mode={draft.traffic_reset_mode} day={draft.traffic_reset_day} onChange={update} />
          <FormField label="公网可访问性" hint="每分钟检测公网连接和延迟。">
            <label className="notification-enable-row"><input type="checkbox" checked={Boolean(draft.connectivity_probe_enabled)} onChange={e => update({ connectivity_probe_enabled: e.target.checked })} aria-label="启用公网可访问性" /></label>
          </FormField>
          <FormField label="连接审计" hint="记录来源 IP、目标与出口摘要。">
            <label className="notification-enable-row"><input type="checkbox" checked={Boolean(draft.connection_audit_enabled)} onChange={e => update({ connection_audit_enabled: e.target.checked })} aria-label="启用连接审计" /></label>
          </FormField>

          <div className="form-extra-row">
            <button type="button" className="ghost" onClick={() => setMtuDialogOpen(true)} aria-haspopup="dialog">MTU 检测设置</button>
            <span>首次部署或设置变化时执行。</span>
          </div>
        </div>
      </div>
      <footer className="dialog-actions">
        <button className="ghost" onClick={onCancel}>取消</button>
        <button onClick={onSubmit} disabled={!portRangeValid}>创建</button>
      </footer>
      {mtuDialogOpen && <MTUSettingsDialog draft={draft} onCancel={() => setMtuDialogOpen(false)} onSave={patch => { update(patch); setMtuDialogOpen(false) }} />}
  </MotionDialogPanel>
}

function serverToDraft(server: Server) {
  return {
    ...defaultServerDraft(),
    ...server,
    public_ipv4: server.public_ipv4 || '',
    public_ipv6: server.public_ipv6 || '',
    entry_ip_mode: (server.entry_ip_mode || 'auto') as EntryIPMode,
  }
}

function ServerEditDialog({ server, onCancel, onSubmit }: { server: Server; onCancel: () => void; onSubmit: (server: any) => Promise<void> }) {
  const [draft, setDraft] = useState<any>(() => serverToDraft(server))
  const [mtuDialogOpen, setMtuDialogOpen] = useState(false)
  const [portRangeValid, setPortRangeValid] = useState(true)
  const update = (patch: any) => setDraft((old: any) => ({ ...old, ...patch }))
  return <MotionDialogPanel onCancel={onCancel} className="server-dialog">
      <header className="dialog-head">
        <div><h2 id="server-edit-title">服务器设置</h2><p className="muted">设置 {server.name} 的入口地址和网络策略。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form server-dialog-form labeled-form">
          <div className="form-section-title">基础信息</div>
          <FormField label="服务器名称" required><input value={draft.name} onChange={e => update({ name: e.target.value })} /></FormField>
          <ServerRegionField draft={draft} update={update} />
          <FormField label="默认入口地址策略" hint="订阅默认使用的服务器地址。"><Select value={draft.entry_ip_mode} onChange={e => update({ entry_ip_mode: e.target.value as EntryIPMode })}>{entryIPModes.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
          <FormField label="自定义入口地址" hint="选择自定义时使用。">
            <input value={draft.entry_address || ''} onChange={e => update({ entry_address: e.target.value })} placeholder="域名 / IPv4 / IPv6" />
            <DetectedEntryAddressNote ipv4={draft.public_ipv4} ipv6={draft.public_ipv6} />
          </FormField>
          <FormField label="监听 IP"><input value={draft.listen_ip} onChange={e => update({ listen_ip: e.target.value })} /></FormField>
		  <FormField label="SSH 端口（可选）" hint="用于 SSH 隧道，留空时再询问。"><input type="number" min={1} max={65535} value={draft.ssh_port || ''} onChange={e => update({ ssh_port: e.target.value ? Number(e.target.value) : 0 })} placeholder="例如：22" /></FormField>
          <div className="form-section-title">网络策略</div>
          <FormField label="出口解析策略"><Select value={draft.ip_stack} onChange={e => update({ ip_stack: e.target.value })}>{ipStacks.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
          <FormField label="UDP 入站" hint="选择 UDP 的处理方式。"><UDPModeSelector value={draft.udp_inbound_mode} onChange={value => update({ udp_inbound_mode: value })} /></FormField>
          <FormField label="BBR + FQ" hint="下次重新安装 Agent 时尝试启用，失败不影响安装。"><label className="notification-enable-row"><input type="checkbox" checked={Boolean(draft.bbr_enabled)} onChange={e => update({ bbr_enabled: e.target.checked })} aria-label="安装时尝试启用 BBR + FQ" /></label></FormField>
          <FormField label="端口范围" hint="100-65535" full><PortRangeInput start={draft.port_range_start} end={draft.port_range_end} onChange={(port_range_start, port_range_end) => update({ port_range_start, port_range_end })} onValidityChange={setPortRangeValid} /></FormField>
          <div className="form-section-title">监控与流量</div>
          <FormField label="回报模式" hint="轻量 20 秒，标准 10 秒。">
            <Select variant="segmented" value={draft.monitoring_mode || 'lightweight'} onChange={e => update({ monitoring_mode: e.target.value })}><option value="lightweight">轻量</option><option value="standard">标准</option></Select>
          </FormField>
          <TrafficResetFields mode={draft.traffic_reset_mode} day={draft.traffic_reset_day} onChange={update} />
          <FormField label="公网可访问性" hint="每分钟检测公网连接和延迟。">
            <label className="notification-enable-row"><input type="checkbox" checked={Boolean(draft.connectivity_probe_enabled)} onChange={e => update({ connectivity_probe_enabled: e.target.checked })} aria-label="启用公网可访问性" /></label>
          </FormField>
          <FormField label="连接审计" hint="关闭后 Agent 停止采集、上报和本地审计状态写入。">
            <label className="notification-enable-row"><input type="checkbox" checked={Boolean(draft.connection_audit_enabled)} onChange={e => update({ connection_audit_enabled: e.target.checked })} aria-label="启用连接审计" /></label>
          </FormField>
          <div className="form-extra-row"><button type="button" className="ghost" onClick={() => setMtuDialogOpen(true)}>MTU 检测设置</button><span>修改后会在下次部署重新检测。</span></div>
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={() => onSubmit(draft)} disabled={!portRangeValid}>保存</button></footer>
      {mtuDialogOpen && <MTUSettingsDialog draft={draft} onCancel={() => setMtuDialogOpen(false)} onSave={patch => { update(patch); setMtuDialogOpen(false) }} />}
  </MotionDialogPanel>
}

function AgentConfigDialog({ server, controllerURL, onCancel, onSubmit }: { server: Server; controllerURL: string; onCancel: () => void; onSubmit: (cfg: any) => Promise<void> }) {
  const [cfg, setCfg] = useState({
    controller_url: controllerURL,
    core_binary: '/usr/local/bin/oboard-sb',
    core_service: 'oboard-sb',
		command_timeout_seconds: 20,
    reload_command: 'auto',
    restart_command: 'auto',
    time_sync_command: 'auto',
    time_sync_interval_seconds: 86400,
    log_max_mb: 16,
    log_backups: 3,
    core_log_max_mb: 64,
    core_log_backups: 3,
  })
  const reloadCommands = ['auto', 'none', 'systemd-reload', 'openrc-reload']
  const restartCommands = ['auto', 'none', 'systemd-restart', 'openrc-restart']
  const timeSyncCommands = ['auto', 'none', 'chrony', 'systemd-timesyncd']
  const update = (patch: any) => setCfg(old => ({ ...old, ...patch }))
  return <MotionDialogPanel onCancel={onCancel} className="server-dialog">
      <header className="dialog-head">
        <div><h2 id="agent-config-title">Agent 设置</h2><p className="muted">设置 {server.name} 的本机运行参数。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form server-dialog-form labeled-form">
          <FormField label="面板连接地址"><input value={cfg.controller_url} onChange={e => update({ controller_url: e.target.value })} /></FormField>
          <FormField label="内核路径"><input value={cfg.core_binary} onChange={e => update({ core_binary: e.target.value })} /></FormField>
          <FormField label="内核服务名"><input value={cfg.core_service} onChange={e => update({ core_service: e.target.value })} /></FormField>
		  <FormField label="命令超时" hint="范围 5–120 秒。"><input type="number" min={5} max={120} value={cfg.command_timeout_seconds} onChange={e => update({ command_timeout_seconds: Number(e.target.value) })} /></FormField>
          <FormField label="热重载方式"><Select value={cfg.reload_command} onChange={e => update({ reload_command: e.target.value })}>{reloadCommands.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
          <FormField label="重启方式"><Select value={cfg.restart_command} onChange={e => update({ restart_command: e.target.value })}>{restartCommands.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
          <FormField label="时间同步方式"><Select value={cfg.time_sync_command} onChange={e => update({ time_sync_command: e.target.value })}>{timeSyncCommands.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
          <div className="form-section-title">日志保留</div>
          <FormField label="Agent 单个日志上限" hint="1-1024 MB"><input type="number" min={1} max={1024} value={cfg.log_max_mb} onChange={e => update({ log_max_mb: Number(e.target.value) })} /></FormField>
          <FormField label="Agent 备份数" hint="0-20 个"><input type="number" min={0} max={20} value={cfg.log_backups} onChange={e => update({ log_backups: Number(e.target.value) })} /></FormField>
          <FormField label="内核单个日志上限" hint="1-1024 MB"><input type="number" min={1} max={1024} value={cfg.core_log_max_mb} onChange={e => update({ core_log_max_mb: Number(e.target.value) })} /></FormField>
          <FormField label="内核备份数" hint="0-20 个"><input type="number" min={0} max={20} value={cfg.core_log_backups} onChange={e => update({ core_log_backups: Number(e.target.value) })} /></FormField>
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={() => onSubmit(cfg)}>同步到 Agent</button></footer>
  </MotionDialogPanel>
}

function MTUSettingsDialog({ draft, onCancel, onSave, nested = true }: { draft: ReturnType<typeof defaultServerDraft>; onCancel: () => void; onSave: (patch: Partial<ReturnType<typeof defaultServerDraft>>) => void; nested?: boolean }) {
  const [value, setValue] = useState({
    mtu_mode: draft.mtu_mode,
    mtu_value: draft.mtu_value,
    mtu_probe_host: draft.mtu_probe_host,
    mtu_probe_port: draft.mtu_probe_port,
    mtu_overhead_bytes: draft.mtu_overhead_bytes,
  })
  const update = (patch: Partial<typeof value>) => setValue(old => ({ ...old, ...patch }))
  return <MotionDialogPanel onCancel={onCancel} className="mtu-dialog" nested={nested}>
      <header className="dialog-head">
        <div><h2 id="mtu-dialog-title">MTU 检测设置</h2><p className="muted">首次部署或设置变化时执行，不会随每次下发重复检测。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form mtu-dialog-form labeled-form">
          <FormField label="MTU 模式" hint="选择只检测或自动应用">
            <Select variant="segmented" value={value.mtu_mode} onChange={e => update({ mtu_mode: e.target.value })}>{mtuModes.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select>
          </FormField>
          <FormField label="指定 MTU" hint="0 表示自动。">
            <input type="number" value={value.mtu_value} onChange={e => update({ mtu_value: Number(e.target.value) })} placeholder="0" />
          </FormField>
          <FormField label="探测目标主机" hint="默认 1.1.1.1">
            <input value={value.mtu_probe_host} onChange={e => update({ mtu_probe_host: e.target.value })} placeholder="1.1.1.1" />
          </FormField>
          <FormField label="探测目标端口" hint="默认 443">
            <input type="number" value={value.mtu_probe_port} onChange={e => update({ mtu_probe_port: Number(e.target.value) })} placeholder="443" />
          </FormField>
          <FormField label="额外开销字节" hint="不确定时保持 0">
            <input type="number" value={value.mtu_overhead_bytes} onChange={e => update({ mtu_overhead_bytes: Number(e.target.value) })} placeholder="0" />
          </FormField>
        </div>
      </div>
      <footer className="dialog-actions">
        <button className="ghost" onClick={onCancel}>取消</button>
        <button onClick={() => onSave(value)}>保存设置</button>
      </footer>
  </MotionDialogPanel>
}

function FormField({ label, hint, required, children, className = '', full = false }: { label: string; hint?: string; required?: boolean; children: React.ReactNode; className?: string; full?: boolean }) {
  return (
    <label className={`form-field${full ? ' form-field-full' : ''}${className ? ` ${className}` : ''}`.trim()}>
      <div className="form-field-meta">
        <span className="form-field-label">
          {label}
          {required ? <em aria-label="必填">*</em> : null}
        </span>
        {hint ? <small className="form-field-hint">{hint}</small> : null}
      </div>
      <div className="form-field-control">{children}</div>
    </label>
  )
}

function DetectedEntryAddressNote({ ipv4, ipv6 }: { ipv4?: string; ipv6?: string }) {
  return (
    <small className="detected-address-note">
      <span>Agent 自动检测，无需手动填写</span>
      <span><strong>IPv4</strong> {ipv4 || '待检测'}<i>·</i><strong>IPv6</strong> {ipv6 || '待检测'}</span>
    </small>
  )
}

type ParsedPortRange = { valid: true; start: number; end: number } | { valid: false; error: string }

function parsePortRangeInput(value: string): ParsedPortRange {
  const match = value.trim().match(/^(\d+)\s*-\s*(\d+)$/)
  if (!match) return { valid: false, error: '请输入“起点-终点”，例如 100-65535' }
  const start = Number(match[1])
  const end = Number(match[2])
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start < 100 || end < 100 || start > 65535 || end > 65535) {
    return { valid: false, error: '端口必须在 100-65535 之间' }
  }
  if (start > end) return { valid: false, error: '起点不能大于终点' }
  return { valid: true, start, end }
}

function PortRangeInput({ start, end, onChange, onValidityChange }: { start: number; end: number; onChange: (start: number, end: number) => void; onValidityChange: (valid: boolean) => void }) {
  const [value, setValue] = useState(`${start}-${end}`)
  const messageID = React.useId()
  const parsed = parsePortRangeInput(value)

  useEffect(() => onValidityChange(parsed.valid), [onValidityChange, parsed.valid])

  const updateValue = (next: string) => {
    setValue(next)
    const nextParsed = parsePortRangeInput(next)
    if (nextParsed.valid) onChange(nextParsed.start, nextParsed.end)
  }

  return <div className={`port-range-control${parsed.valid ? ' valid' : ' invalid'}`}>
    <input
      type="text"
      value={value}
      onChange={event => updateValue(event.target.value)}
      onBlur={() => parsed.valid && setValue(`${parsed.start}-${parsed.end}`)}
      placeholder="100-65535"
      aria-label="端口范围"
      aria-invalid={!parsed.valid}
      aria-describedby={parsed.valid ? undefined : messageID}
    />
    {parsed.valid ? <Check className="port-range-valid-icon" size={17} aria-label="端口范围有效" /> : null}
    {!parsed.valid ? <small id={messageID} className="port-range-error" role="alert">{parsed.error}</small> : null}
  </div>
}

function UDPModeSelector({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return <Select variant="segmented" value={value} onChange={event => onChange(event.target.value)} aria-label="UDP 入站模式">
    {udpModes.map(mode => <option key={mode} value={mode}>{labelValue(mode)}</option>)}
  </Select>
}

function ServerActionsDropdown({ server, role = 'viewer', onAction }: { server: Server; role?: Role; onAction: (type: string, server: Server) => void }) {
  const [isOpen, setIsOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [menuPosition, setMenuPosition] = useState({ top: 0, left: 0 })

  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      const target = event.target as HTMLElement | null
      if (target && ref.current && !ref.current.contains(target) && !menuRef.current?.contains(target)) {
        setIsOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const items = [
	{ label: '详细信息', type: 'details' },
	{ label: '基础设置', type: 'edit' },
	{ label: 'DNS 设置', type: 'dns' },
	{ label: 'MTU 设置', type: 'mtu' },
	{ label: 'Agent 设置', type: 'agent-config', admin: true },
	{ label: '更新 Agent', type: 'update-agent', admin: true },
	{ label: 'Agent 命令', type: 'enroll', admin: true },
	{ label: '日志', type: 'logs', admin: true },
	{ label: '诊断', type: 'diagnose', admin: true },
	{ label: '任务', type: 'tasks' },
	{ label: '删除', type: 'delete', danger: true },
	].filter(item => !item.admin || role === 'admin');

  const updateMenuPosition = () => {
    const button = buttonRef.current
    if (!button) return
    const rect = button.getBoundingClientRect()
    const width = 148
    const estimatedHeight = items.length * 34 + 16
    const left = Math.max(8, Math.min(window.innerWidth - width - 8, rect.right - width))
    const top = rect.bottom + estimatedHeight + 8 <= window.innerHeight
      ? rect.bottom + 6
      : Math.max(8, rect.top - estimatedHeight - 6)
    setMenuPosition({ top, left })
  }

  useEffect(() => {
    if (!isOpen) return
    updateMenuPosition()
    const sync = () => updateMenuPosition()
    window.addEventListener('resize', sync)
    window.addEventListener('scroll', sync, true)
    return () => {
      window.removeEventListener('resize', sync)
      window.removeEventListener('scroll', sync, true)
    }
  }, [isOpen, items.length])

  return (
    <div ref={ref} className={isOpen ? 'server-actions-dropdown is-open' : 'server-actions-dropdown'}>
      <button
        ref={buttonRef}
        onClick={(e) => {
          e.stopPropagation();
          if (!isOpen) updateMenuPosition()
          setIsOpen(!isOpen);
        }}
        className="ghost icon-button"
        style={{
          width: '28px',
          height: '28px',
          borderRadius: '50%',
          border: '1px solid var(--border-color)',
          display: 'grid',
          placeContent: 'center',
          cursor: 'pointer',
          backgroundColor: isOpen ? 'var(--bg-control)' : 'var(--bg-card)',
          color: 'var(--text-primary)',
          transition: 'all 0.15s'
        }}
        title="管理操作"
        aria-haspopup="menu"
        aria-expanded={isOpen}
      >
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
          <circle cx="12" cy="12" r="1"/>
          <circle cx="19" cy="12" r="1"/>
          <circle cx="5" cy="12" r="1"/>
        </svg>
      </button>
      {isOpen && createPortal(
        <div ref={menuRef} className="server-actions-menu action-menu-portal" role="menu" style={{
          position: 'fixed',
          top: menuPosition.top,
          left: menuPosition.left,
          right: 'auto',
          bottom: 'auto',
          zIndex: 10000,
          width: '148px',
          minWidth: '148px',
          maxWidth: 'calc(100vw - 16px)',
          maxHeight: 'calc(100vh - 16px)',
          overflowY: 'auto',
          backgroundColor: 'var(--bg-card)',
          border: '1.5px solid var(--border-color)',
          borderRadius: 'var(--radius-md)',
          boxShadow: 'var(--shadow-lg)',
          padding: '6px',
          display: 'flex',
          flexDirection: 'column',
          gap: '2px',
          textAlign: 'left'
        }}>
          {items.map(item => (
            <button
              key={item.type}
              role="menuitem"
              onClick={(e) => {
                e.stopPropagation();
                onAction(item.type, server);
                setIsOpen(false);
              }}
              style={{
                width: '100%',
                padding: '6px 12px',
                border: 'none',
                borderRadius: 'var(--radius-sm)',
                fontSize: '12px',
                fontWeight: 600,
                textAlign: 'left',
                cursor: 'pointer',
                backgroundColor: 'transparent',
                color: item.danger ? 'var(--color-danger)' : 'var(--text-primary)',
                transition: 'background-color 0.15s'
              }}
              onMouseEnter={e => {
                e.currentTarget.style.backgroundColor = item.danger ? 'var(--color-danger-bg)' : 'var(--bg-control)';
              }}
              onMouseLeave={e => {
                e.currentTarget.style.backgroundColor = 'transparent';
              }}
            >
              {item.label}
            </button>
          ))}
        </div>,
        document.body
      )}
    </div>
  );
}

function compactBuildLabel(value?: string) {
  const build = String(value || '').trim()
  if (/^\d{14}$/.test(build)) return `${build.slice(4, 8)}.${build.slice(8, 12)}`
  if (build.length > 10) return build.slice(-10)
  return build || '—'
}

function telemetryPolyline(values: number[], width = 240, height = 48, scaleMax?: number) {
  if (!values.length) return ''
  const max = Math.max(1, scaleMax || 0, ...values)
  const denominator = Math.max(1, values.length - 1)
  return values.map((value, index) => `${(index / denominator) * width},${height - (Math.max(0, value) / max) * (height - 4) - 2}`).join(' ')
}

function ServerTelemetryChart({ samples, type }: { samples: ServerMetricSample[]; type: 'network' | 'latency' }) {
  const points = samples.slice(-60)
  const first = type === 'network' ? points.map(x => Number(x.network_download_bps || 0)) : points.map(x => Number(x.connectivity_latency_ms || 0))
  const second = type === 'network' ? points.map(x => Number(x.network_upload_bps || 0)) : []
  const scaleMax = Math.max(1, ...first, ...second)
  const formatScale = (value: number) => type === 'network' ? formatByteRate(value) : `${Math.round(value)} ms`
  if (points.length < 2) return <div className="server-chart-empty">等待更多数据</div>
  return <div className={`server-chart-wrap ${type}`}>
    <svg className={`server-mini-chart ${type}`} viewBox="0 0 240 48" preserveAspectRatio="none" role="img" aria-label={type === 'network' ? '近期上下行速率' : '近期公网延迟'}>
      <line x1="0" y1="16" x2="240" y2="16" className="server-chart-grid" />
      <line x1="0" y1="32" x2="240" y2="32" className="server-chart-grid" />
      <polyline points={telemetryPolyline(first, 240, 48, scaleMax)} className="server-chart-line primary" vectorEffect="non-scaling-stroke" />
      {second.length ? <polyline points={telemetryPolyline(second, 240, 48, scaleMax)} className="server-chart-line secondary" vectorEffect="non-scaling-stroke" /> : null}
    </svg>
    <span className="server-chart-scale-label high" aria-hidden="true">{formatScale(scaleMax * 2 / 3)}</span>
    <span className="server-chart-scale-label low" aria-hidden="true">{formatScale(scaleMax / 3)}</span>
  </div>
}

function serverTrafficPeriodLabel(server: Server) {
  if ((server.traffic_reset_mode || 'monthly') === 'month_day') return `每月 ${server.traffic_reset_day || 1} 日重置`
  return '自然月重置'
}

function ServerCard({ server, samples, role, expectedBuild, onAction }: { server: Server; samples: ServerMetricSample[]; role?: Role; expectedBuild?: string; onAction: (type: string, server: Server) => void }) {
  const [updateInfoOpen, setUpdateInfoOpen] = useState(false)
  const outdated = Boolean(expectedBuild && server.agent_build && expectedBuild !== server.agent_build)
  const isOnline = server.status.toLowerCase() === 'online';

  return (
    <MotionCard tag="article" className="server-card" hoverEffect={false}>
      {/* Header */}
      <div className="server-card-head">
        <div className="server-card-title">
          <RegionFlag code={serverRegionCode(server)} size={24} />
          <div>
            <h3>{server.name || `server-${server.id}`}</h3>
            <p>#{server.id} · {regionLabel(serverRegionCode(server))}</p>
          </div>
        </div>

        <div className="server-card-head-actions">
          {outdated && (
            <div className={`server-version-update${updateInfoOpen ? ' open' : ''}`}>
              <button type="button" className="server-version-warning" aria-label="Agent 有可用更新" aria-expanded={updateInfoOpen} onClick={() => setUpdateInfoOpen(value => !value)}>有更新</button>
              <div className="server-version-popover" role="tooltip">
                <span>Agent</span>
                <strong>{compactBuildLabel(server.agent_build)} → {compactBuildLabel(expectedBuild)}</strong>
              </div>
            </div>
          )}
          <span className={`server-status-dot ${isOnline ? 'online' : 'offline'}`} aria-label={isOnline ? '在线' : '离线'} />
		  <ServerActionsDropdown server={server} role={role} onAction={onAction} />
        </div>
      </div>

      {/* Meta grid */}
      <div className="server-meta" style={{
        display: 'grid',
        gridTemplateColumns: '1fr 1fr',
        gap: '8px',
        textAlign: 'left',
        fontSize: '12px'
      }}>
        {/* CPU & Memory bars */}
        <div style={{ gridColumn: 'span 2' }}>
          <span style={{ fontSize: '10px', color: 'var(--text-muted)', textTransform: 'uppercase', fontWeight: 600, display: 'block', marginBottom: '6px' }}>系统资源</span>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            {/* CPU Progress */}
            <div>
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '10px', color: 'var(--text-secondary)', marginBottom: '4px' }}>
                <span>CPU 使用率</span>
                <span style={{ fontWeight: 600 }}>{Number.isFinite(server.cpu_usage_percent) ? `${Number(server.cpu_usage_percent).toFixed(1)}%` : '—'}</span>
              </div>
              <div style={{ height: '5px', backgroundColor: 'var(--bg-control)', borderRadius: '2.5px', overflow: 'hidden' }}>
                <div style={{
                  height: '100%',
                  width: `${server.cpu_usage_percent || 0}%`,
                  backgroundColor: 'var(--color-primary)',
                  borderRadius: '2.5px',
                  transition: 'width 0.3s ease'
                }} />
              </div>
            </div>

            {/* Memory Progress */}
            <div>
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '10px', color: 'var(--text-secondary)', marginBottom: '4px' }}>
                <span>内存 ({serverMemoryLabel(server)})</span>
                <span style={{ fontWeight: 600 }}>{server.memory_total_bytes ? `${((server.memory_used_bytes / server.memory_total_bytes) * 100).toFixed(0)}%` : '—'}</span>
              </div>
              <div style={{ height: '5px', backgroundColor: 'var(--bg-control)', borderRadius: '2.5px', overflow: 'hidden' }}>
                <div style={{
                  height: '100%',
                  width: `${server.memory_total_bytes ? (server.memory_used_bytes / server.memory_total_bytes) * 100 : 0}%`,
                  backgroundColor: 'var(--color-success)',
                  borderRadius: '2.5px',
                  transition: 'width 0.3s ease'
                }} />
              </div>
            </div>
          </div>
        </div>

        <div className="server-telemetry" style={{ gridColumn: 'span 2' }}>
          <div className="server-telemetry-heading">
            <span>网络流量</span>
            <small>{server.monitoring_mode === 'standard' ? '标准' : '轻量'}</small>
          </div>
          <div className="server-rate-row">
            <div><ArrowDown size={13} aria-hidden="true" /><span>下载</span><strong>{formatByteRate(server.network_download_bps || 0)}</strong></div>
            <div><ArrowUp size={13} aria-hidden="true" /><span>上传</span><strong>{formatByteRate(server.network_upload_bps || 0)}</strong></div>
            <div><span>本周期</span><strong>{formatBytes((server.traffic_upload_bytes || 0) + (server.traffic_download_bytes || 0))}</strong></div>
          </div>
          <ServerTelemetryChart samples={samples} type="network" />
          <div className="server-chart-caption"><span><i className="download" />下载</span><span><i className="upload" />上传</span><small>{serverTrafficPeriodLabel(server)}</small></div>
        </div>

        {server.connectivity_probe_enabled && <div className="server-connectivity" style={{ gridColumn: 'span 2' }}>
          <div className="server-telemetry-heading">
            <span>公网可访问性</span>
            <small className={server.connectivity_status === 'available' ? 'is-ok' : server.connectivity_status === 'unavailable' ? 'is-error' : ''}>{server.connectivity_status === 'available' ? '可用' : server.connectivity_status === 'unavailable' ? '不可用' : '等待检测'}</small>
          </div>
          <div className="server-latency-value"><strong>{server.connectivity_status === 'available' ? `${server.connectivity_latency_ms || 0} ms` : '—'}</strong><span>cp.cloudflare.com · 每分钟</span></div>
          <ServerTelemetryChart samples={samples.filter(x => x.connectivity_available !== undefined)} type="latency" />
        </div>}

      </div>
    </MotionCard>
  )
}

function ServerDetailItem({ label, value, wide = false }: { label: string; value: React.ReactNode; wide?: boolean }) {
  return (
    <div className={`server-detail-item${wide ? ' wide' : ''}`}>
      <dt>{label}</dt>
      <dd>{value || '—'}</dd>
    </div>
  )
}

function ServerDetailDialog({ server, onClose }: { server: Server; onClose: () => void }) {
  const isOnline = String(server.status || '').toLowerCase() === 'online'
  const connectivityLabel = !server.connectivity_probe_enabled
    ? '未启用检测'
    : server.connectivity_status === 'available'
      ? `可用 · ${server.connectivity_latency_ms || 0} ms`
      : server.connectivity_status === 'unavailable' ? '不可用' : '等待检测'

  return (
    <MotionDialogPanel onCancel={onClose} className="server-detail-dialog">
      <header className="dialog-head server-detail-head">
        <div className="server-detail-title">
          <RegionFlag code={serverRegionCode(server)} size={28} />
          <div>
            <h2>{server.name || `server-${server.id}`}</h2>
            <p>服务器 #{server.id} · {regionLabel(serverRegionCode(server))}</p>
          </div>
        </div>
        <div className="server-detail-head-actions">
          <span className={`server-detail-status ${isOnline ? 'online' : 'offline'}`}><i />{isOnline ? '在线' : '离线'}</span>
          <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><XIcon /></button>
        </div>
      </header>
      <div className="dialog-body server-detail-body">
        <section className="server-detail-section">
          <div className="server-detail-section-head"><Database size={15} /><h3>系统与核心</h3></div>
          <dl className="server-detail-grid">
            <ServerDetailItem label="Agent 版本" value={server.agent_version || '—'} />
            <ServerDetailItem label="Agent 构建" value={server.agent_build ? compactBuildLabel(server.agent_build) : '—'} />
            <ServerDetailItem label="系统架构" value={server.arch || '未知架构'} />
            <ServerDetailItem label="操作系统" value={serverOSLabel(server)} />
            <ServerDetailItem label="发行版" value={[server.distro_id, server.distro_version].filter(Boolean).join(' ') || '—'} />
            <ServerDetailItem label="核心版本" value={server.sing_box_version || '—'} />
            <ServerDetailItem label="运行库" value={server.libc || '—'} />
            <ServerDetailItem label="服务管理器" value={server.service_manager || '—'} />
            <ServerDetailItem label="包管理器" value={server.package_manager || '—'} />
            <ServerDetailItem label="CPU" value={server.cpu || '—'} />
          </dl>
        </section>

        <section className="server-detail-section">
          <div className="server-detail-section-head"><Globe size={15} /><h3>网络配置</h3></div>
          <dl className="server-detail-grid">
            <ServerDetailItem label="入口地址" value={serverDefaultEntryAddress(server) || '待检测'} wide />
            <ServerDetailItem label="公网 IPv4" value={server.public_ipv4 || '—'} />
            <ServerDetailItem label="公网 IPv6" value={server.public_ipv6 || '—'} />
            <ServerDetailItem label="监听地址" value={server.listen_ip || '—'} />
            <ServerDetailItem label="入口模式" value={labelValue(server.entry_ip_mode || 'auto')} />
            <ServerDetailItem label="网络优先" value={labelValue(server.ip_stack || 'unknown')} />
            <ServerDetailItem label="UDP 模式" value={labelValue(server.udp_inbound_mode || 'unknown')} />
            <ServerDetailItem label="端口范围" value={portRangeLabel(server)} />
			<ServerDetailItem label="SSH 端口" value={server.ssh_port ? String(server.ssh_port) : '未设置'} />
            <ServerDetailItem label="安装时尝试 BBR + FQ" value={server.bbr_enabled ? '是' : '否'} />
            <ServerDetailItem label="公网可访问性" value={connectivityLabel} />
          </dl>
        </section>

        <section className="server-detail-section">
          <div className="server-detail-section-head"><Activity size={15} /><h3>运行状态</h3></div>
          <dl className="server-detail-grid">
            <ServerDetailItem label="CPU 使用率" value={Number.isFinite(server.cpu_usage_percent) ? `${Number(server.cpu_usage_percent).toFixed(1)}%` : '—'} />
            <ServerDetailItem label="内存使用" value={serverMemoryLabel(server)} />
            <ServerDetailItem label="Agent 内存" value={server.agent_memory_bytes ? formatBytes(server.agent_memory_bytes) : '—'} />
            <ServerDetailItem label="监控模式" value={server.monitoring_mode === 'standard' ? '标准' : '轻量'} />
            <ServerDetailItem label="下载速率" value={formatByteRate(server.network_download_bps || 0)} />
            <ServerDetailItem label="上传速率" value={formatByteRate(server.network_upload_bps || 0)} />
            <ServerDetailItem label="周期流量" value={formatBytes((server.traffic_upload_bytes || 0) + (server.traffic_download_bytes || 0))} />
            <ServerDetailItem label="流量重置" value={serverTrafficPeriodLabel(server)} />
            <ServerDetailItem label="数据更新时间" value={server.telemetry_updated_at ? formatTableTime(server.telemetry_updated_at) : '—'} wide />
          </dl>
        </section>
      </div>
      <footer className="dialog-actions"><button type="button" onClick={onClose}>关闭</button></footer>
    </MotionDialogPanel>
  )
}

function compactServerRows(servers: Server[]) {
  return servers.map(s => ({
    _server: s,
    name: <span className="server-list-name"><RegionFlag code={serverRegionCode(s)} size={18} /><span>{s.name}</span></span>,
    region: regionLabel(serverRegionCode(s)),
    status: s.status,
    entry_address: s.entry_address || '',
    entry_ip_mode: labelValue(s.entry_ip_mode || 'auto'),
    public_ipv4: s.public_ipv4,
    public_ipv6: s.public_ipv6,
    listen_ip: s.listen_ip,
    ip_stack: labelValue(s.ip_stack || 'unknown'),
    udp_inbound_mode: labelValue(s.udp_inbound_mode || 'unknown'),
    port_range: portRangeLabel(s),
    system: serverOSLabel(s),
    arch: s.arch,
    cpu_usage: Number.isFinite(s.cpu_usage_percent) ? `${Number(s.cpu_usage_percent).toFixed(1)}%` : '',
    memory: serverMemoryLabel(s),
    download_rate: formatByteRate(s.network_download_bps || 0),
    upload_rate: formatByteRate(s.network_upload_bps || 0),
    period_traffic: formatBytes((s.traffic_upload_bytes || 0) + (s.traffic_download_bytes || 0)),
    agent_memory: formatBytes(s.agent_memory_bytes || 0),
    agent_version: s.agent_version,
    agent_build: s.agent_build,
    sing_box_version: s.sing_box_version,
  }))
}

function portRangeLabel(s: Server) { return s.port_range_start || s.port_range_end ? `${s.port_range_start || 0}-${s.port_range_end || 0}` : '—' }
function serverOSLabel(s: Server) { return [s.distro_name || s.os, s.distro_version].filter(Boolean).join(' ') || '未知系统' }
function serverMemoryLabel(s: Server) { return s.memory_total_bytes ? `${formatBytes(s.memory_used_bytes || 0)} / ${formatBytes(s.memory_total_bytes)}` : '—' }
function serverDefaultEntryAddress(server?: Server) {
  if (!server) return ''
  const mode = server.entry_ip_mode || 'auto'
  if (mode === 'ipv4') return server.public_ipv4 || ''
  if (mode === 'ipv6') return server.public_ipv6 || ''
  if (mode === 'custom') return server.entry_address || ''
  return server.public_ipv4 || server.public_ipv6 || ''
}
function entryAddressByMode(server: Server | undefined, mode: EntryIPMode | undefined, customAddress = '') {
  const selectedMode = mode || 'auto'
  if (selectedMode === 'custom') return customAddress.trim()
  if (!server) return ''
  if (selectedMode === 'ipv4') return server.public_ipv4 || ''
  if (selectedMode === 'ipv6') return server.public_ipv6 || ''
  return serverDefaultEntryAddress(server)
}
function entryAddressModeLabel(mode: EntryIPMode, server?: Server) {
  if (mode === 'auto') {
    const address = serverDefaultEntryAddress(server)
    return `使用服务器默认${server ? `（${labelValue(server.entry_ip_mode || 'auto')}${address ? ` · ${address}` : ' · 待检测'}）` : ''}`
  }
  if (mode === 'ipv4') return `使用 IPv4${server?.public_ipv4 ? `（${server.public_ipv4}）` : '（待检测）'}`
  if (mode === 'ipv6') return `使用 IPv6${server?.public_ipv6 ? `（${server.public_ipv6}）` : '（待检测）'}`
  return '填写其他入口 IP / 域名'
}
function formatHostPort(host: string, port: number) {
  const clean = String(host || '').trim()
  if (!clean) return `待检测:${port || 0}`
  const formattedHost = clean.includes(':') && !clean.startsWith('[') ? `[${clean}]` : clean
  return `${formattedHost}:${port || 0}`
}
function inboundEntryAddress(data: any, entry: Inbound) {
	if (entry.dns_sync_enabled && entry.dns_domain) return entry.dns_domain
  const server = (data.servers || []).find((s: Server) => s.id === entry.server_id)
  return entryAddressByMode(server, entry.entry_ip_mode || 'auto', entry.external_ip || '')
}

type ProxyToolAction = 'server' | 'entry' | 'imported' | 'routing' | 'transport'
const proxyToolDragType = 'application/oboard-proxy-tool'

function ProxyPathsWorkspace({ data, client, load, loading }: any) {
  const preferredRoot = (data.servers || []).find((server: Server) => (data.inbounds || []).some((entry: Inbound) => entry.server_id === server.id && entry.enabled !== false)) || (data.servers || [])[0]
  const [selectedServer, setSelectedServer] = useState<number>(preferredRoot?.id || 0)
  const [panelActionsTarget, setPanelActionsTarget] = useState<HTMLDivElement | null>(null)
  useEffect(() => {
    const servers: Server[] = data.servers || []
    if (selectedServer && servers.some(server => server.id === selectedServer)) return
    const next = servers.find(server => (data.inbounds || []).some((entry: Inbound) => entry.server_id === server.id && entry.enabled !== false)) || servers[0]
    if (next) setSelectedServer(next.id)
  }, [data.servers?.length, data.inbounds?.length, selectedServer])
  if (loading && !data.servers?.length) return <Panel title="代理链路"><DashboardSkeleton /></Panel>
  const conflicts = deploymentConflicts(data)
  return <Panel title="代理链路" className="proxy-path-panel" actions={<div ref={setPanelActionsTarget} className="proxy-path-panel-actions" />}>
    {conflicts.length > 0 && <div className="error"><strong>下发前需要处理：</strong>{conflicts.map((x, i) => <div key={i}>{x}</div>)}</div>}
    <div className="proxy-shell">
      <ProxyGraphBoundary onRetry={load}>
        <ProxyOverview data={data} client={client} load={load} selectedServer={selectedServer} setSelectedServer={setSelectedServer} panelActionsTarget={panelActionsTarget} />
      </ProxyGraphBoundary>
    </div>
  </Panel>
}

class ProxyGraphBoundary extends React.Component<{ children: React.ReactNode; onRetry: () => Promise<void> }, { error: Error | null }> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error) {
    console.error('Proxy path graph rendering failed:', error)
  }

  retry = async () => {
    this.setState({ error: null })
    await this.props.onRetry()
  }

  render() {
    if (this.state.error) {
      return <div className="empty proxy-graph-error">
        <strong>代理链路暂时无法显示</strong>
        <span>数据没有丢失。请刷新链路图；若问题持续，请查看浏览器控制台或联系管理员。</span>
        <button onClick={this.retry}>重新加载</button>
      </div>
    }
    return this.props.children
  }
}

type RoutingMatchKind = 'domain_suffix' | 'domain' | 'ip_cidr' | 'port' | 'port_range' | 'geosite' | 'geoip' | 'all'
type RoutingDraft = { server_id: number; name: string; priority: number; match_kind: RoutingMatchKind; match_value: string; action: RouteAction; outbound_id: number; external_outbound_id: number; warp_profile_id: number; interface_name: string; enabled: boolean }
type TransportMode = 'port-forward' | 'tunnel'
type TransportDraft = { mode: TransportMode; name: string; source_server_id: number; target_server_id: number; listen_ip: string; listen_port: number; target_port: number; protocol: ForwardProtocol; backend: ForwardBackend; type: TunnelType; priority: number; config_json: string; enabled: boolean }
type GraphEntity = { type: 'server' | 'entry' | 'imported' | 'port-forward' | 'tunnel' | 'proxy-path' | 'proxy-path-step'; id: number; label: string; path_id?: number; node_id?: string }
type ImportedNodeDraft = { content: string; scope: 'global' | 'server'; server_id: number; expose_to_users: boolean; position?: GraphPosition | null }
type CanvasServerInstance = { instance_id: string; server_id: number }
type TransportDialogRequest = {
  target: TransportDialogTarget
  current?: string
  resolve: (value: TransportSelection | null) => void
}

function ProxyOverview({ data, client, load, selectedServer, setSelectedServer, panelActionsTarget }: any) {
  const dialogs = useDialogs()
  const servers: Server[] = data.servers || []
  const entries: Inbound[] = data.inbounds || []
  const selected = servers.find(s => s.id === selectedServer) || servers[0]
  const selectedEntries = selected ? entries.filter(x => x.server_id === selected.id) : []
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const [isToolbarCollapsed, setIsToolbarCollapsed] = useState(() => window.innerWidth <= 820)
  const [toolboxPosition, setToolboxPosition] = useState<GraphPosition>(() => loadGraphToolboxPosition())
  const [toolboxDragging, setToolboxDragging] = useState(false)
  const workspaceRef = useRef<HTMLDivElement>(null)
  const toolboxRef = useRef<HTMLDivElement>(null)
  const toolboxWasDragged = useRef(false)
  const hasInitialSafeFit = useRef(false)
  const initialSafeFitFrame = useRef<number | null>(null)
  const pendingServerSafeFit = useRef(0)
  const serverSafeFitTimer = useRef<number | null>(null)
  const [initialViewportReady, setInitialViewportReady] = useState(false)
  const [positions, setPositions] = useState<Record<string, { x: number; y: number }>>(() => loadGraphPositions())
  const [flowInstance, setFlowInstance] = useState<ReactFlowInstance | null>(null)
	const [canvasImportedIDs, setCanvasImportedIDs] = useState<number[]>([])
	const [canvasServerInstances, setCanvasServerInstances] = useState<CanvasServerInstance[]>([])
	const canvasServerSequence = useRef(0)
	const builtFlow = useMemo(() => editableProxyFlow(data, positions, selected?.id || 0, canvasImportedIDs, canvasServerInstances), [data.servers, data.inbounds, data.external_outbounds, data.proxy_paths, data.proxy_path_steps, data.port_forwards, data.tunnels, positions, selected?.id, canvasImportedIDs.join(','), canvasServerInstances.map(item => `${item.instance_id}:${item.server_id}`).join(',')])
  const [nodes, setNodes] = useState<Node[]>(builtFlow.nodes)
  const [edges, setEdges] = useState<Edge[]>(builtFlow.edges)
  const [serverDraft, setServerDraft] = useState<ReturnType<typeof defaultServerDraft> | null>(null)
  const serverDraftPosition = useRef<GraphPosition | null>(null)
  const [entryDraft, setEntryDraft] = useState<any | null>(null)
  const [accessEntry, setAccessEntry] = useState<Inbound | null>(null)
  const [editEntry, setEditEntry] = useState<any | null>(null)
	const [routingDraft, setRoutingDraft] = useState<RoutingDraft | null>(null)
	const [transportDraft, setTransportDraft] = useState<TransportDraft | null>(null)
	const [importDraft, setImportDraft] = useState<ImportedNodeDraft | null>(null)
	const [configNode, setConfigNode] = useState<ExternalOutbound | null>(null)
	const [namingPath, setNamingPath] = useState<ProxyPath | null>(null)
	const [transportRequest, setTransportRequest] = useState<TransportDialogRequest | null>(null)
  const [graphMenu, setGraphMenu] = useState<{ x: number; y: number; entity: GraphEntity } | null>(null)
  const [activeGraphEntity, setActiveGraphEntity] = useState<GraphEntity | null>(null)
  useEffect(() => { setNodes(builtFlow.nodes); setEdges(builtFlow.edges) }, [builtFlow])
  useEffect(() => {
    if (!selectedServer && servers[0]) setSelectedServer(servers[0].id)
    if (selectedServer && !servers.some(s => s.id === selectedServer) && servers[0]) setSelectedServer(servers[0].id)
  }, [servers.length, selectedServer])
  // Fit the graph into the free canvas area only — leave room for the floating
  // left toolbox (and optional right inspector) so nodes are not covered on open.
  const fitGraphToSafeArea = (duration = 260, instance = flowInstance) => {
    if (!instance || !builtFlow.nodes.length) return false
    const flowEl = workspaceRef.current?.querySelector('.proxy-flow') as HTMLElement | null
    const toolbox = toolboxRef.current
    if (!flowEl) {
      instance.fitView({ padding: 0.18, maxZoom: 0.92, duration })
      return true
    }
    const width = flowEl.clientWidth
    const height = flowEl.clientHeight
    if (width < 40 || height < 40) return false

    const pad = 28
    let leftInset = pad
    let rightInset = pad
    const topInset = pad
    const bottomInset = pad + 56 // React Flow controls sit bottom-right

    if (toolbox && !isToolbarCollapsed) {
      const flowRect = flowEl.getBoundingClientRect()
      const boxRect = toolbox.getBoundingClientRect()
      // Only reserve space when the toolbox still overlaps the left side of the canvas.
      if (boxRect.right > flowRect.left + 8 && boxRect.left < flowRect.left + width * 0.55) {
        leftInset = Math.max(leftInset, Math.round(boxRect.right - flowRect.left) + 20)
      }
    }
    if (inspectorOpen) {
      rightInset = Math.max(rightInset, 24)
    }

    const usableWidth = Math.max(120, width - leftInset - rightInset)
    const usableHeight = Math.max(120, height - topInset - bottomInset)
    const bounds = getNodesBounds(instance.getNodes())
    if (!Number.isFinite(bounds.width) || !Number.isFinite(bounds.height) || bounds.width <= 0 || bounds.height <= 0) {
      instance.fitView({ padding: 0.18, maxZoom: 0.92, duration })
      return true
    }

    const viewport = getViewportForBounds(bounds, usableWidth, usableHeight, 0.3, 0.92, 0.12)
    instance.setViewport(
      {
        x: viewport.x + leftInset,
        y: viewport.y + topInset,
        zoom: viewport.zoom,
      },
      { duration },
    )
    return true
  }
  const prepareInitialViewport = (instance: ReactFlowInstance, attempt = 0) => {
    if (hasInitialSafeFit.current) {
      setInitialViewportReady(true)
      return
    }
    if (!builtFlow.nodes.length) {
      hasInitialSafeFit.current = true
      setInitialViewportReady(true)
      return
    }
    // onInit can run before the browser has committed the final canvas size.
    // Keep the node layer hidden and retry for a few frames instead of showing
    // the default viewport and visibly moving it afterwards.
    if (fitGraphToSafeArea(0, instance)) {
      hasInitialSafeFit.current = true
      setInitialViewportReady(true)
      return
    }
    if (attempt >= 5) {
      instance.fitView({ padding: 0.18, maxZoom: 0.92, duration: 0 })
      hasInitialSafeFit.current = true
      setInitialViewportReady(true)
      return
    }
    initialSafeFitFrame.current = window.requestAnimationFrame(() => prepareInitialViewport(instance, attempt + 1))
  }
  const initializeFlow = (instance: ReactFlowInstance) => {
    setFlowInstance(instance)
    if (initialSafeFitFrame.current !== null) window.cancelAnimationFrame(initialSafeFitFrame.current)
    initialSafeFitFrame.current = window.requestAnimationFrame(() => prepareInitialViewport(instance))
  }
  useEffect(() => () => {
    if (initialSafeFitFrame.current !== null) window.cancelAnimationFrame(initialSafeFitFrame.current)
    if (serverSafeFitTimer.current !== null) window.clearTimeout(serverSafeFitTimer.current)
  }, [])
  useEffect(() => {
    if (!flowInstance || !selected?.id || pendingServerSafeFit.current !== selected.id) return
    if (serverSafeFitTimer.current !== null) window.clearTimeout(serverSafeFitTimer.current)
    // Let React Flow commit and measure the newly selected server's nodes before
    // fitting the same toolbox-safe viewport used by the Auto arrange button.
    serverSafeFitTimer.current = window.setTimeout(() => {
      if (pendingServerSafeFit.current !== selected.id) return
      pendingServerSafeFit.current = 0
      fitGraphToSafeArea(280)
      serverSafeFitTimer.current = null
    }, 40)
    return () => {
      if (serverSafeFitTimer.current !== null) {
        window.clearTimeout(serverSafeFitTimer.current)
        serverSafeFitTimer.current = null
      }
    }
  }, [flowInstance, selected?.id, builtFlow])
  const clampToolboxPosition = (position: GraphPosition) => {
    const workspace = workspaceRef.current
    const toolbox = toolboxRef.current
    if (!workspace || !toolbox) return position
    const maxX = Math.max(8, workspace.clientWidth - toolbox.offsetWidth - 8)
    const maxY = Math.max(8, workspace.clientHeight - toolbox.offsetHeight - 8)
    return { x: Math.max(8, Math.min(maxX, position.x)), y: Math.max(8, Math.min(maxY, position.y)) }
  }
  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      setToolboxPosition(current => {
        const next = clampToolboxPosition(current)
        saveGraphToolboxPosition(next)
        return next
      })
    })
    return () => window.cancelAnimationFrame(frame)
  }, [isToolbarCollapsed, inspectorOpen])
  useEffect(() => {
    const clampOnResize = () => setToolboxPosition(current => {
      const next = clampToolboxPosition(current)
      saveGraphToolboxPosition(next)
      return next
    })
    window.addEventListener('resize', clampOnResize)
    return () => window.removeEventListener('resize', clampOnResize)
  }, [])
  const beginToolboxDrag = (event: React.PointerEvent<HTMLElement>) => {
    if (event.button !== 0) return
    const start = { x: event.clientX, y: event.clientY, position: toolboxPosition }
    let latest = toolboxPosition
    toolboxWasDragged.current = false
    setToolboxDragging(true)
    document.body.style.userSelect = 'none'
    const move = (nextEvent: PointerEvent) => {
      const dx = nextEvent.clientX - start.x
      const dy = nextEvent.clientY - start.y
      if (Math.abs(dx) + Math.abs(dy) > 4) toolboxWasDragged.current = true
      latest = clampToolboxPosition({ x: start.position.x + dx, y: start.position.y + dy })
      setToolboxPosition(latest)
    }
    const stop = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', stop)
      window.removeEventListener('pointercancel', stop)
      document.body.style.userSelect = ''
      setToolboxDragging(false)
      saveGraphToolboxPosition(latest)
      window.setTimeout(() => { toolboxWasDragged.current = false }, 0)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', stop, { once: true })
    window.addEventListener('pointercancel', stop, { once: true })
  }
  const toggleToolbar = () => {
    if (toolboxWasDragged.current) return
    setIsToolbarCollapsed(value => !value)
  }
  const autoArrangeGraph = () => {
    const laidOut = autoLayoutProxyGraphPositions(data, selected?.id || 0, canvasImportedIDs, canvasServerInstances)
    if (!Object.keys(laidOut).length) return
    const next = { ...positions, ...laidOut }
    setPositions(next)
    saveGraphPositions(next)
    window.setTimeout(() => fitGraphToSafeArea(280), 40)
  }
  const selectEntryServer = (value: string | number) => {
    const nextServerID = Number(value)
    if (!nextServerID || nextServerID === selected?.id) return
    const laidOut = autoLayoutProxyGraphPositions(data, nextServerID, canvasImportedIDs, canvasServerInstances)
    if (Object.keys(laidOut).length) {
      const next = { ...positions, ...laidOut }
      setPositions(next)
      saveGraphPositions(next)
    }
    pendingServerSafeFit.current = nextServerID
    setSelectedServer(nextServerID)
  }
  const placeGraphNode = (id: string, position: GraphPosition) => {
    const next = { ...positions, [id]: snapGraphPosition(position) }
    setPositions(next)
    saveGraphPositions(next)
  }
  const onNodesChange = (changes: NodeChange[]) => setNodes(nds => {
    const next = applyNodeChanges(changes, nds)
    return next
  })
  const onNodeDragStop = (_: React.MouseEvent, node: Node) => {
    const next = { ...positions, [node.id]: { x: node.position.x, y: node.position.y } }
    setPositions(next)
    saveGraphPositions(next)
	}
	const onEdgesChange = (changes: EdgeChange[]) => setEdges(eds => applyEdgeChanges(changes, eds))
	const graphEntity = (id: string) => nodes.find(n => n.id === id)?.data?.entity as GraphEntity | undefined
	const targetStepForGraphTarget = async (targetID: string): Promise<({ node_type: 'imported'; external_outbound_id: number } | { node_type: 'server_inbound'; server_id?: number; inbound_id?: number }) | null> => {
	  const entity = graphEntity(targetID)
	  if (entity?.type === 'imported') return { node_type: 'imported', external_outbound_id: entity.id }
	  if (entity?.type === 'entry') return { node_type: 'server_inbound', inbound_id: entity.id }
	  const serverID = entity?.type === 'server' ? entity.id : graphNodeServerId(targetID, data, canvasServerInstances)
	  return serverID ? { node_type: 'server_inbound', server_id: serverID } : null
	}
	const chooseEntryForServer = async (serverID: number, context: 'current' | 'source' = 'source'): Promise<Inbound | null> => {
	  const available = entries.filter(x => x.server_id === serverID && x.enabled !== false)
	  if (!available.length) {
	    await dialogs.alert({
	      title: '没有可用入口',
	      message: context === 'current'
	        ? '当前一级服务器还没有入口协议。先创建入口，再把导入节点加入链路。'
	        : '这台服务器还没有入口协议。先创建入口，再把导入节点加入链路。',
	    })
	    return null
	  }
	  if (available.length === 1) return available[0]
	  const selectedID = await dialogs.prompt({
	    title: '选择入口',
	    message: context === 'current' ? '选择这个导入节点要加入哪一个入口链路。' : '选择这台服务器的哪个入口要走这个导入节点。',
	    defaultValue: String(available[0].id),
	    choices: available.map(x => ({ value: String(x.id), label: `${x.name} / ${labelProtocol(x.protocol)}:${x.port}` })),
	  })
	  return available.find(x => x.id === Number(selectedID)) || null
	}
	const chooseCurrentEntry = async (): Promise<Inbound | null> => {
	  if (!selected?.id) return null
	  return chooseEntryForServer(selected.id, 'current')
	}
	const putImportedOnCanvas = (node: ExternalOutbound) => {
	  setCanvasImportedIDs(ids => ids.includes(node.id) ? ids : [...ids, node.id])
	  const id = `imported-${node.id}`
	  if (!positions[id]) placeGraphNode(id, defaultImportedGraphPosition(canvasImportedIDs.length))
	}
	const putServerOnCanvas = (server: Server) => {
	  const instance: CanvasServerInstance = { instance_id: `${server.id}-${Date.now()}-${++canvasServerSequence.current}`, server_id: server.id }
	  const id = canvasServerNodeID(instance)
	  setCanvasServerInstances(items => [...items, instance])
	  placeGraphNode(id, defaultServerGraphPosition(Math.max(1, builtFlow.nodes.filter(node => String(node.className || '').includes('server-graph-node')).length)))
	  window.setTimeout(() => fitGraphToSafeArea(280), 40)
	}
	const addImportedToCurrentEntry = async (node: ExternalOutbound) => {
	  const entry = await chooseCurrentEntry()
	  if (!entry) return
	  setCanvasImportedIDs(ids => ids.includes(node.id) ? ids : [...ids, node.id])
	  await createPathFromEntry(entry, { node_type: 'imported', external_outbound_id: node.id })
	}
	// One panel collects the whole selection so the operator can revise any field
	// before committing, instead of walking an unrevisable chain of prompts.
	const openTransportDialog = (request: Omit<TransportDialogRequest, 'resolve'>) =>
	  new Promise<TransportSelection | null>(resolve => setTransportRequest({ ...request, resolve }))
	const chooseTransportForTarget = async (target: { node_type: 'imported' | 'server_inbound'; server_id?: number; inbound_id?: number }, current?: ProxyPathStep, sourceLabel?: string): Promise<TransportSelection | null> => {
	  const targetInbound = target.inbound_id ? entries.find(item => item.id === target.inbound_id) : null
	  const targetServerID = target.server_id || targetInbound?.server_id
	  const targetServer = targetServerID ? servers.find(item => item.id === targetServerID) || null : null
	  const importedNode = target.node_type === 'imported' && (target as any).external_outbound_id
	    ? ((data.external_outbounds || []) as ExternalOutbound[]).find(item => item.id === (target as any).external_outbound_id)
	    : undefined
	  const targetLabel = target.node_type === 'imported'
	    ? importedNode?.name || '导入节点'
	    : targetServer?.name || `服务器 ${targetServerID || ''}`.trim()
	  return openTransportDialog({
	    target: {
	      sourceLabel: sourceLabel || selected?.name || '当前节点',
	      targetLabel,
	      targetInboundLabel: targetInbound ? `${targetInbound.name || `入口 ${targetInbound.id}`} / ${labelProtocol(targetInbound.protocol)}:${targetInbound.port}` : undefined,
	      targetSSHPort: targetServer?.ssh_port || 0,
	      importedOnly: target.node_type === 'imported',
	    },
	    current: current?.config_json,
	  })
	}
	const proxyPathDisplayName = (path: ProxyPath) => path.name || `路径 ${path.id}`
	const confirmSharedAppend = async (count: number, targetLabel: string, names: string[] = [], mode: 'append' | 'create' = 'append') => {
	  if (count <= 1) return true
	  const appending = mode === 'append'
	  return dialogs.confirm({
	    title: appending ? '追加到多条路径' : '为每个入口新建链路',
	    message: <div className="dialog-detail">
	      <p>
	        {appending
	          ? `会把 ${targetLabel || '目标节点'} 追加到以下 ${count} 条已有路径的末尾。`
	          : `会为以下 ${count} 个入口各新建一条链路，出口都是 ${targetLabel || '目标节点'}。`}
	        每条路径独立保存，需要下发配置后生效。
	      </p>
	      {names.length > 0 && <ul>{names.map((name, index) => <li key={index}>{name}</li>)}</ul>}
	    </div>,
	    confirmText: appending ? `追加到这 ${count} 条路径` : `新建这 ${count} 条链路`,
	  })
	}
	// Every shared append is a separate request. Report what actually landed rather
	// than stopping at the first failure and leaving the operator to guess which
	// branches were modified.
	const runSharedAppends = async <T,>(items: T[], label: (item: T) => string, append: (item: T) => Promise<ProxyPathStep | null>) => {
	  const createdSteps: ProxyPathStep[] = []
	  const failures: string[] = []
	  for (const item of items) {
	    try {
	      const created = await append(item)
	      if (created) createdSteps.push(created)
	      else failures.push(`${label(item)}：未创建路径步骤`)
	    } catch (error: any) {
	      failures.push(`${label(item)}：${localizeErrorMessage(error?.message || error)}`)
	    }
	  }
	  if (failures.length) {
	    await dialogs.alert({
	      title: createdSteps.length ? '部分路径未追加' : '追加失败',
	      message: <div className="dialog-detail">
	        <p>{createdSteps.length ? `${createdSteps.length} 条路径已追加，以下 ${failures.length} 条失败：` : '没有路径被修改。失败原因：'}</p>
	        <ul>{failures.map((item, index) => <li key={index}>{item}</li>)}</ul>
	      </div>,
	    })
	  }
	  return createdSteps
	}
	const ensureTransparentPathExclusive = async (pathID: number, inboundID: number) => {
	  const siblings: ProxyPath[] = (data.proxy_paths || []).filter((path: ProxyPath) => path.enabled !== false && path.inbound_id === inboundID && path.id !== pathID)
	  if (!siblings.length) return true
	  const ok = await dialogs.confirm({
	    title: '端口转发将独占入口',
	    message: <div className="dialog-detail">
	      <p>透明端口转发必须独占入口端口，因此以下 {siblings.length} 条分支及其后续节点会被删除，对应的订阅节点也会消失。</p>
	      <ul>{siblings.map(path => <li key={path.id}>{proxyPathDisplayName(path)}</li>)}</ul>
	    </div>,
	    tone: 'danger',
	    confirmText: `删除这 ${siblings.length} 条分支`,
	  })
	  if (!ok) return false
	  const failures: string[] = []
	  for (const path of siblings) {
	    try {
	      await client.request(`/proxy-paths/${path.id}`, { method: 'DELETE' })
	    } catch (error: any) {
	      failures.push(`${proxyPathDisplayName(path)}：${localizeErrorMessage(error?.message || error)}`)
	    }
	  }
	  if (failures.length) {
	    await dialogs.alert({
	      title: '分支未全部删除',
	      message: <div className="dialog-detail">
	        <p>入口仍被其他分支占用，端口转发无法启用。请先处理：</p>
	        <ul>{failures.map((item, index) => <li key={index}>{item}</li>)}</ul>
	      </div>,
	    })
	    return false
	  }
	  return true
	}
	const createPathFromEntry = async (entry: Inbound, target: ({ node_type: 'imported'; external_outbound_id: number } | { node_type: 'server_inbound'; server_id?: number; inbound_id?: number }) & Partial<ProxyPathStep>): Promise<ProxyPathStep | null> => {
	  if (target.transport_mode === 'port_forward' && !await ensureTransparentPathExclusive(0, entry.id)) return null
	  const result = await client.request('/proxy-paths', { method: 'POST', body: JSON.stringify({ name_mode: 'auto', name_template: [], inbound_id: entry.id, enabled: true }) }) as { proxy_path?: ProxyPath }
	  if (!result.proxy_path?.id) return null
	  let createdStep: ProxyPathStep | null = null
	  try {
	    const stepResult = await client.request('/proxy-path-steps', { method: 'POST', body: JSON.stringify({ path_id: result.proxy_path.id, position: 1, transport_mode: 'singbox', config_json: '{}', ...target }) }) as { proxy_path_step?: ProxyPathStep }
	    createdStep = stepResult.proxy_path_step || null
	  } catch (error) {
	    await client.request(`/proxy-paths/${result.proxy_path.id}`, { method: 'DELETE' }).catch(() => undefined)
	    throw error
	  }
	  await load()
	  return createdStep
	}
	const appendPathAfterStep = async (stepID: number, target: ({ node_type: 'imported'; external_outbound_id: number } | { node_type: 'server_inbound'; server_id?: number; inbound_id?: number }) & Partial<ProxyPathStep>): Promise<ProxyPathStep | null> => {
	  const steps: ProxyPathStep[] = data.proxy_path_steps || []
	  const step = steps.find(x => x.id === stepID)
	  if (!step) {
	    await dialogs.alert({ title: '无法追加链路', message: '没有找到这条路径的当前位置，请刷新后重试。' })
	    return null
	  }
	  const path = ((data.proxy_paths || []) as ProxyPath[]).find(item => item.id === step.path_id)
	  if (target.transport_mode === 'port_forward' && path && !await ensureTransparentPathExclusive(path.id, path.inbound_id)) return null
	  const pathSteps = steps.filter(x => x.path_id === step.path_id)
	  const nextPosition = Math.max(0, ...pathSteps.map(x => x.position || 0)) + 1
	  const result = await client.request('/proxy-path-steps', { method: 'POST', body: JSON.stringify({ path_id: step.path_id, position: nextPosition, transport_mode: 'singbox', config_json: '{}', ...target }) }) as { proxy_path_step?: ProxyPathStep }
	  await load()
	  return result.proxy_path_step || null
	}
	const consumeCanvasServerTarget = (targetID: string, createdSteps: ProxyPathStep[]) => {
	  if (!targetID.startsWith('canvas-server-') || !createdSteps.length) return
	  setCanvasServerInstances(items => items.filter(item => canvasServerNodeID(item) !== targetID))
	  setPositions(current => {
	    const sourcePosition = current[targetID]
	    const next = { ...current }
	    delete next[targetID]
	    createdSteps.forEach((step, index) => {
	      if (sourcePosition) next[proxyPathStepNodeID(step)] = { x: sourcePosition.x + index * 300, y: sourcePosition.y }
	    })
	    saveGraphPositions(next)
	    return next
	  })
	  window.setTimeout(() => fitGraphToSafeArea(280), 60)
	}
	const connect = async (conn: Connection) => {
	  if (!conn.source || !conn.target) return
	  const sourceEntity = graphEntity(conn.source)
	  const targetEntity = graphEntity(conn.target)
	  if (sourceEntity?.type === 'server' && conn.sourceHandle === 'server-shared') {
	    const target = await targetStepForGraphTarget(conn.target)
	    if (!target) return
	    const allSteps = (data.proxy_path_steps || []) as ProxyPathStep[]
	    const terminalSteps = allSteps.filter(step => proxyPathStepNodeID(step) === conn.source && !allSteps.some(other => other.path_id === step.path_id && other.position > step.position))
	    if (terminalSteps.length) {
	      const stepPathName = (step: ProxyPathStep) => {
	        const path = ((data.proxy_paths || []) as ProxyPath[]).find(item => item.id === step.path_id)
	        return path ? proxyPathDisplayName(path) : `路径 ${step.path_id}`
	      }
	      if (!await confirmSharedAppend(terminalSteps.length, targetEntity?.label || '目标节点', terminalSteps.map(stepPathName))) return
	      const transport = await chooseTransportForTarget(target)
	      if (!transport) return
	      const createdSteps = await runSharedAppends(terminalSteps, stepPathName, step => appendPathAfterStep(step.id, { ...target, ...transport }))
	      consumeCanvasServerTarget(conn.target, createdSteps)
	      return
	    }
	    const serverEntries = entries.filter(entry => entry.server_id === sourceEntity.id && entry.enabled !== false)
	    if (!serverEntries.length) return dialogs.alert({ title: '没有可共享的入口', message: '这台服务器还没有可用入口，无法创建共享后续路径。' })
	    const entryLabel = (entry: Inbound) => `${entry.name || `入口 ${entry.id}`} / ${labelProtocol(entry.protocol)}:${entry.port}`
	    if (!await confirmSharedAppend(serverEntries.length, targetEntity?.label || '目标节点', serverEntries.map(entryLabel), 'create')) return
	    const transport = await chooseTransportForTarget(target)
	    if (!transport) return
	    const createdSteps = await runSharedAppends(serverEntries, entryLabel, entry => createPathFromEntry(entry, { ...target, ...transport }))
	    consumeCanvasServerTarget(conn.target, createdSteps)
	    return
	  }
	  const sourcePathStepID = pathStepIDFromHandle(conn.sourceHandle)
	  if (sourcePathStepID) {
	    const target = await targetStepForGraphTarget(conn.target)
	    const transport = target ? await chooseTransportForTarget(target) : null
	    if (target && transport) {
	      const created = await appendPathAfterStep(sourcePathStepID, { ...target, ...transport })
	      if (created) consumeCanvasServerTarget(conn.target, [created])
	    }
	    return
	  }
	  const sourceHandleInboundID = inboundIDFromServerHandle(conn.sourceHandle)
	  const sourceHandleEntry = sourceHandleInboundID ? entries.find(x => x.id === sourceHandleInboundID) || null : null
	  if (sourceEntity?.type === 'entry' && targetEntity?.type === 'imported') {
	    const entry = entries.find(x => x.id === sourceEntity.id)
	    const transport = await chooseTransportForTarget({ node_type: 'imported', external_outbound_id: targetEntity.id } as any, undefined, sourceEntity.label)
	    if (entry && transport) await createPathFromEntry(entry, { node_type: 'imported', external_outbound_id: targetEntity.id, ...transport })
	    return
	  }
	  if (sourceEntity?.type === 'entry' && (targetEntity?.type === 'entry' || targetEntity?.type === 'server')) {
	    const entry = entries.find(x => x.id === sourceEntity.id)
	    const target = await targetStepForGraphTarget(conn.target)
	    const transport = target ? await chooseTransportForTarget(target) : null
	    if (entry && target && transport) {
	      const created = await createPathFromEntry(entry, { ...target, ...transport })
	      if (created) consumeCanvasServerTarget(conn.target, [created])
	    }
	    return
	  }
	  if (sourceEntity?.type === 'imported' && (targetEntity?.type === 'entry' || targetEntity?.type === 'server')) {
	    const candidates = ((data.proxy_path_steps || []) as ProxyPathStep[]).filter(step => step.node_type === 'imported' && step.external_outbound_id === sourceEntity.id)
	    const target = await targetStepForGraphTarget(conn.target)
	    const transport = target ? await chooseTransportForTarget(target) : null
	    if (target && transport && candidates.length === 1) {
	      const created = await appendPathAfterStep(candidates[0].id, { ...target, ...transport })
	      if (created) consumeCanvasServerTarget(conn.target, [created])
	    }
	    else if (target && transport && !candidates.length) await dialogs.alert({ title: '无法追加链路', message: '请先从某个入口节点连到这个导入节点，再从导入节点继续连到服务器。' })
	    else if (target && transport) await dialogs.alert({ title: '请选择继续连接点', message: '这个导入节点属于多条路径，请从导入节点旁边对应路径的小连接点拖线。' })
	    return
	  }
	  if (sourceEntity?.type === 'server' && targetEntity?.type === 'imported') {
	    const entry = sourceHandleEntry && sourceHandleEntry.server_id === sourceEntity.id ? sourceHandleEntry : await chooseEntryForServer(sourceEntity.id, 'source')
	    const transport = await chooseTransportForTarget({ node_type: 'imported', external_outbound_id: targetEntity.id } as any, undefined, sourceEntity.label)
	    if (entry && transport) await createPathFromEntry(entry, { node_type: 'imported', external_outbound_id: targetEntity.id, ...transport })
	    return
	  }
	  if (sourceEntity?.type === 'server' && sourceHandleEntry && (targetEntity?.type === 'entry' || targetEntity?.type === 'server')) {
	    const target = await targetStepForGraphTarget(conn.target)
	    const transport = target ? await chooseTransportForTarget(target) : null
	    if (target && transport) {
	      const created = await createPathFromEntry(sourceHandleEntry, { ...target, ...transport })
	      if (created) consumeCanvasServerTarget(conn.target, [created])
	    }
	    return
	  }
	  return dialogs.alert({ title: '请选择路径连接点', message: '从入口节点、一级服务器上的入口点，或路径中服务器/导入节点旁的继续连接点拖线。这样系统才能知道要追加哪一条代理路径。' })
  }
  // React Flow does not await onConnect, so a rejected request would otherwise
  // surface only as an unhandled rejection in the console.
  const onConnect = (conn: Connection) => {
    void connect(conn).catch(async (error: any) => {
      await dialogs.alert({ title: '连接失败', message: localizeErrorMessage(error?.message || error) })
    })
  }
  const addServer = (position?: GraphPosition) => {
    serverDraftPosition.current = position || nextServerGraphPosition(data)
    setServerDraft({ ...defaultServerDraft(data.server_creation_defaults || {}), name: `server-${servers.length + 1}` })
  }
  const submitServerDraft = async () => {
    if (!serverDraft) return
    try {
      const result = await client.request('/servers', { method: 'POST', body: JSON.stringify(serverDraft) }) as { server?: Server }
      if (result.server?.id) placeGraphNode(`server-${result.server.id}`, serverDraftPosition.current || nextServerGraphPosition(data))
      setServerDraft(null)
      serverDraftPosition.current = null
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '添加服务器失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const inboundDraftFromEntry = (entry: Inbound) => ({
    ...entry,
    __edit: true,
    __graphPosition: null,
    access_scope: 'inbound' as AccessScopeType,
    access_user_ids: [] as number[],
    access_group_ids: [] as number[],
  })
  const openEditEntry = (entry: Inbound) => setEditEntry(inboundDraftFromEntry(entry))
  const addEntry = async (position?: GraphPosition) => {
    const server = selected || servers[0]
    if (!server) return dialogs.alert({ title: '无法添加入口', message: '请先添加服务器。' })
    const preset = inboundPreset(defaultInboundPreset('vless'))
    const port = nextAvailableInboundPort(data, server, preset.protocol, preset.defaultPort)
    setEntryDraft({ __graphPosition: position || null, __port_manual: false, access_scope: 'inbound' as AccessScopeType, access_user_ids: [] as number[], access_group_ids: [] as number[], server_id: server.id, name: autoInboundName(server, preset.protocol, port), protocol: preset.protocol, listen_ip: server.listen_ip || '0.0.0.0', port, entry_ip_mode: 'auto' as EntryIPMode, external_ip: '', dns_sync_enabled: false, dns_credential_id: undefined, dns_domain: '', dns_proxy_enabled: false, dns_record_types: 'a' as DNSRecordTypes, ddns_enabled: false, ddns_interval_seconds: 300, tls: presetRequiresCertificate(preset.id), certificate_mode: presetRequiresCertificate(preset.id) ? 'auto' : 'external', certificate_domain: presetRequiresCertificate(preset.id) ? 'example.com' : '', certificate_id: undefined, config_json: buildInboundPresetConfig(preset.id), enabled: true })
  }
  const submitEntryDraft = async () => {
    if (!entryDraft) return
    try {
      let finalDraft = entryDraft
      if (finalDraft.protocol === 'ssh') {
        const confirmed = await dialogs.confirm({ title: '确认公开 SSH 入口', message: 'SSH 会向已授权用户公开。即使只允许受限代理转发，也应视为用户可能获得 SSH 访问能力。它不提供 shell、SFTP、远程转发或主机账户；仅支持公钥认证和本地/动态转发。确认后请为每位授权用户添加 SSH 公钥。', tone: 'danger', confirmText: '确认创建 SSH 入口' })
        if (!confirmed) return
        finalDraft = { ...finalDraft, config_json: JSON.stringify({ ...(parseConfig(finalDraft.config_json) || {}), exposure_confirmed: true, exposure_confirmation_version: 'ssh-inbound-v1', access_mode: 'restricted_proxy' }) }
      }
      const { __graphPosition, __port_manual, access_scope, access_user_ids, access_group_ids, ...body } = finalDraft
      const result = await client.request('/inbounds', { method: 'POST', body: JSON.stringify(body) }) as { inbound?: Inbound }
      if (result.inbound?.id) {
        placeGraphNode(`entry-${result.inbound.id}`, __graphPosition || nextEntryGraphPosition(data, positions, Number(body.server_id), selected?.id || Number(body.server_id)))
        await createEntryAccessGrants(client, result.inbound, access_scope || 'inbound', access_user_ids || [], access_group_ids || [])
      }
      setEntryDraft(null)
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '创建入口失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const submitEditEntry = async () => {
    if (!editEntry) return
    try {
      let finalDraft = editEntry
      if (finalDraft.protocol === 'ssh') {
        const confirmed = await dialogs.confirm({ title: '确认更新 SSH 入口', message: '此操作会让用户通过 SSH 访问受限代理服务。即使权限受限，也请按 SSH 暴露风险处理。该入口不提供 shell、SFTP、远程转发或主机账户。', tone: 'danger', confirmText: '确认保存' })
        if (!confirmed) return
        finalDraft = { ...finalDraft, config_json: JSON.stringify({ ...(parseConfig(finalDraft.config_json) || {}), exposure_confirmed: true, exposure_confirmation_version: 'ssh-inbound-v1', access_mode: 'restricted_proxy' }) }
      }
      const { __edit, __graphPosition, __port_manual, access_scope, access_user_ids, access_group_ids, ...body } = finalDraft
      await client.request(`/inbounds/${body.id}`, { method: 'PATCH', body: JSON.stringify(body) })
      setEditEntry(null)
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '保存入口失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const openRouting = () => {
    const server = selected || servers[0]
    if (!server) return dialogs.alert({ title: '无法添加分流', message: '请先添加服务器。' })
    setRoutingDraft(defaultRoutingDraft(server))
  }
  const submitRoutingDraft = async () => {
    if (!routingDraft) return
    try {
      const body: any = {
        server_id: routingDraft.server_id,
        name: routingDraft.name,
        priority: routingDraft.priority,
        match_json: routingMatchJSON(routingDraft.match_kind, routingDraft.match_value),
        action: routingDraft.action,
        enabled: routingDraft.enabled,
      }
      if (routingDraft.action === 'outbound' && routingDraft.outbound_id) body.outbound_id = routingDraft.outbound_id
      if (routingDraft.action === 'external' && routingDraft.external_outbound_id) body.external_outbound_id = routingDraft.external_outbound_id
      if (routingDraft.action === 'warp' && routingDraft.warp_profile_id) body.warp_profile_id = routingDraft.warp_profile_id
      if (routingDraft.action === 'interface') body.interface_name = routingDraft.interface_name.trim()
      await client.request('/routing-rules', { method: 'POST', body: JSON.stringify(body) })
      setRoutingDraft(null)
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '创建分流失败', message: localizeErrorMessage(e.message || e) })
    }
  }
	const openTransport = () => {
	  if (servers.length < 2) return dialogs.alert({ title: '无法添加转发隧道', message: '至少需要两台服务器。' })
	  setTransportDraft(defaultTransportDraft(servers, selected))
	}
	const openImportNode = (position?: GraphPosition) => {
	  setImportDraft({ content: 'socks5://user:password@example.com:1080#SOCKS-A', scope: 'global', server_id: selected?.id || 0, expose_to_users: false, position: position || null })
	}
	const submitImportNode = async () => {
	  if (!importDraft) return
	  try {
	    const result = await client.request('/external-outbounds/import', { method: 'POST', body: JSON.stringify({ content: importDraft.content, scope: importDraft.scope, server_id: importDraft.scope === 'server' && importDraft.server_id ? importDraft.server_id : undefined, expose_to_users: importDraft.expose_to_users }) }) as { external_outbounds?: ExternalOutbound[] }
	    const first = result.external_outbounds?.[0]
	    if (first?.id && importDraft.position) { setCanvasImportedIDs(ids => ids.includes(first.id) ? ids : [...ids, first.id]); placeGraphNode(`imported-${first.id}`, importDraft.position) }
	    setImportDraft(null)
	    await load()
	  } catch (e: any) {
	    await dialogs.alert({ title: '导入节点失败', message: localizeErrorMessage(e.message || e) })
	  }
	}
	const submitTransportDraft = async () => {
    if (!transportDraft) return
    try {
      if (transportDraft.mode === 'port-forward') {
        await client.request('/port-forwards', { method: 'POST', body: JSON.stringify({
          name: transportDraft.name,
          source_server_id: transportDraft.source_server_id,
          target_server_id: transportDraft.target_server_id,
          listen_ip: transportDraft.listen_ip,
          listen_port: transportDraft.listen_port,
          target_port: transportDraft.target_port,
          protocol: transportDraft.protocol,
          backend: transportDraft.backend,
		  probe_mode: 'periodic',
          probe_interval_seconds: 300,
          sample_rate: 0,
          priority: transportDraft.priority,
          config_json: '{}',
          enabled: transportDraft.enabled,
        }) })
      } else {
        await client.request('/tunnels', { method: 'POST', body: JSON.stringify({
          name: transportDraft.name,
          source_server_id: transportDraft.source_server_id,
          target_server_id: transportDraft.target_server_id,
          type: transportDraft.type,
          local_address: '',
          peer_address: '',
          listen_port: transportDraft.listen_port,
          target_endpoint: '',
          target_port: transportDraft.target_port,
          priority: transportDraft.priority,
          config_json: transportDraft.config_json,
          enabled: transportDraft.enabled,
        }) })
      }
      setTransportDraft(null)
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '创建转发隧道失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const deleteGraphEntity = async (entity: GraphEntity) => {
	  if (entity.node_id?.startsWith('canvas-server-')) {
	    setCanvasServerInstances(items => items.filter(item => canvasServerNodeID(item) !== entity.node_id))
	    setPositions(current => {
	      const next = { ...current }
	      delete next[entity.node_id!]
	      saveGraphPositions(next)
	      return next
	    })
	    return
	  }
	  const meta: Record<GraphEntity['type'], { name: string; path: string }> = {
	    server: { name: '服务器', path: `/servers/${entity.id}` },
	    entry: { name: '入口节点', path: `/inbounds/${entity.id}` },
	    imported: { name: '导入节点', path: `/external-outbounds/${entity.id}` },
	    'port-forward': { name: '端口转发', path: `/port-forwards/${entity.id}` },
	    tunnel: { name: '隧道', path: `/tunnels/${entity.id}` },
	    'proxy-path': { name: '代理路径', path: `/proxy-paths/${entity.id}` },
	    'proxy-path-step': { name: '路径步骤', path: `/proxy-path-steps/${entity.id}` },
	  }
    const item = meta[entity.type]
    const cascading = entity.type === 'proxy-path-step'
    // Deleting a server or an entry cuts every path that traverses it, including
    // branches rooted at another entry server that this canvas does not draw.
    const affected = entity.type === 'server' || entity.type === 'entry'
      ? proxyPathsTouchingEntity(data, entity)
      : []
    const ok = await dialogs.confirm({
      title: cascading ? '取消后续链路' : `删除${item.name}`,
      message: cascading
        ? `确认从 ${entity.label} 开始断开？该位置及其全部后续节点都会从这条路径移除。`
        : <div className="dialog-detail">
            <p>确认删除 {entity.label}？</p>
            {affected.length > 0 && <>
              <p>以下 {affected.length} 条链路经过它，会被同步截断或删除：</p>
              <ul>{affected.map((name, index) => <li key={index}>{name}</li>)}</ul>
            </>}
          </div>,
      tone: 'danger',
      confirmText: cascading ? '取消后续节点' : '删除',
    })
    if (!ok) return
    try {
      await client.request(item.path, { method: 'DELETE' })
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '删除失败', message: localizeErrorMessage(e.message || e) })
    }
  }
	const editProxyPathTransportForEntity = async (entity: GraphEntity | null | undefined) => {
	  if (!entity || entity.type !== 'proxy-path-step') return
	  const step = (data.proxy_path_steps || []).find((x: ProxyPathStep) => x.id === entity.id)
	  if (!step) return
	  if (step.node_type !== 'server_inbound') return dialogs.alert({ title: '无法更改', message: '导入节点必须使用 sing-box 出站链。' })
	  const path = ((data.proxy_paths || []) as ProxyPath[]).find(item => item.id === step.path_id)
	  const transport = await chooseTransportForTarget(step, step, proxyPathStepUpstreamLabel(data, step))
	  if (!transport) return
	  try {
	    if (transport.transport_mode === 'port_forward' && path && !await ensureTransparentPathExclusive(path.id, path.inbound_id)) return
	    await client.request(`/proxy-path-steps/${step.id}`, { method: 'PATCH', body: JSON.stringify({ ...step, ...transport }) })
	    await load()
	  } catch (e: any) {
	    await dialogs.alert({ title: '更新失败', message: localizeErrorMessage(e.message || e) })
    }
  }
	const editProxyPathTransport = async () => {
	  const entity = graphMenu?.entity
	  setGraphMenu(null)
	  await editProxyPathTransportForEntity(entity)
	}
	const editProxyPathNameForEntity = (entity: GraphEntity | null | undefined) => {
	  if (!entity || entity.type !== 'proxy-path-step') return
	  const step = ((data.proxy_path_steps || []) as ProxyPathStep[]).find(item => item.id === entity.id)
	  const pathID = entity.path_id || step?.path_id || 0
	  const path = ((data.proxy_paths || []) as ProxyPath[]).find(item => item.id === pathID)
	  if (path) setNamingPath(path)
	}
	const editProxyPathName = () => {
	  const entity = graphMenu?.entity
	  setGraphMenu(null)
	  editProxyPathNameForEntity(entity)
	}
  const runTool = (action: ProxyToolAction, position?: GraphPosition) => {
	  if (action === 'server') return void addServer(position)
	  if (action === 'entry') return void addEntry(position)
	  if (action === 'imported') return void openImportNode(position)
    if (action === 'routing') return void openRouting()
    if (action === 'transport') return void openTransport()
  }
  const onToolDragOver = (e: React.DragEvent) => {
    if (!Array.from(e.dataTransfer.types).includes(proxyToolDragType)) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }
  const onToolDrop = (e: React.DragEvent) => {
    const rawAction = e.dataTransfer.getData(proxyToolDragType)
    if (!isProxyToolAction(rawAction)) return
    e.preventDefault()
    const position = flowInstance?.screenToFlowPosition({ x: e.clientX, y: e.clientY })
    runTool(rawAction, position ? snapGraphPosition({ x: position.x - 110, y: position.y - 44 }) : undefined)
  }
  const onNodeDoubleClick = (_: React.MouseEvent, node: Node) => {
	  const entity = node.data?.entity as GraphEntity | undefined
	  if (entity?.type === 'server') {
      setSelectedServer(entity.id)
      setInspectorOpen(true)
      setIsToolbarCollapsed(true)
    }
	  if (entity?.type === 'imported') {
      const imported = (data.external_outbounds || []).find((item: ExternalOutbound) => item.id === entity.id)
      if (imported) setConfigNode(imported)
    }
	  if (entity?.type === 'entry') {
      const inbound = entries.find(x => x.id === entity.id)
      if (inbound) openEditEntry(inbound)
    }
  }
  const onNodeClick = (_: React.MouseEvent, _node: Node) => {
	  setGraphMenu(null)
    setActiveGraphEntity(_node.data?.entity as GraphEntity || null)
  }
  const openGraphContextMenu = (clientX: number, clientY: number, entity: GraphEntity) => {
    const menuWidth = Math.min(260, Math.max(148, window.innerWidth - 16))
    const menuHeight = entity.type === 'proxy-path-step' ? 166 : 86
    setGraphMenu({
      x: Math.max(8, Math.min(clientX, window.innerWidth - menuWidth - 8)),
      y: Math.max(8, Math.min(clientY, window.innerHeight - menuHeight - 8)),
      entity,
    })
  }
  const onNodeContextMenu = (e: React.MouseEvent, node: Node) => {
    const entity = node.data?.entity as GraphEntity | undefined
    if (!entity) return
    e.preventDefault()
    e.stopPropagation()
    openGraphContextMenu(e.clientX, e.clientY, entity)
  }
  const onEdgeContextMenu = (e: React.MouseEvent, edge: Edge) => {
    const entity = edge.data?.entity as GraphEntity | undefined
    if (!entity) return
    e.preventDefault()
    e.stopPropagation()
    openGraphContextMenu(e.clientX, e.clientY, entity)
  }
  const onEdgeClick = (_: React.MouseEvent, edge: Edge) => {
    setGraphMenu(null)
    setActiveGraphEntity(edge.data?.entity as GraphEntity || null)
  }
  const closeGraphMenu = () => {
    setGraphMenu(null)
    setActiveGraphEntity(null)
  }
  const toggleInspector = () => {
    const next = !inspectorOpen
    setInspectorOpen(next)
    if (next) setIsToolbarCollapsed(true)
  }
  const deleteGraphMenuEntity = async () => {
    const entity = graphMenu?.entity
    setGraphMenu(null)
    if (entity) await deleteGraphEntity(entity)
  }
  const activeGraphActionLabel = activeGraphEntity?.type === 'proxy-path-step'
    ? '传递方式'
    : activeGraphEntity?.type === 'entry'
      ? '编辑入口'
      : activeGraphEntity?.type === 'imported'
        ? '节点设置'
        : activeGraphEntity?.type === 'server'
          ? '链路详情'
          : ''
  const openActiveGraphEntity = async () => {
    const entity = activeGraphEntity
    if (!entity) return
    if (entity.type === 'proxy-path-step') return editProxyPathTransportForEntity(entity)
    if (entity.type === 'entry') {
      const inbound = entries.find(item => item.id === entity.id)
      if (inbound) openEditEntry(inbound)
      return
    }
    if (entity.type === 'imported') {
      const imported = (data.external_outbounds || []).find((item: ExternalOutbound) => item.id === entity.id)
      if (imported) setConfigNode(imported)
      return
    }
    if (entity.type === 'server') {
      setSelectedServer(entity.id)
      setInspectorOpen(true)
      setIsToolbarCollapsed(true)
    }
  }
  const deleteActiveGraphEntity = async () => {
    const entity = activeGraphEntity
    setActiveGraphEntity(null)
    if (entity) await deleteGraphEntity(entity)
  }
  // Map servers for CustomSelect
  const selectOptions = servers.map(s => {
    const isServerOnline = s.status.toLowerCase() === 'online';
    const entryCount = entries.filter(x => x.server_id === s.id).length;
    return {
      value: String(s.id),
      label: (
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', width: '100%' }}>
          <span style={{
            width: '8px',
            height: '8px',
            borderRadius: '50%',
            backgroundColor: isServerOnline ? 'var(--color-success)' : 'var(--color-danger)',
            display: 'inline-block',
            boxShadow: isServerOnline ? '0 0 6px var(--color-success)' : '0 0 6px var(--color-danger)',
            flexShrink: 0
          }} />
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.name || `server-${s.id}`}</span>
          <span style={{ fontSize: '11px', color: 'var(--text-muted)', marginLeft: 'auto', flexShrink: 0 }}>({entryCount} 入口)</span>
        </div>
      )
    };
  });

  return <div className="proxy-overview">
    <div className="proxy-editor">
      <div ref={workspaceRef} className={`proxy-editor-workspace${isToolbarCollapsed ? ' toolbox-collapsed' : ''}${inspectorOpen ? ' inspector-open' : ''}`}>
        <div ref={toolboxRef} className={`proxy-editor-sidebar${toolboxDragging ? ' is-dragging' : ''}`} style={{ left: toolboxPosition.x, top: toolboxPosition.y }}>
          <ProxyGraphToolbox
            collapsed={isToolbarCollapsed}
            dragging={toolboxDragging}
            selected={selected}
            servers={servers}
            importedNodes={(data.external_outbounds || []).filter((item: ExternalOutbound) => item.enabled !== false && (item.scope === 'global' || !item.server_id || !selected?.id || item.server_id === selected.id))}
            canAutoArrange={nodes.length > 0}
            inspectorOpen={inspectorOpen}
            onToggle={toggleToolbar}
            onMoveStart={beginToolboxDrag}
            onAction={action => runTool(action)}
            onShowServer={putServerOnCanvas}
            onAddImported={addImportedToCurrentEntry}
            onShowImported={putImportedOnCanvas}
            onInspectImported={setConfigNode}
            onAutoArrange={autoArrangeGraph}
            onToggleInspector={toggleInspector}
          />
        </div>
        {panelActionsTarget && createPortal(
          <div className="proxy-path-entry-picker">
            <span className="proxy-path-entry-label">入口服务器</span>
            <CustomSelect className="graph-entry-select" value={String(selected?.id || 0)} onChange={selectEntryServer} options={selectOptions} ariaLabel="选择当前入口服务器" />
          </div>,
          panelActionsTarget,
        )}
        <div className={`flow proxy-flow ${initialViewportReady ? 'initial-viewport-ready' : 'initial-viewport-pending'}`} onDragOver={onToolDragOver} onDrop={onToolDrop}>
        <ReactFlow 
          nodes={nodes} 
          edges={edges} 
          nodeTypes={proxyGraphNodeTypes}
          edgeTypes={proxyGraphEdgeTypes}
          onInit={initializeFlow} 
          onNodesChange={onNodesChange} 
          onNodeDragStop={onNodeDragStop} 
          onNodeClick={onNodeClick} 
          onNodeContextMenu={onNodeContextMenu} 
          onNodeDoubleClick={onNodeDoubleClick} 
          onEdgesChange={onEdgesChange} 
          onEdgeClick={onEdgeClick}
          onEdgeContextMenu={onEdgeContextMenu} 
          onPaneClick={closeGraphMenu} 
          onMoveStart={closeGraphMenu} 
          onConnect={onConnect}
          panOnScroll
          zoomOnScroll={false}
          zoomOnPinch 
          preventScrolling
          panOnDrag 
          // The node layer stays hidden until initializeFlow has applied the safe-area viewport.
          fitView={false}
          defaultViewport={{ x: 0, y: 0, zoom: 0.78 }} 
          minZoom={0.3} 
          maxZoom={1.6} 
          nodesConnectable
          deleteKeyCode={null}
          proOptions={{ hideAttribution: true }}
        >
          <Background id="minor-grid" color="var(--border-strong)" gap={20} size={1} variant={BackgroundVariant.Lines} style={{ opacity: 0.5 }} />
          <Background id="major-grid" color="var(--muted-2)" gap={100} size={1} variant={BackgroundVariant.Lines} style={{ opacity: 0.28 }} />
          <Controls position="bottom-right" />
        </ReactFlow>
        <ProxyGraphLegend />
        {activeGraphEntity && <div className="graph-selection-toolbar" role="toolbar" aria-label="当前选中项操作">
          <strong title={activeGraphEntity.label}>{activeGraphEntity.label}</strong>
		  {activeGraphEntity.type === 'proxy-path-step' && <button type="button" className="ghost" onClick={() => editProxyPathNameForEntity(activeGraphEntity)}><Edit3 size={13} />链路命名</button>}
          {activeGraphActionLabel && <button type="button" className="ghost" onClick={() => void openActiveGraphEntity()}><Edit3 size={13} />{activeGraphActionLabel}</button>}
          <button type="button" className="ghost danger-text" onClick={() => void deleteActiveGraphEntity()}><Trash2 size={13} />{activeGraphEntity.node_id?.startsWith('canvas-server-') ? '移出画布' : activeGraphEntity.type === 'proxy-path-step' ? '断开后续' : '删除'}</button>
          <button type="button" className="ghost icon-button" onClick={() => setActiveGraphEntity(null)} aria-label="取消选择" title="取消选择"><X size={13} /></button>
        </div>}
        {!nodes.length && <div className="graph-empty-state"><ServerIcon size={22} /><strong>还没有服务器</strong><span>添加服务器后即可创建入口和代理链路。</span><button onClick={() => addServer()}>添加服务器</button></div>}

        {graphMenu && <div className="graph-context-menu" style={{ left: graphMenu.x, top: graphMenu.y }} onContextMenu={e => e.preventDefault()}>
          <div className="graph-context-menu-title">{graphMenu.entity.label}</div>
		  {graphMenu.entity.type === 'proxy-path-step' && <button onClick={editProxyPathName}><Edit3 size={14} />链路命名</button>}
		  {graphMenu.entity.type === 'proxy-path-step' && <button onClick={editProxyPathTransport}><ArrowLeftRight size={14} />更改传递方式</button>}
		  <button className="danger-text" onClick={deleteGraphMenuEntity}><Trash2 size={14} />{graphMenu.entity.node_id?.startsWith('canvas-server-') ? '从画布移除' : graphMenu.entity.type === 'proxy-path-step' ? '取消此处及后续节点' : '删除'}</button>
        </div>}
        </div>
        {inspectorOpen && <aside className="graph-inspector open">
          <button className="ghost inspector-toggle" onClick={toggleInspector}><X size={15} />收起详情</button>
          <div className="graph-inspector-content">
            <div className="host-picker compact-panel">
              <h3>主机</h3>
              <p className="muted">当前一级服务器及其后续链路。</p>
              <div className="host-list">{servers.map(s => <button key={s.id} className={selected?.id === s.id ? '' : 'ghost'} onClick={() => selectEntryServer(s.id)}>{s.name}<small>{labelValue(s.status || 'unknown')}</small></button>)}</div>
            </div>
            <div className="branch-panel compact-panel">
              {selected ? <ServerBranchTree data={data} server={selected} onManageEntry={setAccessEntry} /> : <p className="muted">暂无服务器</p>}
            </div>
          </div>
        </aside>}
      </div>
    </div>
    <AnimatePresence>{serverDraft && <ServerCreateDialog draft={serverDraft} setDraft={setServerDraft as React.Dispatch<React.SetStateAction<ReturnType<typeof defaultServerDraft>>>} onCancel={() => { setServerDraft(null); serverDraftPosition.current = null }} onSubmit={submitServerDraft} />}</AnimatePresence>
    <AnimatePresence>{entryDraft && <EntryDraftDialog mode="create" draft={entryDraft} setDraft={setEntryDraft} data={data} servers={servers} client={client} onCancel={() => setEntryDraft(null)} onSubmit={submitEntryDraft} />}</AnimatePresence>
    <AnimatePresence>{editEntry && <EntryDraftDialog mode="edit" draft={editEntry} setDraft={setEditEntry} data={data} servers={servers} client={client} onCancel={() => setEditEntry(null)} onSubmit={submitEditEntry} />}</AnimatePresence>
    <AnimatePresence>{accessEntry && <EntryUsersDialog entry={accessEntry} data={data} client={client} load={load} onCancel={() => setAccessEntry(null)} />}</AnimatePresence>
    <AnimatePresence>{routingDraft && <RoutingRuleDraftDialog draft={routingDraft} setDraft={setRoutingDraft} data={data} onCancel={() => setRoutingDraft(null)} onSubmit={submitRoutingDraft} />}</AnimatePresence>
    <AnimatePresence>{transportDraft && <TransportDraftDialog draft={transportDraft} setDraft={setTransportDraft} servers={servers} onCancel={() => setTransportDraft(null)} onSubmit={submitTransportDraft} />}</AnimatePresence>
    <AnimatePresence>{importDraft && <ImportNodeDialog draft={importDraft} setDraft={setImportDraft} servers={servers} onCancel={() => setImportDraft(null)} onSubmit={submitImportNode} />}</AnimatePresence>
    <AnimatePresence>{configNode && <ImportedNodeConfigDialog node={configNode} data={data} client={client} load={load} onClose={() => setConfigNode(null)} />}</AnimatePresence>
	<AnimatePresence>{namingPath && <ProxyPathNameDialog path={namingPath} data={data} client={client} load={load} onClose={() => setNamingPath(null)} />}</AnimatePresence>
	<AnimatePresence>{transportRequest && <TransportDialog
	  target={transportRequest.target}
	  current={transportRequest.current}
	  chainMethods={proxyPathChainMethods}
	  onCancel={() => { transportRequest.resolve(null); setTransportRequest(null) }}
	  onSubmit={selection => { transportRequest.resolve(selection); setTransportRequest(null) }}
	/>}</AnimatePresence>
  </div>
}

type ProxyPathNameReference = { key: string; label: string; part: ProxyPathNamePart }

function ProxyPathNameDialog({ path, data, client, load, onClose }: { path: ProxyPath; data: any; client: ReturnType<typeof api>; load: () => Promise<void>; onClose: () => void }) {
  const dialogs = useDialogs()
  const references = useMemo(() => proxyPathNameReferences(data, path), [data.servers, data.inbounds, data.external_outbounds, data.proxy_path_steps, path.id])
  const initialTemplate = path.name_template?.length ? path.name_template : defaultProxyPathNameTemplate(references)
  const [mode, setMode] = useState<'auto' | 'custom'>(path.name_mode === 'custom' ? 'custom' : 'auto')
  const [parts, setParts] = useState<ProxyPathNamePart[]>(initialTemplate)
  const [saving, setSaving] = useState(false)
  const chain = proxyPathChainLabels(data, path)
  const preview = mode === 'auto'
    ? (path.name_mode === 'auto' ? path.name : (chain.length > 1 ? `${chain[0]}｜${chain[chain.length - 1]}` : chain[0] || path.name))
    : renderProxyPathNameTemplate(parts, data)

  const switchMode = (next: 'auto' | 'custom') => {
    setMode(next)
    if (next === 'custom' && !parts.length) setParts(defaultProxyPathNameTemplate(references))
  }
  const addReference = (key: string) => {
    const reference = references.find(item => item.key === key)
    if (!reference) return
    setParts(current => [...current, reference.part])
  }
  const addLiteral = () => setParts(current => [...current, { kind: 'literal', value: '' }])
  const updateLiteral = (index: number, value: string) => setParts(current => current.map((part, partIndex) => partIndex === index ? { kind: 'literal', value } : part))
  const removePart = (index: number) => setParts(current => current.filter((_, partIndex) => partIndex !== index))
  const movePart = (index: number, offset: number) => setParts(current => {
    const target = index + offset
    if (target < 0 || target >= current.length) return current
    const next = current.slice()
    ;[next[index], next[target]] = [next[target], next[index]]
    return next
  })
  const save = async () => {
    if (mode === 'custom' && !preview.trim()) return
    setSaving(true)
    try {
      await client.request(`/proxy-paths/${path.id}`, { method: 'PATCH', body: JSON.stringify({ name_mode: mode, name_template: mode === 'custom' ? parts : [] }) })
      await load()
      onClose()
    } catch (error: any) {
      await dialogs.alert({ title: '保存链路名称失败', message: localizeErrorMessage(error.message || error) })
    } finally {
      setSaving(false)
    }
  }

  return <MotionDialogPanel onCancel={onClose} className="proxy-path-name-dialog">
    <header className="dialog-head">
      <div><h2>链路命名</h2><p className="muted">{chain.join(' → ')}</p></div>
      <button type="button" className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><X size={16} /></button>
    </header>
    <div className="dialog-body proxy-path-name-body">
      <Select variant="segmented" value={mode} onChange={event => switchMode(event.target.value as 'auto' | 'custom')} aria-label="链路命名方式">
        <option value="auto">自动命名</option>
        <option value="custom">自定义模板</option>
      </Select>
      <div className="proxy-path-name-preview"><span>名称预览</span><strong>{preview || '链路名称'}</strong></div>
      {mode === 'custom' && <>
        <div className="proxy-path-name-insert">
          <Select value="" onChange={event => addReference(event.target.value)} aria-label="插入链路节点">
            <option value="">插入链路节点</option>
            {references.map(reference => <option key={reference.key} value={reference.key}>{reference.label}</option>)}
          </Select>
          <button type="button" className="ghost" onClick={addLiteral}><Plus size={14} />添加文字</button>
        </div>
        <div className="proxy-path-name-parts">
          {parts.map((part, index) => <div className="proxy-path-name-part" key={`${proxyPathNamePartKey(part)}-${index}`}>
            <span className={`proxy-path-name-part-kind ${part.kind}`}>{part.kind === 'literal' ? '文字' : '动态'}</span>
            {part.kind === 'literal'
              ? <input value={part.value} onChange={event => updateLiteral(index, event.target.value)} placeholder="输入名称文字" autoFocus={parts.length === 1} />
              : <strong>{proxyPathNameReferenceLabel(part, data)}</strong>}
            <div className="proxy-path-name-part-actions">
              <button type="button" className="ghost icon-button" onClick={() => movePart(index, -1)} disabled={index === 0} aria-label="向前移动" title="向前移动"><ArrowUp size={14} /></button>
              <button type="button" className="ghost icon-button" onClick={() => movePart(index, 1)} disabled={index === parts.length - 1} aria-label="向后移动" title="向后移动"><ArrowDown size={14} /></button>
              <button type="button" className="ghost icon-button danger-text" onClick={() => removePart(index)} aria-label="删除片段" title="删除片段"><Trash2 size={14} /></button>
            </div>
          </div>)}
        </div>
      </>}
    </div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={onClose}>取消</button><button type="button" onClick={() => void save()} disabled={saving || (mode === 'custom' && !preview.trim())}>{saving ? '保存中...' : '保存'}</button></footer>
  </MotionDialogPanel>
}

function proxyPathNameReferences(data: any, path: ProxyPath): ProxyPathNameReference[] {
  const servers: Server[] = data.servers || []
  const inbounds: Inbound[] = data.inbounds || []
  const externals: ExternalOutbound[] = data.external_outbounds || []
  const root = inbounds.find(inbound => inbound.id === path.inbound_id)
  const rootServer = root ? servers.find(server => server.id === root.server_id) : undefined
  const references: ProxyPathNameReference[] = []
  const seen = new Set<string>()
  const add = (reference: ProxyPathNameReference) => {
    if (seen.has(reference.key)) return
    seen.add(reference.key)
    references.push(reference)
  }
  if (rootServer) add({ key: `server:${rootServer.id}`, label: `入口 · ${rootServer.name}`, part: { kind: 'server', server_id: rootServer.id } })
  const steps = ((data.proxy_path_steps || []) as ProxyPathStep[]).filter(step => step.path_id === path.id).slice().sort((a, b) => (a.position - b.position) || (a.id - b.id))
  steps.forEach((step, index) => {
    const role = index === steps.length - 1 ? '出口' : `第 ${step.position} 跳`
    if (step.external_outbound_id) {
      const external = externals.find(item => item.id === step.external_outbound_id)
      if (external) add({ key: `external:${external.id}`, label: `${role} · ${external.name}`, part: { kind: 'external_outbound', external_outbound_id: external.id } })
      return
    }
    const inbound = step.inbound_id ? inbounds.find(item => item.id === step.inbound_id) : undefined
    const serverID = step.server_id || inbound?.server_id || 0
    const server = servers.find(item => item.id === serverID)
    if (server) add({ key: `server:${server.id}`, label: `${role} · ${server.name}`, part: { kind: 'server', server_id: server.id } })
  })
  return references
}

function defaultProxyPathNameTemplate(references: ProxyPathNameReference[]): ProxyPathNamePart[] {
  if (!references.length) return []
  if (references.length === 1) return [references[0].part]
  return [references[0].part, { kind: 'literal', value: '｜' }, references[references.length - 1].part]
}

function renderProxyPathNameTemplate(parts: ProxyPathNamePart[], data: any) {
  return parts.map(part => part.kind === 'literal' ? part.value : proxyPathNameReferenceLabel(part, data)).join('').trim()
}

function proxyPathNameReferenceLabel(part: ProxyPathNamePart, data: any) {
  if (part.kind === 'server') return ((data.servers || []) as Server[]).find(server => server.id === part.server_id)?.name || `服务器 #${part.server_id}`
  if (part.kind === 'external_outbound') return ((data.external_outbounds || []) as ExternalOutbound[]).find(node => node.id === part.external_outbound_id)?.name || `导入节点 #${part.external_outbound_id}`
  return part.value
}

function proxyPathNamePartKey(part: ProxyPathNamePart) {
  if (part.kind === 'server') return `server-${part.server_id}`
  if (part.kind === 'external_outbound') return `external-${part.external_outbound_id}`
  return `literal-${part.value}`
}


const proxyTools: Array<{ id: ProxyToolAction; label: string; desc: string }> = [
  { id: 'server', label: '添加服务器', desc: '登记一台主机' },
  { id: 'entry', label: '入口节点', desc: '新建协议入口' },
  { id: 'imported', label: '导入节点', desc: 'SS / SOCKS / VLESS' },
  { id: 'routing', label: '分流出口', desc: '规则与出口' },
  { id: 'transport', label: '独立转发', desc: '端口转发或隧道' },
]

function isProxyToolAction(value: string): value is ProxyToolAction {
  return proxyTools.some(x => x.id === value)
}

function ProxyGraphToolbox({ collapsed, dragging, selected, servers, importedNodes, canAutoArrange, inspectorOpen, onToggle, onMoveStart, onAction, onShowServer, onAddImported, onShowImported, onInspectImported, onAutoArrange, onToggleInspector }: {
  collapsed: boolean
  dragging: boolean
  selected?: Server
  servers: Server[]
  importedNodes: ExternalOutbound[]
  canAutoArrange: boolean
  inspectorOpen: boolean
  onToggle: () => void
  onMoveStart: (event: React.PointerEvent<HTMLElement>) => void
  onAction: (action: ProxyToolAction) => void
  onShowServer: (server: Server) => void
  onAddImported: (node: ExternalOutbound) => void
  onShowImported: (node: ExternalOutbound) => void
  onInspectImported: (node: ExternalOutbound) => void
  onAutoArrange: () => void
  onToggleInspector: () => void
}) {
  const availableServers = servers.filter(server => server.id !== selected?.id)
  const [nodePickerOpen, setNodePickerOpen] = useState(false)
  const [nodeQuery, setNodeQuery] = useState('')
  const closeNodePicker = () => {
    setNodePickerOpen(false)
    setNodeQuery('')
  }
  const normalizedQuery = nodeQuery.trim().toLowerCase()
  const filteredServers = normalizedQuery
    ? availableServers.filter(server => [server.name, server.entry_address, server.public_ipv4, server.public_ipv6].some(value => String(value || '').toLowerCase().includes(normalizedQuery)))
    : availableServers
  const filteredImportedNodes = normalizedQuery
    ? importedNodes.filter(node => [node.name, node.protocol, node.target_address, node.target_port].some(value => String(value || '').toLowerCase().includes(normalizedQuery)))
    : importedNodes
  const availableCount = availableServers.length + importedNodes.length
  const filteredCount = filteredServers.length + filteredImportedNodes.length
  return <>
  <aside className={`graph-toolbox${collapsed ? ' collapsed' : ''}${dragging ? ' dragging' : ''}`} aria-label="链路编排工具箱">
    <div className="graph-toolbox-head" onPointerDown={onMoveStart}>
      {!collapsed && <div><span className="graph-toolbox-title"><Workflow size={15} />节点与链路</span><small>选择节点后在画布中连接</small></div>}
      <button className="ghost icon-button graph-toolbox-toggle" onClick={onToggle} title={collapsed ? '展开工具箱' : '收起工具箱'} aria-label={collapsed ? '展开工具箱' : '收起工具箱'}>{collapsed ? <Menu size={17} /> : <X size={15} />}</button>
    </div>
    {!collapsed && <>
      <div className="graph-toolbox-section">
        <span className="graph-toolbox-label">画布</span>
        <div className="graph-toolbox-actions">
          <button type="button" className="ghost" onClick={onAutoArrange} disabled={!canAutoArrange} title="按链路拓扑重新排列节点">
            <Sliders size={14} />
            <span>自动归位</span>
          </button>
          <button type="button" className="ghost" onClick={onToggleInspector} title={inspectorOpen ? '收起链路详情' : '打开链路详情'}>
            <Workflow size={14} />
            <span>{inspectorOpen ? '收起详情' : '链路详情'}</span>
          </button>
        </div>
      </div>
      <div className="graph-toolbox-section">
        <span className="graph-toolbox-label">新建</span>
        <div className="graph-tool-list">
          {proxyTools.map(tool => <button
            key={tool.id}
            type="button"
            className="graph-tool"
            draggable
            onClick={() => onAction(tool.id)}
            onDragStart={event => {
              event.dataTransfer.effectAllowed = 'copy'
              event.dataTransfer.setData(proxyToolDragType, tool.id)
              event.dataTransfer.setData('text/plain', tool.label)
            }}
            title={`${tool.label}：${tool.desc}`}
          >
            <span className="graph-tool-icon"><ProxyToolIcon kind={tool.id} /></span>
            <strong>{tool.label}</strong>
          </button>)}
          <button
            type="button"
            className="graph-tool graph-node-picker-trigger"
            onClick={() => setNodePickerOpen(true)}
            disabled={!availableCount}
            aria-haspopup="dialog"
            aria-expanded={nodePickerOpen}
            title={availableCount ? `添加其他服务器或已有节点，共 ${availableCount} 个` : '暂无其他服务器或节点'}
          >
            <span className="graph-tool-icon"><ServerIcon size={14} /></span>
            <strong>其他服务器</strong>
          </button>
        </div>
      </div>
    </>}
  </aside>
  <AnimatePresence>{nodePickerOpen && <MotionDialogPanel onCancel={closeNodePicker} className="graph-node-picker-dialog">
    <header className="dialog-head">
      <div><h2>添加其他服务器</h2><p className="muted">选择已有服务器或导入节点放入画布；同一服务器可以多次添加。</p></div>
      <button className="ghost dialog-close icon-button" onClick={closeNodePicker} aria-label="关闭" title="关闭"><X /></button>
    </header>
    <div className="dialog-body graph-node-picker-body">
      <div className="graph-toolbox-section-head"><span className="graph-toolbox-label">可用节点</span><small>{normalizedQuery ? `${filteredCount} / ${availableCount}` : `${availableCount} 个`}</small></div>
      {availableCount > 3 && <label className="graph-palette-search">
        <Search size={13} aria-hidden="true" />
        <input value={nodeQuery} onChange={event => setNodeQuery(event.target.value)} placeholder="搜索服务器或节点" aria-label="搜索可用节点" autoFocus />
      </label>}
      {!filteredCount ? <div className="graph-palette-empty">没有匹配的节点</div> : <div className="graph-palette-groups">
        {filteredServers.length > 0 && <section className="graph-palette-group">
          <div className="graph-palette-group-head"><span>服务器</span><small>{filteredServers.length}</small></div>
          <div className="graph-server-grid">
        {filteredServers.map(server => {
          const online = server.status === 'online'
          const address = serverDefaultEntryAddress(server) || 'IP 待检测'
          return <button type="button" className="graph-server-tile" key={`server-${server.id}`} onClick={() => onShowServer(server)} title={`${server.name} · ${online ? '在线' : labelValue(server.status || 'unknown')} · ${address}`} aria-label={`将 ${server.name} 放入画布`}>
            <span className={`graph-palette-kind server${online ? ' online' : ''}`}><ServerIcon size={14} /></span>
            <span className="graph-palette-copy"><strong>{server.name || `服务器 ${server.id}`}</strong><small className={online ? 'online' : ''}>{online ? '在线' : labelValue(server.status || 'unknown')}</small></span>
            <Plus size={13} className="graph-server-tile-add" aria-hidden="true" />
          </button>
        })}
          </div>
        </section>}
        {filteredImportedNodes.length > 0 && <section className="graph-palette-group">
          <div className="graph-palette-group-head"><span>导入节点</span><small>{filteredImportedNodes.length}</small></div>
          <div className="graph-imported-list">
        {filteredImportedNodes.map(node => <div className="graph-palette-item" key={`imported-${node.id}`}>
          <span className="graph-palette-kind imported"><Globe size={14} /></span>
          <span className="graph-palette-copy"><strong>{node.name || `导入节点 ${node.id}`}</strong><small>{labelProtocol(node.protocol)} · {formatHostPort(node.target_address, node.target_port)}</small></span>
          <div className="graph-palette-actions">
            <button className="ghost icon-button" onClick={() => onAddImported(node)} title="连接到当前入口" aria-label={`将 ${node.name} 连接到当前入口`}><LinkIcon size={14} /></button>
            <button className="ghost icon-button" onClick={() => onShowImported(node)} title="放入画布" aria-label={`将 ${node.name} 放入画布`}><Plus size={15} /></button>
            <button className="ghost icon-button" onClick={() => onInspectImported(node)} title="节点设置" aria-label={`${node.name} 设置`}><SettingsIcon size={14} /></button>
          </div>
        </div>)}
          </div>
        </section>}
      </div>}
    </div>
    <footer className="dialog-actions"><button type="button" onClick={closeNodePicker}>完成</button></footer>
  </MotionDialogPanel>}</AnimatePresence>
  </>
}

function ProxyToolIcon({ kind }: { kind: ProxyToolAction }) {
	if (kind === 'server') return <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="4" width="16" height="6" rx="2" /><rect x="4" y="14" width="16" height="6" rx="2" /><path d="M8 7h.01M8 17h.01M12 10v4" /></svg>
	if (kind === 'entry') return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 12h11" /><path d="m11 8 4 4-4 4" /><circle cx="18" cy="12" r="2.5" /></svg>
	if (kind === 'imported') return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 7h10v10H7z" /><path d="M3 12h4M17 12h4" /><path d="M12 3v4M12 17v4" /><circle cx="12" cy="12" r="2" /></svg>
  if (kind === 'routing') return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h9" /><path d="M4 17h9" /><path d="m14 4 3 3-3 3" /><path d="m14 14 3 3-3 3" /><path d="M17 7h3" /><path d="M17 17h3" /></svg>
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 7h8a4 4 0 0 1 0 8H9" /><path d="m9 11-4 4 4 4" /><path d="M18 5v4" /><path d="M16 7h4" /></svg>
}

function defaultRoutingDraft(server: Server): RoutingDraft {
  return { server_id: server.id, name: `${server.name || 'server'}-route`, priority: 100, match_kind: 'domain_suffix', match_value: 'example.com', action: 'direct', outbound_id: 0, external_outbound_id: 0, warp_profile_id: 0, interface_name: '', enabled: true }
}

function routingMatchJSON(kind: RoutingMatchKind, value: string) {
  if (kind === 'all') return '{}'
  const items = value.split(/[\n,]/).map(x => x.trim()).filter(Boolean)
  if (!items.length) throw new Error('请填写匹配内容')
  if (kind === 'port') {
    const ports = items.map(item => Number(item))
    if (ports.some(port => !Number.isInteger(port) || port < 1 || port > 65535)) throw new Error('目标端口必须是 1 到 65535 的整数')
    return JSON.stringify({ port: ports }, null, 2)
  }
  if (kind === 'port_range') {
    for (const item of items) {
      const [startRaw, endRaw, extra] = item.split(':')
      const start = Number(startRaw)
      const end = Number(endRaw)
      if (extra !== undefined || !Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end > 65535 || start > end) throw new Error('端口范围格式应为起始:结束，例如 10000:20000')
    }
    return JSON.stringify({ port_range: items }, null, 2)
  }
  return JSON.stringify({ [kind]: items }, null, 2)
}

function routingMatchHint(kind: RoutingMatchKind) {
  if (kind === 'all') return '匹配全部流量，不需要填写内容。'
  if (kind === 'domain_suffix') return '例如 google.com；多个值用逗号或换行分隔。'
  if (kind === 'domain') return '填写完整域名；多个值用逗号或换行分隔。'
  if (kind === 'ip_cidr') return '例如 10.0.0.0/8 或 1.1.1.1；多个值用逗号或换行分隔。'
  if (kind === 'port') return '填写目标端口，例如 22 或 22, 443。'
  if (kind === 'port_range') return '填写目标端口范围，例如 10000:20000；多个范围用逗号或换行分隔。'
  if (kind === 'geosite') return '例如 cn、geolocation-!cn；需要底层规则集支持。'
  return '例如 cn、private；需要底层规则集支持。'
}

function defaultTransportDraft(servers: Server[], selected?: Server): TransportDraft {
  const source = selected || servers[0]
  const target = servers.find(s => s.id !== source?.id) || servers[1] || servers[0]
  const sourceName = source?.name || 'source'
  const targetName = target?.name || 'target'
  return {
    mode: 'port-forward',
    name: `${sourceName}-to-${targetName}`,
    source_server_id: source?.id || 0,
    target_server_id: target?.id || 0,
    listen_ip: source?.listen_ip || '0.0.0.0',
    listen_port: 10000,
    target_port: 443,
    protocol: 'tcp',
    backend: 'auto',
    type: 'wireguard',
    priority: 100,
    config_json: JSON.stringify({ private_key: '', peer_public_key: '', allowed_ips: [] }, null, 2),
    enabled: true,
  }
}

function RoutingRuleDraftDialog({ draft, setDraft, data, onCancel, onSubmit }: { draft: RoutingDraft; setDraft: React.Dispatch<React.SetStateAction<RoutingDraft | null>>; data: any; onCancel: () => void; onSubmit: () => Promise<void> }) {
  const update = (patch: Partial<RoutingDraft>) => setDraft(old => old ? { ...old, ...patch } : old)
  const serverOutbounds = (data.outbounds || []).filter((x: Outbound) => x.server_id === Number(draft.server_id))
  const externalOutbounds = (data.external_outbounds || []).filter((x: ExternalOutbound) => x.scope === 'global' || !x.server_id || x.server_id === Number(draft.server_id))
  const warpProfiles = (data.warp_profiles || []).filter((x: WARPProfile) => x.server_id === Number(draft.server_id))
  return <MotionDialogPanel onCancel={onCancel} className="graph-form-dialog">
      <header className="dialog-head">
        <div><h2 id="routing-dialog-title">添加分流出口</h2><p className="muted">设置匹配条件和处理方式。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form graph-dialog-form">
          <FormField label="服务器" required><Select value={draft.server_id} onChange={e => update({ server_id: Number(e.target.value), outbound_id: 0, warp_profile_id: 0 })}><option value={0}>选择服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select></FormField>
          <FormField label="名称" required><input value={draft.name} onChange={e => update({ name: e.target.value })} placeholder="例如 google-direct" /></FormField>
          <FormField label="优先级"><input value={draft.priority} onChange={e => update({ priority: Number(e.target.value) })} inputMode="numeric" /></FormField>
          <FormField label="匹配类型"><Select value={draft.match_kind} onChange={e => update({ match_kind: e.target.value as RoutingMatchKind, match_value: e.target.value === 'port' ? '22' : e.target.value === 'port_range' ? '10000:20000' : draft.match_value })}><option value="domain_suffix">域名后缀</option><option value="domain">完整域名</option><option value="ip_cidr">IP / CIDR</option><option value="port">目标端口</option><option value="port_range">目标端口范围</option><option value="geosite">Geosite</option><option value="geoip">GeoIP</option><option value="all">全部流量</option></Select></FormField>
          {draft.match_kind !== 'all' && <FormField label="匹配内容" hint={routingMatchHint(draft.match_kind)}><textarea value={draft.match_value} onChange={e => update({ match_value: e.target.value })} rows={3} /></FormField>}
          <FormField label="处理方式"><Select value={draft.action} onChange={e => update({ action: e.target.value as RouteAction })}>{routeActions.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
          {draft.action === 'outbound' && <FormField label="本机出口" required><Select value={draft.outbound_id} onChange={e => update({ outbound_id: Number(e.target.value) })}><option value={0}>选择出口</option>{serverOutbounds.map((x: Outbound) => <option value={x.id} key={x.id}>{x.name}</option>)}</Select></FormField>}
          {draft.action === 'external' && <FormField label="导入节点" required><Select value={draft.external_outbound_id} onChange={e => update({ external_outbound_id: Number(e.target.value) })}><option value={0}>选择导入节点</option>{externalOutbounds.map((x: ExternalOutbound) => <option value={x.id} key={x.id}>{x.name}</option>)}</Select></FormField>}
          {draft.action === 'warp' && <FormField label="WARP" required><Select value={draft.warp_profile_id} onChange={e => update({ warp_profile_id: Number(e.target.value) })}><option value={0}>选择 WARP</option>{warpProfiles.map((x: WARPProfile) => <option value={x.id} key={x.id}>{x.name}（{labelValue(x.status)}）</option>)}</Select></FormField>}
          {draft.action === 'interface' && <FormField label="出口网卡" required hint="填写 Agent 主机上的网卡名，例如 eth1、ens6。"><input value={draft.interface_name} onChange={e => update({ interface_name: e.target.value })} placeholder="eth1" autoComplete="off" /></FormField>}
          <FormField label="状态"><Select variant="segmented" value={String(draft.enabled)} onChange={e => update({ enabled: e.target.value === 'true' })}><option value="true">启用</option><option value="false">禁用</option></Select></FormField>
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={onSubmit}>创建分流</button></footer>
  </MotionDialogPanel>
}

function TransportDraftDialog({ draft, setDraft, servers, onCancel, onSubmit }: { draft: TransportDraft; setDraft: React.Dispatch<React.SetStateAction<TransportDraft | null>>; servers: Server[]; onCancel: () => void; onSubmit: () => Promise<void> }) {
  const update = (patch: Partial<TransportDraft>) => setDraft(old => old ? { ...old, ...patch } : old)
  const cfg = parseConfig(draft.config_json) || {}
  const updateTunnelConfig = (patch: Record<string, any>) => update({ config_json: JSON.stringify({ ...cfg, ...patch }, null, 2) })
  const setMode = (mode: TransportMode) => update({
    mode,
    name: `${serverName(servers, draft.source_server_id)}-to-${serverName(servers, draft.target_server_id)}`,
    listen_port: mode === 'port-forward' ? (draft.listen_port || 10000) : 0,
    target_port: mode === 'port-forward' ? (draft.target_port || 443) : 0,
  })
  const setTunnelType = (type: TunnelType) => update({ type, config_json: JSON.stringify({ private_key: '', peer_public_key: '', allowed_ips: [] }, null, 2) })
  return <MotionDialogPanel onCancel={onCancel} className="graph-form-dialog">
      <header className="dialog-head">
        <div><h2 id="transport-dialog-title">添加转发隧道</h2><p className="muted">连接源服务器和目标服务器。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form graph-dialog-form">
          <FormField label="类型"><Select variant="segmented" value={draft.mode} onChange={e => setMode(e.target.value as TransportMode)}><option value="port-forward">端口转发</option><option value="tunnel">服务器隧道</option></Select></FormField>
          <FormField label="名称" required><input value={draft.name} onChange={e => update({ name: e.target.value })} /></FormField>
          <FormField label="源服务器" required><Select value={draft.source_server_id} onChange={e => update({ source_server_id: Number(e.target.value) })}><option value={0}>选择源服务器</option>{servers.map(s => <option value={s.id} key={s.id}>{s.name}</option>)}</Select></FormField>
          <FormField label="目标服务器" required><Select value={draft.target_server_id} onChange={e => update({ target_server_id: Number(e.target.value) })}><option value={0}>选择目标服务器</option>{servers.map(s => <option value={s.id} key={s.id}>{s.name}</option>)}</Select></FormField>
          {draft.mode === 'port-forward' && <>
            <FormField label="监听 IP"><input value={draft.listen_ip} onChange={e => update({ listen_ip: e.target.value })} placeholder="0.0.0.0" /></FormField>
            <FormField label="监听端口" required><input value={draft.listen_port} onChange={e => update({ listen_port: Number(e.target.value) })} inputMode="numeric" /></FormField>
            <FormField label="目标端口" required><input value={draft.target_port} onChange={e => update({ target_port: Number(e.target.value) })} inputMode="numeric" /></FormField>
          <FormField label="协议"><Select variant="segmented" value={draft.protocol} onChange={e => update({ protocol: e.target.value as ForwardProtocol })}>{forwardProtocols.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
            <FormField label="后端"><Select value={draft.backend} onChange={e => update({ backend: e.target.value as ForwardBackend })}>{forwardBackends.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select></FormField>
          </>}
          {draft.mode === 'tunnel' && <>
            <FormField label="隧道类型"><Select variant="segmented" value={draft.type} onChange={e => setTunnelType(e.target.value as TunnelType)}><option value="wireguard">WireGuard</option></Select></FormField>
            {draft.type === 'wireguard' && <>
              <FormField label="本机私钥" required><input value={(cfg.private_key as string) || ''} onChange={e => updateTunnelConfig({ private_key: e.target.value })} /></FormField>
              <FormField label="对端公钥" required><input value={(cfg.peer_public_key as string) || ''} onChange={e => updateTunnelConfig({ peer_public_key: e.target.value })} /></FormField>
              <FormField label="允许 IP" hint="多个地址用逗号分隔。"><input value={Array.isArray(cfg.allowed_ips) ? cfg.allowed_ips.join(', ') : ''} onChange={e => updateTunnelConfig({ allowed_ips: e.target.value.split(',').map(x => x.trim()).filter(Boolean) })} /></FormField>
            </>}
            <FormField label="监听端口"><input value={draft.listen_port} onChange={e => update({ listen_port: Number(e.target.value) })} inputMode="numeric" /></FormField>
            <FormField label="目标端口"><input value={draft.target_port} onChange={e => update({ target_port: Number(e.target.value) })} inputMode="numeric" /></FormField>
          </>}
          <FormField label="优先级"><input value={draft.priority} onChange={e => update({ priority: Number(e.target.value) })} inputMode="numeric" /></FormField>
          <FormField label="状态"><Select variant="segmented" value={String(draft.enabled)} onChange={e => update({ enabled: e.target.value === 'true' })}><option value="true">启用</option><option value="false">禁用</option></Select></FormField>
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={onSubmit}>创建</button></footer>
  </MotionDialogPanel>
}

function ImportNodeDialog({ draft, setDraft, servers, onCancel, onSubmit }: { draft: ImportedNodeDraft; setDraft: React.Dispatch<React.SetStateAction<ImportedNodeDraft | null>>; servers: Server[]; onCancel: () => void; onSubmit: () => Promise<void> }) {
  const update = (patch: Partial<ImportedNodeDraft>) => setDraft(old => old ? { ...old, ...patch } : old)
  return <MotionDialogPanel onCancel={onCancel} className="graph-form-dialog">
      <header className="dialog-head">
        <div><h2 id="import-node-title">导入第三方节点</h2><p className="muted">粘贴节点链接或出站 JSON。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form graph-dialog-form">
          <FormField label="使用范围"><Select variant="segmented" value={draft.scope} onChange={e => update({ scope: e.target.value as 'global' | 'server' })}><option value="global">全部服务器</option><option value="server">指定服务器</option></Select></FormField>
          {draft.scope === 'server' && <FormField label="服务器" required><Select value={draft.server_id} onChange={e => update({ server_id: Number(e.target.value) })}><option value={0}>选择服务器</option>{servers.map(s => <option value={s.id} key={s.id}>{s.name}</option>)}</Select></FormField>}
          <FormField label="订阅展示"><Select variant="segmented" value={String(draft.expose_to_users)} onChange={e => update({ expose_to_users: e.target.value === 'true' })}><option value="false">隐藏</option><option value="true">允许授权</option></Select></FormField>
          <FormField label="链接或 JSON" required hint="每行一个链接。"><textarea value={draft.content} onChange={e => update({ content: e.target.value })} rows={8} /></FormField>
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={onSubmit}>导入节点</button></footer>
  </MotionDialogPanel>
}

function ImportedNodeConfigDialog({ node, data, client, load, onClose }: { node: ExternalOutbound; data: any; client: ReturnType<typeof api>; load: () => Promise<void>; onClose: () => void }) {
  const dialogs = useDialogs()
  const grants: ExternalOutboundAccessGrant[] = (data.external_outbound_access_grants || []).filter((x: ExternalOutboundAccessGrant) => x.external_outbound_id === node.id)
  const raw = safePrettyJSON(node.config_json)
  const toggleExpose = async () => {
    await client.request(`/external-outbounds/${node.id}`, { method: 'PATCH', body: JSON.stringify({ ...node, expose_to_users: !node.expose_to_users }) })
    await load()
    onClose()
  }
  const grantUser = async () => {
    const users: User[] = data.users || []
    if (!users.length) return dialogs.alert({ title: '没有用户', message: '请先创建用户。' })
    const selected = await dialogs.prompt({ title: '授权用户使用导入节点', message: '授权后该节点会出现在该用户订阅中。', defaultValue: String(users[0].id), choices: users.map(u => ({ value: String(u.id), label: u.username })) })
    if (!selected) return
    await client.request('/external-outbound-access-grants', { method: 'POST', body: JSON.stringify({ external_outbound_id: node.id, subject_type: 'user', subject_id: Number(selected), enabled: true }) })
    await load()
  }
  const grantGroup = async () => {
    const groups: UserGroup[] = data.user_groups || []
    if (!groups.length) return dialogs.alert({ title: '没有用户组', message: '请先在用户页面创建用户组。' })
    const selected = await dialogs.prompt({ title: '授权用户组使用导入节点', message: '授权后该节点会出现在该用户组成员订阅中。', defaultValue: String(groups[0].id), choices: groups.map(g => ({ value: String(g.id), label: g.name })) })
    if (!selected) return
    await client.request('/external-outbound-access-grants', { method: 'POST', body: JSON.stringify({ external_outbound_id: node.id, subject_type: 'group', subject_id: Number(selected), enabled: true }) })
    await load()
  }
  return <MotionDialogPanel onCancel={onClose} className="graph-form-dialog">
      <header className="dialog-head">
        <div><h2 id="imported-node-title">{node.name}</h2><p className="muted">{labelProtocol(node.protocol)} · {formatHostPort(node.target_address, node.target_port)} · {node.scope === 'server' ? '单服务器可用' : '全部服务器可用'}</p></div>
        <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="imported-node-summary">
          <div><span>协议</span><strong>{labelProtocol(node.protocol)}</strong></div>
          <div><span>地址</span><strong>{formatHostPort(node.target_address, node.target_port)}</strong></div>
          <div><span>订阅</span><strong>{node.expose_to_users ? '可授权展示' : '默认隐藏'}</strong></div>
        </div>
        {node.expose_to_users && <div className="compact-panel">
          <div className="section-toolbar"><div><h3>订阅授权</h3><p className="muted">只控制这个导入节点是否出现在用户订阅，不影响链路图下发。</p></div><div className="section-actions"><button onClick={grantUser}>授权用户</button><button onClick={grantGroup}>授权用户组</button></div></div>
          {grants.length ? <div className="chip-list">{grants.map(g => <span className="chip" key={g.id}>{g.subject_type === 'user' ? userName(data, g.subject_id) : groupName(data, g.subject_id)} <button onClick={async () => { await client.request(`/external-outbound-access-grants/${g.id}`, { method: 'DELETE' }); await load() }}>×</button></span>)}</div> : <p className="muted">暂无授权。</p>}
        </div>}
        <div className="compact-panel">
          <h3>配置</h3>
          <p className="muted">列表里默认隐藏密钥；这里用于排查和复制原始配置。</p>
          <CopyBlock value={raw} />
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={toggleExpose}>{node.expose_to_users ? '从订阅隐藏' : '允许订阅授权'}</button><button onClick={onClose}>完成</button></footer>
  </MotionDialogPanel>
}

function safePrettyJSON(raw: string) {
  try { return JSON.stringify(JSON.parse(raw || '{}'), null, 2) } catch { return raw || '{}' }
}

function serverName(servers: Server[], id: number) {
  return servers.find(s => s.id === id)?.name || `server-${id || 0}`
}

function autoInboundName(server: Server | undefined, protocol: Protocol, port: number) {
  return `${server?.name || 'server'}-${protocol}-${port || 443}`
}

function nextAvailableInboundPort(data: any, server: Server | undefined, protocol: Protocol, fallbackPort = 443, excludeInboundID = 0) {
  if (!server) return fallbackPort
  const start = Number(server.port_range_start || 0)
  const end = Number(server.port_range_end || 0)
  const used = new Set<number>((data.inbounds || []).filter((x: Inbound) => x.server_id === server.id && x.id !== excludeInboundID).map((x: Inbound) => Number(x.port)))
  if (start > 0 && end >= start) {
    const preferred = preferredProtocolPortInRange(protocol, start, end, used)
    if (preferred) return preferred
    for (let port = start; port <= end; port++) if (!used.has(port)) return port
  }
  if (fallbackPort > 0 && !used.has(fallbackPort)) return fallbackPort
  for (let port = 10000; port <= 65535; port++) if (!used.has(port)) return port
  return fallbackPort
}

function preferredProtocolPortInRange(protocol: Protocol, start: number, end: number, used: Set<number>) {
  const preferred: Record<Protocol, number[]> = {
    vless: [443, 8443, 10443],
    hy2: [443, 8443, 10443],
    anytls: [443, 8443, 10443],
    shadowsocks: [8388, 18388, 38388],
	ssh: [2222, 22022, 22222],
  }
  for (const port of preferred[protocol] || []) {
    if (port >= start && port <= end && !used.has(port)) return port
  }
  return 0
}

async function createEntryAccessGrants(client: any, inbound: Inbound, scope: AccessScopeType, userIDs: number[], groupIDs: number[]) {
  const grantPayload = (subjectType: AccessSubjectType, subjectID: number) => {
    const payload: any = { subject_type: subjectType, subject_id: subjectID, scope_type: scope, enabled: true }
    if (scope === 'server') payload.server_id = inbound.server_id
    if (scope === 'inbound') payload.inbound_id = inbound.id
    return payload
  }
  for (const userID of userIDs) {
    if (scope === 'inbound') {
      await client.request('/inbound-users', { method: 'POST', body: JSON.stringify({ inbound_id: inbound.id, user_id: userID, enabled: true }) })
    } else {
      await client.request('/inbound-access-grants', { method: 'POST', body: JSON.stringify(grantPayload('user', userID)) })
    }
  }
  for (const groupID of groupIDs) {
    await client.request('/inbound-access-grants', { method: 'POST', body: JSON.stringify(grantPayload('group', groupID)) })
  }
}

type RealityKeyPair = { private_key: string; public_key: string; short_id: string }

function EntryDraftDialog({ mode = 'create', draft, setDraft, data, servers, client, onCancel, onSubmit }: { mode?: 'create' | 'edit'; draft: any; setDraft: React.Dispatch<React.SetStateAction<any | null>>; data: any; servers: Server[]; client: ReturnType<typeof api>; onCancel: () => void; onSubmit: () => Promise<void> }) {
  const dialogs = useDialogs()
  const server = servers.find(s => s.id === Number(draft.server_id)) || servers[0]
  const protocol = draft.protocol as Protocol
  const [presetID, setPresetID] = useState(() => inferInboundPreset(protocol, draft.config_json))
  const [realityKeyLoading, setRealityKeyLoading] = useState(false)
  const selectedPreset = inboundPreset(presetID)
  const presetProtocol = selectedPreset.protocol
  const presetOptions = inboundPresetsForProtocol(presetProtocol)
  const cfg = parseConfig(draft.config_json) || {}
  const tlsForReality = objectConfig(cfg.tls)
  const realityForKeys = objectConfig(tlsForReality.reality)
  const dnsCredentials: DNSCredential[] = data.dns_credentials || []
  const certificates: Certificate[] = data.certificates || []
  const selectedDNSCredential = dnsCredentials.find(item => item.id === Number(draft.dns_credential_id || 0))
  const certificateRequired = presetRequiresCertificate(presetID)
  const entryMode = (draft.entry_ip_mode || 'auto') as EntryIPMode
  const entryAddress = draft.dns_sync_enabled && draft.dns_domain ? draft.dns_domain : entryAddressByMode(server, entryMode, draft.external_ip || '')
  const update = (patch: any) => setDraft((old: any) => old ? { ...old, ...patch } : old)
  const generateRealityKeypair = async (silent = false) => {
    if (realityKeyLoading) return
    setRealityKeyLoading(true)
    try {
      const pair = await client.request<RealityKeyPair>('/reality/keypair', { method: 'POST', body: '{}' })
      setDraft((old: any) => old ? { ...old, config_json: mergeRealityKeypair(old.config_json, pair) } : old)
    } catch (e: any) {
      if (!silent) await dialogs.alert({ title: '生成 Reality 密钥失败', message: localizeErrorMessage(e.message || e) })
    } finally {
      setRealityKeyLoading(false)
    }
  }
  const autoPortFor = (targetServer = server, targetProtocol = protocol, fallback = Number(draft.port) || inboundPreset(presetID).defaultPort) => nextAvailableInboundPort(data, targetServer, targetProtocol, fallback, draft.id)
  const applyPreset = (id: string) => {
    const preset = inboundPreset(id)
    const previousRequiresCertificate = presetRequiresCertificate(presetID)
    setPresetID(preset.id)
    setDraft((old: any) => {
      if (!old) return old
      const currentPort = Number(old.port) || preset.defaultPort
      const keepManualPort = mode === 'edit' || old.__port_manual === true
      const nextPort = keepManualPort ? currentPort : nextAvailableInboundPort(data, server, preset.protocol, preset.defaultPort, old.id)
      const oldAutoName = autoInboundName(server, old.protocol || protocol, currentPort)
      const shouldRename = !old.name || old.name === oldAutoName || /^.+-(vless|hy2|anytls|shadowsocks|ssh)-\d+$/.test(String(old.name))
      return {
        ...old,
        protocol: preset.protocol,
        port: nextPort,
        name: shouldRename ? autoInboundName(server, preset.protocol, nextPort) : old.name,
        config_json: buildInboundPresetConfig(preset.id),
        tls: presetRequiresCertificate(preset.id),
        certificate_mode: presetRequiresCertificate(preset.id) ? (previousRequiresCertificate ? (old.certificate_mode || 'auto') : 'auto') : 'external',
        certificate_domain: presetRequiresCertificate(preset.id) ? (old.certificate_domain || 'example.com') : '',
        certificate_id: presetRequiresCertificate(preset.id) ? old.certificate_id : undefined,
      }
    })
  }
  const changePresetProtocol = (nextProtocol: Protocol) => applyPreset(defaultInboundPreset(nextProtocol))
  const changeServer = (serverID: number) => {
    const nextServer = servers.find(s => s.id === serverID)
    const currentPort = Number(draft.port) || 443
    const keepManualPort = mode === 'edit' || draft.__port_manual === true
    const nextPort = keepManualPort ? currentPort : nextAvailableInboundPort(data, nextServer, protocol, currentPort, draft.id)
    const oldName = autoInboundName(server, protocol, currentPort)
    const shouldRename = !draft.name || draft.name === oldName || /^.+-(vless|hy2|anytls|shadowsocks|ssh)-\d+$/.test(String(draft.name))
    update({ server_id: serverID, listen_ip: nextServer?.listen_ip || '0.0.0.0', port: nextPort, name: shouldRename ? autoInboundName(nextServer, protocol, nextPort) : draft.name })
  }
  const chooseAutoPort = () => {
    const nextPort = autoPortFor(server, protocol, inboundPreset(presetID).defaultPort)
    changePort(nextPort, false)
  }
  const changePort = (nextPort: number, manual = true) => {
    const oldName = autoInboundName(server, protocol, Number(draft.port) || 443)
    update({ port: nextPort, __port_manual: manual, name: draft.name === oldName ? autoInboundName(server, protocol, nextPort) : draft.name })
  }
  const changeEntryMode = (nextMode: EntryIPMode) => update(nextMode === 'custom' ? { entry_ip_mode: nextMode, ddns_enabled: false } : { entry_ip_mode: nextMode })
  const updateConfig = (patch: Record<string, any>) => update({ config_json: JSON.stringify({ ...cfg, ...patch }, null, 2) })
  const toggleDraftID = (key: 'access_user_ids' | 'access_group_ids', id: number) => {
    const current = new Set<number>(draft[key] || [])
    if (current.has(id)) current.delete(id)
    else current.add(id)
    update({ [key]: Array.from(current) })
  }
  const previewConfig = presetID === 'vless-reality' ? redactRealityPrivateKey(draft.config_json || '') : (draft.config_json || '')
  const copyConfig = async () => { await copyText(previewConfig) }
  const regenerate = () => {
    applyPreset(presetID)
    if (presetID === 'vless-reality') void generateRealityKeypair(true)
  }
  useEffect(() => {
    if (presetID !== 'vless-reality' || realityKeyLoading) return
    if (String(realityForKeys.private_key || '').trim() && String(realityForKeys.public_key || '').trim()) return
    void generateRealityKeypair(true)
  }, [presetID, draft.config_json, realityKeyLoading])
  return <MotionDialogPanel onCancel={onCancel} className="entry-dialog">
      <header className="dialog-head">
        <div><h2 id="entry-dialog-title">{mode === 'edit' ? '编辑入口协议' : '添加入口协议'}</h2><p className="muted">设置入口协议、地址和端口。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="entry-form labeled-form">
          <FormField label="服务器" required><Select value={draft.server_id} onChange={e => changeServer(Number(e.target.value))}><option value={0}>选择服务器</option>{servers.map(s => <option value={s.id} key={s.id}>{s.name}</option>)}</Select></FormField>
          <FormField label="入口名称" required><input value={draft.name} onChange={e => update({ name: e.target.value })} /></FormField>
          <FormField label="协议大类" required><Select value={presetProtocol} onChange={e => changePresetProtocol(e.target.value as Protocol)}>{protocols.map(p => <option key={p} value={p}>{labelProtocol(p)}</option>)}</Select></FormField>
          <FormField label="具体类型" required><Select value={presetID} onChange={e => applyPreset(e.target.value)}>{presetOptions.map(p => <option key={p.id} value={p.id}>{p.label}</option>)}</Select></FormField>
          <div className="preset-help"><strong>{selectedPreset.label}</strong><span>{selectedPreset.description}</span><small>协议：{labelProtocol(selectedPreset.protocol)}</small></div>
          {protocol === 'ssh' && <div className="access-note warning"><strong>SSH 暴露确认</strong><span>仅允许公钥认证和本地/动态转发；不提供 shell、SFTP、远程转发或主机账户。保存时仍需再次确认。</span></div>}
          <FormField label="入口地址策略" hint="客户端订阅使用的连接地址。">
            <Select value={entryMode} onChange={e => changeEntryMode(e.target.value as EntryIPMode)}>{entryIPModes.map(x => <option key={x} value={x}>{entryAddressModeLabel(x, server)}</option>)}</Select>
          </FormField>
          {entryMode === 'custom' && <FormField label="自定义入口地址" required hint="可填写域名、IPv4 或 IPv6。">
            <input value={draft.external_ip || ''} onChange={e => update({ external_ip: e.target.value })} placeholder="例如 1.2.3.4 或 origin.example.net" />
          </FormField>}
          <div className="managed-entry-box">
            <label className="check-row">
              <input type="checkbox" checked={Boolean(draft.dns_sync_enabled)} onChange={e => update({ dns_sync_enabled: e.target.checked, dns_credential_id: e.target.checked ? (draft.dns_credential_id || dnsCredentials.find(item => item.verified_at)?.id) : undefined, dns_record_types: draft.dns_record_types === 'auto' ? 'a' : (draft.dns_record_types || 'a'), dns_proxy_enabled: e.target.checked && selectedDNSCredential?.provider === 'cloudflare' ? Boolean(draft.dns_proxy_enabled) : false })} />
              <span>自动同步 DNS 解析</span>
            </label>
            {draft.dns_sync_enabled && <>
              <FormField label="域名服务账号" required>
                <Select value={Number(draft.dns_credential_id || 0)} onChange={e => { const id = Number(e.target.value); const credential = dnsCredentials.find(item => item.id === id); update({ dns_credential_id: id || undefined, dns_proxy_enabled: credential?.provider === 'cloudflare' ? Boolean(draft.dns_proxy_enabled) : false }) }}><option value={0}>选择凭据</option>{dnsCredentials.filter(item => item.enabled).map(item => <option key={item.id} value={item.id}>{item.name} · {dnsProviderLabels[item.provider]}</option>)}</Select>
              </FormField>
              <FormField label="解析域名" required hint="客户端连接使用的域名。">
                <input value={draft.dns_domain || ''} onChange={e => update({ dns_domain: e.target.value })} placeholder="entry.example.com" />
              </FormField>
              <FormField label="地址类型" hint={entryMode === 'custom' && isDomainLike(draft.external_ip || '') ? '域名目标会创建 CNAME。' : '选择要创建的解析记录。'}>
                <Select variant="segmented" value={draft.dns_record_types === 'auto' ? 'a' : (draft.dns_record_types || 'a')} onChange={e => update({ dns_record_types: e.target.value as DNSRecordTypes })} disabled={entryMode === 'custom' && isDomainLike(draft.external_ip || '')}>
                  {dnsRecordTypes.map(x => <option key={x} value={x}>{dnsRecordTypeLabel(x)}</option>)}
                </Select>
              </FormField>
              {selectedDNSCredential?.provider === 'cloudflare' && <FormField label="Cloudflare 代理" hint="普通代理建议使用 DNS only。">
                <Select variant="segmented" value={String(Boolean(draft.dns_proxy_enabled))} onChange={e => update({ dns_proxy_enabled: e.target.value === 'true' })}><option value="false">DNS only</option><option value="true">开启代理</option></Select>
              </FormField>}
              {entryMode !== 'custom' && <label className="check-row"><input type="checkbox" checked={Boolean(draft.ddns_enabled)} onChange={e => update({ ddns_enabled: e.target.checked })} /><span>公网 IP 变化时定时更新</span></label>}
              {draft.ddns_enabled && <FormField label="检查间隔"><Select value={Number(draft.ddns_interval_seconds || 300)} onChange={e => update({ ddns_interval_seconds: Number(e.target.value) })}><option value={300}>5 分钟</option><option value={900}>15 分钟</option><option value={3600}>1 小时</option><option value={21600}>6 小时</option></Select></FormField>}
              <div className="access-note compact"><strong>同步时机</strong><span>下发前同步，开启定时更新后按间隔刷新。</span></div>
              {!dnsCredentials.length && <div className="access-note warning"><strong>还没有可用的域名服务账号</strong><span>请先到“域名解析”创建并验证账号。</span></div>}
            </>}
          </div>
          {certificateRequired && <div className="managed-entry-box">
            <FormField label="证书模式"><Select value={draft.certificate_mode || 'auto'} onChange={e => update({ certificate_mode: e.target.value as CertificateMode, certificate_id: e.target.value === 'explicit' ? draft.certificate_id : undefined })}><option value="auto">自动匹配</option><option value="exact">仅精确子域证书</option><option value="wildcard">仅泛域名证书</option><option value="explicit">指定证书</option><option value="external">Agent 外部路径</option></Select></FormField>
            {draft.certificate_mode !== 'external' && <FormField label="证书域名" required><input value={draft.certificate_domain || ''} onChange={e => update({ certificate_domain: e.target.value })} placeholder="entry.example.com" /></FormField>}
            {draft.certificate_mode === 'explicit' && <FormField label="指定证书" required><Select value={Number(draft.certificate_id || 0)} onChange={e => update({ certificate_id: Number(e.target.value) || undefined })}><option value={0}>选择证书</option>{certificates.filter(item => item.status === 'ready').map(item => <option key={item.id} value={item.id}>{item.name} · {item.domains.join(', ')}</option>)}</Select></FormField>}
            {draft.certificate_mode === 'external' && <><FormField label="证书路径" required><input value={String(tlsForReality.certificate_path || '')} onChange={e => updateConfig({ tls: { ...tlsForReality, certificate_path: e.target.value } })} placeholder="/etc/ssl/example/fullchain.pem" /></FormField><FormField label="私钥路径" required><input value={String(tlsForReality.key_path || '')} onChange={e => updateConfig({ tls: { ...tlsForReality, key_path: e.target.value } })} placeholder="/etc/ssl/example/privkey.pem" /></FormField></>}
          </div>}
          <div className="access-note compact"><strong>当前入口地址</strong><span>{entryAddress ? formatHostPort(entryAddress, Number(draft.port) || 0) : '待 Agent 检测或填写自定义地址。'}</span></div>
          <FormField label="监听 IP"><input value={draft.listen_ip} onChange={e => update({ listen_ip: e.target.value })} placeholder="0.0.0.0" /></FormField>
          <FormField label="监听端口" required><div className="inline-field-action"><input value={draft.port} onChange={e => changePort(Number(e.target.value))} inputMode="numeric" /><button type="button" className="ghost" onClick={chooseAutoPort}>自动选择</button></div><small className="field-hint">{draft.__port_manual ? '已手动指定。' : '从服务器端口池自动选择。'}</small></FormField>
          <FormField label="状态"><Select variant="segmented" value={String(draft.enabled)} onChange={e => update({ enabled: e.target.value === 'true' })}><option value="true">启用</option><option value="false">禁用</option></Select></FormField>
          {protocol !== 'ssh' && <InboundPresetFields presetID={presetID} config={cfg} updateConfig={updateConfig} onGenerateRealityKeypair={() => generateRealityKeypair(false)} realityKeyLoading={realityKeyLoading} />}
          {mode === 'create' && <details className="entry-access-settings">
            <summary>用户权限与授权范围</summary>
            <div className="entry-access-settings-body">
              <div className="access-note"><strong>默认绑定创建者</strong><span>也可授权其他用户或用户组。</span></div>
              <FormField label="授权范围">
                <Select variant="segmented" value={draft.access_scope || 'inbound'} onChange={e => update({ access_scope: e.target.value as AccessScopeType })}>
                  <option value="inbound">仅此入口</option>
                  <option value="server">这台服务器全部入口</option>
                  <option value="global">全部入口</option>
                </Select>
              </FormField>
              <div className="access-pick-grid">
                <div><strong>用户</strong>{(data.users || []).map((u: User) => <label className="check-row compact" key={u.id}><input type="checkbox" checked={(draft.access_user_ids || []).includes(u.id)} onChange={() => toggleDraftID('access_user_ids', u.id)} /><span>{u.username}（{labelValue(u.status)}）</span></label>)}</div>
                <div><strong>用户组</strong>{(data.user_groups || []).length ? (data.user_groups || []).map((g: UserGroup) => <label className="check-row compact" key={g.id}><input type="checkbox" checked={(draft.access_group_ids || []).includes(g.id)} onChange={() => toggleDraftID('access_group_ids', g.id)} /><span>{g.name}{g.enabled === false ? '（禁用）' : ''}</span></label>) : <p className="muted">还没有用户组。</p>}</div>
              </div>
            </div>
          </details>}
        </div>
        <details className="advanced-config">
          <summary>高级：查看生成配置</summary>
          <div className="config-preview-head">
            <div><strong>生成配置</strong><p className="muted">上方表单会同步更新这里；一般不需要手动修改。</p></div>
            <div><button className="ghost" onClick={regenerate}>按类型重新生成</button><button className="ghost" onClick={copyConfig}>复制配置</button></div>
          </div>
          <textarea className="config-preview" value={previewConfig} onChange={e => presetID !== 'vless-reality' && update({ config_json: e.target.value })} readOnly={presetID === 'vless-reality'} spellCheck={false} />
        </details>
      </div>
      <footer className="dialog-actions">
        <button className="ghost" onClick={onCancel}>取消</button>
        <button onClick={onSubmit}>{mode === 'edit' ? '保存入口协议' : '创建入口协议'}</button>
      </footer>
  </MotionDialogPanel>
}

function InboundPresetFields({ presetID, config, updateConfig, onGenerateRealityKeypair, realityKeyLoading }: { presetID: string; config: Record<string, any>; updateConfig: (patch: Record<string, any>) => void; onGenerateRealityKeypair?: () => void; realityKeyLoading?: boolean }) {
  const tls = objectConfig(config.tls)
  const transport = objectConfig(config.transport)
  const headers = objectConfig(transport.headers)
  const reality = objectConfig(tls.reality)
  const handshake = objectConfig(reality.handshake)
  const setTLS = (patch: Record<string, any>) => updateConfig({ tls: { ...tls, ...patch } })
  const setTransport = (patch: Record<string, any>) => updateConfig({ transport: { ...transport, ...patch } })
  const setReality = (patch: Record<string, any>) => setTLS({ reality: { ...reality, ...patch } })
  const setHandshake = (patch: Record<string, any>) => setReality({ handshake: { ...handshake, ...patch } })
  if (presetID === 'vless-reality') return <div className="preset-fields">
    <div className="form-section-title">TCP / Reality / Vision 设置</div>
    <div className="access-note compact"><strong>协议形态</strong><span>默认 TCP，自动使用 Vision flow（xtls-rprx-vision）。</span></div>
    <FormField label="SNI 域名" hint="客户端连接使用的 SNI。"><input value={String(tls.server_name || '')} onChange={e => setTLS({ server_name: e.target.value })} placeholder="cdn.icloud-content.com" /></FormField>
    <FormField label="握手域名" hint="Reality 的伪装站点。"><input value={String(handshake.server || '')} onChange={e => setHandshake({ server: e.target.value })} placeholder="cdn.icloud-content.com" /></FormField>
    <FormField label="握手端口"><input value={Number(handshake.server_port || 443)} onChange={e => setHandshake({ server_port: Number(e.target.value) })} inputMode="numeric" /></FormField>
    <div className="reality-key-summary">
      <div><strong>Reality 密钥</strong><span>{realityKeyLoading ? '正在生成…' : String(reality.public_key || '').trim() ? `已生成 · 公钥 ${compactSecret(String(reality.public_key))}` : '保存时服务端会自动生成'}</span></div>
      <button type="button" className="ghost" onClick={onGenerateRealityKeypair} disabled={realityKeyLoading}>{realityKeyLoading ? '生成中' : '重新生成密钥对'}</button>
    </div>
    <FormField label="Short ID"><input value={String(reality.short_id || '')} onChange={e => setReality({ short_id: e.target.value })} placeholder="8-16 位十六进制" /></FormField>
  </div>
  if (presetID === 'vless-ws') return <div className="preset-fields">
    <div className="form-section-title">WebSocket / TLS 设置</div>
    <FormField label="SNI 域名"><input value={String(tls.server_name || '')} onChange={e => setTLS({ server_name: e.target.value })} placeholder="example.com" /></FormField>
    <FormField label="WebSocket 路径"><input value={String(transport.path || '')} onChange={e => setTransport({ path: e.target.value })} placeholder="/vless" /></FormField>
    <FormField label="Host 头"><input value={String(headers.Host || '')} onChange={e => setTransport({ headers: { ...headers, Host: e.target.value } })} placeholder="example.com" /></FormField>
  </div>
  if (presetID === 'vless-tls-vision' || presetID === 'hy2-tls' || presetID === 'anytls-basic') return <div className="preset-fields">
    <div className="form-section-title">TLS 设置</div>
    <FormField label="SNI 域名"><input value={String(tls.server_name || '')} onChange={e => setTLS({ server_name: e.target.value })} placeholder="example.com" /></FormField>
    {presetID === 'hy2-tls' && <>
      <FormField label="上传带宽 Mbps"><input value={Number(config.up_mbps || 100)} onChange={e => updateConfig({ up_mbps: Number(e.target.value) })} inputMode="numeric" /></FormField>
      <FormField label="下载带宽 Mbps"><input value={Number(config.down_mbps || 100)} onChange={e => updateConfig({ down_mbps: Number(e.target.value) })} inputMode="numeric" /></FormField>
    </>}
  </div>
  if (presetID.startsWith('ss-')) return <div className="preset-fields">
    <div className="form-section-title">SS 设置</div>
    <FormField label="加密方法"><Select value={String(config.method || '2022-blake3-aes-128-gcm')} onChange={e => updateConfig({ method: e.target.value })}>{shadowsocksMethods.map(x => <option key={x.value} value={x.value}>{x.label}</option>)}</Select></FormField>
    {!String(config.method || '').startsWith('2022-') && <div className="access-note compact"><strong>单用户入口</strong><span>多人使用请选择 SS 2022 或其他多用户协议。</span></div>}
  </div>
  return null
}

function objectConfig(value: any): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {}
}

function mergeRealityKeypair(configJson: string, pair: RealityKeyPair) {
  const cfg = parseConfig(configJson) || {}
  const tls = objectConfig(cfg.tls)
  const reality = objectConfig(tls.reality)
  const handshake = objectConfig(reality.handshake)
  cfg.tls = {
    ...tls,
    enabled: tls.enabled ?? true,
    server_name: tls.server_name || 'cdn.icloud-content.com',
    reality: {
      ...reality,
      enabled: true,
      handshake: Object.keys(handshake).length ? handshake : { server: 'cdn.icloud-content.com', server_port: 443 },
      private_key: pair.private_key,
      public_key: pair.public_key,
      short_id: pair.short_id || reality.short_id || randomHex(8),
    },
  }
  return JSON.stringify(cfg, null, 2)
}

function redactRealityPrivateKey(configJson: string) {
  const cfg = parseConfig(configJson)
  if (!cfg) return configJson
  const tls = objectConfig(cfg.tls)
  const reality = objectConfig(tls.reality)
  if (Object.keys(reality).length && reality.private_key) {
    cfg.tls = { ...tls, reality: { ...reality, private_key: '<已隐藏>' } }
  }
  return JSON.stringify(cfg, null, 2)
}

function compactSecret(value: string) {
  const text = String(value || '').trim()
  if (!text) return '待生成'
  if (text.length <= 14) return text
  return `${text.slice(0, 6)}…${text.slice(-6)}`
}

function isDomainLike(value: string) {
  const text = String(value || '').trim().toLowerCase().replace(/\.$/, '')
  if (!text || !text.includes('.') || /[/:@]/.test(text)) return false
  if (/^\[?[0-9a-f:.]+\]?$/.test(text) && text.includes(':')) return false
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(text)) return false
  return text.split('.').every(label => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))
}

function dnsRecordTypeLabel(value: DNSRecordTypes) {
  if (value === 'a') return 'A（IPv4）'
  if (value === 'aaaa') return 'AAAA（IPv6）'
  if (value === 'both') return 'A + AAAA'
  return '自动'
}

function inboundBindings(data: any, entry: Inbound) {
  return ((data.inbound_users || []) as InboundUser[]).filter(x => x.inbound_id === entry.id && x.enabled !== false)
}

function activeUsers(data: any) {
  return ((data.users || []) as User[]).filter(u => u.status === 'active')
}

function accessGrantAppliesToEntry(grant: InboundAccessGrant, entry: Inbound) {
  if (grant.enabled === false) return false
  if (grant.scope_type === 'global') return true
  if (grant.scope_type === 'server') return Number(grant.server_id || 0) === entry.server_id
  if (grant.scope_type === 'inbound') return Number(grant.inbound_id || 0) === entry.id
  return false
}

function entryAccessGrants(data: any, entry: Inbound) {
  return ((data.inbound_access_grants || []) as InboundAccessGrant[]).filter(x => accessGrantAppliesToEntry(x, entry))
}

function effectiveInboundUserIDs(data: any, entry: Inbound) {
  const ids = new Set<number>()
  const active = new Set(activeUsers(data).map(u => u.id))
  inboundBindings(data, entry).forEach(x => { if (active.has(x.user_id)) ids.add(x.user_id) })
  const groups: UserGroup[] = data.user_groups || []
  const members: UserGroupMember[] = data.user_group_members || []
  const groupEnabled = new Set(groups.filter(g => g.enabled !== false).map(g => g.id))
  entryAccessGrants(data, entry).forEach(grant => {
    if (grant.subject_type === 'user' && active.has(grant.subject_id)) ids.add(grant.subject_id)
    if (grant.subject_type === 'group' && groupEnabled.has(grant.subject_id)) {
      members.filter(m => m.enabled !== false && m.group_id === grant.subject_id && active.has(m.user_id)).forEach(m => ids.add(m.user_id))
    }
  })
  return ids
}

function inboundAccessSummary(data: any, entry: Inbound) {
  const count = effectiveInboundUserIDs(data, entry).size
  if (!count) return '未绑定用户 · 临时占位下发'
  const hasGroup = entryAccessGrants(data, entry).some(x => x.subject_type === 'group')
  const hasRange = entryAccessGrants(data, entry).some(x => x.scope_type !== 'inbound')
  return `${count} 个用户${hasGroup ? ' · 含用户组' : ''}${hasRange ? ' · 含范围授权' : ''}`
}

function latestInboundProbeSummary(data: any, inboundID: number) {
  const probes: InboundProbeResult[] = (data.inbound_probes || []).filter((x: InboundProbeResult) => x.inbound_id === inboundID)
  const local = probes.find(x => x.mode === 'agent_listener')
  const external = probes.find(x => x.mode.startsWith('controller_external'))
  if (local && !local.available) return { tone: 'danger', label: '本机监听异常', detail: local.error || '端口未监听', probe: local }
  if (external && !external.available) return { tone: 'danger', label: '公网端口异常', detail: external.error || '无法连接', probe: external }
  if (external?.available && external.confirmed) return { tone: 'ok', label: `公网可用 · ${external.latency_ms}ms`, detail: `${external.success_count}/${external.sample_count} 次成功 · P95 ${external.p95_latency_ms}ms`, probe: external }
  if (local?.available && local.transport === 'udp') return { tone: 'ok', label: 'UDP 监听正常', detail: '主控已发出公网 UDP 探测包', probe: local }
  if (local?.available) return { tone: 'ok', label: `本机正常 · ${local.latency_ms}ms`, detail: `${local.success_count}/${local.sample_count} 次成功`, probe: local }
  return { tone: 'pending', label: '等待端口探测', detail: '下发后自动检测', probe: undefined }
}

function latestForwardProbe(data: any, forwardID: number): PortForwardProbeResult | undefined {
  return (data.port_forward_probes || []).find((x: PortForwardProbeResult) => x.port_forward_id === forwardID)
}

function forwardProbeDetails(probe?: PortForwardProbeResult) {
  if (!probe) return { successCount: 0, p95: 0, jitter: 0, listenerOK: false }
  const raw = parseConfig(probe.result_json) || {}
  return {
    successCount: Number(raw.success_count ?? (probe.available ? probe.sample_count : 0)),
    p95: Number(raw.p95_latency_ms || 0),
    jitter: Number(raw.jitter_ms || 0),
    listenerOK: raw.listener_ok === true,
  }
}

function inboundSupportsMultipleUsersUI(entry: Inbound) {
	if (entry.protocol === 'vless' || entry.protocol === 'hy2' || entry.protocol === 'anytls' || entry.protocol === 'ssh') return true
  if (entry.protocol !== 'shadowsocks') return false
  const cfg = parseConfig(entry.config_json) || {}
  return String(cfg.method || '2022-blake3-aes-128-gcm').toLowerCase().startsWith('2022-')
}

function userByID(data: any, id: number) {
  return ((data.users || []) as User[]).find(u => u.id === id)
}

function groupByID(data: any, id: number) {
  return ((data.user_groups || []) as UserGroup[]).find(g => g.id === id)
}
function userName(data: any, id: number) {
  return userByID(data, id)?.username || `用户 ${id}`
}
function groupName(data: any, id: number) {
  return groupByID(data, id)?.name || `用户组 ${id}`
}

function accessSubjectLabel(data: any, grant: InboundAccessGrant) {
  if (grant.subject_type === 'group') return `用户组 ${groupByID(data, grant.subject_id)?.name || grant.subject_id}`
  return userByID(data, grant.subject_id)?.username || `用户 ${grant.subject_id}`
}

function accessScopeLabel(data: any, grant: InboundAccessGrant) {
  if (grant.scope_type === 'global') return '全部入口'
  if (grant.scope_type === 'server') {
    const server = ((data.servers || []) as Server[]).find(s => s.id === Number(grant.server_id || 0))
    return `${server?.name || '该服务器'}全部入口`
  }
  const inbound = ((data.inbounds || []) as Inbound[]).find(x => x.id === Number(grant.inbound_id || 0))
  return inbound?.name || '仅此入口'
}

function EntryUsersDialog({ entry, data, client, load, onCancel }: { entry: Inbound; data: any; client: any; load: () => Promise<void>; onCancel: () => void }) {
  const dialogs = useDialogs()
  const liveEntry: Inbound = (data.inbounds || []).find((item: Inbound) => item.id === entry.id) || entry
  const users: User[] = data.users || []
  const groups: UserGroup[] = data.user_groups || []
  const bindings = inboundBindings(data, entry)
  const grants = entryAccessGrants(data, entry)
  const effectiveIDs = effectiveInboundUserIDs(data, entry)
  const multi = inboundSupportsMultipleUsersUI(entry)
  const [subjectType, setSubjectType] = useState<AccessSubjectType>('user')
  const [subjectID, setSubjectID] = useState(0)
  const [scopeType, setScopeType] = useState<AccessScopeType>('inbound')
  const [dnsSyncing, setDNSSyncing] = useState(false)
  const [dnsFeedback, setDNSFeedback] = useState('')
  const boundIDs = new Set(bindings.map(x => x.user_id))
  const candidates = subjectType === 'user' ? users.filter(u => scopeType !== 'inbound' || !boundIDs.has(u.id)) : groups.filter(g => g.enabled !== false)
  const canAdd = !!subjectID && (multi || effectiveIDs.size === 0)
  const add = async () => {
    if (!canAdd) return
    try {
      if (subjectType === 'user' && scopeType === 'inbound') {
        await client.request('/inbound-users', { method: 'POST', body: JSON.stringify({ inbound_id: entry.id, user_id: subjectID, enabled: true }) })
      } else {
        const payload: any = { subject_type: subjectType, subject_id: subjectID, scope_type: scopeType, enabled: true }
        if (scopeType === 'server') payload.server_id = entry.server_id
        if (scopeType === 'inbound') payload.inbound_id = entry.id
        await client.request('/inbound-access-grants', { method: 'POST', body: JSON.stringify(payload) })
      }
      setSubjectID(0)
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '添加授权失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const removeBinding = async (binding: InboundUser) => {
    const user = userByID(data, binding.user_id)
    const ok = await dialogs.confirm({ title: '移除用户', message: `确认移除 ${user?.username || '该用户'} 对这个入口的使用权限？`, tone: 'danger', confirmText: '移除' })
    if (!ok) return
    try {
      await client.request(`/inbound-users/${binding.id}`, { method: 'DELETE' })
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '移除失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const removeGrant = async (grant: InboundAccessGrant) => {
    const ok = await dialogs.confirm({ title: '移除授权', message: `确认移除 ${accessSubjectLabel(data, grant)} 对 ${accessScopeLabel(data, grant)} 的使用权限？`, tone: 'danger', confirmText: '移除' })
    if (!ok) return
    try {
      await client.request(`/inbound-access-grants/${grant.id}`, { method: 'DELETE' })
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '移除失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const deleteEntry = async () => {
    const ok = await dialogs.confirm({ title: '删除入口协议', message: `确认删除 ${entry.name}？这个入口下的用户权限也会一起移除。`, tone: 'danger', confirmText: '删除' })
    if (!ok) return
    try {
      await client.request(`/inbounds/${entry.id}`, { method: 'DELETE' })
      onCancel()
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '删除失败', message: localizeErrorMessage(e.message || e) })
    }
  }
  const syncDNSEntry = async () => {
    if (dnsSyncing) return
    setDNSSyncing(true)
    setDNSFeedback('')
    try {
      const result = await client.request('/dns-sync', { method: 'POST', body: JSON.stringify({ inbound_id: entry.id }) }) as { success_count?: number; items?: Array<{ status: string; error?: string }> }
      const item = result.items?.[0]
      if (!result.success_count || item?.error) {
        throw new Error(item?.error || '同步失败')
      }
      setDNSFeedback(item?.status || '同步成功')
      await load()
    } catch (error: any) {
      await dialogs.alert({ title: '域名同步失败', message: localizeErrorMessage(error?.message || error) })
    } finally {
      setDNSSyncing(false)
    }
  }
  return <MotionDialogPanel onCancel={onCancel} className="entry-users-dialog">
      <header className="dialog-head">
        <div><h2 id="entry-users-title">入口用户</h2><p className="muted">{entry.name} · {labelProtocol(entry.protocol)} · {entry.listen_ip}:{entry.port}</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        {liveEntry.dns_sync_enabled && <div className={`access-note managed-entry-status${liveEntry.dns_sync_error ? ' warning' : ''}`}>
          <div><strong>DNS 解析</strong><span>{liveEntry.dns_domain}</span></div>
          <span>{dnsFeedback || liveEntry.dns_sync_error || liveEntry.dns_sync_status || '等待首次同步'}{liveEntry.dns_last_synced_at ? ` · ${formatTableTime(liveEntry.dns_last_synced_at)}` : ''}</span>
          <button className="ghost" onClick={syncDNSEntry} disabled={dnsSyncing}>{dnsSyncing ? '同步中...' : '立即同步'}</button>
        </div>}
        <div className="access-note"><strong>使用权限</strong><span>授权用户或用户组使用此入口。</span></div>
        {!effectiveIDs.size && <div className="access-note warning"><strong>未绑定用户</strong><span>可先下发，绑定后会自动替换占位凭据。</span></div>}
        {!multi && <div className="access-note warning"><strong>单用户协议</strong><span>此入口只能绑定一个用户。</span></div>}
        {!users.length ? <p className="muted">没有可选择的用户，或当前账号没有用户管理权限。</p> : <div className="entry-user-add entry-access-add">
          <Select variant="segmented" value={subjectType} onChange={e => { setSubjectType(e.target.value as AccessSubjectType); setSubjectID(0) }}>
            <option value="user">用户</option>
            <option value="group">用户组</option>
          </Select>
          <Select value={subjectID} onChange={e => setSubjectID(Number(e.target.value))} disabled={!multi && effectiveIDs.size > 0}>
            <option value={0}>选择{subjectType === 'group' ? '用户组' : '用户'}</option>
            {subjectType === 'user'
              ? (candidates as User[]).map(u => <option key={u.id} value={u.id}>{u.username}（{labelValue(u.status)}）</option>)
              : (candidates as UserGroup[]).map(g => <option key={g.id} value={g.id}>{g.name}</option>)}
          </Select>
          <Select variant="segmented" value={scopeType} onChange={e => setScopeType(e.target.value as AccessScopeType)} disabled={!multi && effectiveIDs.size > 0}>
            <option value="inbound">仅此入口</option>
            <option value="server">这台服务器全部入口</option>
            <option value="global">全部入口</option>
          </Select>
          <button onClick={add} disabled={!canAdd}>添加授权</button>
        </div>}
        <div className="entry-user-list">
          {!!grants.length && <div className="entry-access-section"><strong>范围 / 用户组授权</strong>{grants.map(grant => <div className="entry-user-row" key={`grant-${grant.id}`}>
            <div><strong>{accessSubjectLabel(data, grant)}</strong><span>{accessScopeLabel(data, grant)} · {grant.enabled === false ? '已禁用' : '已启用'}</span></div>
            <button className="ghost" onClick={() => removeGrant(grant)}>移除</button>
          </div>)}</div>}
          {bindings.length ? bindings.map(binding => {
            const user = userByID(data, binding.user_id)
            return <div className="entry-user-row" key={binding.id}>
              <div><strong>{user?.username || `用户 ${binding.user_id}`}</strong><span>{user ? `${labelValue(user.status)} · ${userLimitSummary(data, user)} · 已用 ${formatBytes(user.traffic_used_bytes || 0)}` : '用户信息不可见'}</span></div>
              <button className="ghost" onClick={() => removeBinding(binding)}>移除</button>
            </div>
          }) : !grants.length ? <div className="empty small">暂无用户能使用这个入口；下发时会使用临时占位凭据。</div> : null}
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost danger-text" onClick={deleteEntry}>删除入口</button><button className="ghost" onClick={onCancel}>关闭</button></footer>
  </MotionDialogPanel>
}

function ServerBranchTree({ data, server, onManageEntry }: { data: any; server: Server; onManageEntry?: (entry: Inbound) => void }) {
  const entries: Inbound[] = (data.inbounds || []).filter((x: Inbound) => x.server_id === server.id)
  const forwards: PortForward[] = (data.port_forwards || []).filter((x: PortForward) => x.source_server_id === server.id)
  const tunnels: Tunnel[] = (data.tunnels || []).filter((x: Tunnel) => x.source_server_id === server.id)
  return <div className="branch-tree">
    <div className="branch-root"><h3>{server.name}</h3><p>{serverOSLabel(server)} · {serverDefaultEntryAddress(server) || '无公网 IP'}</p></div>
    <MotionList className="tree-lane" stagger={0.03}>
      <MotionCard className="tree-node root-node" whileHover={{}}><strong>主机</strong><span>{labelValue(server.status || 'unknown')}</span></MotionCard>
      {entries.length ? entries.map(x => <div className="tree-branch" key={`in-${x.id}`}>
        <MotionCard className="tree-node entry-node" whileHover={{}}><strong>{x.name}</strong><span>{labelProtocol(x.protocol)} · {formatHostPort(inboundEntryAddress(data, x), x.port)}</span><small>{entryAddressModeLabel(x.entry_ip_mode || 'auto', server)} · {inboundAccessSummary(data, x)}{x.dns_sync_enabled ? ` · DNS ${x.dns_sync_error ? '失败' : (x.dns_sync_status || '待同步')}` : ''}</small>{onManageEntry && <button className="tiny-button" onClick={() => onManageEntry(x)}>用户</button>}</MotionCard>
        <MotionCard className="tree-node exit-node" whileHover={{}}><strong>出口</strong><span>Direct / 路径出口</span></MotionCard>
      </div>) : <MotionCard className="tree-node muted-node" whileHover={{}}>这台主机还没有入口节点。把“入口节点”工具拖到链路图里创建。</MotionCard>}
      {!!forwards.length && <MotionCard className="branch-note" whileHover={{}}>端口转发：{forwards.map(x => `${x.listen_port}->${x.target_port}`).join('、')}</MotionCard>}
      {!!tunnels.length && <MotionCard className="branch-note" whileHover={{}}>隧道：{tunnels.map(x => `${labelValue(x.type)}:${x.listen_port || '-'}`).join('、')}</MotionCard>}
    </MotionList>
  </div>
}

type GraphTransportKind = 'singbox' | 'port_forward' | 'wireguard' | 'ssh'
type GraphTransportEdgeData = {
  entity?: GraphEntity
  kind: GraphTransportKind
  title: string
  detail?: string
  unhealthy?: boolean
}
type GraphServerRole = {
  isRoot: boolean
  processingPaths: number
  transparentPaths: number
  relayPaths: number
  /** Paths rooted at a different entry server that traverse this server. The
   *  canvas only renders one root at a time, so without this the operator cannot
   *  see that a change here also affects branches they are not looking at. */
  foreignPaths: string[]
}
type GraphEntryPathInfo = {
  pathCount: number
  localProcessing: number
  remoteProcessing: number
}
type GraphEntryDetails = {
  protocol: string
  address: string
  probe: string
  access: string
  dns?: string
}

function ProxyGraphEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  markerEnd,
  style,
  data,
  selected,
}: EdgeProps<GraphTransportEdgeData>) {
  const [path, labelX, labelY] = getStraightPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
  })
  const kind = data?.kind || 'singbox'
  return <>
    <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style} interactionWidth={22} />
    <EdgeLabelRenderer>
      <div
        className={`graph-edge-label graph-edge-label-${kind}${data?.unhealthy ? ' is-unhealthy' : ''}${selected ? ' is-selected' : ''}`}
        style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
      >
        <span className="graph-edge-label-icon">
          {kind === 'port_forward' ? <ArrowLeftRight size={12} /> : kind === 'wireguard' ? <Shield size={12} /> : kind === 'ssh' ? <Lock size={12} /> : <Workflow size={12} />}
        </span>
        <span className="graph-edge-label-copy">
          <strong>{data?.title || '链式代理'}</strong>
          {data?.detail && <small>{data.detail}</small>}
        </span>
        <ChevronDown className="graph-edge-direction" size={12} />
      </div>
    </EdgeLabelRenderer>
  </>
}

const proxyGraphEdgeTypes = { proxyTransport: ProxyGraphEdge }

function ProxyGraphNodeRenderer({ data }: { data: { label?: React.ReactNode } }) {
  return <>{data.label || null}</>
}

const proxyGraphNodeTypes = { proxyGraphNode: ProxyGraphNodeRenderer }

function ProxyGraphLegend() {
  const [open, setOpen] = useState(false)
  return <details className="proxy-graph-legend" open={open}>
    <summary onClick={event => { event.preventDefault(); setOpen(value => !value) }}><Info size={13} />图例</summary>
    <div className="proxy-graph-legend-body">
      <div className="proxy-graph-legend-section">
        <strong>节点角色</strong>
        <span><i className="legend-node legend-entry" /><em>代理入口</em></span>
        <span><i className="legend-node legend-gateway" /><em>一级服务器</em></span>
        <span><i className="legend-node legend-relay" /><em>链路服务器</em></span>
        <span><i className="legend-node legend-imported" /><em>第三方代理</em></span>
      </div>
      <div className="proxy-graph-legend-section">
        <strong>传递方式</strong>
        <span><i className="legend-line legend-chain" /><em>sing-box 链式代理</em></span>
        <span><i className="legend-line legend-forward" /><em>端口转发</em></span>
        <span><i className="legend-line legend-wg" /><em>WireGuard 组网</em></span>
        <span><i className="legend-line legend-ssh" /><em>SSH 隧道</em></span>
      </div>
    </div>
  </details>
}

function emptyGraphServerRole(isRoot = false): GraphServerRole {
  return { isRoot, processingPaths: 0, transparentPaths: 0, relayPaths: 0, foreignPaths: [] }
}

// Names of the enabled paths that traverse each server but are rooted at another
// entry server. Shared Shadowsocks services and shared tunnels are keyed by
// target server, not by root, so this is exactly the coupling a single-root view
// hides.
function foreignProxyPathsByServer(data: any, rootServerID: number) {
  const inboundByID = new Map<number, Inbound>(((data.inbounds || []) as Inbound[]).map(x => [x.id, x]))
  const stepsByPath = new Map<number, ProxyPathStep[]>()
  ;((data.proxy_path_steps || []) as ProxyPathStep[]).forEach(step => {
    const list = stepsByPath.get(step.path_id) || []
    list.push(step)
    stepsByPath.set(step.path_id, list)
  })
  const out = new Map<number, string[]>()
  ;((data.proxy_paths || []) as ProxyPath[]).forEach(path => {
    if (path.enabled === false) return
    const root = inboundByID.get(path.inbound_id)
    if (!root || root.server_id === rootServerID) return
    const label = path.name || `路径 ${path.id}`
    const touched = new Set<number>([root.server_id])
    ;(stepsByPath.get(path.id) || []).forEach(step => {
      const serverID = graphStepServerID(step, inboundByID)
      if (serverID) touched.add(serverID)
    })
    touched.forEach(serverID => {
      const list = out.get(serverID) || []
      if (!list.includes(label)) list.push(label)
      out.set(serverID, list)
    })
  })
  return out
}

function graphStepServerID(step: ProxyPathStep, inboundByID: Map<number, Inbound>) {
  if (step.node_type !== 'server_inbound') return 0
  if (step.server_id) return step.server_id
  return step.inbound_id ? inboundByID.get(step.inbound_id)?.server_id || 0 : 0
}

function proxyPathTransportPresentation(step: Pick<ProxyPathStep, 'transport_mode' | 'config_json' | 'inbound_id'>) {
  const mode = step.transport_mode || 'singbox'
  if (mode === 'port_forward') return { kind: 'port_forward' as const, title: '端口转发' }
  if (mode === 'tunnel') {
    const tunnelType = String((parseConfig(step.config_json || '{}') || {}).type || 'ssh').toLowerCase()
    if (tunnelType === 'wireguard' || tunnelType === 'wg') return { kind: 'wireguard' as const, title: 'WireGuard 组网' }
    return { kind: 'ssh' as const, title: 'SSH 隧道' }
  }
	if (step.inbound_id) return { kind: 'singbox' as const, title: '已有入口链式代理' }
  const method = String((parseConfig(step.config_json || '{}') || {}).chain_method || '2022-blake3-aes-128-gcm')
  const methodLabel = proxyPathChainMethods.find(item => item.value === method)?.label || method
  return { kind: 'singbox' as const, title: `共享 ${methodLabel} 链式代理` }
}

function graphTransportColor(kind: GraphTransportKind, unhealthy = false) {
  if (unhealthy) return 'var(--graph-unhealthy)'
  if (kind === 'port_forward') return 'var(--graph-forward)'
  if (kind === 'wireguard') return 'var(--graph-wireguard)'
  if (kind === 'ssh') return 'var(--graph-ssh)'
  return 'var(--graph-chain)'
}

function graphTransportEdge(
  id: string,
  source: string,
  target: string,
  data: GraphTransportEdgeData,
  options: Partial<Edge> = {},
): Edge {
  const color = graphTransportColor(data.kind, data.unhealthy)
  return {
    id,
    source,
    target,
    type: 'proxyTransport',
    animated: !data.unhealthy,
    className: `proxy-transport-edge transport-${data.kind}${data.unhealthy ? ' unhealthy-edge' : ''}`,
    style: { stroke: color, strokeWidth: 2.8 },
    markerEnd: { type: MarkerType.ArrowClosed, color, width: 18, height: 18 },
    data,
    ...options,
  }
}

function editableProxyFlow(data: any, positions: Record<string, { x: number; y: number }>, rootServerId = 0, canvasImportedIDs: number[] = [], canvasServerInstances: CanvasServerInstance[] = []) {
  const nodes: Node[] = []
  const edges: Edge[] = []
  const entries: Inbound[] = data.inbounds || []
  const rootID = rootServerId || (data.servers || [])[0]?.id || 0
  const visibleServerIds = rootID ? reachableServerIds(data, rootID) : new Set<number>()
  const inboundByID = new Map<number, Inbound>(entries.map(x => [x.id, x]))
  const visibleRootEntryIDs = new Set(entries.filter(x => x.server_id === rootID).map(x => x.id))
  const visiblePaths: ProxyPath[] = (data.proxy_paths || []).filter((path: ProxyPath) => {
    const root = inboundByID.get(path.inbound_id)
    return path.enabled !== false && root?.server_id === rootID
  })
  const stepsByPath = new Map<number, ProxyPathStep[]>()
  ;(data.proxy_path_steps || []).forEach((step: ProxyPathStep) => {
    const list = stepsByPath.get(step.path_id) || []
    list.push(step)
    stepsByPath.set(step.path_id, list)
  })
  const serverRoles = new Map<number, GraphServerRole>()
  const entryPathInfo = new Map<number, GraphEntryPathInfo>()
  const foreignPaths = foreignProxyPathsByServer(data, rootID)
  const roleFor = (serverID: number, isRoot = false) => {
    const current = serverRoles.get(serverID) || emptyGraphServerRole(isRoot)
    if (isRoot) current.isRoot = true
    current.foreignPaths = foreignPaths.get(serverID) || []
    serverRoles.set(serverID, current)
    return current
  }
  // A server can be on the canvas without joining any visible path and still be
  // used by branches rooted elsewhere, so read through this instead of falling
  // back to a blank role.
  const displayRole = (serverID: number, isRoot = false) => {
    const role = serverRoles.get(serverID) || emptyGraphServerRole(isRoot)
    return { ...role, foreignPaths: foreignPaths.get(serverID) || role.foreignPaths }
  }
  roleFor(rootID, true)
  entries.filter(entry => entry.server_id === rootID && entry.enabled !== false).forEach(entry => {
    const paths = visiblePaths.filter(path => path.inbound_id === entry.id)
    const info: GraphEntryPathInfo = { pathCount: paths.length, localProcessing: 0, remoteProcessing: 0 }
    if (!paths.length) {
      roleFor(rootID, true).processingPaths++
      info.localProcessing = 1
    }
	  paths.forEach(path => {
	    const pathSteps = (stepsByPath.get(path.id) || []).slice().sort((a, b) => (a.position - b.position) || (a.id - b.id))
	    const hasRemoteProcessor = pathSteps.some(step => step.transport_mode === 'port_forward' && step.processing_role && graphStepServerID(step, inboundByID))
      if (hasRemoteProcessor) {
        roleFor(rootID, true).transparentPaths++
        info.remoteProcessing++
      } else {
        roleFor(rootID, true).processingPaths++
        info.localProcessing++
      }
	    pathSteps.forEach(step => {
	      const serverID = graphStepServerID(step, inboundByID)
	      if (!serverID) return
	      const role = roleFor(serverID)
	      if (step.transport_mode === 'port_forward' && !step.processing_role) role.relayPaths++
	      else role.processingPaths++
	    })
    })
    entryPathInfo.set(entry.id, info)
  })
  const importedIDs = new Set<number>()
  const continuationByNode = new Map<string, GraphPathHandle[]>()
  const addContinuation = (nodeID: string, step: ProxyPathStep, path: ProxyPath) => {
    const list = continuationByNode.get(nodeID) || []
    list.push({ step_id: step.id, label: `继续 · 路径 ${path.id}`, title: `${path.name || `路径 ${path.id}`} / 第 ${step.position} 跳后继续连接` })
    continuationByNode.set(nodeID, list)
  }
  visiblePaths.forEach(path => {
    ;(stepsByPath.get(path.id) || []).forEach(step => {
      const nodeID = proxyPathStepNodeID(step)
      if (nodeID) addContinuation(nodeID, step, path)
    })
  })
  canvasImportedIDs.forEach(id => importedIDs.add(id))
  const visibleEntries = entries.filter(x => visibleRootEntryIDs.has(x.id))
  const visibleServers = (data.servers || []).filter((s: Server) => visibleServerIds.has(s.id))
  const visibleEntryCountByServer = new Map<number, number>()
  visibleEntries.forEach(x => visibleEntryCountByServer.set(x.server_id, (visibleEntryCountByServer.get(x.server_id) || 0) + 1))
  const serverPositions = new Map<number, GraphPosition>()
  const serverWidths = new Map<number, number>()
  const serverEntryCounts = new Map<number, number>()
  const serverEntryIndexes = new Map<number, number>()
  visibleServers.forEach((s: Server, i: number) => {
    const id = `server-${s.id}`
    const position = positions[id] || defaultServerGraphPosition(i)
    serverPositions.set(s.id, position)
    const serverEntries = entries.filter(x => x.server_id === s.id && x.enabled !== false).sort((a, b) => (a.port - b.port) || (a.id - b.id))
    serverEntryCounts.set(s.id, Math.max(1, serverEntries.length))
    serverEntries.forEach((entry, index) => serverEntryIndexes.set(entry.id, index))
    const serverWidth = graphServerNodeWidth(serverEntries.length)
    serverWidths.set(s.id, serverWidth)
    nodes.push({ id, className: 'graph-node server-graph-node', position, style: { width: serverWidth }, data: { entity: { type: 'server', id: s.id, label: s.name || `服务器 ${s.id}` } as GraphEntity, label: <GraphNode kind={s.id === rootID ? '一级服务器' : '服务器'} title={s.name} meta={`${labelValue(s.status || 'unknown')} · ${serverDefaultEntryAddress(s) || '无公网 IP'}`} entryHandles={serverEntries.map(x => ({ id: x.id, label: `${labelProtocol(x.protocol)}:${x.port}`, title: x.name || `入口 ${x.id}` }))} pathHandles={continuationByNode.get(id) || []} role={displayRole(s.id, s.id === rootID)} status={s.status} ipv4={s.public_ipv4 || '未检测'} cpu={Math.round(s.cpu_usage_percent || 0)} memory={s.memory_total_bytes ? Math.round((s.memory_used_bytes / s.memory_total_bytes) * 100) : 0} /> } })
  })
  canvasServerInstances.forEach((instance, index) => {
    const server = (data.servers || []).find((item: Server) => item.id === instance.server_id) as Server | undefined
    if (!server) return
    const id = canvasServerNodeID(instance)
    const serverEntries = entries.filter(x => x.server_id === server.id && x.enabled !== false).sort((a, b) => (a.port - b.port) || (a.id - b.id))
    const serverWidth = graphServerNodeWidth(serverEntries.length)
    const position = positions[id] || defaultServerGraphPosition(visibleServers.length + index)
    nodes.push({ id, className: 'graph-node server-graph-node canvas-server-node', position, style: { width: serverWidth }, data: { entity: { type: 'server', id: server.id, label: server.name || `服务器 ${server.id}`, node_id: id } as GraphEntity, label: <GraphNode kind="服务器" title={server.name} meta={`${labelValue(server.status || 'unknown')} · ${serverDefaultEntryAddress(server) || '无公网 IP'}`} entryHandles={serverEntries.map(x => ({ id: x.id, label: `${labelProtocol(x.protocol)}:${x.port}`, title: x.name || `入口 ${x.id}` }))} role={displayRole(server.id)} status={server.status} ipv4={server.public_ipv4 || '未检测'} cpu={Math.round(server.cpu_usage_percent || 0)} memory={server.memory_total_bytes ? Math.round((server.memory_used_bytes / server.memory_total_bytes) * 100) : 0} /> } })
  })
  const entryIndexesByServer = new Map<number, number>()
  visibleEntries.forEach((x: Inbound, i: number) => {
    const id = `entry-${x.id}`
    const fallbackIndex = entryIndexesByServer.get(x.server_id) || 0
    entryIndexesByServer.set(x.server_id, fallbackIndex + 1)
    const entryIndex = serverEntryIndexes.get(x.id) ?? fallbackIndex
    const serverPosition = serverPositions.get(x.server_id) || defaultServerGraphPosition(i)
    const entryCount = serverEntryCounts.get(x.server_id) || visibleEntryCountByServer.get(x.server_id) || 1
    const position = positions[id] || defaultEntryGraphPosition(serverPosition, entryIndex, entryCount, serverWidths.get(x.server_id) || graphServerNodeWidth(entryCount))
    const probe = latestInboundProbeSummary(data, x.id)
    nodes.push({ id, className: `graph-node entry-graph-node probe-${probe.tone}`, position, style: { width: GRAPH_ENTRY_NODE_WIDTH }, data: { entity: { type: 'entry', id: x.id, label: x.name || `入口 ${x.id}` } as GraphEntity, label: <GraphNode kind="一级入口" title={x.name} meta="" pathHandles={continuationByNode.get(id) || []} entryPathInfo={entryPathInfo.get(x.id)} entryDetails={{ protocol: labelProtocol(x.protocol), address: formatHostPort(inboundEntryAddress(data, x), x.port), probe: probe.label, access: inboundAccessSummary(data, x), dns: x.dns_sync_enabled ? `DNS ${x.dns_sync_error ? '同步失败' : '已同步'}` : '' }} /> } })
    edges.push({
      id: `belongs-${x.id}`,
      source: id,
      sourceHandle: 'source-bottom',
      target: `server-${x.server_id}`,
      targetHandle: serverEntryTargetHandleID(x.id),
      label: '所属主机',
      type: 'straight',
      animated: false,
      className: 'belongs-edge auxiliary-edge',
      style: { stroke: 'rgba(71, 85, 105, 0.42)', strokeWidth: 1.8, strokeDasharray: '5 5', pointerEvents: 'none' },
      labelStyle: { fill: 'var(--text-strong)', fontSize: 11, fontWeight: 700 },
      labelBgStyle: { fill: 'var(--surface-solid)', fillOpacity: 0.94 },
      labelBgPadding: [4, 2],
      data: { auxiliary: true },
    })
  })
  ;(data.external_outbounds || []).filter((x: ExternalOutbound) => importedIDs.has(x.id)).forEach((x: ExternalOutbound, i: number) => {
    const id = `imported-${x.id}`
    const position = positions[id] || defaultImportedGraphPosition(i)
    nodes.push({ id, className: 'graph-node imported-graph-node', position, style: { width: GRAPH_ENTRY_NODE_WIDTH }, data: { entity: { type: 'imported', id: x.id, label: x.name || `导入节点 ${x.id}` } as GraphEntity, label: <GraphNode kind="导入节点" title={x.name} meta={`${labelProtocol(x.protocol)} · ${formatHostPort(x.target_address, x.target_port)} · ${x.expose_to_users ? '可订阅' : '订阅隐藏'}`} pathHandles={continuationByNode.get(id) || []} /> } })
  })
  let pathStepNodeIndex = 0
  visiblePaths.forEach(path => {
    const pathSteps = (stepsByPath.get(path.id) || []).slice().sort((a, b) => (a.position - b.position) || (a.id - b.id))
    pathSteps.forEach(step => {
      const id = proxyPathStepNodeID(step)
      if (!id) return
      const stepIndex = pathStepNodeIndex++
      const position = positions[id] || defaultServerGraphPosition(visibleServers.length + canvasServerInstances.length + stepIndex)
      const entity = { type: 'proxy-path-step', id: step.id, path_id: path.id, label: `${path.name || `代理路径 ${path.id}`} / 第 ${step.position} 跳` } as GraphEntity
      if (step.node_type === 'imported' && step.external_outbound_id) {
        const imported = (data.external_outbounds || []).find((item: ExternalOutbound) => item.id === step.external_outbound_id) as ExternalOutbound | undefined
        if (!imported) return
        nodes.push({ id, className: 'graph-node imported-graph-node proxy-path-instance-node', position, style: { width: GRAPH_ENTRY_NODE_WIDTH }, data: { entity, label: <GraphNode kind="导入节点" title={imported.name} meta={`${labelProtocol(imported.protocol)} · ${formatHostPort(imported.target_address, imported.target_port)} · 路径 ${path.id}`} pathHandles={continuationByNode.get(id) || []} /> } })
        return
      }
      const serverID = graphStepServerID(step, inboundByID)
      const server = (data.servers || []).find((item: Server) => item.id === serverID) as Server | undefined
      if (!server) return
      nodes.push({ id, className: 'graph-node server-graph-node proxy-path-instance-node', position, style: { width: GRAPH_ENTRY_NODE_WIDTH }, data: { entity, label: <GraphNode kind="服务器" title={server.name} meta={`${labelValue(server.status || 'unknown')} · 路径 ${path.id}`} pathHandles={continuationByNode.get(id) || []} role={displayRole(server.id)} status={server.status} ipv4={server.public_ipv4 || '未检测'} cpu={Math.round(server.cpu_usage_percent || 0)} memory={server.memory_total_bytes ? Math.round((server.memory_used_bytes / server.memory_total_bytes) * 100) : 0} /> } })
    })
  })
  visiblePaths.forEach(path => {
    const root = inboundByID.get(path.inbound_id)
    if (!root) return
    const pathSteps = (stepsByPath.get(path.id) || []).slice().sort((a, b) => (a.position - b.position) || (a.id - b.id))
    let source = `server-${root.server_id}`
    let sourceHandle: string | undefined = serverEntryHandleID(root.id)
    pathSteps.forEach((step, index) => {
      const target = proxyPathStepNodeID(step)
      if (!target) return
      const transport = proxyPathTransportPresentation(step)
      edges.push(graphTransportEdge(
        `proxy-path-${path.id}-${step.id}`,
        source,
        target,
        {
          entity: { type: 'proxy-path-step', id: step.id, path_id: path.id, label: `${path.name || `代理路径 ${path.id}`} / 第 ${step.position} 跳` },
          kind: transport.kind,
          title: transport.title,
          detail: index === 0 ? (path.name || root.name || `路径 ${path.id}`) : `第 ${step.position} 跳`,
        },
        { sourceHandle, targetHandle: 'target-top', animated: path.enabled !== false },
      ))
      source = target
      sourceHandle = pathStepHandleID(step.id)
    })
  })
  ;(data.port_forwards || []).forEach((x: PortForward) => {
    if (!visibleServerIds.has(x.source_server_id) || !visibleServerIds.has(x.target_server_id)) return
    const probe = latestForwardProbe(data, x.id)
    const probeLabel = !probe ? '待探测' : probe.available ? `${probe.latency_ms}ms` : '异常'
    edges.push(graphTransportEdge(`pf-${x.id}`, `server-${x.source_server_id}`, `server-${x.target_server_id}`, {
      entity: { type: 'port-forward', id: x.id, label: x.name || `端口转发 ${x.id}` },
      kind: 'port_forward',
      title: '端口转发',
      detail: `${x.listen_port} → ${x.target_port} · ${probeLabel}`,
      unhealthy: Boolean(probe && !probe.available),
    }, { animated: x.enabled !== false, targetHandle: 'target-top' }))
  })
  ;(data.tunnels || []).forEach((x: Tunnel) => {
    if (!visibleServerIds.has(x.source_server_id) || !visibleServerIds.has(x.target_server_id)) return
    const kind: GraphTransportKind = x.type === 'wireguard' ? 'wireguard' : 'ssh'
    edges.push(graphTransportEdge(`tu-${x.id}`, `server-${x.source_server_id}`, `server-${x.target_server_id}`, {
      entity: { type: 'tunnel', id: x.id, label: x.name || `隧道 ${x.id}` },
      kind,
      title: x.type === 'wireguard' ? 'WireGuard 组网' : 'SSH 隧道',
      detail: x.listen_port ? `本地端口 ${x.listen_port}` : (x.target_endpoint || x.name),
    }, { animated: x.enabled !== false, targetHandle: 'target-top' }))
  })
  return { nodes: nodes.map(node => ({ ...node, type: 'proxyGraphNode' })), edges }
}

// Label of the node that feeds this step, so the transport preview reads as an
// actual hop instead of falling back to the current canvas root.
// Every enabled path that traverses a server or uses an inbound, regardless of
// which entry server it is rooted at. Used to spell out the blast radius of a
// delete that the single-root canvas would otherwise hide.
function proxyPathsTouchingEntity(data: any, entity: GraphEntity) {
  const inboundByID = new Map<number, Inbound>(((data.inbounds || []) as Inbound[]).map(x => [x.id, x]))
  const stepsByPath = new Map<number, ProxyPathStep[]>()
  ;((data.proxy_path_steps || []) as ProxyPathStep[]).forEach(step => {
    const list = stepsByPath.get(step.path_id) || []
    list.push(step)
    stepsByPath.set(step.path_id, list)
  })
  const out: string[] = []
  ;((data.proxy_paths || []) as ProxyPath[]).forEach(path => {
    if (path.enabled === false) return
    const root = inboundByID.get(path.inbound_id)
    if (!root) return
    const steps = stepsByPath.get(path.id) || []
    const usesEntity = entity.type === 'entry'
      ? root.id === entity.id || steps.some(step => step.inbound_id === entity.id)
      : root.server_id === entity.id || steps.some(step => graphStepServerID(step, inboundByID) === entity.id)
    if (!usesEntity) return
    const label = path.name || `路径 ${path.id}`
    if (!out.includes(label)) out.push(label)
  })
  return out
}

function proxyPathStepUpstreamLabel(data: any, step: ProxyPathStep) {
  const earlier = ((data.proxy_path_steps || []) as ProxyPathStep[])
    .filter(item => item.path_id === step.path_id && item.position < step.position)
    .sort((a, b) => (a.position - b.position) || (a.id - b.id))
  const previous = earlier[earlier.length - 1]
  if (previous) {
    if (previous.node_type === 'imported' && previous.external_outbound_id) {
      const node = ((data.external_outbounds || []) as ExternalOutbound[]).find(item => item.id === previous.external_outbound_id)
      return node?.name || `导入节点 ${previous.external_outbound_id}`
    }
    const serverID = previous.server_id || ((data.inbounds || []) as Inbound[]).find(item => item.id === previous.inbound_id)?.server_id
    const server = ((data.servers || []) as Server[]).find(item => item.id === serverID)
    if (server) return server.name || `服务器 ${server.id}`
  }
  const path = ((data.proxy_paths || []) as ProxyPath[]).find(item => item.id === step.path_id)
  const root = path ? ((data.inbounds || []) as Inbound[]).find(item => item.id === path.inbound_id) : undefined
  const rootServer = root ? ((data.servers || []) as Server[]).find(item => item.id === root.server_id) : undefined
  return rootServer?.name || root?.name || '入口'
}

function proxyPathStepNodeID(step: ProxyPathStep) {
  if (step.node_type === 'imported' && step.external_outbound_id) return `proxy-imported-step-${step.id}`
  if (step.node_type === 'server_inbound' && (step.inbound_id || step.server_id)) return `proxy-server-step-${step.id}`
  return ''
}

function canvasServerNodeID(instance: CanvasServerInstance) {
  return `canvas-server-${instance.instance_id}`
}

function reachableServerIds(data: any, rootServerId: number) {
  const seen = new Set<number>([rootServerId])
  let changed = true
  while (changed) {
    changed = false
	  ;[...(data.port_forwards || []), ...(data.tunnels || [])].forEach((x: any) => {
	    const source = x.source_server_id
	    const target = x.target_server_id
	    if (x.enabled === false || !source || !target) return
	    if (seen.has(source) && !seen.has(target)) { seen.add(target); changed = true }
	  })
	}
  return seen
}

function serverEntryOptionLabel(data: any, server: Server) {
  const entries: Inbound[] = (data.inbounds || []).filter((x: Inbound) => x.server_id === server.id && x.enabled !== false)
  const protocolsText = entries.length ? entries.map(x => `${labelProtocol(x.protocol)}:${x.port}`).join('、') : '暂无入口协议'
  const address = serverDefaultEntryAddress(server)
  return `${server.name || `服务器 ${server.id}`} / ${address || '入口地址待检测'} / ${protocolsText}`
}

function serverEntryHandleID(inboundID: number) {
  return `server-entry-${inboundID}`
}

function serverEntryTargetHandleID(inboundID: number) {
  return `server-entry-target-${inboundID}`
}

function inboundIDFromServerHandle(handle?: string | null) {
  const match = /^server-entry-(\d+)$/.exec(handle || '')
  return match ? Number(match[1]) : 0
}

function pathStepHandleID(stepID: number) {
  return `path-step-${stepID}`
}

function pathStepIDFromHandle(handle?: string | null) {
  const match = /^path-step-(\d+)$/.exec(handle || '')
  return match ? Number(match[1]) : 0
}

type GraphEntryHandle = { id: number; label: string; title: string }
type GraphPathHandle = { step_id: number; label: string; title: string }

function GraphNode({ 
  kind, 
  title, 
  meta, 
  entryHandles = [], 
  pathHandles = [],
  role,
  entryPathInfo,
  entryDetails,
  status,
  ipv4,
  cpu,
  memory
}: { 
  kind: string; 
  title: string; 
  meta: string; 
  entryHandles?: GraphEntryHandle[]; 
  pathHandles?: GraphPathHandle[];
  role?: GraphServerRole;
  entryPathInfo?: GraphEntryPathInfo;
  entryDetails?: GraphEntryDetails;
  status?: string;
  ipv4?: string;
  cpu?: number;
  memory?: number;
}) {
  const isOnline = status === 'online'
  const hasSharedPathHandle = entryHandles.length > 1 || pathHandles.length > 1
  // One handle, two outcomes: append to the paths that already end here, or start
  // one path per entry when none does. Label whichever one will actually happen.
  const sharedAppendsToPaths = pathHandles.length > 1
  const sharedHandleLabel = sharedAppendsToPaths ? '全部路径' : '全部入口'
  const sharedHandleTitle = sharedAppendsToPaths
    ? `向经过这里的 ${pathHandles.length} 条路径各追加同一个下一跳`
    : `为这台服务器的 ${entryHandles.length} 个入口各新建一条链路`
  const metaParts = meta.split(' · ')
  const subtitle1 = metaParts[0] || ''
  const subtitle2 = metaParts[1] || ''
  const subtitle3 = metaParts[2] || ''
  const subtitle4 = metaParts.slice(3).join(' · ')
  const isEntry = kind === '一级入口'
  const isImported = kind === '导入节点'
  const isServer = !isEntry && !isImported
  const variant = isEntry
    ? 'entry'
    : isImported
      ? 'imported'
      : role?.isRoot
        ? 'gateway'
        : (role?.processingPaths || 0) > 0 || (role?.relayPaths || 0) > 0
            ? 'relay'
            : 'server'
  const headerLabel = isEntry
    ? '代理入口'
    : isImported
      ? '第三方代理'
      : role?.isRoot
        ? '一级服务器'
        : (role?.processingPaths || 0) > 0 || (role?.relayPaths || 0) > 0
            ? '链路服务器'
            : '可控服务器'
  const Icon = isEntry
    ? <Globe size={15} />
    : isImported
      ? <LinkIcon size={15} />
      : role?.isRoot
          ? <ServerIcon size={15} />
          : <Workflow size={15} />
  const entryProtocol = entryDetails?.protocol || subtitle1
  const entryAddress = entryDetails?.address || subtitle2
  const entryProbe = entryDetails?.probe || subtitle3
  const entryAccess = entryDetails?.access || subtitle4
  const entryProbeTone = /正常|可用|成功|在线/.test(entryProbe) ? 'ok' : /异常|失败|不可用/.test(entryProbe) ? 'danger' : 'neutral'
  return (
    <div className={`rf-node-custom graph-card-${variant}`}>
      {/* Handles */}
      <Handle id="target-top" className="connect-handle connect-target connect-target-top" type="target" position={Position.Top} />
      <Handle id="target-left" className="connect-handle connect-target connect-target-left" type="target" position={Position.Left} />
      <Handle id="target-right" className="connect-handle connect-target connect-target-right" type="target" position={Position.Right} />
      
      {entryHandles.length ? entryHandles.map((entry, index) => {
        const left = graphEntryHandleLeft(index, entryHandles.length, hasSharedPathHandle)
        return (
          <React.Fragment key={entry.id}>
            <span className="server-entry-axis-line" style={{ left }} />
            <Handle id={serverEntryTargetHandleID(entry.id)} className="connect-handle connect-target server-entry-target-handle" type="target" position={Position.Top} style={{ left }} />
            <Handle id={serverEntryHandleID(entry.id)} className="connect-handle connect-source server-entry-source-handle" type="source" position={Position.Bottom} style={{ left }} />
            <span className="server-entry-source-label" style={{ left }} title={`${entry.title} / ${entry.label}`}>{entry.label}</span>
          </React.Fragment>
        )
      }) : !pathHandles.length ? <Handle id="source-bottom" className="connect-handle connect-source connect-source-bottom" type="source" position={Position.Bottom} /> : null}

      {hasSharedPathHandle && <>
        {/* The same handle appends to every path that terminates here, or, when no
            path does yet, creates one new path per entry. Name the actual effect. */}
        <Handle id="server-shared" className="connect-handle connect-source server-shared-source-handle" type="source" position={Position.Bottom} title={sharedHandleTitle} />
        <span className="server-shared-source-label">{sharedHandleLabel}</span>
      </>}
      
      {pathHandles.map((path, index) => {
        const left = graphPathHandleLeft(index, pathHandles.length)
        return (
          <React.Fragment key={path.step_id}>
            <Handle id={pathStepHandleID(path.step_id)} className="connect-handle connect-source path-step-source-handle" type="source" position={Position.Bottom} style={{ left }} />
            <span className="path-step-source-label" style={{ left }} title={path.title}>{path.label}</span>
          </React.Fragment>
        )
      })}

      {/* Header */}
      <div className="rf-node-header">
        <span className="rf-node-kind-icon">{Icon}</span>
        <span className="rf-node-heading">
          <small>{headerLabel}</small>
          <strong>{title || headerLabel}</strong>
        </span>
        {isServer
          ? <span className={`rf-node-status ${isOnline ? 'online' : 'offline'}`}><i />{isOnline ? '在线' : '离线'}</span>
          : <span className="rf-node-protocol">{isEntry ? entryProtocol : subtitle1 || '代理'}</span>}
      </div>

      {/* Body */}
      <div className="rf-node-body">
        {isServer ? (
          <>
            <div className="rf-node-detail"><span>公网地址</span><code>{ipv4 || subtitle2 || '未检测'}</code></div>
            {isOnline && <div className="rf-node-metrics"><span>CPU {cpu || 0}%</span><span>内存 {memory || 0}%</span></div>}
            <div className="graph-role-chips">
              {role?.isRoot && <span className="role-chip role-gateway"><ServerIcon size={10} />一级接入</span>}
              {(role?.foreignPaths?.length || 0) > 0 && <span
                className="role-chip role-foreign"
                title={`来自其他入口服务器的链路也经过这里：\n${role!.foreignPaths.join('\n')}`}
              ><Workflow size={10} />其他入口 {role!.foreignPaths.length} 条</span>}
              {(role?.processingPaths || 0) > 0 && <span className="role-chip role-processing"><Shield size={10} />在此处理加解密{role!.processingPaths > 1 ? ` ×${role!.processingPaths}` : ''}</span>}
              {(role?.transparentPaths || 0) > 0 && <span className="role-chip role-transparent"><ArrowLeftRight size={10} />透明传递{role!.transparentPaths > 1 ? ` ×${role!.transparentPaths}` : ''}</span>}
              {(role?.relayPaths || 0) > 0 && <span className="role-chip role-relay"><Workflow size={10} />中继{role!.relayPaths > 1 ? ` ×${role!.relayPaths}` : ''}</span>}
            </div>
          </>
        ) : isImported ? (
          <>
            <div className="rf-node-detail"><span>代理地址</span><code>{subtitle2 || '未配置'}</code></div>
            <div className="rf-node-footnote"><LinkIcon size={10} />{subtitle3 || '仅作为链路出口'}</div>
          </>
        ) : (
          <>
            <div className="rf-node-detail"><span>客户端连接</span><code>{entryAddress || '地址待检测'}</code></div>
            <div className="rf-node-entry-state">
              <span className={`entry-probe entry-probe-${entryProbeTone}`}>{entryProbe || '待探测'}</span>
              <span><UsersIcon size={10} />{entryAccess || '未授权用户'}</span>
            </div>
            <div className="rf-node-route-summary"><Workflow size={10} />{entryPathInfo?.pathCount ? `${entryPathInfo.pathCount} 条代理路径` : '直接接入'}</div>
            {entryDetails?.dns && <div className="rf-node-footnote"><Globe size={10} />{entryDetails.dns}</div>}
          </>
        )}
      </div>
    </div>
  )
}

/** Topology-aware layout for the current proxy-path canvas. */
function autoLayoutProxyGraphPositions(
  data: any,
  rootServerId: number,
  canvasImportedIDs: number[] = [],
  canvasServerInstances: CanvasServerInstance[] = [],
): Record<string, GraphPosition> {
  const servers: Server[] = data.servers || []
  const entries: Inbound[] = data.inbounds || []
  const rootID = rootServerId || servers[0]?.id || 0
  if (!rootID) return {}

  const inboundByID = new Map<number, Inbound>(entries.map(x => [x.id, x]))
  const visibleServerIds = reachableServerIds(data, rootID)
  const visibleImportedIDs = new Set<number>(canvasImportedIDs)
  const layoutEdges = new Map<string, Set<string>>()
  const addLayoutHop = (from: string, to: string) => {
    if (!from || !to || from === to) return
    if (!layoutEdges.has(from)) layoutEdges.set(from, new Set())
    layoutEdges.get(from)!.add(to)
  }

  ;(data.port_forwards || []).forEach((x: PortForward) => {
    if (x.enabled === false) return
    addLayoutHop(`server-${x.source_server_id}`, `server-${x.target_server_id}`)
  })
  ;(data.tunnels || []).forEach((x: Tunnel) => {
    if (x.enabled === false) return
    addLayoutHop(`server-${x.source_server_id}`, `server-${x.target_server_id}`)
  })

  const stepsByPath = new Map<number, ProxyPathStep[]>()
  ;(data.proxy_path_steps || []).forEach((step: ProxyPathStep) => {
    const list = stepsByPath.get(step.path_id) || []
    list.push(step)
    stepsByPath.set(step.path_id, list)
  })

  ;(data.proxy_paths || []).forEach((path: ProxyPath) => {
    if (path.enabled === false) return
    const root = inboundByID.get(path.inbound_id)
    if (!root || root.server_id !== rootID) return
    let previousNodeID = `server-${root.server_id}`
    const pathSteps = (stepsByPath.get(path.id) || []).slice().sort((a, b) => (a.position - b.position) || (a.id - b.id))
    pathSteps.forEach(step => {
      const nextNodeID = proxyPathStepNodeID(step)
      if (!nextNodeID) return
      addLayoutHop(previousNodeID, nextNodeID)
      previousNodeID = nextNodeID
    })
  })

  // Calculate hop depth across both controlled servers and imported proxies so
  // mixed paths such as A -> SOCKS -> B stay in one vertical sequence.
  const rootNodeID = `server-${rootID}`
  const depth = new Map<string, number>([[rootNodeID, 0]])
  const queue = [rootNodeID]
  while (queue.length) {
    const cur = queue.shift()!
    for (const next of layoutEdges.get(cur) || []) {
      const nextDepth = (depth.get(cur) || 0) + 1
      if (!depth.has(next) || nextDepth < (depth.get(next) || 0)) {
        depth.set(next, nextDepth)
        queue.push(next)
      }
    }
  }
  visibleServerIds.forEach(id => { if (!depth.has(`server-${id}`)) depth.set(`server-${id}`, id === rootID ? 0 : 1) })
  visibleImportedIDs.forEach(id => { if (!depth.has(`imported-${id}`)) depth.set(`imported-${id}`, 1) })
  canvasServerInstances.forEach(instance => depth.set(canvasServerNodeID(instance), 1))

  const layers = new Map<number, string[]>()
  Array.from(depth.entries())
    .sort((a, b) => a[1] - b[1] || a[0].localeCompare(b[0]))
    .forEach(([nodeID, d]) => {
      const list = layers.get(d) || []
      list.push(nodeID)
      layers.set(d, list)
    })

  const CENTER_X = 760
  const ORIGIN_Y = 300
  const LAYER_GAP = 370
  const SIBLING_GAP = 100
  const positions: Record<string, GraphPosition> = {}
  const nodeWidth = (nodeID: string) => {
    if (nodeID.startsWith('proxy-imported-step-') || nodeID.startsWith('imported-')) return GRAPH_ENTRY_NODE_WIDTH
    const serverID = graphNodeServerId(nodeID, data, canvasServerInstances)
    if (!serverID) return GRAPH_ENTRY_NODE_WIDTH
    if (!nodeID.startsWith('server-') && !nodeID.startsWith('canvas-server-')) return GRAPH_ENTRY_NODE_WIDTH
    const entryCount = entries.filter(x => x.server_id === serverID && x.enabled !== false).length
    return graphServerNodeWidth(Math.max(1, entryCount))
  }
  const nodeLabel = (nodeID: string) => {
    const serverID = graphNodeServerId(nodeID, data, canvasServerInstances)
    if (serverID) {
      return servers.find(server => server.id === serverID)?.name || nodeID
    }
    const stepID = nodeID.startsWith('proxy-imported-step-') ? Number(nodeID.slice(20)) : 0
    const step = stepID ? ((data.proxy_path_steps || []) as ProxyPathStep[]).find(item => item.id === stepID) : undefined
    const importedID = step?.external_outbound_id || (nodeID.startsWith('imported-') ? Number(nodeID.slice(9)) : 0)
    return (data.external_outbounds || []).find((outbound: ExternalOutbound) => outbound.id === importedID)?.name || nodeID
  }

  layers.forEach((nodeIDs, layer) => {
    const ordered = nodeIDs.slice().sort((a, b) => nodeLabel(a).localeCompare(nodeLabel(b), 'zh') || a.localeCompare(b))
    const widths = ordered.map(nodeWidth)
    const totalWidth = widths.reduce((sum, width) => sum + width, 0) + Math.max(0, ordered.length - 1) * SIBLING_GAP
    let cursorX = CENTER_X - totalWidth / 2
    ordered.forEach((nodeID, index) => {
      const width = widths[index]
      const nodePosition = snapGraphPosition({ x: cursorX, y: ORIGIN_Y + layer * LAYER_GAP })
      cursorX += width + SIBLING_GAP
      if (nodeID.startsWith('imported-') || nodeID.startsWith('proxy-imported-step-')) {
        positions[nodeID] = nodePosition
        return
      }
      const serverID = graphNodeServerId(nodeID, data, canvasServerInstances)
      if (!serverID) return
      const serverEntries = entries
        .filter(x => x.server_id === serverID && x.enabled !== false)
        .slice()
        .sort((a, b) => (a.port - b.port) || (a.id - b.id))
      positions[nodeID] = nodePosition
      if (!nodeID.startsWith('server-')) return
      serverEntries.forEach((entry, entryIndex) => {
        if (entry.server_id !== rootID) return
        positions[`entry-${entry.id}`] = defaultEntryGraphPosition(nodePosition, entryIndex, Math.max(1, serverEntries.length), width)
      })
    })
  })

  return positions
}

function defaultServerGraphPositionFor(data: any, serverID: number, rootServerID = 0): GraphPosition {
  const servers: Server[] = data.servers || []
  const rootID = rootServerID || serverID || servers[0]?.id || 0
  const visibleIds = rootID ? reachableServerIds(data, rootID) : new Set<number>()
  const visibleServers = servers.filter(s => visibleIds.has(s.id))
  const visibleIndex = visibleServers.findIndex(s => s.id === serverID)
  if (visibleIndex >= 0) return defaultServerGraphPosition(visibleIndex)
  const index = servers.findIndex(s => s.id === serverID)
  return defaultServerGraphPosition(Math.max(0, index))
}

function nextServerGraphPosition(data: any): GraphPosition {
  return defaultServerGraphPosition((data.servers || []).length)
}

function nextEntryGraphPosition(data: any, positions: Record<string, GraphPosition>, serverID: number, rootServerID = 0): GraphPosition {
  const serverPosition = positions[`server-${serverID}`] || defaultServerGraphPositionFor(data, serverID, rootServerID)
  const entryCount = ((data.inbounds || []) as Inbound[]).filter(x => x.server_id === serverID).length
  return defaultEntryGraphPosition(serverPosition, entryCount, entryCount + 1, graphServerNodeWidth(entryCount + 1))
}

function graphNodeServerId(id: string, data: any, canvasServerInstances: CanvasServerInstance[] = []) {
  if (id.startsWith('server-')) return Number(id.slice(7))
  if (id.startsWith('canvas-server-')) {
    return canvasServerInstances.find(instance => canvasServerNodeID(instance) === id)?.server_id || 0
  }
  if (id.startsWith('proxy-server-step-')) {
    const stepID = Number(id.slice(18))
    const step = ((data.proxy_path_steps || []) as ProxyPathStep[]).find(item => item.id === stepID)
    if (!step) return 0
    const inbound = step.inbound_id ? ((data.inbounds || []) as Inbound[]).find(item => item.id === step.inbound_id) : undefined
    return inbound?.server_id || step.server_id || 0
  }
  if (id.startsWith('entry-')) {
    const inbound = (data.inbounds || []).find((x: Inbound) => x.id === Number(id.slice(6)))
    return inbound?.server_id || 0
  }
  return 0
}

// Fast client-side pre-check over explicitly created resources only. It cannot
// see path-derived forwards, tunnels or shared listeners, and it compares
// addresses literally, so an empty result does not promise a successful deploy —
// Controller still runs the authoritative listener validation per server.
function deploymentConflicts(data: any) {
  const conflicts: string[] = []
  const seen = new Map<string, string>()
  const add = (key: string, label: string) => {
    const old = seen.get(key)
    if (old) conflicts.push(`${old} 与 ${label} 使用了相同监听资源`)
    else seen.set(key, label)
  }
  ;(data.inbounds || []).filter((x: Inbound) => x.enabled !== false).forEach((x: Inbound) => add(`listen:${x.server_id}:${x.listen_ip || '0.0.0.0'}:${x.port}:tcp`, `入口节点 ${x.name}#${x.id}`))
  ;(data.port_forwards || []).filter((x: PortForward) => x.enabled !== false).forEach((x: PortForward) => {
    const proto = x.protocol === 'tcp_udp' ? ['tcp', 'udp'] : [x.protocol]
    proto.forEach(p => add(`listen:${x.source_server_id}:${x.listen_ip || '0.0.0.0'}:${x.listen_port}:${p}`, `端口转发 ${x.name}`))
  })
  ;(data.tunnels || []).filter((x: Tunnel) => x.enabled !== false && x.listen_port).forEach((x: Tunnel) => add(`listen:${x.source_server_id}:0.0.0.0:${x.listen_port}:${x.type}`, `隧道 ${x.name}`))
  const circular = detectTopologyCycles([...(data.port_forwards || []), ...(data.tunnels || [])])
  conflicts.push(...circular)
  return conflicts
}

function detectTopologyCycles(edges: Array<PortForward | Tunnel>) {
  const out = new Map<number, number[]>()
	edges.filter(x => x.enabled !== false).forEach(x => out.set(x.source_server_id, [...(out.get(x.source_server_id) || []), x.target_server_id]))
  const conflicts: string[] = []
  const visiting = new Set<number>()
  const visited = new Set<number>()
  const walk = (node: number, path: number[]) => {
    if (visiting.has(node)) {
      conflicts.push(`链路存在循环：${[...path, node].join(' → ')}`)
      return
    }
    if (visited.has(node)) return
    visiting.add(node)
    ;(out.get(node) || []).forEach(next => walk(next, [...path, node]))
    visiting.delete(node)
    visited.add(node)
  }
  Array.from(out.keys()).forEach(k => walk(k, []))
  return Array.from(new Set(conflicts))
}

function Inbounds({ data, client, load }: any) {
  const dialogs = useDialogs()
  const [f, setF] = useState({ server_id: 0, name: 'vless-in', protocol: 'vless', listen_ip: '0.0.0.0', port: 443, config_json: '{}', enabled: true })
  const rows = (data.inbounds || []).map((inbound: Inbound) => {
    const probe = latestInboundProbeSummary(data, inbound.id)
    return { id: inbound.id, name: inbound.name, protocol: inbound.protocol, endpoint: formatHostPort(inboundEntryAddress(data, inbound), inbound.port), probe_status: probe.label, probe_detail: probe.detail, enabled: inbound.enabled, _raw: inbound }
  })
  return <Panel title="入口节点"><p className="muted">每个入口节点都是一条代理链路的第一个节点。下发完成后会自动检查本机监听和公网端口，在线入口每 5 分钟复检一次。</p><ProtocolForm value={f} setValue={setF} servers={data.servers || []} submit={async () => { await client.request('/inbounds', { method: 'POST', body: JSON.stringify(f) }); await load() }} /><Table rows={rows} actions={(r: any) => <><button onClick={async () => { await client.request(`/inbounds/${r._raw.id}/probe`, { method: 'POST', body: '{}' }); await load() }}>立即探测</button><button onClick={() => remove(client, `/inbounds/${r._raw.id}`, load, dialogs, r._raw)}>删除</button></>} /></Panel>
}

function Outbounds({ data, client, load }: any) {
  const dialogs = useDialogs()
  const [f, setF] = useState({ server_id: 0, name: 'next-hop', protocol: 'vless', target_address: '', target_port: 443, config_json: '{}', enabled: true })
  return <Panel title="出口 / 下一跳"><p className="muted">出口可以是本机 direct，也可以是后续链路要使用的下一跳协议节点；下发时会按服务器合并到同一份 sing-box 配置。</p><ProtocolForm value={f} setValue={setF} servers={data.servers || []} submit={async () => { await client.request('/outbounds', { method: 'POST', body: JSON.stringify(f) }); await load() }} outbound /><Table rows={data.outbounds || []} actions={(r: Outbound) => <button onClick={() => remove(client, `/outbounds/${r.id}`, load, dialogs, r)}>删除</button>} /></Panel>
}

function RoutingRules({ data, client, load }: any) {
  const dialogs = useDialogs()
  const [f, setF] = useState({ server_id: 0, name: 'route-google', priority: 100, match_json: '{"domain_suffix":["google.com"]}', action: 'direct' as RouteAction, outbound_id: 0, external_outbound_id: 0, warp_profile_id: 0, outbound_tag: '', interface_name: '', enabled: true })
  const payload = () => {
    const body: any = { server_id: f.server_id, name: f.name, priority: f.priority, match_json: f.match_json, action: f.action, outbound_tag: f.outbound_tag, enabled: f.enabled }
    if (f.action === 'outbound' && f.outbound_id) body.outbound_id = f.outbound_id
    if (f.action === 'external' && f.external_outbound_id) body.external_outbound_id = f.external_outbound_id
    if (f.action === 'warp' && f.warp_profile_id) body.warp_profile_id = f.warp_profile_id
		if (f.action === 'interface') body.interface_name = f.interface_name.trim()
    return body
  }
  return <Panel title="分流规则"><p className="muted">规则会生成到所选服务器的 sing-box route.rules 中。目标端口可使用 <code>{'{"port":[22]}'}</code>，端口段可使用 <code>{'{"port_range":["10000:20000"]}'}</code>。</p><div className="form"><Select value={f.server_id} onChange={e => setF({ ...f, server_id: Number(e.target.value) })}><option value={0}>选择服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="名称" /><input value={f.priority} onChange={e => setF({ ...f, priority: Number(e.target.value) })} placeholder="优先级" /><Select value={f.action} onChange={e => setF({ ...f, action: e.target.value as RouteAction })}>{routeActions.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select><input value={f.match_json} onChange={e => setF({ ...f, match_json: e.target.value })} placeholder='匹配 JSON，例如 {"port":[22]}' />{f.action === 'outbound' && <Select value={f.outbound_id} onChange={e => setF({ ...f, outbound_id: Number(e.target.value) })}><option value={0}>选择出口</option>{(data.outbounds || []).filter((x: Outbound) => !f.server_id || x.server_id === f.server_id).map((x: Outbound) => <option value={x.id} key={x.id}>{x.name}</option>)}</Select>}{f.action === 'external' && <Select value={f.external_outbound_id} onChange={e => setF({ ...f, external_outbound_id: Number(e.target.value) })}><option value={0}>选择导入节点</option>{(data.external_outbounds || []).map((x: ExternalOutbound) => <option value={x.id} key={x.id}>{x.name}</option>)}</Select>}{f.action === 'warp' && <Select value={f.warp_profile_id} onChange={e => setF({ ...f, warp_profile_id: Number(e.target.value) })}><option value={0}>选择 WARP 配置</option>{(data.warp_profiles || []).filter((x: WARPProfile) => !f.server_id || x.server_id === f.server_id).map((x: WARPProfile) => <option value={x.id} key={x.id}>{x.name}（{labelValue(x.status)}）</option>)}</Select>}{f.action === 'interface' && <input value={f.interface_name} onChange={e => setF({ ...f, interface_name: e.target.value })} placeholder="出口网卡，例如 eth1" />}<input value={f.outbound_tag} onChange={e => setF({ ...f, outbound_tag: e.target.value })} placeholder="出口标签覆盖，可选" /><Select variant="segmented" value={String(f.enabled)} onChange={e => setF({ ...f, enabled: e.target.value === 'true' })}><option value="true">启用</option><option value="false">禁用</option></Select><button onClick={async () => { await client.request('/routing-rules', { method: 'POST', body: JSON.stringify(payload()) }); await load() }}>创建</button></div><Table rows={data.routing_rules || []} actions={(r: RoutingRule) => <button onClick={() => remove(client, `/routing-rules/${r.id}`, load, dialogs, r)}>删除</button>} /></Panel>
}

function ExternalOutbounds({ data, client, load }: any) {
  const dialogs = useDialogs()
  const [f, setF] = useState({ server_id: 0, name: 'imported-node', protocol: 'vless' as ExternalProtocol, scope: 'global', target_address: '', target_port: 443, config_json: '{}', expose_to_users: false, enabled: true })
  const [content, setContent] = useState('socks5://user:password@example.com:1080#SOCKS-A')
  const payload = () => ({ ...f, server_id: f.scope === 'server' && f.server_id ? f.server_id : undefined })
  const importPayload = () => ({ scope: f.scope, server_id: f.scope === 'server' && f.server_id ? f.server_id : undefined, expose_to_users: f.expose_to_users, content })
  useEffect(() => {
    if (f.protocol === 'socks') return
    const next = ensureAuthConfig(f.config_json, f.protocol as Protocol)
    if (next !== f.config_json) setF({ ...f, config_json: next })
  }, [f.protocol])
  return <Panel title="导入节点">
    <p className="muted">导入或创建可被链路图使用的第三方节点，也可以限制为单台服务器使用。推荐在“代理链路”图内操作。</p>
    <div className="form">
      <Select variant="segmented" value={f.scope} onChange={e => setF({ ...f, scope: e.target.value })}>{outboundScopes.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select>
      {f.scope === 'server' && <Select value={f.server_id} onChange={e => setF({ ...f, server_id: Number(e.target.value) })}><option value={0}>选择服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select>}
      <input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="名称" />
      <Select value={f.protocol} onChange={e => {
        const protocol = e.target.value as ExternalProtocol
        setF({ ...f, protocol, config_json: protocol === 'socks' ? f.config_json : ensureAuthConfig(f.config_json, protocol as Protocol) })
      }}>{externalProtocols.map(p => <option key={p} value={p}>{labelProtocol(p)}</option>)}</Select>
      <input value={f.target_address} onChange={e => setF({ ...f, target_address: e.target.value })} placeholder="目标地址" />
      <input value={f.target_port} onChange={e => setF({ ...f, target_port: Number(e.target.value) })} placeholder="目标端口" />
      {f.protocol !== 'socks' && <AuthFields value={f as any} setValue={setF as any} />}
      <textarea value={f.config_json} onChange={e => setF({ ...f, config_json: e.target.value })} placeholder="JSON 配置" />
      <Select variant="segmented" value={String(f.expose_to_users)} onChange={e => setF({ ...f, expose_to_users: e.target.value === 'true' })}><option value="false">默认不进订阅</option><option value="true">允许授权到订阅</option></Select>
      <Select variant="segmented" value={String(f.enabled)} onChange={e => setF({ ...f, enabled: e.target.value === 'true' })}><option value="true">启用</option><option value="false">禁用</option></Select>
      <button onClick={async () => { await client.request('/external-outbounds', { method: 'POST', body: JSON.stringify(payload()) }); await load() }}>创建</button>
    </div>
    <h3>导入链接 / JSON</h3>
    <textarea value={content} onChange={e => setContent(e.target.value)} rows={6} />
    <button onClick={async () => { await client.request('/external-outbounds/import', { method: 'POST', body: JSON.stringify(importPayload()) }); await load() }}>导入</button>
    <Table rows={(data.external_outbounds || []).map((x: ExternalOutbound) => ({ id: x.id, name: x.name, protocol: x.protocol, scope: x.scope, target_address: x.target_address, target_port: x.target_port, expose_to_users: x.expose_to_users, enabled: x.enabled, _raw: x }))} actions={(r: any) => <><button onClick={() => dialogs.alert({ title: r.name, message: <CopyBlock value={safePrettyJSON(r._raw.config_json)} /> })}>查看配置</button><button onClick={() => remove(client, `/external-outbounds/${r.id}`, load, dialogs, r)}>删除</button></>} />
  </Panel>
}

function WARPProfiles({ data, client, load }: any) {
  const dialogs = useDialogs()
  const [f, setF] = useState({ server_id: 0, name: 'warp-1', status: 'needed', config_json: '{}', mtu: 0, dns_strategy: 'auto', enabled: true })
  return <Panel title="WARP 配置"><p className="muted">每个 WARP 配置只能绑定一台服务器。如果 JSON 配置为空，下一次部署会由该服务器 Agent 准备 WARP 配置并随部署结果回传；再次部署会启用 WireGuard 出口和分流规则。</p><div className="form"><Select value={f.server_id} onChange={e => setF({ ...f, server_id: Number(e.target.value) })}><option value={0}>选择服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="名称" /><Select value={f.status} onChange={e => setF({ ...f, status: e.target.value })}>{warpStatuses.map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select><input value={f.mtu} onChange={e => setF({ ...f, mtu: Number(e.target.value) })} placeholder="MTU，0 为自动" /><input value={f.dns_strategy} onChange={e => setF({ ...f, dns_strategy: e.target.value })} placeholder="DNS 策略：auto 为自动，ipv6_only 为仅 IPv6" /><input value={f.config_json} onChange={e => setF({ ...f, config_json: e.target.value })} placeholder="就绪后的 sing-box WireGuard 出口 JSON" /><Select variant="segmented" value={String(f.enabled)} onChange={e => setF({ ...f, enabled: e.target.value === 'true' })}><option value="true">启用</option><option value="false">禁用</option></Select><button onClick={async () => { await client.request('/warp-profiles', { method: 'POST', body: JSON.stringify(f) }); await load() }}>创建</button></div><Table rows={data.warp_profiles || []} actions={(r: WARPProfile) => <button onClick={() => remove(client, `/warp-profiles/${r.id}`, load, dialogs, r)}>删除</button>} /></Panel>
}

function defaultUserDraft(): UserDraft {
  return { username: 'user1', nickname: '', password: 'change-me-123', role: 'viewer', status: 'active', speed_limit_mbps: 0, traffic_limit_bytes: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1, limit_mode: 'inherit' }
}

function userToDraft(user: User): UserDraft {
  return { username: user.username, nickname: user.nickname || '', role: user.role, status: user.status, speed_limit_mbps: user.speed_limit_mbps || 0, traffic_limit_bytes: user.traffic_limit_bytes || 0, traffic_reset_mode: user.traffic_reset_mode || 'monthly', traffic_reset_day: user.traffic_reset_day || 1, limit_mode: userLimitMode(user) }
}

function userDraftPayload(draft: UserDraft, includePassword: boolean) {
  const custom = draft.limit_mode === 'custom'
  return {
    username: draft.username.trim(),
    nickname: draft.nickname.trim(),
    role: draft.role,
    status: draft.status,
    speed_limit_mbps: custom ? Number(draft.speed_limit_mbps || 0) : 0,
    traffic_limit_bytes: custom ? Number(draft.traffic_limit_bytes || 0) : 0,
    traffic_reset_mode: custom ? (draft.traffic_reset_mode || 'monthly') : 'monthly',
    traffic_reset_day: custom ? Number(draft.traffic_reset_day || 1) : 1,
    ...(includePassword ? { password: draft.password || '' } : {}),
  }
}

function defaultUserGroupDraft(): UserGroupDraft {
  return { name: 'group-1', description: '', role: 'viewer', enabled: true, speed_limit_mbps: 0, traffic_limit_bytes: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1 }
}

function groupToDraft(group: UserGroup): UserGroupDraft {
  return { name: group.name, description: group.description || '', role: group.role || 'viewer', enabled: group.enabled !== false, speed_limit_mbps: group.speed_limit_mbps || 0, traffic_limit_bytes: group.traffic_limit_bytes || 0, traffic_reset_mode: group.traffic_reset_mode || 'monthly', traffic_reset_day: group.traffic_reset_day || 1 }
}

function CopySubscriptionButton({ user }: { user: User }) {
  const [copied, setCopied] = useState(false);
  const token = user.subscription_token
  const handleCopy = async () => {
    if (!token) return;
    const url = `${appControllerURL()}/api/v1/subscriptions/${token}`;
    if (await copyText(url)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const idleLabel = !token
    ? user.subscription_burned_at ? '已焚毁' : '已吊销'
    : user.subscription_burn_after_read ? '复制单次订阅' : '复制订阅'

  return (
    <button
      onClick={handleCopy}
      disabled={!token}
      className="btn-custom btn-secondary user-subscription-button"
      title={user.subscription_burn_after_read ? '首次成功获取订阅内容后，此链接立即失效' : '复制长期订阅链接'}
    >
      {copied ? (
        <>
          <Check size={12} style={{ color: 'var(--color-success)' }} />
          <span style={{ color: 'var(--color-success)' }}>已复制</span>
        </>
      ) : (
        <>
          <Copy size={12} />
          <span>{idleLabel}</span>
        </>
      )}
    </button>
  );
}

function QuickOneTimeSubscriptionButton({ user, client, format = 'sing-box', encrypted = false, notify, className = 'ghost' }: { user: User; client: ReturnType<typeof api>; format?: SubscriptionFormat; encrypted?: boolean; notify?: (message: string, tone?: ToastKind) => void; className?: string }) {
  const dialogs = useDialogs()
  const [creating, setCreating] = useState(false)
  const [copied, setCopied] = useState(false)
  const disabled = creating || user.status !== 'active' || (encrypted && !user.subscription_age_public_key)
  const createAndCopy = async () => {
    if (disabled) return
    setCreating(true)
    try {
      const result = await client.request(`/users/${user.id}/subscription-token/one-time`, { method: 'POST', body: '{}' }) as { subscription_token?: string }
      const url = subscriptionURLForToken(result.subscription_token || '', format, encrypted)
      if (!url) throw new Error('未能生成一次性订阅链接')
      if (await copyText(url)) {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 2000)
        notify?.(`${user.username} 的一次性订阅链接已创建并复制，首次读取后失效`, 'success')
      } else {
        await dialogs.alert({
          title: '一次性链接已创建',
          message: <div><p className="muted">自动复制失败，请手动复制。链接首次成功读取后立即失效。</p><CommandCopyBlock value={url} buttonText="复制一次性链接" /></div>,
        })
      }
    } catch (error: any) {
      const message = localizeErrorMessage(error?.message || error)
      if (notify) notify(message, 'error')
      else await dialogs.alert({ title: '创建失败', message })
    } finally {
      setCreating(false)
    }
  }
  return <button type="button" className={className} onClick={() => void createAndCopy()} disabled={disabled} title="不改变阅后即焚开关，直接创建一个首次读取后失效的独立链接">
    {copied ? <Check size={13} /> : <Zap size={13} />}{creating ? '创建中…' : copied ? '已复制' : '快速一次性'}
  </button>
}

function UserMoreActionsDropdown({ user, client, load, dialogs, onEdit, onPassword, onDelete }: any) {
  const [isOpen, setIsOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (ref.current && !ref.current.contains(event.target as any)) {
        setIsOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const items = [
    { label: '基础设置', action: 'edit' },
    { label: '修改密码', action: 'password' },
    { label: user.subscription_burn_after_read ? '关闭阅后即焚' : '开启阅后即焚', action: 'burn' },
    { label: '轮换订阅', action: 'rotate' },
    { label: '吊销订阅', action: 'revoke' },
    { label: '注销所有会话', action: 'revoke-sessions', danger: true },
    ...(!user.protected ? [{ label: '删除用户', action: 'delete', danger: true }] : []),
  ];

  const handleActionClick = async (action: string) => {
    setIsOpen(false);
    if (action === 'edit') onEdit(user);
    else if (action === 'password') onPassword(user);
    else if (action === 'burn') await setSubscriptionBurnPolicy(client, user, !user.subscription_burn_after_read, load, dialogs);
    else if (action === 'rotate') await rotateSub(client, user, load, dialogs);
    else if (action === 'revoke') await revokeSub(client, user, load, dialogs);
    else if (action === 'revoke-sessions') {
      const ok = await dialogs.confirm({ title: '注销所有会话', message: `确认注销 ${user.username} 的所有登录会话？`, tone: 'danger', confirmText: '注销' })
      if (!ok) return
      await client.request(`/users/${user.id}/sessions/revoke`, { method: 'POST', body: '{}' })
      await load()
    }
    else if (action === 'delete') onDelete(user);
  };

  return (
    <div ref={ref} style={{ position: 'relative', display: 'inline-block' }}>
      <button
        onClick={(e) => {
          e.stopPropagation();
          setIsOpen(!isOpen);
        }}
        className="btn-custom btn-secondary user-row-icon-button"
        style={{ backgroundColor: isOpen ? 'var(--bg-control)' : 'transparent', color: 'var(--text-secondary)' }}
        title="更多操作"
        aria-label="更多操作"
      >
        <MoreHorizontal size={16} />
      </button>
      {isOpen && (
        <div style={{
          position: 'absolute',
          top: '100%',
          right: 0,
          marginTop: '4px',
          zIndex: 50,
          minWidth: '120px',
          backgroundColor: 'var(--bg-card)',
          border: '1.5px solid var(--border-color)',
          borderRadius: 'var(--radius-md)',
          boxShadow: 'var(--shadow-lg)',
          padding: '6px',
          display: 'flex',
          flexDirection: 'column',
          gap: '2px',
          textAlign: 'left'
        }}>
          {items.map(item => (
            <button
              key={item.action}
              onClick={() => handleActionClick(item.action)}
              style={{
                width: '100%',
                padding: '6px 10px',
                border: 'none',
                borderRadius: 'var(--radius-sm)',
                fontSize: '12px',
                fontWeight: 600,
                textAlign: 'left',
                cursor: 'pointer',
                backgroundColor: 'transparent',
                color: item.danger ? 'var(--color-danger)' : 'var(--text-primary)',
                transition: 'background-color 0.15s'
              }}
              onMouseEnter={e => {
                e.currentTarget.style.backgroundColor = item.danger ? 'var(--color-danger-bg)' : 'var(--bg-control)';
              }}
              onMouseLeave={e => {
                e.currentTarget.style.backgroundColor = 'transparent';
              }}
            >
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function UserManagement({ data, client, load }: any) {
  const dialogs = useDialogs()
  const [draft, setDraft] = useState<UserDraft>(() => defaultUserDraft())
  const [editUser, setEditUser] = useState<User | null>(null)
  const [editDraft, setEditDraft] = useState<UserDraft>(() => defaultUserDraft())
  const [groupDraft, setGroupDraft] = useState<UserGroupDraft>(() => defaultUserGroupDraft())
  const [editingGroup, setEditingGroup] = useState<UserGroup | null>(null)
  const [groupEditDraft, setGroupEditDraft] = useState<UserGroupDraft>(() => defaultUserGroupDraft())
  const [memberDraft, setMemberDraft] = useState<Record<number, number>>({})
  const [createOpen, setCreateOpen] = useState(false)
  const [groupCreateOpen, setGroupCreateOpen] = useState(false)
  const [managingGroupID, setManagingGroupID] = useState<number | null>(null)
  const [passwordUser, setPasswordUser] = useState<User | null>(null)
  const createUser = async () => {
    await client.request('/users', { method: 'POST', body: JSON.stringify(userDraftPayload(draft, true)) })
    setCreateOpen(false)
    setDraft(defaultUserDraft())
    await load()
  }
  const openEditUser = (user: User) => {
    setEditUser(user)
    setEditDraft(userToDraft(user))
  }
  const updateUser = async () => {
    if (!editUser) return
    await client.request('/users/' + editUser.id, { method: 'PATCH', body: JSON.stringify(userDraftPayload(editDraft, false)) })
    setEditUser(null)
    await load()
  }
  const updatePassword = async (password: string, confirm: string) => {
    if (!password) {
      await dialogs.alert({ title: '无法修改密码', message: '请输入新密码' })
      return
    }
    if (password !== confirm) {
      await dialogs.alert({ title: '无法修改密码', message: '两次输入的新密码不一致' })
      return
    }
    if (!passwordUser) return
    await client.request('/users/' + passwordUser.id, { method: 'PATCH', body: JSON.stringify({ password }) })
    setPasswordUser(null)
    await load()
    await dialogs.alert({ title: '密码已修改', message: '用户密码已更新。' })
  }
  const createGroup = async () => {
    await client.request('/user-groups', { method: 'POST', body: JSON.stringify(groupDraft) })
    setGroupCreateOpen(false)
    setGroupDraft(defaultUserGroupDraft())
    await load()
  }
  const openCreateGroup = () => {
    setGroupDraft(defaultUserGroupDraft())
    setGroupCreateOpen(true)
  }
  const openEditGroup = (group: UserGroup) => {
    setEditingGroup(group)
    setGroupEditDraft(groupToDraft(group))
  }
  const updateGroup = async () => {
    if (!editingGroup) return
    await client.request('/user-groups/' + editingGroup.id, { method: 'PATCH', body: JSON.stringify(groupEditDraft) })
    setEditingGroup(null)
    await load()
  }
  const addGroupMember = async (groupID: number) => {
    const userID = memberDraft[groupID] || 0
    if (!userID) return dialogs.alert({ title: '无法添加成员', message: '请选择用户。' })
    await client.request('/user-group-members', { method: 'POST', body: JSON.stringify({ group_id: groupID, user_id: userID, enabled: true }) })
    setMemberDraft({ ...memberDraft, [groupID]: 0 })
    await load()
  }
  const deleteGroup = async (group: UserGroup) => {
    const ok = await dialogs.confirm({ title: '删除用户组', message: `确认删除用户组 ${group.name}？相关入口授权会一起移除。`, tone: 'danger', confirmText: '删除' })
    if (!ok) return
    await client.request(`/user-groups/${group.id}`, { method: 'DELETE' })
    await load()
  }
  const deleteMember = async (member: UserGroupMember) => {
    await client.request(`/user-group-members/${member.id}`, { method: 'DELETE' })
    await load()
  }
  return <Panel title="用户" className="user-management-panel">
    <div className="section-toolbar"><div><h3>用户列表</h3><p className="muted">新增用户、修改密码、订阅轮换、吊销和删除都从表格操作执行。</p></div><button onClick={() => setCreateOpen(true)}>添加用户</button></div>
    
    <div className="card-custom user-table-card">
      <div className="user-table-scroll">
        <table className="user-data-table">
          <colgroup>
            <col style={{ width: '22%' }} />
            <col style={{ width: '12%' }} />
            <col style={{ width: '15%' }} />
            <col style={{ width: '20%' }} />
            <col style={{ width: '23%' }} />
            <col style={{ width: '8%' }} />
          </colgroup>
          <thead>
            <tr style={{ borderBottom: '1.5px solid var(--border-color)', color: 'var(--text-muted)' }}>
              <th className="user-col-user" style={{ fontWeight: 600 }}>用户</th>
              <th className="user-col-limit" style={{ fontWeight: 600 }}>限速</th>
              <th className="user-col-groups" style={{ fontWeight: 600 }}>所属组</th>
              <th className="user-col-traffic" style={{ fontWeight: 600 }}>流量配额</th>
              <th className="user-col-subscription" style={{ fontWeight: 600 }}>订阅凭证</th>
              <th className="user-col-actions" style={{ fontWeight: 600, textAlign: 'right' }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {(data.users || []).map((usr: User) => {
              const limits = effectiveUserLimits(data, usr);
              const isSuspended = usr.status === 'suspended';
              const isQuotaExceeded = usr.traffic_quota_state === 'quota_exceeded';
              const isUnavailable = isSuspended || isQuotaExceeded;
              const speedLimitText = limits.speed > 0 ? `${limits.speed} Mbps` : '不限速';
              
              // Groups membership
              const userMembers = (data.user_group_members || []).filter((m: UserGroupMember) => m.user_id === usr.id && m.enabled !== false);
              const groupList = userMembers.map((m: UserGroupMember) => {
                const g = (data.user_groups || []).find((x: UserGroup) => x.id === m.group_id);
                return g ? g.name : null;
              }).filter(Boolean);
              const groupsText = groupList.length ? groupList.join(', ') : '无组';

              // Traffic details
              const usagePercent = limits.traffic > 0 ? (usr.traffic_used_bytes / limits.traffic) * 100 : 0;
              
              return (
                <tr key={usr.id} style={{ 
                  borderBottom: '1px solid var(--border-color)', 
                  opacity: isUnavailable ? 0.72 : 1,
                  backgroundColor: isUnavailable ? 'var(--bg-page)' : 'transparent',
                  transition: 'background-color 0.2s'
                }} className="table-row-hover">
                  <td className="user-col-user" style={{ fontWeight: 600 }}>
                    <div className="user-table-identity">
                      <div>
                        <span className="user-table-name">{usr.username}</span>
                        <span className={`badge-custom ${isUnavailable ? 'badge-danger' : 'badge-success'}`}>
                          {isSuspended ? '已暂停' : isQuotaExceeded ? '已达量' : '正常'}
                        </span>
                      </div>
                      {usr.traffic_quota_state && <span className="user-table-period">{trafficQuotaLabel(usr.traffic_quota_state)}{usr.traffic_period_end ? ` · ${formatDate(usr.traffic_period_end)}` : ''}</span>}
                    </div>
                  </td>
                  <td className="user-col-limit" data-label="限速" style={{ fontFamily: 'var(--font-mono)', color: 'var(--text-primary)' }}>
                    {speedLimitText}
                  </td>
                  <td className="user-col-groups" data-label="所属组">
                    <span className="badge-custom badge-muted user-table-group" style={{ fontWeight: 500 }} title={groupsText}>
                      {groupsText}
                    </span>
                  </td>
                  <td className="user-col-traffic" data-label="流量配额">
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', width: '100%', maxWidth: '150px' }}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '11px' }}>
                        <span style={{ fontWeight: 600, color: 'var(--text-primary)' }}>{formatBytes(usr.traffic_used_bytes || 0)}</span>
                        <span style={{ color: 'var(--text-muted)' }}>共 {limits.traffic > 0 ? formatBytes(limits.traffic) : '不限量'}</span>
                      </div>
                      {limits.traffic > 0 && (
                        <div style={{ width: '100%', height: '5px', backgroundColor: 'var(--border-color)', borderRadius: '3px', overflow: 'hidden' }}>
                          <div style={{ 
                            width: `${Math.min(100, usagePercent)}%`, 
                            height: '100%', 
                            backgroundColor: usagePercent > 90 ? 'var(--color-danger)' : usagePercent > 70 ? 'var(--color-warning)' : 'var(--color-primary)', 
                            borderRadius: '3px' 
                          }} />
                        </div>
                      )}
                    </div>
                  </td>
                  <td className="user-col-subscription" data-label="订阅凭证">
                    <div className="user-subscription-actions">
                      <CopySubscriptionButton user={usr} />
                      <QuickOneTimeSubscriptionButton user={usr} client={client} className="btn-custom btn-secondary user-subscription-button" />
                    </div>
                  </td>
                  <td className="user-col-actions" style={{ textAlign: 'right' }}>
                    <div className="user-row-actions">
                      <button 
                        onClick={() => openEditUser(usr)}
                        className="btn-custom btn-secondary user-row-icon-button" 
                        title="编辑"
                        aria-label={`编辑 ${usr.username}`}
                      >
                        <Edit3 size={14} />
                      </button>
                      <UserMoreActionsDropdown 
                        user={usr} 
                        client={client} 
                        load={load} 
                        dialogs={dialogs}
                        onEdit={openEditUser}
                        onPassword={setPasswordUser}
                        onDelete={(u: any) => remove(client, `/users/${u.id}`, load, dialogs, u)}
                      />
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>

    <UserGroupsPanel data={data} onCreateGroup={openCreateGroup} onEditGroup={openEditGroup} onManageMembers={(group: UserGroup) => setManagingGroupID(group.id)} onDeleteGroup={deleteGroup} />
    <AnimatePresence>{createOpen && <UserCreateDialog draft={draft} setDraft={setDraft} onCancel={() => setCreateOpen(false)} onSubmit={createUser} />}</AnimatePresence>
    <AnimatePresence>{editUser && <UserEditDialog user={editUser} draft={editDraft} setDraft={setEditDraft} onCancel={() => setEditUser(null)} onSubmit={updateUser} />}</AnimatePresence>
    <AnimatePresence>{groupCreateOpen && <UserGroupCreateDialog draft={groupDraft} setDraft={setGroupDraft} onCancel={() => setGroupCreateOpen(false)} onSubmit={createGroup} />}</AnimatePresence>
    <AnimatePresence>{editingGroup && <UserGroupEditDialog group={editingGroup} draft={groupEditDraft} setDraft={setGroupEditDraft} onCancel={() => setEditingGroup(null)} onSubmit={updateGroup} />}</AnimatePresence>
    <AnimatePresence>{managingGroupID !== null && <UserGroupMembersDialog groupID={managingGroupID} data={data} selectedUserID={memberDraft[managingGroupID] || 0} onSelectUser={userID => setMemberDraft({ ...memberDraft, [managingGroupID]: userID })} onAddMember={() => addGroupMember(managingGroupID)} onDeleteMember={deleteMember} onCancel={() => setManagingGroupID(null)} />}</AnimatePresence>
    <AnimatePresence>{passwordUser && <UserPasswordDialog user={passwordUser} onCancel={() => setPasswordUser(null)} onSubmit={updatePassword} />}</AnimatePresence>
  </Panel>
}

function UserGroupsPanel({ data, onCreateGroup, onEditGroup, onManageMembers, onDeleteGroup }: any) {
  const groups: UserGroup[] = data.user_groups || []
  const members: UserGroupMember[] = data.user_group_members || []
  return <section className="user-groups-panel">
    <div className="section-toolbar user-groups-heading">
      <div>
        <div className="user-groups-title"><h3>用户分组</h3><span>{groups.length}</span></div>
        <p className="muted">统一设置成员的入口权限、速度和流量额度。</p>
      </div>
      <button onClick={onCreateGroup}><Plus size={16} />新建分组</button>
    </div>
    <div className="user-group-list">
      {groups.length ? groups.map(group => {
        const groupMembers = members.filter(m => m.group_id === group.id && m.enabled !== false)
        return <article className={`user-group-card${group.enabled === false ? ' is-disabled' : ''}`} key={group.id}>
          <div className="user-group-summary">
            <div className="user-group-mark"><UsersIcon size={18} /></div>
            <div className="user-group-identity">
              <div><strong>{group.name}</strong>{group.system_key && <span className="badge neutral">系统组</span>}<span className={`badge ${group.enabled === false ? 'neutral' : 'success'}`}>{group.enabled === false ? '已停用' : '启用中'}</span></div>
              <p>{group.description || '暂无备注'}</p>
            </div>
            <div className="user-group-policies">
              <div><span><Shield size={14} />权限</span><strong>{sessionRoleLabel(group.role)}</strong></div>
              <div><span><Gauge size={14} />速度</span><strong>{formatSpeedLimit(group.speed_limit_mbps || 0)}</strong></div>
              <div><span><Database size={14} />流量</span><strong>{formatTrafficLimit(group.traffic_limit_bytes || 0)}</strong></div>
              <div><span><CalendarDays size={14} />重置</span><strong>{trafficResetSummary(group)}</strong></div>
            </div>
            <div className="user-group-actions">
              <button className="ghost user-group-member-toggle" onClick={() => onManageMembers(group)}>
                <UsersIcon size={15} /><span>{groupMembers.length} 位成员</span><ChevronRight size={15} />
              </button>
              <button className="ghost icon-button" onClick={() => onEditGroup(group)} title="编辑分组" aria-label={`编辑 ${group.name}`}><Edit3 /></button>
              {!group.system_key && <button className="ghost icon-button danger-text" onClick={() => onDeleteGroup(group)} title="删除分组" aria-label={`删除 ${group.name}`}><Trash2 /></button>}
            </div>
          </div>
        </article>
      }) : <div className="empty small">暂无用户组。</div>}
    </div>
  </section>
}

function UserGroupMembersDialog({ groupID, data, selectedUserID, onSelectUser, onAddMember, onDeleteMember, onCancel }: { groupID: number; data: any; selectedUserID: number; onSelectUser: (userID: number) => void; onAddMember: () => Promise<void>; onDeleteMember: (member: UserGroupMember) => Promise<void>; onCancel: () => void }) {
  const group = (data.user_groups || []).find((item: UserGroup) => item.id === groupID)
  const members: UserGroupMember[] = (data.user_group_members || []).filter((member: UserGroupMember) => member.group_id === groupID && member.enabled !== false)
  const used = new Set(members.map(member => member.user_id))
  const availableUsers: User[] = (data.users || []).filter((user: User) => !used.has(user.id))
  return <MotionDialogPanel onCancel={onCancel} className="user-group-members-dialog user-form-dialog">
    <header className="dialog-head">
      <div><h2 id="user-group-members-title">管理成员</h2><p className="muted">用户组：{group?.name || `#${groupID}`}</p></div>
      <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
    </header>
    <div className="dialog-body user-group-members-body">
      <div className="user-group-member-add">
        <Select value={selectedUserID} onChange={event => onSelectUser(Number(event.target.value))} aria-label="选择用户">
          <option value={0}>{availableUsers.length ? '选择要添加的用户' : '所有用户均已加入'}</option>
          {availableUsers.map(user => <option key={user.id} value={user.id}>{user.username}（{labelValue(user.status)}）</option>)}
        </Select>
        <button onClick={onAddMember} disabled={!selectedUserID}><UserPlus size={15} />添加成员</button>
      </div>
      <div className="user-group-member-list">
        <div className="user-group-member-list-head"><strong>当前成员</strong><span>{members.length} 人</span></div>
        {members.length ? members.map(member => {
          const user = userByID(data, member.user_id)
          const username = user?.username || `用户 #${member.user_id}`
          return <div className="user-group-member-row" key={member.id}>
            <div className="user-group-member-avatar">{username.slice(0, 1).toUpperCase()}</div>
            <div><strong>{username}</strong><span>{labelValue(user?.status || 'unknown')}</span></div>
            {!(group?.system_key === 'administrators' && user?.protected) && <button className="ghost icon-button danger-text" onClick={() => onDeleteMember(member)} title={`移除 ${username}`} aria-label={`移除 ${username}`}><X size={15} /></button>}
          </div>
        }) : <div className="empty small">该分组还没有成员。</div>}
      </div>
    </div>
    <footer className="dialog-actions"><button onClick={onCancel}>完成</button></footer>
  </MotionDialogPanel>
}

type TrafficDisplayUnit = 'GB' | 'TB'

function TrafficLimitInput({ bytes, onChange }: { bytes: number; onChange: (bytes: number) => void }) {
  const [unit, setUnit] = useState<TrafficDisplayUnit>(() => bytes >= 1024 ** 4 ? 'TB' : 'GB')
  const multiplier = unit === 'TB' ? 1024 ** 4 : 1024 ** 3
  const displayValue = bytes > 0 ? Number((bytes / multiplier).toFixed(3)) : 0
  return <div className="traffic-limit-input">
    <input type="number" min={0} step="any" value={displayValue} onChange={e => onChange(Math.round(Math.max(0, Number(e.target.value)) * multiplier))} />
    <Select variant="segmented" value={unit} onChange={e => setUnit(e.target.value as TrafficDisplayUnit)} aria-label="流量额度单位"><option value="GB">GB</option><option value="TB">TB</option></Select>
  </div>
}

function UserPasswordDialog({ user, onCancel, onSubmit }: { user: User; onCancel: () => void; onSubmit: (password: string, confirm: string) => Promise<void> }) {
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  return <MotionDialogPanel onCancel={onCancel} className="user-settings-dialog">
      <header className="dialog-head">
        <div><h2 id="user-password-title">修改用户密码</h2><p className="muted">用户：{user.username}</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form user-settings-form user-form">
          <FormField label="新密码" required hint="至少 8 位。">
            <input value={password} onChange={e => setPassword(e.target.value)} placeholder="新密码" type="password" autoComplete="new-password" />
          </FormField>
          <FormField label="确认密码" required>
            <input value={confirm} onChange={e => setConfirm(e.target.value)} placeholder="再次输入新密码" type="password" autoComplete="new-password" />
          </FormField>
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={() => onSubmit(password, confirm)}>保存密码</button></footer>
  </MotionDialogPanel>
}

function UserLimitFields({ draft, setDraft }: { draft: UserDraft; setDraft: React.Dispatch<React.SetStateAction<UserDraft>> }) {
  return <>
    <FormField label="限速策略" hint="可跟随用户组或单独设置。">
      <Select variant="segmented" value={draft.limit_mode} onChange={e => setDraft({ ...draft, limit_mode: e.target.value as LimitMode })}>
        <option value="inherit">跟随用户组</option>
        <option value="custom">单独设置</option>
      </Select>
    </FormField>
    {draft.limit_mode === 'custom' && <>
      <FormField label="用户限速" hint="0 表示不限速。">
        <input type="number" min={0} value={draft.speed_limit_mbps} onChange={e => setDraft({ ...draft, speed_limit_mbps: Number(e.target.value) })} placeholder="Mbps" />
      </FormField>
      <FormField label="用户流量额度" hint="0 表示不限量。">
        <input type="number" min={0} value={draft.traffic_limit_bytes} onChange={e => setDraft({ ...draft, traffic_limit_bytes: Number(e.target.value) })} placeholder="字节" />
      </FormField>
      <TrafficResetFields mode={draft.traffic_reset_mode} day={draft.traffic_reset_day} onChange={(patch) => setDraft({ ...draft, ...patch })} />
    </>}
  </>
}

function TrafficResetFields({ mode, day, onChange }: { mode: string; day: number; onChange: (patch: any) => void }) {
  return <>
    <FormField label="流量重置">
      <Select variant="segmented" value={mode || 'monthly'} onChange={e => onChange({ traffic_reset_mode: e.target.value })}>
        <option value="monthly">自然月</option>
        <option value="month_day">每月指定日</option>
      </Select>
    </FormField>
    {(mode || 'monthly') === 'month_day' && <FormField label="重置日" hint="短月使用最后一天。">
      <input type="number" min={1} max={31} value={day || 1} onChange={e => onChange({ traffic_reset_day: Number(e.target.value) })} />
    </FormField>}
  </>
}

function UserBaseFields({ draft, setDraft, includePassword }: { draft: UserDraft; setDraft: React.Dispatch<React.SetStateAction<UserDraft>>; includePassword?: boolean }) {
  return <>
    <FormField label="用户名" required hint="用于登录面板。">
      <input value={draft.username} onChange={e => setDraft({ ...draft, username: e.target.value })} placeholder="user1" />
    </FormField>
    <FormField label="昵称" hint="用于面板显示。">
      <input value={draft.nickname} onChange={e => setDraft({ ...draft, nickname: e.target.value })} placeholder="可选" maxLength={40} />
    </FormField>
    {includePassword && <FormField label="初始密码" required hint="创建后可修改。">
      <input value={draft.password || ''} onChange={e => setDraft({ ...draft, password: e.target.value })} placeholder="change-me-123" type="password" autoComplete="new-password" />
    </FormField>}
    <FormField label="角色">
      <Select variant="segmented" value={draft.role} onChange={e => setDraft({ ...draft, role: e.target.value as Role })}>
        <option value="viewer">只读</option>
        <option value="operator">操作员</option>
        <option value="admin">管理员</option>
      </Select>
    </FormField>
    <FormField label="状态">
      <Select variant="segmented" value={draft.status} onChange={e => setDraft({ ...draft, status: e.target.value })}>
        <option value="active">活跃</option>
        <option value="disabled">禁用</option>
      </Select>
    </FormField>
    <UserLimitFields draft={draft} setDraft={setDraft} />
  </>
}

function UserCreateDialog({ draft, setDraft, onCancel, onSubmit }: { draft: UserDraft; setDraft: React.Dispatch<React.SetStateAction<UserDraft>>; onCancel: () => void; onSubmit: () => Promise<void> }) {
  return <MotionDialogPanel onCancel={onCancel} className="user-create-dialog user-form-dialog">
      <header className="dialog-head">
        <div><h2 id="user-create-title">添加用户</h2><p className="muted">创建登录账号和订阅凭据。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form user-create-form user-form">
          <UserBaseFields draft={draft} setDraft={setDraft} includePassword />
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={onSubmit}>创建</button></footer>
  </MotionDialogPanel>
}

function UserEditDialog({ user, draft, setDraft, onCancel, onSubmit }: { user: User; draft: UserDraft; setDraft: React.Dispatch<React.SetStateAction<UserDraft>>; onCancel: () => void; onSubmit: () => Promise<void> }) {
  return <MotionDialogPanel onCancel={onCancel} className="user-create-dialog user-form-dialog">
      <header className="dialog-head">
        <div><h2 id="user-edit-title">编辑用户</h2><p className="muted">用户：{user.username}</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body">
        <div className="form user-create-form user-form">
          <UserBaseFields draft={draft} setDraft={setDraft} />
        </div>
      </div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={onSubmit}>保存</button></footer>
  </MotionDialogPanel>
}

function UserGroupFields({ draft, setDraft }: { draft: UserGroupDraft; setDraft: React.Dispatch<React.SetStateAction<UserGroupDraft>> }) {
  return <div className="form user-group-form">
    <FormField label="分组名称" required><input autoFocus value={draft.name} onChange={e => setDraft({ ...draft, name: e.target.value })} placeholder="例如：高级用户" /></FormField>
    <FormField label="后台权限" hint="成员继承该组权限。"><Select variant="segmented" value={draft.role} onChange={e => setDraft({ ...draft, role: e.target.value as Role })}><option value="viewer">普通用户</option><option value="operator">操作员</option><option value="admin">管理员</option></Select></FormField>
    <FormField label="状态"><Select variant="segmented" value={String(draft.enabled)} onChange={e => setDraft({ ...draft, enabled: e.target.value === 'true' })}><option value="true">启用</option><option value="false">停用</option></Select></FormField>
    <FormField className="user-group-description-field" label="备注"><input value={draft.description} onChange={e => setDraft({ ...draft, description: e.target.value })} placeholder="可选" /></FormField>
    <FormField label="速度上限" hint="0 表示不限速"><div className="input-with-unit"><input type="number" min={0} value={draft.speed_limit_mbps} onChange={e => setDraft({ ...draft, speed_limit_mbps: Number(e.target.value) })} /><span>Mbps</span></div></FormField>
    <FormField label="流量额度" hint="0 表示不限量"><TrafficLimitInput bytes={draft.traffic_limit_bytes} onChange={(traffic_limit_bytes) => setDraft({ ...draft, traffic_limit_bytes })} /></FormField>
    <TrafficResetFields mode={draft.traffic_reset_mode} day={draft.traffic_reset_day} onChange={(patch) => setDraft({ ...draft, ...patch })} />
  </div>
}

function UserGroupCreateDialog({ draft, setDraft, onCancel, onSubmit }: { draft: UserGroupDraft; setDraft: React.Dispatch<React.SetStateAction<UserGroupDraft>>; onCancel: () => void; onSubmit: () => Promise<void> }) {
  return <MotionDialogPanel onCancel={onCancel} className="user-group-dialog user-form-dialog">
      <header className="dialog-head">
        <div><h2 id="user-group-create-title">新建用户组</h2><p className="muted">设置成员共用的权限和额度。</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body"><UserGroupFields draft={draft} setDraft={setDraft} /></div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={onSubmit} disabled={!draft.name.trim()}>创建分组</button></footer>
  </MotionDialogPanel>
}

function UserGroupEditDialog({ group, draft, setDraft, onCancel, onSubmit }: { group: UserGroup; draft: UserGroupDraft; setDraft: React.Dispatch<React.SetStateAction<UserGroupDraft>>; onCancel: () => void; onSubmit: () => Promise<void> }) {
  return <MotionDialogPanel onCancel={onCancel} className="user-group-dialog user-form-dialog">
      <header className="dialog-head">
        <div><h2 id="user-group-edit-title">编辑用户组</h2><p className="muted">用户组：{group.name}</p></div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
      </header>
      <div className="dialog-body"><UserGroupFields draft={draft} setDraft={setDraft} /></div>
      <footer className="dialog-actions"><button className="ghost" onClick={onCancel}>取消</button><button onClick={onSubmit} disabled={!draft.name.trim()}>保存</button></footer>
  </MotionDialogPanel>
}

function dnsTransportLabel(value: DNSTransport) {
  return ({ udp: 'UDP', tcp: 'TCP', dot: 'DoT', doh: 'DoH', doq: 'DoQ' } as Record<DNSTransport, string>)[value]
}

function dnsDefaultPort(transport: DNSTransport) {
  if (transport === 'doh') return 443
  if (transport === 'dot' || transport === 'doq') return 853
  return 53
}

function dnsCandidateInput(candidate?: DNSCandidate) {
  if (!candidate?.server) return ''
  const scheme = ({ udp: 'udp', tcp: 'tcp', dot: 'tls', doh: 'https', doq: 'quic' } as Record<DNSTransport, string>)[candidate.transport]
  const defaultPort = dnsDefaultPort(candidate.transport)
  const port = candidate.port && candidate.port !== defaultPort ? `:${candidate.port}` : ''
  const host = candidate.server.includes(':') && !candidate.server.startsWith('[') ? `[${candidate.server}]` : candidate.server
  return `${scheme}://${host}${port}${candidate.transport === 'doh' ? candidate.path || '/dns-query' : ''}`
}

function parseDNSCandidate(value: string, fallback: DNSTransport, tag: string): DNSCandidate | null {
  const raw = value.trim()
  if (!raw) return null
  const withScheme = /^[a-z]+:\/\//i.test(raw) ? raw : `${fallback === 'dot' ? 'tls' : fallback === 'doh' ? 'https' : fallback === 'doq' ? 'quic' : fallback}://${raw}`
  let parsed: URL
  try { parsed = new URL(withScheme) } catch { throw new Error(`DNS 地址无效：${raw}`) }
  const transport = ({ udp: 'udp', tcp: 'tcp', tls: 'dot', dot: 'dot', https: 'doh', doh: 'doh', quic: 'doq', doq: 'doq' } as Record<string, DNSTransport>)[parsed.protocol.replace(':', '')]
  if (!transport || !parsed.hostname) throw new Error(`DNS 地址无效：${raw}`)
  const server = parsed.hostname.replace(/^\[|\]$/g, '')
  return {
    tag,
    transport,
    server,
    port: parsed.port ? Number(parsed.port) : dnsDefaultPort(transport),
    ...(transport === 'doh' ? { path: parsed.pathname || '/dns-query' } : {}),
    ...(['dot', 'doh', 'doq'].includes(transport) ? { tls_name: server } : {}),
  }
}

function serializeDNSListCandidates(candidates: DNSCandidate[]) {
  return candidates.map(candidate => `${candidate.tag} | ${dnsCandidateInput(candidate)}`).join('\n')
}

function parseDNSListCandidates(value: string, kind: DNSListKind) {
  const candidates = value.split(/\r?\n/).map(line => line.trim()).filter(Boolean).map((line, index) => {
    const separator = line.indexOf('|')
    if (separator < 1) throw new Error(`第 ${index + 1} 行需要使用“名称 | 服务地址”格式`)
    const tag = line.slice(0, separator).trim()
    const candidate = parseDNSCandidate(line.slice(separator + 1).trim(), kind === 'encrypted' ? 'doh' : 'udp', tag)
    if (!candidate) throw new Error(`第 ${index + 1} 行地址无效`)
  if (kind === 'encrypted' && !['doh', 'dot', 'doq'].includes(candidate.transport)) throw new Error('加密解析服务只支持 DoH、DoT 或 DoQ')
  if (kind === 'bootstrap' && !['udp', 'tcp'].includes(candidate.transport)) throw new Error('基础解析服务只支持 UDP 或 TCP')
    return candidate
  })
  if (candidates.length < 2 || candidates.length > 32) throw new Error('每个服务列表需要填写 2–32 个解析服务')
  if (new Set(candidates.map(candidate => candidate.tag)).size !== candidates.length) throw new Error('每个解析服务的名称必须唯一')
  return candidates
}

function DNSListDialog({ draft, setDraft, editing, saving, onCancel, onSave }: {
  draft: DNSListDraft
  setDraft: React.Dispatch<React.SetStateAction<DNSListDraft>>
  editing: DNSList | null
  saving: boolean
  onCancel: () => void
  onSave: () => void
}) {
  const update = (patch: Partial<DNSListDraft>) => setDraft(current => ({ ...current, ...patch }))
  const typeLabel = draft.kind === 'encrypted' ? '加密解析' : '基础解析'
  return <MotionDialogPanel onCancel={onCancel} className="dns-list-dialog">
    <header className="dialog-head">
      <div><h2>{editing ? '编辑解析服务列表' : '新建解析服务列表'}</h2><p className="muted">{editing ? editing.name : typeLabel}</p></div>
      <button type="button" className="ghost dialog-close icon-button" onClick={onCancel} disabled={saving} aria-label="关闭" title="关闭"><XIcon /></button>
    </header>
    <div className="dialog-body">
      <div className="form server-dialog-form labeled-form dns-list-dialog-form">
        <FormField label="列表名称" required><input value={draft.name} onChange={event => update({ name: event.target.value })} placeholder={draft.kind === 'encrypted' ? '海外加密解析' : '公网基础解析'} autoFocus /></FormField>
        <FormField label="列表类型" required><Select variant="segmented" value={draft.kind} disabled={Boolean(editing)} onChange={event => update({ kind: event.target.value as DNSListKind, candidates: '' })}><option value="encrypted">加密解析</option><option value="bootstrap">基础解析</option></Select></FormField>
        <FormField label="解析服务" hint="每行填写：名称 | 服务地址" required><textarea rows={8} value={draft.candidates} onChange={event => update({ candidates: event.target.value })} placeholder={draft.kind === 'encrypted' ? 'Cloudflare | https://cloudflare-dns.com/dns-query\nGoogle | tls://dns.google' : 'Cloudflare | udp://1.1.1.1\nGoogle | tcp://8.8.8.8'} /></FormField>
        <FormField label="使用状态"><div className="notification-enable-row"><input type="checkbox" checked={draft.enabled} disabled={Boolean(editing?.protected)} onChange={event => update({ enabled: event.target.checked })} /><span>{editing?.protected ? '默认列表始终启用' : draft.enabled ? '列表已启用' : '列表已停用'}</span></div></FormField>
      </div>
    </div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={onCancel} disabled={saving}>取消</button><button type="button" onClick={onSave} disabled={saving || !draft.name.trim() || !draft.candidates.trim()}>{saving ? '保存中…' : editing ? '保存修改' : '创建列表'}</button></footer>
  </MotionDialogPanel>
}

function DNSListSettings({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
  const lists: DNSList[] = data.dns_lists || []
  const [filter, setFilter] = useState<DNSListKind>('encrypted')
  const [editing, setEditing] = useState<DNSList | null>(null)
  const [draft, setDraft] = useState<DNSListDraft>({ name: '', kind: 'encrypted', enabled: true, candidates: '' })
  const [editorOpen, setEditorOpen] = useState(false)
  const [working, setWorking] = useState('')
  const openCreate = (kind = filter) => { setEditing(null); setDraft({ name: '', kind, enabled: true, candidates: '' }); setEditorOpen(true) }
  const edit = (list: DNSList) => { setEditing(list); setDraft({ name: list.name, kind: list.kind, enabled: list.enabled, candidates: serializeDNSListCandidates(list.candidates) }); setEditorOpen(true) }
  const copy = (list: DNSList) => { setEditing(null); setFilter(list.kind); setDraft({ name: `${list.name} 副本`, kind: list.kind, enabled: true, candidates: serializeDNSListCandidates(list.candidates) }); setEditorOpen(true) }
  const closeEditor = () => { if (!working) setEditorOpen(false) }
  const save = async () => {
    setWorking('save')
    try {
      const wasEditing = Boolean(editing)
      const payload = { name: draft.name.trim(), kind: draft.kind, enabled: draft.enabled, candidates: parseDNSListCandidates(draft.candidates, draft.kind) }
      if (!payload.name) throw new Error('请填写服务列表名称')
      await client.request(editing ? `/dns-lists/${editing.id}` : '/dns-lists', { method: editing ? 'PUT' : 'POST', body: JSON.stringify(payload) })
      setEditorOpen(false)
      setEditing(null)
      await load()
      notify?.(wasEditing ? '解析服务列表已更新' : '解析服务列表已创建', 'success')
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  const toggle = async (list: DNSList) => {
    try {
      await client.request(`/dns-lists/${list.id}`, { method: 'PUT', body: JSON.stringify({ ...list, enabled: !list.enabled }) })
      await load()
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const removeList = async (list: DNSList) => {
    const ok = await dialogs.confirm({ title: '删除服务列表', message: `确认删除 ${list.name}？`, confirmText: '删除', tone: 'danger' })
    if (!ok) return
    try { await client.request(`/dns-lists/${list.id}`, { method: 'DELETE' }); await load(); notify?.('解析服务列表已删除', 'success') } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') }
  }
  const visible = lists.filter(list => list.kind === filter)
  return <section className="settings-card dns-lists-card">
    <div className="settings-card-head"><div><h3>解析服务列表</h3><p className="muted">为服务器准备可复用的加密解析和基础解析服务。</p></div><button type="button" className="ghost" onClick={() => openCreate(filter)}><Plus size={14} />新建列表</button></div>
    <div className="dns-list-toolbar"><Select variant="segmented" value={filter} onChange={event => setFilter(event.target.value as DNSListKind)}><option value="encrypted">加密解析</option><option value="bootstrap">基础解析</option></Select></div>
    <div className="dns-record-list">{visible.length ? visible.map(list => <div className="dns-record-row dns-list-row" key={list.id}>
        <span className="record-type">{list.kind === 'encrypted' ? '加密' : '基础'}</span>
        <div className="record-main"><strong>{list.name}</strong><span>{Array.from(new Set(list.candidates.map(candidate => dnsTransportLabel(candidate.transport)))).join(' · ')}</span><small>{list.candidates.length} 个解析服务 · {list.usage_count} 台服务器使用</small></div>
        <span className={`status-pill ${list.enabled ? 'ok' : 'warning'}`}>{list.enabled ? '启用' : '禁用'}</span>
        <div className="record-actions"><button type="button" className="ghost icon-button" onClick={() => copy(list)} aria-label="复制" title="复制"><Copy size={14} /></button><button type="button" className="ghost icon-button" onClick={() => edit(list)} aria-label="编辑" title="编辑"><Edit3 size={14} /></button><button type="button" className="ghost icon-button" onClick={() => void toggle(list)} disabled={list.protected} aria-label={list.protected ? '默认列表始终启用' : list.enabled ? '禁用' : '启用'} title={list.protected ? '默认列表始终启用' : list.enabled ? '禁用' : '启用'}><CheckSquare size={14} /></button><button type="button" className="ghost icon-button danger-text" onClick={() => void removeList(list)} disabled={list.protected} aria-label={list.protected ? '默认列表不能删除' : '删除'} title={list.protected ? '默认列表不能删除' : '删除'}><Trash2 size={14} /></button></div>
      </div>) : <div className="empty-inline">暂无{filter === 'encrypted' ? '加密解析' : '基础解析'}列表</div>}</div>
    <AnimatePresence>{editorOpen && <DNSListDialog draft={draft} setDraft={setDraft} editing={editing} saving={working === 'save'} onCancel={closeEditor} onSave={() => void save()} />}</AnimatePresence>
  </section>
}

function dnsPolicyDraft(policy: ServerDNSPolicy | undefined, lists: DNSList[]) {
  return {
    encryptedListID: Number(policy?.encrypted_list_id || lists.find(list => list.kind === 'encrypted' && list.enabled)?.id || 0),
    bootstrapListID: Number(policy?.bootstrap_list_id || lists.find(list => list.kind === 'bootstrap' && list.enabled)?.id || 0),
    strategy: policy?.strategy || 'auto',
    hourlyTest: policy?.auto_test === 'periodic',
  }
}

function isDNSPolicyStale(policy: ServerDNSPolicy | undefined, lists: DNSList[]) {
  if (!policy?.last_success_at) return false
  const encryptedRevision = lists.find(list => list.id === policy.encrypted_list_id)?.revision
  const bootstrapRevision = lists.find(list => list.id === policy.bootstrap_list_id)?.revision
  return policy.encrypted_selection_revision !== encryptedRevision || policy.bootstrap_selection_revision !== bootstrapRevision
}

function DNSGroupStatus({ title, selected, group }: { title: string; selected: DNSCandidate[]; group?: DNSBenchmarkGroup }) {
  const tags = group?.best_tags?.length ? group.best_tags : selected.map(candidate => candidate.tag)
  const itemByTag = new Map((group?.items || []).map(item => [item.tag, item]))
  return <div className="dns-group-status"><header><strong>{title}</strong><span>{tags.length ? `${tags.length} 个可用` : '等待检查'}</span></header>{[0, 1].map(index => {
    const tag = tags[index]
    const item = tag ? itemByTag.get(tag) : undefined
    return <div key={index}><small>{index === 0 ? '第一名' : '第二名'}</small><strong>{tag || '—'}</strong><span>{item && !item.error ? `${item.latency_ms} ms` : item?.error || ''}</span></div>
  })}</div>
}

function DNSSettingsDialog({ server, policy, lists, benchmarks, client, onClose, onChanged, notify }: { server: Server; policy?: ServerDNSPolicy; lists: DNSList[]; benchmarks: DNSBenchmarkResult[]; client: ReturnType<typeof api>; onClose: () => void; onChanged: () => Promise<void>; notify?: (message: string, tone?: ToastKind) => void }) {
  const [draft, setDraft] = useState(() => dnsPolicyDraft(policy, lists))
  const [working, setWorking] = useState('')
  const latest = benchmarks[0]
  const encryptedList = lists.find(list => list.id === draft.encryptedListID)
  const bootstrapList = lists.find(list => list.id === draft.bootstrapListID)
  const stale = isDNSPolicyStale(policy, lists)
  useEffect(() => setDraft(dnsPolicyDraft(policy, lists)), [policy?.revision, lists.length])
  const save = async () => {
    const response = await client.request(`/servers/${server.id}/dns-policy`, { method: 'PUT', body: JSON.stringify({
      encrypted_list_id: draft.encryptedListID,
      bootstrap_list_id: draft.bootstrapListID,
      strategy: draft.strategy,
      auto_test: draft.hourlyTest ? 'periodic' : 'first_apply',
      test_interval_seconds: 3600,
    }) })
    await onChanged()
    return response.dns_policy as ServerDNSPolicy
  }
  const run = async (action: 'save' | 'test' | 'test_and_apply') => {
    if (working) return
    setWorking(action)
    try {
      await save()
      if (action !== 'save') {
        const result = await client.request(`/servers/${server.id}/dns-test`, { method: 'POST', body: JSON.stringify({ action }) })
        if (result.task?.status === 'failed') {
          const failure = parseJSONLoose(result.task.result_json)
          throw new Error(failure?.error || failure?.message || '暂时无法检查解析服务')
        }
        notify?.(action === 'test' ? '解析服务检查已开始' : '解析服务检查已开始，成功后会自动应用', 'success')
      } else {
        notify?.('服务器解析设置已保存', 'success')
      }
      if (action !== 'test') onClose()
    } catch (error: any) { notify?.(localizeErrorMessage(error?.message || error), 'error') } finally { setWorking('') }
  }
  return <MotionDialogPanel onCancel={onClose} className="dns-settings-dialog">
    <header className="dialog-head"><div><h2>DNS 设置</h2><p className="muted">{server.name}</p></div><button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body dns-settings-body">
      <div className="dns-status-strip"><span><strong>{stale ? '等待重新检查' : policy?.last_success_at ? '解析服务正常' : '尚未检查'}</strong><small>{policy?.last_success_at ? formatTableTime(policy.last_success_at) : '保存后会使用当前列表'}</small></span><span><strong>{draft.hourlyTest ? '每小时检查' : '关闭自动检查'}</strong><small>自动检查不会直接修改服务器配置</small></span><span className={policy?.last_error ? 'has-error' : ''}><strong>{policy?.last_error ? '最近检查失败' : latest?.status === 'stale' ? '检查结果已过期' : '状态正常'}</strong><small>{policy?.last_error || latest?.error || '—'}</small></span></div>
      {stale && <div className="access-note warning"><strong>解析服务列表已更新，需要重新检查</strong><span>旧的检查结果已停止使用。</span></div>}
      <div className="dns-group-grid"><DNSGroupStatus title="加密解析" selected={policy?.encrypted_selected || []} group={latest?.encrypted} /><DNSGroupStatus title="基础解析" selected={policy?.bootstrap_selected || []} group={latest?.bootstrap} /></div>
      <div className="form dns-settings-form labeled-form">
        <FormField label="加密解析服务列表" full><Select value={draft.encryptedListID} onChange={event => setDraft({ ...draft, encryptedListID: Number(event.target.value) })}>{lists.filter(list => list.kind === 'encrypted' && (list.enabled || list.id === policy?.encrypted_list_id)).map(list => <option key={list.id} value={list.id}>{list.name} · {list.candidates.length} 项</option>)}</Select></FormField>
        <FormField label="基础解析服务列表" full><Select value={draft.bootstrapListID} onChange={event => setDraft({ ...draft, bootstrapListID: Number(event.target.value) })}>{lists.filter(list => list.kind === 'bootstrap' && (list.enabled || list.id === policy?.bootstrap_list_id)).map(list => <option key={list.id} value={list.id}>{list.name} · {list.candidates.length} 项</option>)}</Select></FormField>
        <FormField label="IP 类型"><Select value={draft.strategy} onChange={event => setDraft({ ...draft, strategy: event.target.value })}><option value="auto">跟随服务器</option><option value="prefer_ipv4">优先 IPv4</option><option value="prefer_ipv6">优先 IPv6</option><option value="ipv4_only">仅 IPv4</option><option value="ipv6_only">仅 IPv6</option></Select></FormField>
        <FormField label="每小时自动检查"><label className="notification-enable-row"><input type="checkbox" checked={draft.hourlyTest} onChange={event => setDraft({ ...draft, hourlyTest: event.target.checked })} aria-label="启用每小时自动检查" /></label></FormField>
        <div className="dns-list-preview"><span>{encryptedList?.candidates.map(candidate => dnsTransportLabel(candidate.transport)).join(' · ')}</span><span>{bootstrapList?.candidates.map(candidate => dnsTransportLabel(candidate.transport)).join(' · ')}</span></div>
      </div>
    </div>
    <footer className="dialog-actions dns-dialog-actions"><button className="ghost" disabled={Boolean(working)} onClick={() => void run('save')}>{working === 'save' ? '保存中...' : '仅保存'}</button><button className="ghost" disabled={Boolean(working)} onClick={() => void run('test')}><Gauge size={15} />{working === 'test' ? '检查中...' : '仅检查'}</button><button disabled={Boolean(working)} onClick={() => void run('test_and_apply')}><RefreshCw size={15} />{working === 'test_and_apply' ? '检查中...' : '检查并应用'}</button></footer>
  </MotionDialogPanel>
}

function DNS({ data, client, load, notify }: any) {
  const servers: Server[] = data.servers || []
  const lists: DNSList[] = data.dns_lists || []
  const policies: ServerDNSPolicy[] = data.server_dns_policies || []
  const benchmarks: DNSBenchmarkResult[] = data.dns_benchmarks || []
  const rows = policies.map(policy => ({
    id: policy.server_id,
    server: servers.find(server => server.id === policy.server_id)?.name || `#${policy.server_id}`,
    encrypted_list: lists.find(list => list.id === policy.encrypted_list_id)?.name || '—',
    bootstrap_list: lists.find(list => list.id === policy.bootstrap_list_id)?.name || '—',
    encrypted_selected: policy.encrypted_selected.map(candidate => candidate.tag).join(' · ') || '等待检查',
    bootstrap_selected: policy.bootstrap_selected.map(candidate => candidate.tag).join(' · ') || '等待检查',
    status: isDNSPolicyStale(policy, lists) ? '等待重新检查' : policy.last_error ? '失败' : policy.last_success_at ? '正常' : '尚未检查',
    last_success_at: policy.last_success_at || '',
    _policy: policy,
  }))
  const resultRows = benchmarks.map(result => ({ id: result.id, server: servers.find(server => server.id === result.server_id)?.name || `#${result.server_id}`, encrypted: result.encrypted.best_tags.join(' · ') || '无可用项', bootstrap: result.bootstrap.best_tags.join(' · ') || '无可用项', status: result.status, error: result.error, checked_at: result.created_at }))
  const test = async (policy: ServerDNSPolicy) => { await client.request(`/servers/${policy.server_id}/dns-test`, { method: 'POST', body: JSON.stringify({ action: 'test' }) }); await load() }
  return <div className="dns-settings-page"><DNSListSettings data={data} client={client} load={load} notify={notify} /><Panel title="服务器解析设置"><div className="section-toolbar"><div><h3>服务器解析策略</h3><p className="muted">每台服务器可以选择一组加密解析服务和一组基础解析服务。</p></div><button className="ghost" onClick={() => goTab('servers')}><ServerIcon size={15} />打开服务器设置</button></div><Table rows={rows} actions={(row: any) => <button onClick={() => void test(row._policy)}><Gauge size={14} />重新检查</button>} /><h3>检查记录</h3><Table rows={resultRows} /></Panel></div>
}

function MTU({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
  const servers = data.servers || []
  const [f, setF] = useState({ server_id: 0, mode: 'detect', target_host: '', target_port: 0, interface_name: '', overhead_bytes: 0, desired_mtu: 0, sample_count: 3, timeout_ms: 1200 })
  const submit = async () => {
    if (!f.server_id) return dialogs.alert({ title: '无法执行 MTU 检测', message: '请选择服务器。' })
    const payload = Object.fromEntries(Object.entries(f).filter(([, v]) => v !== '' && v !== 0))
    await client.request(`/servers/${f.server_id}/mtu-detect`, { method: 'POST', body: JSON.stringify(payload) })
    await load()
    const name = servers.find((s: Server) => s.id === f.server_id)?.name || '服务器'
    notify?.(`已创建 ${name} 的 MTU 检测，请在任务中心查看结果`, 'success')
  }
  return <Panel title="MTU 检测"><p className="muted">MTU 检测会交叉校验出口网卡 MTU、系统路由 MTU、tracepath PMTU、DF ping 二分探测和 TCP 可达性。仅检测模式只记录建议值；应用模式会要求 Agent 修改网卡 MTU，通常需要 root/CAP_NET_ADMIN，容器环境建议优先使用仅检测。</p><div className="form"><Select value={f.server_id} onChange={e => setF({ ...f, server_id: Number(e.target.value) })}><option value={0}>选择服务器</option>{servers.map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><Select variant="segmented" value={f.mode} onChange={e => setF({ ...f, mode: e.target.value })}>{mtuModes.filter(x => x !== 'disabled').map(x => <option key={x} value={x}>{labelValue(x)}</option>)}</Select><input value={f.target_host} onChange={e => setF({ ...f, target_host: e.target.value })} placeholder="目标主机，空为默认" /><input value={f.target_port} onChange={e => setF({ ...f, target_port: Number(e.target.value) })} placeholder="目标端口，空为默认" /><input value={f.interface_name} onChange={e => setF({ ...f, interface_name: e.target.value })} placeholder="网卡名，可选" /><input value={f.overhead_bytes} onChange={e => setF({ ...f, overhead_bytes: Number(e.target.value) })} placeholder="额外开销字节" /><input value={f.desired_mtu} onChange={e => setF({ ...f, desired_mtu: Number(e.target.value) })} placeholder="目标 MTU，0 为自动" /><input value={f.sample_count} onChange={e => setF({ ...f, sample_count: Number(e.target.value) })} placeholder="采样次数" /><input value={f.timeout_ms} onChange={e => setF({ ...f, timeout_ms: Number(e.target.value) })} placeholder="超时毫秒" /><button onClick={submit}>检测/应用</button></div><Table rows={data.mtu_detections || []} /></Panel>
}

function PortForwards({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
	const [f, setF] = useState({ name: 'forward-1', source_server_id: 0, target_server_id: 0, listen_ip: '0.0.0.0', listen_port: 443, target_address: '', target_port: 443, protocol: 'tcp' as ForwardProtocol, backend: 'auto' as ForwardBackend, probe_mode: 'periodic' as ProbeMode, probe_interval_seconds: 300, sample_rate: 0, priority: 100, config_json: '{}', enabled: true })
  const submit = async () => { await client.request('/port-forwards', { method: 'POST', body: JSON.stringify(f) }); await load() }
  const forwardRows = (data.port_forwards || []).map((forward: PortForward) => {
    const probe = latestForwardProbe(data, forward.id)
    return { id: forward.id, name: forward.name, source_server_id: forward.source_server_id, target_server_id: forward.target_server_id, protocol: forward.protocol, listen_port: forward.listen_port, target_port: forward.target_port, probe_status: !probe ? '等待探测' : probe.available ? `正常 · ${probe.latency_ms}ms` : '转发异常', checked_at: probe?.created_at || '', enabled: forward.enabled, _raw: forward }
  })
  const probeRows = (data.port_forward_probes || []).map((probe: PortForwardProbeResult) => {
    const details = forwardProbeDetails(probe)
    const forward = (data.port_forwards || []).find((x: PortForward) => x.id === probe.port_forward_id)
    return { id: probe.id, name: forward?.name || `转发 ${probe.port_forward_id}`, mode: probe.mode, probe_status: probe.available ? '正常' : '异常', latency_ms: probe.latency_ms, p95_latency_ms: details.p95, jitter_ms: details.jitter, success_count: details.successCount, sample_count: probe.sample_count, checked_at: probe.created_at, error: probe.error }
  })
  return <Panel title="端口转发"><p className="muted">下发后自动检查源端监听，并从 A 节点连续 5 次连接 B 节点目标端口，回报平均延迟、P95、抖动和成功率。周期模式默认每 5 分钟复检；内置后端还可以采样真实连接的目标建立延迟。</p><div className="form"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="名称" /><Select value={f.source_server_id} onChange={e => setF({ ...f, source_server_id: Number(e.target.value) })}><option value={0}>源服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><Select value={f.target_server_id} onChange={e => setF({ ...f, target_server_id: Number(e.target.value) })}><option value={0}>目标服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><input value={f.listen_ip} onChange={e => setF({ ...f, listen_ip: e.target.value })} placeholder="监听 IP" /><input value={f.listen_port} onChange={e => setF({ ...f, listen_port: Number(e.target.value) })} placeholder="监听端口" /><input value={f.target_address} onChange={e => setF({ ...f, target_address: e.target.value })} placeholder="目标地址，可选" /><input value={f.target_port} onChange={e => setF({ ...f, target_port: Number(e.target.value) })} placeholder="目标端口" /><Select variant="segmented" value={f.protocol} onChange={e => setF({ ...f, protocol: e.target.value as ForwardProtocol })}>{forwardProtocols.map(p => <option key={p} value={p}>{labelValue(p)}</option>)}</Select><Select value={f.backend} onChange={e => setF({ ...f, backend: e.target.value as ForwardBackend })}>{forwardBackends.map(p => <option key={p} value={p}>{labelValue(p)}</option>)}</Select><Select value={f.probe_mode} onChange={e => setF({ ...f, probe_mode: e.target.value as ProbeMode })}>{probeModes.map(p => <option key={p} value={p}>{labelValue(p)}</option>)}</Select><input value={f.probe_interval_seconds} onChange={e => setF({ ...f, probe_interval_seconds: Number(e.target.value) })} placeholder="探测间隔秒" /><input value={f.sample_rate} onChange={e => setF({ ...f, sample_rate: Number(e.target.value) })} placeholder="采样率 0-1" /><input value={f.priority} onChange={e => setF({ ...f, priority: Number(e.target.value) })} placeholder="优先级" /><input value={f.config_json} onChange={e => setF({ ...f, config_json: e.target.value })} placeholder="JSON 配置" /><button onClick={submit}>创建</button></div><Table rows={forwardRows} actions={(r: any) => <><button onClick={() => void probeForwardNow(client, r._raw, load, notify)}>立即探测</button><button onClick={() => remove(client, `/port-forwards/${r._raw.id}`, load, dialogs, r._raw)}>删除</button></>} /><h3>探测结果</h3><Table rows={probeRows} /></Panel>
}

function Tunnels({ data, client, load }: any) {
  const dialogs = useDialogs()
  const [f, setF] = useState({ name: 'tunnel-1', source_server_id: 0, target_server_id: 0, type: 'wireguard' as TunnelType, local_address: '', peer_address: '', listen_port: 0, target_endpoint: '', target_port: 0, priority: 100, config_json: '{}', enabled: true })
  const submit = async () => { await client.request('/tunnels', { method: 'POST', body: JSON.stringify(f) }); await load() }
  return <Panel title="隧道"><p className="muted">独立隧道使用 WireGuard。代理路径中的 SSH 由系统自动创建专用账户和密钥。</p><div className="form"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="名称" /><Select value={f.source_server_id} onChange={e => setF({ ...f, source_server_id: Number(e.target.value) })}><option value={0}>源服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><Select value={f.target_server_id} onChange={e => setF({ ...f, target_server_id: Number(e.target.value) })}><option value={0}>目标服务器</option>{(data.servers || []).map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><Select variant="segmented" value={f.type} onChange={e => setF({ ...f, type: e.target.value as TunnelType })}><option value="wireguard">WireGuard</option></Select><input value={f.local_address} onChange={e => setF({ ...f, local_address: e.target.value })} placeholder="本地地址" /><input value={f.peer_address} onChange={e => setF({ ...f, peer_address: e.target.value })} placeholder="对端地址 / 允许 IP" /><input value={f.listen_port} onChange={e => setF({ ...f, listen_port: Number(e.target.value) })} placeholder="监听端口" /><input value={f.target_endpoint} onChange={e => setF({ ...f, target_endpoint: e.target.value })} placeholder="目标端点，可选" /><input value={f.target_port} onChange={e => setF({ ...f, target_port: Number(e.target.value) })} placeholder="目标端口" /><input value={f.priority} onChange={e => setF({ ...f, priority: Number(e.target.value) })} placeholder="优先级" /><input value={f.config_json} onChange={e => setF({ ...f, config_json: e.target.value })} placeholder="JSON 配置" /><button onClick={submit}>创建</button></div><Table rows={data.tunnels || []} actions={(r: Tunnel) => <button onClick={() => remove(client, `/tunnels/${r.id}`, load, dialogs, r)}>删除</button>} /></Panel>
}

const subscriptionClientIcons: Record<string, string> = {
  'sing-box': singBoxClientIcon,
  'clash-meta': clashMetaClientIcon,
  mihomo: clashMetaClientIcon,
  stash: stashClientIcon,
  surge: surgeClientIcon,
  'surge-mac': surgeClientIcon,
  shadowrocket: shadowrocketClientIcon,
  qx: quantumultXClientIcon,
  loon: loonClientIcon,
  surfboard: surfboardClientIcon,
  egern: egernClientIcon,
  v2ray: v2rayNClientIcon,
  'v2ray-uri': v2rayNClientIcon,
  clash: clashClassicClientIcon,
}

let subscriptionClientIconsReady: Promise<void> | null = null

function preloadSubscriptionClientIcons() {
  if (subscriptionClientIconsReady) return subscriptionClientIconsReady
  const sources = Array.from(new Set(Object.values(subscriptionClientIcons)))
  subscriptionClientIconsReady = Promise.all(sources.map(source => new Promise<void>(resolve => {
    const image = new Image()
    const finish = () => resolve()
    image.onload = () => {
      if (typeof image.decode === 'function') image.decode().then(finish, finish)
      else finish()
    }
    image.onerror = finish
    image.src = source
  }))).then(() => undefined)
  return subscriptionClientIconsReady
}

function renderFormatIcon(id: string) {
  const source = subscriptionClientIcons[id] || clashClassicClientIcon
  return <img className="subscription-client-icon" src={source} alt="" aria-hidden="true" width="42" height="42" />
}

function isAgeSubscriptionFormat(format: SubscriptionFormat) {
  return format === 'mihomo' || format === 'clash-meta' || format === 'clash'
}

function subscriptionURLForToken(token: string, format: SubscriptionFormat, encrypted = false) {
  if (!token) return ''
	const base = `${appControllerURL()}/api/v1/subscriptions/${token}`
	const params = new URLSearchParams()
  if (format !== 'sing-box') params.set('format', format)
  if (encrypted) params.set('age', '1')
	const query = params.toString()
	return query ? `${base}?${query}` : base
}

function subscriptionURLForUser(user: User, format: SubscriptionFormat, encrypted = false) {
  return subscriptionURLForToken(user.subscription_token, format, encrypted)
}

function proxyPathChainLabels(data: any, path: ProxyPath): string[] {
  const servers: Server[] = data.servers || []
  const inbounds: Inbound[] = data.inbounds || []
  const externals: ExternalOutbound[] = data.external_outbounds || []
  const root = inbounds.find(x => x.id === path.inbound_id)
  const rootServer = root ? servers.find(s => s.id === root.server_id) : undefined
  const labels = [rootServer?.name || root?.name || `入口 #${path.inbound_id}`]
  const steps = ((data.proxy_path_steps || []) as ProxyPathStep[])
    .filter(step => step.path_id === path.id)
    .slice()
    .sort((a, b) => (a.position - b.position) || (a.id - b.id))
  steps.forEach(step => {
    if (step.node_type === 'imported' && step.external_outbound_id) {
      const node = externals.find(x => x.id === step.external_outbound_id)
      labels.push(node?.name || `导入 #${step.external_outbound_id}`)
      return
    }
    if (step.inbound_id) {
      const inbound = inbounds.find(x => x.id === step.inbound_id)
      const server = inbound ? servers.find(s => s.id === inbound.server_id) : undefined
      labels.push(server?.name || inbound?.name || `入口 #${step.inbound_id}`)
      return
    }
    if (step.server_id) {
      const server = servers.find(s => s.id === step.server_id)
      labels.push(server?.name || `服务器 #${step.server_id}`)
    }
  })
  return labels
}

function Subscriptions({ data, client, load, notify }: any) {
  const dialogs = useDialogs()
  const [iconsReady, setIconsReady] = useState(false)
  const [subscriptionFormat, setSubscriptionFormat] = useState<SubscriptionFormat>('sing-box')
  const [expandedServers, setExpandedServers] = useState<Record<number, boolean>>({})
  const [selectedInboundIDs, setSelectedInboundIDs] = useState<number[]>([])
  const [selectedUserIDs, setSelectedUserIDs] = useState<number[]>([])
  const [selectedGroupIDs, setSelectedGroupIDs] = useState<number[]>([])
  const [activeProfileID, setActiveProfileID] = useState<number>(0)
  const [newGroupName, setNewGroupName] = useState('')
  const [creatingGroup, setCreatingGroup] = useState(false)
  const [assigning, setAssigning] = useState(false)
  const [namingPath, setNamingPath] = useState<ProxyPath | null>(null)

  const users: User[] = data.users || []
  const servers: Server[] = data.servers || []
  const inbounds: Inbound[] = (data.inbounds || []).filter((x: Inbound) => x.enabled !== false && x.protocol !== 'ssh')
  const sshInbounds: Inbound[] = (data.inbounds || []).filter((x: Inbound) => x.enabled !== false && x.protocol === 'ssh')
  const sshUserKeys: SSHUserKey[] = data.ssh_user_keys || []
  const profiles: SubscriptionProfile[] = data.subscription_profiles || []
  const assignments: SubscriptionAssignment[] = data.subscription_assignments || []
  const userGroups: UserGroup[] = data.user_groups || []
  const members: UserGroupMember[] = data.user_group_members || []
  const paths: ProxyPath[] = (data.proxy_paths || []).filter((p: ProxyPath) => p.enabled !== false)
  const agePolicy = data.settings?.subscription_age_policy === 'required' ? 'required' : 'optional'
  const ageRequired = agePolicy === 'required'

  useEffect(() => {
    let active = true
    preloadSubscriptionClientIcons().then(() => {
      if (active) setIconsReady(true)
    })
    return () => { active = false }
  }, [])

  useEffect(() => {
    if (!activeProfileID && profiles[0]?.id) setActiveProfileID(profiles[0].id)
    if (activeProfileID && !profiles.some(p => p.id === activeProfileID)) {
      setActiveProfileID(profiles[0]?.id || 0)
    }
  }, [profiles.length, activeProfileID])

  const entryServers = useMemo(() => {
    const byServer = new Map<number, Inbound[]>()
    inbounds.forEach(inbound => {
      const list = byServer.get(inbound.server_id) || []
      list.push(inbound)
      byServer.set(inbound.server_id, list)
    })
    return servers
      .filter(s => byServer.has(s.id))
      .slice()
      .sort((a, b) => String(a.name || a.id).localeCompare(String(b.name || b.id), 'zh'))
      .map(server => {
        const serverInbounds = (byServer.get(server.id) || []).slice().sort((a, b) => (a.port - b.port) || (a.id - b.id))
        const serverPaths = paths.filter(path => {
          const root = inbounds.find(x => x.id === path.inbound_id)
          return root?.server_id === server.id
        })
        return { server, inbounds: serverInbounds, paths: serverPaths }
      })
  }, [servers, inbounds, paths])

  const activeProfile = profiles.find(p => p.id === activeProfileID) || null

  const clientFormats = [
    { id: 'sing-box', name: 'sing-box', type: 'Native JSON' },
    { id: 'clash-meta', name: 'Clash.Meta', type: 'YAML Config' },
    { id: 'mihomo', name: 'Mihomo', type: 'YAML Config' },
    { id: 'stash', name: 'Stash', type: 'YAML' },
    { id: 'surge', name: 'Surge', type: 'Conf' },
    { id: 'shadowrocket', name: 'Shadowrocket', type: 'URI' },
    { id: 'qx', name: 'Quantumult X', type: 'URI' },
    { id: 'loon', name: 'Loon', type: 'URI' },
    { id: 'surfboard', name: 'Surfboard', type: 'Conf' },
    { id: 'egern', name: 'Egern', type: 'YAML' },
    { id: 'v2ray-uri', name: 'V2Ray URI', type: 'URI' },
    { id: 'clash', name: 'Clash', type: 'YAML' },
  ]

  const toggleServerExpand = (serverID: number) => {
    setExpandedServers(prev => ({ ...prev, [serverID]: !prev[serverID] }))
  }

  const toggleInbound = (inboundID: number) => {
    setSelectedInboundIDs(prev => prev.includes(inboundID) ? prev.filter(id => id !== inboundID) : [...prev, inboundID])
  }

  const toggleServerInbounds = (serverInbounds: Inbound[]) => {
    const ids = serverInbounds.map(x => x.id)
    const allSelected = ids.every(id => selectedInboundIDs.includes(id))
    setSelectedInboundIDs(prev => allSelected
      ? prev.filter(id => !ids.includes(id))
      : Array.from(new Set([...prev, ...ids])))
  }

  const createGroup = async () => {
    const name = newGroupName.trim()
    if (!name) {
      notify?.('请填写分组名称', 'warning')
      return
    }
    setCreatingGroup(true)
    try {
      const res = await client.request('/subscription-profiles', {
        method: 'POST',
        body: JSON.stringify({ name, group_name: name, description: '', config_json: '{}', enabled: true }),
      })
      const created = res.subscription_profile as SubscriptionProfile | undefined
      setNewGroupName('')
      await load()
      if (created?.id) setActiveProfileID(created.id)
      notify?.(`已创建分组「${name}」`, 'success')
    } catch (e: any) {
      notify?.(localizeErrorMessage(e.message || e), 'error')
    } finally {
      setCreatingGroup(false)
    }
  }

  const resolveTargetUserIDs = () => {
    const ids = new Set(selectedUserIDs)
    selectedGroupIDs.forEach(groupID => {
      members.filter(m => m.group_id === groupID && m.enabled !== false).forEach(m => ids.add(m.user_id))
    })
    return Array.from(ids)
  }

  const assignSelection = async () => {
    if (!activeProfileID) {
      notify?.('请先选择或创建一个订阅分组', 'warning')
      return
    }
    if (!selectedInboundIDs.length) {
      notify?.('请先勾选要分配的入口节点', 'warning')
      return
    }
    const targetUsers = resolveTargetUserIDs()
    if (!targetUsers.length) {
      notify?.('请选择目标用户或用户组', 'warning')
      return
    }
    setAssigning(true)
    try {
      const groupName = activeProfile?.group_name || activeProfile?.name || 'default'
      let created = 0
      for (const userID of targetUsers) {
        for (const inboundID of selectedInboundIDs) {
          const exists = assignments.some(a => a.enabled !== false && a.profile_id === activeProfileID && a.user_id === userID && a.inbound_id === inboundID)
          if (exists) continue
          await client.request('/subscription-assignments', {
            method: 'POST',
            body: JSON.stringify({
              profile_id: activeProfileID,
              user_id: userID,
              inbound_id: inboundID,
              group_name: groupName,
              enabled: true,
            }),
          })
          created++
        }
      }
      await load()
      notify?.(created ? `已分配 ${created} 条订阅规则` : '所选规则均已存在，未重复创建', created ? 'success' : 'info')
    } catch (e: any) {
      notify?.(localizeErrorMessage(e.message || e), 'error')
    } finally {
      setAssigning(false)
    }
  }

  const configureUserAge = async (user: User) => {
    const publicKey = await dialogs.prompt({
      title: `配置 ${user.username} 的 Age 公钥`,
      message: '只粘贴公钥（age1... 或 age1pq1...），不要粘贴 AGE-SECRET-KEY 私钥。清空可关闭。',
      defaultValue: user.subscription_age_public_key || '',
      placeholder: 'age1...',
    })
    if (publicKey === null) return
    const value = publicKey.trim()
    if (ageRequired && !value) {
      notify?.('强制加密模式下不能清空 Age 公钥', 'warning')
      return
    }
    try {
      await client.request(`/users/${user.id}/subscription-age`, { method: 'PATCH', body: JSON.stringify({ enabled: Boolean(value), public_key: value }) })
      await load()
      notify?.(value ? `${user.username} 的 Age 公钥已保存` : `${user.username} 的 Age 加密已关闭`, 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    }
  }

  const addSSHUserKey = async (user: User) => {
    const publicKey = await dialogs.prompt({ title: `添加 ${user.username} 的 SSH 公钥`, message: '仅粘贴公钥（ssh-ed25519、ssh-rsa 等），不要粘贴私钥。此密钥用于 SSH 受限代理入口。', placeholder: 'ssh-ed25519 AAAA...' })
    if (publicKey === null || !publicKey.trim()) return
    const name = await dialogs.prompt({ title: '密钥名称', message: '用于识别此公钥。', defaultValue: '默认密钥', placeholder: '例如：MacBook' })
    if (name === null) return
    try {
      await client.request('/ssh-user-keys', { method: 'POST', body: JSON.stringify({ user_id: user.id, name: name.trim() || '默认密钥', public_key: publicKey.trim(), enabled: true }) })
      await load()
      notify?.('SSH 公钥已添加', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    }
  }

  const sshCommandFor = (inbound: Inbound, user: User) => {
    const address = inboundEntryAddress(data, inbound)
    const host = address.includes(':') && !address.startsWith('[') ? `[${address}]` : address
    return `ssh -N -D 127.0.0.1:1080 -p ${inbound.port} oboard-${user.id}@${host}`
  }

  const copyUserSub = async (user: User, encrypted = false) => {
    const ageCapable = isAgeSubscriptionFormat(subscriptionFormat)
    const useAge = ageCapable && (encrypted || ageRequired)
    if (useAge && !user.subscription_age_public_key) {
      notify?.(`${user.username} 尚未配置 Age 公钥`, 'warning')
      return
    }
    if (useAge && !ageRequired && !user.subscription_age_enabled) {
      notify?.(`${user.username} 尚未开启 Age 加密`, 'warning')
      return
    }
    const url = subscriptionURLForUser(user, subscriptionFormat, useAge)
    if (!url) {
      notify?.(`${user.username} 尚无有效订阅令牌`, 'warning')
      return
    }
    const ok = await copyText(url)
    const copiedMessage = `${useAge ? 'Age 加密' : '普通'}订阅链接已复制${user.subscription_burn_after_read ? '，首次读取后失效' : ''}`
    notify?.(ok ? copiedMessage : '复制失败，请手动复制', ok ? 'success' : 'error')
  }

  const assignmentSummaryForInbound = (inboundID: number) => {
    const related = assignments.filter(a => a.enabled !== false && a.inbound_id === inboundID)
    if (!related.length) return '未分配'
    const userNames = related.map(a => users.find(u => u.id === a.user_id)?.username || `#${a.user_id}`)
    const unique = Array.from(new Set(userNames))
    if (unique.length <= 2) return unique.join('、')
    return `${unique.slice(0, 2).join('、')} 等 ${unique.length} 人`
  }

  return <>
    <Panel title="订阅" className="subscriptions-panel">
      {!iconsReady ? (
        <div className="subscription-page-loading" role="status" aria-live="polite">
          <RefreshCw aria-hidden="true" />
          <strong>正在加载订阅页面</strong>
          <span>图标准备完成后将自动显示</span>
        </div>
      ) : (
        <div className="subscription-page-content">
          <section className="sub-section">
            <div className="sub-section-head">
              <div>
                <h3><LinkIcon size={16} />客户端格式</h3>
                <p className="muted">复制订阅时使用的客户端格式，可随时切换。</p>
              </div>
            </div>
            <div className="sub-format-grid">
              {clientFormats.map(fmt => {
                const active = subscriptionFormat === fmt.id
                return (
                  <button key={fmt.id} type="button" className={`sub-format-card ${active ? 'active' : ''}`} onClick={() => setSubscriptionFormat(fmt.id as SubscriptionFormat)}>
                    <div className="subscription-client-icon-shell">{renderFormatIcon(fmt.id)}</div>
                    <strong>{fmt.name}</strong>
                    <span>{fmt.type}</span>
                  </button>
                )
              })}
            </div>
          </section>

          <section className="sub-section">
            <div className="sub-section-head">
              <div>
                <h3><UsersIcon size={16} />用户订阅</h3>
                <p className="muted">不直接展示完整链接，按需复制即可。当前格式：{subscriptionFormats.find(x => x.value === subscriptionFormat)?.label || subscriptionFormat}</p>
              </div>
            </div>
            <div className="sub-user-table">
              <div className="sub-user-table-head">
                <span>用户</span>
                <span>状态</span>
                <span>订阅</span>
                <span>安全策略</span>
                <span>操作</span>
              </div>
              {users.length ? users.map(user => (
                <div className="sub-user-row" key={user.id}>
                  <div className="sub-user-main">
                    <span className="sub-user-avatar">{String(user.username || '?').slice(0, 1).toUpperCase()}</span>
                    <div>
                      <strong>{user.username}</strong>
                      <small>#{user.id}</small>
                    </div>
                  </div>
                  <div>{cell(user.status, 'status')}</div>
                  <div className="sub-user-token-state">
                    {user.subscription_token
                      ? user.subscription_burn_after_read
                        ? <span className="sub-pill warn">一次性</span>
                        : <span className="sub-pill ok">长期有效</span>
                      : user.subscription_burned_at
                        ? <span className="sub-pill danger">已焚毁</span>
                        : <span className="sub-pill warn">已吊销</span>}
                  </div>
                  <div className="sub-security-stack">
                    <label className="subscription-burn-toggle" title="开启后，链接首次成功获取订阅内容便立即失效">
                      <input
                        type="checkbox"
                        checked={Boolean(user.subscription_burn_after_read)}
                        onChange={() => void setSubscriptionBurnPolicy(client, user, !user.subscription_burn_after_read, load, dialogs, notify)}
                      />
                      <span className="setting-switch" aria-hidden="true"><span /></span>
                      <span>阅后即焚</span>
                    </label>
                    <button type="button" className="age-key-status" onClick={() => void configureUserAge(user)}>
                      <Shield size={13} />{user.subscription_age_public_key ? ageRequired ? 'Age · 强制' : user.subscription_age_enabled ? 'Age · 已开启' : 'Age · 已保存' : '配置 Age 公钥'}
                    </button>
                  </div>
                  <div className="sub-user-actions">
                    {(!isAgeSubscriptionFormat(subscriptionFormat) || !ageRequired) && <button type="button" className="ghost" onClick={() => void copyUserSub(user, false)} disabled={!user.subscription_token}>普通链接</button>}
                    {isAgeSubscriptionFormat(subscriptionFormat) && <button type="button" className="ghost" onClick={() => void copyUserSub(user, true)} disabled={!user.subscription_token || !user.subscription_age_public_key || (!ageRequired && !user.subscription_age_enabled)}>Age 链接</button>}
                    <QuickOneTimeSubscriptionButton user={user} client={client} format={subscriptionFormat} encrypted={isAgeSubscriptionFormat(subscriptionFormat) && ageRequired} notify={notify} />
                    <button type="button" className="ghost" onClick={() => void rotateSub(client, user, load, dialogs, notify)}>{user.subscription_token ? '轮换' : '重新签发'}</button>
                    <button type="button" className="ghost danger-text" onClick={() => void revokeSub(client, user, load, dialogs, notify)} disabled={!user.subscription_token}>吊销</button>
                  </div>
                </div>
              )) : <div className="sub-empty">暂无用户</div>}
            </div>
          </section>

          {sshInbounds.length > 0 && <section className="sub-section">
            <div className="sub-section-head">
              <div>
                <h3><Lock size={16} />SSH 受限代理</h3>
                <p className="muted">SSH 不写入常规代理订阅；每个用户使用自己的公钥和独立登录名。仅支持本地/动态转发，不提供 shell、SFTP 或远程转发。</p>
              </div>
            </div>
            <div className="sub-user-table">
              {sshInbounds.map(inbound => {
                const granted = Array.from(effectiveInboundUserIDs(data, inbound)).map(id => users.find(user => user.id === id)).filter(Boolean) as User[]
                const endpoint = formatHostPort(inboundEntryAddress(data, inbound), inbound.port)
                return <div className="sub-user-row" key={inbound.id}>
                  <div className="sub-user-main"><span className="sub-user-avatar"><Lock size={14} /></span><div><strong>{inbound.name}</strong><small>{endpoint}</small></div></div>
                  <div className="sub-security-stack">
                    {granted.length ? granted.map(user => {
                      const keys = sshUserKeys.filter(key => key.user_id === user.id && key.enabled !== false)
                      return <div key={user.id} className="sub-user-actions"><span className={`sub-pill ${keys.length ? 'ok' : 'warn'}`}>{user.username} · {keys.length ? `${keys.length} 把公钥` : '未配置公钥'}</span><button type="button" className="ghost" onClick={() => void addSSHUserKey(user)}>添加公钥</button>{keys.map(key => <button type="button" className="ghost danger-text" key={key.id} title={key.fingerprint} onClick={async () => { await client.request(`/ssh-user-keys/${key.id}`, { method: 'DELETE' }); await load() }}>移除 {key.name}</button>)}</div>
                    }) : <span className="sub-pill warn">暂无授权用户</span>}
                  </div>
                  <div className="sub-user-actions">
                    {granted.map(user => <button type="button" className="ghost" key={user.id} disabled={!sshUserKeys.some(key => key.user_id === user.id && key.enabled !== false)} onClick={() => void copyText(sshCommandFor(inbound, user)).then(ok => notify?.(ok ? `${user.username} 的 SSH 命令已复制` : '复制失败', ok ? 'success' : 'error'))}>复制 {user.username} 命令</button>)}
                  </div>
                </div>
              })}
            </div>
          </section>}

          <section className="sub-section sub-assign-layout">
            <div className="sub-assign-main">
              <div className="sub-section-head">
                <div>
                  <h3><ServerIcon size={16} />入口服务器与代理链路</h3>
                  <p className="muted">展开查看入口节点和简单代理链，勾选后分配到右侧分组与用户。</p>
                </div>
                <div className="sub-selection-meta">
                  已选 <strong>{selectedInboundIDs.length}</strong> 个入口
                  {selectedInboundIDs.length > 0 && (
                    <button type="button" className="ghost" onClick={() => setSelectedInboundIDs([])}>清空选择</button>
                  )}
                </div>
              </div>

              {!entryServers.length ? (
                <div className="sub-empty large">还没有可用入口。请先在代理链路中创建入口节点。</div>
              ) : (
                <div className="sub-server-list">
                  {entryServers.map(({ server, inbounds: serverInbounds, paths: serverPaths }) => {
                    const open = Boolean(expandedServers[server.id])
                    const selectedCount = serverInbounds.filter(x => selectedInboundIDs.includes(x.id)).length
                    const allSelected = serverInbounds.length > 0 && selectedCount === serverInbounds.length
                    return (
                      <article key={server.id} className={`sub-server-card ${open ? 'open' : ''}`}>
                        <div className="sub-server-head">
                          <label className="sub-check">
                            <input
                              type="checkbox"
                              checked={allSelected}
                              ref={el => { if (el) el.indeterminate = selectedCount > 0 && !allSelected }}
                              onChange={() => toggleServerInbounds(serverInbounds)}
                            />
                          </label>
                          <button type="button" className="sub-server-toggle" onClick={() => toggleServerExpand(server.id)}>
                            <div className="sub-server-title">
                              <strong>{server.name || `服务器 #${server.id}`}</strong>
                              <span>{labelValue(server.status || 'unknown')} · {serverInbounds.length} 个入口 · {serverPaths.length} 条链路</span>
                            </div>
                            <ChevronRight size={16} className={open ? 'task-chevron open' : 'task-chevron'} />
                          </button>
                        </div>
                        {open && (
                          <div className="sub-server-body">
                            {serverInbounds.map(inbound => {
                              const inboundPaths = serverPaths.filter(p => p.inbound_id === inbound.id)
                              const checked = selectedInboundIDs.includes(inbound.id)
                              return (
                                <div key={inbound.id} className={`sub-entry-block ${checked ? 'selected' : ''}`}>
                                  <label className="sub-entry-row">
                                    <input type="checkbox" checked={checked} onChange={() => toggleInbound(inbound.id)} />
                                    <div className="sub-entry-copy">
                                      <strong>{inbound.name || `${labelProtocol(inbound.protocol)}:${inbound.port}`}</strong>
                                      <span>{labelProtocol(inbound.protocol)} · 端口 {inbound.port} · {assignmentSummaryForInbound(inbound.id)}</span>
                                    </div>
                                  </label>
                                  <div className="sub-chain-list">
                                    {inboundPaths.length ? inboundPaths.map(path => {
                                      const hops = proxyPathChainLabels(data, path)
                                      return (
                                        <div key={path.id} className="sub-chain-row">
                                          <span className="sub-chain-name">{path.name || `链路 #${path.id}`}</span>
                                          <div className="sub-chain-hops">
                                            {hops.map((hop, index) => (
                                              <React.Fragment key={`${path.id}-${index}`}>
                                                {index > 0 && <span className="sub-chain-arrow">→</span>}
                                                <span className="sub-chain-hop">{hop}</span>
                                              </React.Fragment>
                                            ))}
                                          </div>
										  <button type="button" className="ghost icon-button sub-chain-name-edit" onClick={() => setNamingPath(path)} aria-label={`编辑 ${path.name || `链路 #${path.id}`} 名称`} title="编辑链路名称"><Edit3 size={14} /></button>
                                        </div>
                                      )
                                    }) : (
                                      <div className="sub-chain-row muted-row">
                                        <span>直连入口节点（暂无后续代理链路）</span>
                                      </div>
                                    )}
                                  </div>
                                </div>
                              )
                            })}
                          </div>
                        )}
                      </article>
                    )
                  })}
                </div>
              )}
            </div>

            <aside className="sub-assign-side">
              <div className="sub-side-card">
                <div className="sub-section-head compact">
                  <div>
                    <h3>订阅分组</h3>
                    <p className="muted">创建分组后，将勾选的入口分配给用户或用户组。</p>
                  </div>
                </div>
                <div className="sub-group-create">
                  <input value={newGroupName} onChange={e => setNewGroupName(e.target.value)} placeholder="新分组名称，例如 香港线路" />
                  <button type="button" onClick={() => void createGroup()} disabled={creatingGroup}>{creatingGroup ? '创建中…' : '创建'}</button>
                </div>
                <div className="sub-group-list">
                  {profiles.length ? profiles.map(profile => (
                    <button
                      key={profile.id}
                      type="button"
                      className={`sub-group-item ${activeProfileID === profile.id ? 'active' : ''}`}
                      onClick={() => setActiveProfileID(profile.id)}
                    >
                      <div>
                        <strong>{profile.name}</strong>
                        <span>{profile.group_name || profile.name} · {assignments.filter(a => a.profile_id === profile.id && a.enabled !== false).length} 条规则</span>
                      </div>
                      <button
                        type="button"
                        className="ghost icon-button danger-text"
                        title="删除分组"
                        onClick={e => {
                          e.stopPropagation()
                          void remove(client, `/subscription-profiles/${profile.id}`, load, dialogs, profile)
                        }}
                      >
                        <Trash2 size={14} />
                      </button>
                    </button>
                  )) : <div className="sub-empty">还没有分组，先创建一个。</div>}
                </div>
              </div>

              <div className="sub-side-card">
                <div className="sub-section-head compact">
                  <div>
                    <h3>分配给</h3>
                    <p className="muted">可同时选择用户与用户组（用户组会展开为成员）。</p>
                  </div>
                </div>
                <div className="sub-target-block">
                  <span className="sub-target-label">用户组</span>
                  <div className="sub-chip-list">
                    {userGroups.length ? userGroups.map(group => {
                      const active = selectedGroupIDs.includes(group.id)
                      const count = members.filter(m => m.group_id === group.id && m.enabled !== false).length
                      return (
                        <button key={group.id} type="button" className={`sub-chip ${active ? 'active' : ''}`} onClick={() => setSelectedGroupIDs(prev => active ? prev.filter(id => id !== group.id) : [...prev, group.id])}>
                          {group.name}<small>{count}</small>
                        </button>
                      )
                    }) : <span className="muted">暂无用户组</span>}
                  </div>
                </div>
                <div className="sub-target-block">
                  <span className="sub-target-label">用户</span>
                  <div className="sub-chip-list">
                    {users.map(user => {
                      const active = selectedUserIDs.includes(user.id)
                      return (
                        <button key={user.id} type="button" className={`sub-chip ${active ? 'active' : ''}`} onClick={() => setSelectedUserIDs(prev => active ? prev.filter(id => id !== user.id) : [...prev, user.id])}>
                          {user.username}
                        </button>
                      )
                    })}
                  </div>
                </div>
                <button type="button" className="sub-assign-submit" onClick={() => void assignSelection()} disabled={assigning}>
                  {assigning ? '分配中…' : `分配到${activeProfile ? `「${activeProfile.name}」` : '分组'}`}
                </button>
              </div>
            </aside>
          </section>
        </div>
      )}
    </Panel>
	<AnimatePresence>{namingPath && <ProxyPathNameDialog path={namingPath} data={data} client={client} load={load} onClose={() => setNamingPath(null)} />}</AnimatePresence>
  </>
}

const fallbackNotificationEventOptions: NotificationEventDefinition[] = [
  { value: 'server_offline', label: '服务器失联', description: '服务器超过两分钟未连接时提醒', variables: ['ServerName', 'ServerID', 'LastSeen', 'Time'] },
  { value: 'server_online', label: '服务器恢复', description: '失联服务器重新连接时提醒', variables: ['ServerName', 'ServerID', 'Time'] },
  { value: 'traffic_quota_exceeded', label: '流量达到上限', description: '所选用户的周期流量达到上限时提醒', variables: ['UserName', 'UserID', 'Used', 'Limit', 'ResetAt', 'Time'] },
  { value: 'user_risk_detected', label: '异常使用', description: '已开启连接审计的服务器发现所选用户大量来源 IP、跨网段或异常并发时提醒', variables: ['UserName', 'UserID', 'RiskLevel', 'RiskScore', 'Signals', 'SourceIPCount', 'ActivePeak', 'Time'] },
  { value: 'task_failed', label: '任务失败', description: '配置下发、更新或检测任务失败时提醒', variables: ['TaskType', 'TaskID', 'ServerName', 'Error', 'Time'] },
  { value: 'task_timeout', label: '任务超时', description: '任务等待或执行超过五分钟时提醒', variables: ['TaskType', 'TaskID', 'ServerName', 'Error', 'Time'] },
  { value: 'certificate_issuance_failed', label: '证书签发失败', description: '证书首次签发或自动续期失败时提醒', variables: ['CertificateName', 'Domains', 'Issuer', 'EABKeyID', 'Error', 'Time'] },
  { value: 'certificate_expiring', label: '证书到期', description: '证书有效期不足三十天或已经到期时提醒', variables: ['CertificateName', 'Domains', 'Issuer', 'ExpiresAt', 'ExpiryStatus', 'Time'] },
  { value: 'backup_failed', label: '自动备份失败', description: '本地自动备份或第三方上传未完成时提醒', variables: ['Stage', 'Error', 'Time'] },
  { value: 'controller_update_failed', label: '主控自动更新失败', description: '自动检查、备份或安装主控更新失败时提醒', variables: ['Stage', 'CurrentVersion', 'TargetVersion', 'Error', 'Time'] },
  { value: 'dns_sync_failed', label: '域名自动更新失败', description: '入口域名记录自动更新失败时提醒', variables: ['InboundName', 'Domain', 'ServerName', 'Error', 'Time'] },
  { value: 'admin_announcement', label: '管理员通知', description: '管理员向你发送消息时提醒', variables: ['Title', 'Message', 'Sender', 'Time'] },
]

const fallbackNotificationTemplates: Record<string, NotificationTemplate> = {
  server_offline: { title: '服务器失联 · {{.ServerName}}', body: '{{.ServerName}} 已失去连接\n最后在线：{{.LastSeen}}\n时间：{{.Time}}' },
  server_online: { title: '服务器恢复 · {{.ServerName}}', body: '{{.ServerName}} 已恢复在线\n时间：{{.Time}}' },
  traffic_quota_exceeded: { title: '流量达到上限 · {{.UserName}}', body: '{{.UserName}} 本周期流量已达到上限\n已用：{{.Used}} / {{.Limit}}\n重置：{{.ResetAt}}' },
  user_risk_detected: { title: '异常使用提醒 · {{.UserName}}', body: '{{.UserName}} 的连接行为达到{{.RiskLevel}}\n风险分：{{.RiskScore}}\n异常表现：{{.Signals}}\n来源 IP：{{.SourceIPCount}} 个\n并发峰值：{{.ActivePeak}}\n时间：{{.Time}}' },
  task_failed: { title: '任务失败 · {{.TaskType}}', body: '服务器：{{.ServerName}}\n任务：#{{.TaskID}} {{.TaskType}}\n原因：{{.Error}}\n时间：{{.Time}}' },
  task_timeout: { title: '任务超时 · {{.TaskType}}', body: '服务器：{{.ServerName}}\n任务：#{{.TaskID}} {{.TaskType}}\n原因：{{.Error}}\n时间：{{.Time}}' },
  certificate_issuance_failed: { title: '证书签发失败 · {{.CertificateName}}', body: '证书：{{.CertificateName}}\n域名：{{.Domains}}\n签发机构：{{.Issuer}}\n外部账号：{{.EABKeyID}}\n原因：{{.Error}}\n时间：{{.Time}}' },
  certificate_expiring: { title: '证书到期提醒 · {{.CertificateName}}', body: '证书：{{.CertificateName}}\n域名：{{.Domains}}\n签发机构：{{.Issuer}}\n状态：{{.ExpiryStatus}}\n到期时间：{{.ExpiresAt}}' },
  backup_failed: { title: '自动备份失败 · {{.Stage}}', body: '{{.Stage}}未完成\n原因：{{.Error}}\n时间：{{.Time}}' },
  controller_update_failed: { title: '主控自动更新失败 · {{.Stage}}', body: '当前版本：{{.CurrentVersion}}\n目标版本：{{.TargetVersion}}\n阶段：{{.Stage}}\n原因：{{.Error}}\n时间：{{.Time}}' },
  dns_sync_failed: { title: '域名自动更新失败 · {{.Domain}}', body: '服务器：{{.ServerName}}\n入口：{{.InboundName}}\n域名：{{.Domain}}\n原因：{{.Error}}\n时间：{{.Time}}' },
  admin_announcement: { title: '{{.Title}}', body: '{{.Message}}\n\n来自：{{.Sender}}' },
}

const userScopedNotificationEvents = new Set(['traffic_quota_exceeded', 'user_risk_detected'])

type NotificationDraft = {
  id: number
  name: string
  type: 'telegram' | 'bark'
  enabled: boolean
  events: string[]
  bot_token: string
  chat_id: string
  server_url: string
  device_key: string
  templates: Record<string, NotificationTemplate>
  user_ids: number[]
}

function mergedNotificationTemplates(raw: string | undefined, defaults: Record<string, NotificationTemplate>) {
  let parsed: Record<string, NotificationTemplate> = {}
  try { parsed = JSON.parse(raw || '{}') } catch { parsed = {} }
  return Object.fromEntries(Object.entries(defaults).map(([event, value]) => [event, { ...value, ...(parsed[event] || {}) }]))
}

function emptyNotificationDraft(defaults: Record<string, NotificationTemplate>, eventOptions: NotificationEventDefinition[], isAdmin: boolean, ownerUserID: number, type: 'telegram' | 'bark' = 'telegram'): NotificationDraft {
  return {
    id: 0,
    name: '',
    type,
    enabled: true,
    events: eventOptions.filter(option => !isAdmin || option.value !== 'admin_announcement').map(option => option.value),
    bot_token: '',
    chat_id: '',
    server_url: 'https://api.day.app',
    device_key: '',
    templates: mergedNotificationTemplates('{}', defaults),
    user_ids: [ownerUserID],
  }
}

function notificationDraftFromChannel(channel: NotificationChannel, defaults: Record<string, NotificationTemplate>): NotificationDraft {
  let cfg: any = {}
  try { cfg = JSON.parse(channel.config_json || '{}') } catch { cfg = {} }
  return {
    id: channel.id,
    name: channel.name || '',
    type: channel.type === 'bark' ? 'bark' : 'telegram',
    enabled: channel.enabled !== false,
    events: String(channel.events || '').split(',').map(x => x.trim()).filter(Boolean),
    bot_token: String(cfg.bot_token || ''),
    chat_id: String(cfg.chat_id ?? ''),
    server_url: String(cfg.server_url || 'https://api.day.app'),
    device_key: String(cfg.device_key || ''),
    templates: mergedNotificationTemplates(channel.templates_json, defaults),
    user_ids: channel.user_ids || [],
  }
}

function notificationPayloadFromDraft(draft: NotificationDraft) {
  const events = draft.events.join(',')
  const config = draft.type === 'telegram'
    ? { bot_token: draft.bot_token.trim(), chat_id: draft.chat_id.trim() }
    : { server_url: draft.server_url.trim() || 'https://api.day.app', device_key: draft.device_key.trim() }
  return {
    name: draft.name.trim(),
    type: draft.type,
    enabled: draft.enabled,
    events,
    config_json: JSON.stringify(config),
    templates_json: JSON.stringify(draft.templates),
    user_ids: draft.user_ids,
  }
}

function Notifications({ data, client, load, notify, sessionUser }: any) {
  const dialogs = useDialogs()
  const channels: NotificationChannel[] = data.notification_channels || []
  const users: User[] = data.users || []
  const announcements: NotificationAnnouncement[] = data.notification_announcements || []
  const isAdmin = sessionUser?.role === 'admin'
  const eventOptions: NotificationEventDefinition[] = data.notification_config?.events || fallbackNotificationEventOptions.filter(option => isAdmin || ['traffic_quota_exceeded', 'user_risk_detected', 'admin_announcement'].includes(option.value))
  const defaultTemplates: Record<string, NotificationTemplate> = data.notification_config?.templates || fallbackNotificationTemplates
  const ownerUserID = Number(sessionUser?.id || data.current_user?.id || 0)
  const [editor, setEditor] = useState<NotificationDraft | null>(null)
  const [saving, setSaving] = useState(false)
  const [testingID, setTestingID] = useState<number | 'draft' | null>(null)
  const [announcementTitle, setAnnouncementTitle] = useState('')
  const [announcementBody, setAnnouncementBody] = useState('')
  const [announcementAll, setAnnouncementAll] = useState(true)
  const [announcementUserIDs, setAnnouncementUserIDs] = useState<number[]>([])
  const [sendingAnnouncement, setSendingAnnouncement] = useState(false)

  const openCreate = () => setEditor(emptyNotificationDraft(defaultTemplates, eventOptions, isAdmin, ownerUserID))
  const openEdit = (channel: NotificationChannel) => setEditor(notificationDraftFromChannel(channel, defaultTemplates))

  const saveChannel = async () => {
    if (!editor) return
    if (!editor.name.trim()) {
      await dialogs.alert({ title: '请填写名称', message: '给通知通道起一个便于识别的名字，例如“运维 Telegram”。' })
      return
    }
    if (!editor.events.length) {
      await dialogs.alert({ title: '请选择事件', message: '至少勾选一个通知事件。' })
      return
    }
    setSaving(true)
    try {
      const body = notificationPayloadFromDraft(editor)
      if (editor.id) {
        await client.request(`/notification-channels/${editor.id}`, { method: 'PATCH', body: JSON.stringify(body) })
      } else {
        await client.request('/notification-channels', { method: 'POST', body: JSON.stringify(body) })
      }
      setEditor(null)
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '保存失败', message: localizeErrorMessage(e.message || e) })
    } finally {
      setSaving(false)
    }
  }

  const testChannel = async (channel?: NotificationChannel, draft?: NotificationDraft) => {
    const target = draft || (channel ? notificationDraftFromChannel(channel, defaultTemplates) : null)
    if (!target) return
    const key = channel?.id || 'draft'
    setTestingID(key)
    try {
      if (channel?.id) {
        await client.request(`/notification-channels/${channel.id}/test`, { method: 'POST', body: '{}' })
      } else {
        await client.request('/notification-channels/test', { method: 'POST', body: JSON.stringify(notificationPayloadFromDraft(target)) })
      }
      await dialogs.alert({ title: '测试已发送', message: '请到 Telegram / Bark 客户端确认是否收到“OBoard 测试通知”。' })
    } catch (e: any) {
      await dialogs.alert({ title: '测试失败', message: localizeErrorMessage(e.message || e) })
    } finally {
      setTestingID(null)
    }
  }

  const toggleEnabled = async (channel: NotificationChannel) => {
    try {
      const draft = notificationDraftFromChannel(channel, defaultTemplates)
      draft.enabled = !draft.enabled
      await client.request(`/notification-channels/${channel.id}`, { method: 'PATCH', body: JSON.stringify(notificationPayloadFromDraft(draft)) })
      await load()
    } catch (e: any) {
      await dialogs.alert({ title: '更新失败', message: localizeErrorMessage(e.message || e) })
    }
  }

  const sendAnnouncement = async () => {
    if (!announcementTitle.trim() || !announcementBody.trim()) {
      notify?.('请填写通知标题和内容', 'warning')
      return
    }
    if (!announcementAll && !announcementUserIDs.length) {
      notify?.('请选择至少一个接收用户', 'warning')
      return
    }
    setSendingAnnouncement(true)
    try {
      const result = await client.request('/notification-announcements', {
        method: 'POST',
        body: JSON.stringify({ title: announcementTitle.trim(), body: announcementBody.trim(), all_users: announcementAll, user_ids: announcementAll ? [] : announcementUserIDs }),
      })
      setAnnouncementTitle('')
      setAnnouncementBody('')
      setAnnouncementUserIDs([])
      await load()
      notify?.(`管理员通知已加入发送队列 · ${Number(result.queued_count || 0)} 条推送`, 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSendingAnnouncement(false)
    }
  }

  return <Panel title="通知" className="notifications-panel">
    {isAdmin && <section className="notification-announcement-section">
      <div className="notification-announcement-head">
        <div><h3>发送管理员通知</h3><p className="muted">消息会发送到用户已配置且勾选“管理员通知”的通道。</p></div>
        <span className="notification-type-pill telegram">管理员</span>
      </div>
      <div className="notification-announcement-form">
        <FormField label="标题" required><input value={announcementTitle} onChange={event => setAnnouncementTitle(event.target.value)} maxLength={120} placeholder="例如：线路维护通知" /></FormField>
        <FormField label="内容" required><textarea value={announcementBody} onChange={event => setAnnouncementBody(event.target.value)} maxLength={3000} rows={4} placeholder="写清楚影响范围和预计恢复时间" /></FormField>
        <FormField label="接收用户">
          <div className="notification-audience-picker">
            <Select variant="segmented" value={announcementAll ? 'all' : 'selected'} onChange={event => setAnnouncementAll(event.target.value === 'all')} aria-label="通知范围">
              <option value="all">全部普通用户</option>
              <option value="selected">指定用户</option>
            </Select>
            {!announcementAll && <div className="notification-user-options">
              {users.filter(user => user.id !== ownerUserID && user.status === 'active').map(user => {
                const active = announcementUserIDs.includes(user.id)
                return <button key={user.id} type="button" className={`sub-chip ${active ? 'active' : ''}`} onClick={() => setAnnouncementUserIDs(current => active ? current.filter(id => id !== user.id) : [...current, user.id])}>{user.nickname || user.username}</button>
              })}
              {!users.some(user => user.id !== ownerUserID && user.status === 'active') && <span className="muted">暂无可选用户</span>}
            </div>}
          </div>
        </FormField>
        <div className="notification-announcement-actions"><button type="button" onClick={() => void sendAnnouncement()} disabled={sendingAnnouncement}>{sendingAnnouncement ? '发送中…' : '发送通知'}</button></div>
      </div>
      {announcements.length > 0 && <div className="notification-announcement-history">
        <strong>最近发送</strong>
        {announcements.slice(0, 5).map(item => <div key={item.id}><span>{item.title}</span><small>{item.user_ids.length} 位用户 · {item.queued_count} 条推送 · {formatTableTime(item.created_at)}</small></div>)}
      </div>}
    </section>}
    <div className="section-toolbar">
      <div>
        <h3>通知通道</h3>
        <p className="muted">{isAdmin ? '接收服务器、证书、备份、域名、任务和用户风险提醒。' : '接收自己的流量、异常使用和管理员消息。'} 支持 Telegram 与 Bark。</p>
      </div>
      <div className="section-actions">
        <button type="button" onClick={openCreate}><Plus size={15} /><span>新建通道</span></button>
      </div>
    </div>

    {!channels.length ? (
      <div className="notification-empty">
        <Bell size={22} />
        <strong>还没有通知通道</strong>
        <span>点击右上角「新建通道」，配置 Telegram 或 Bark 后即可接收已选择的提醒。</span>
      </div>
    ) : (
      <MotionList className="notification-channel-list">
        {channels.map(channel => {
          const events = String(channel.events || '').split(',').map(x => x.trim()).filter(Boolean)
          const enabled = channel.enabled !== false
          return <MotionCard key={channel.id} tag="article" className={`notification-channel-card ${enabled ? '' : 'is-disabled'}`}>
            <div className="notification-channel-main">
              <div className="notification-channel-icon" data-type={channel.type} aria-hidden="true">
                {channel.type === 'bark' ? <Bell size={18} /> : <Globe size={18} />}
              </div>
              <div className="notification-channel-copy">
                <div className="notification-channel-title-row">
                  <strong>{channel.name}</strong>
                  <span className={`notification-type-pill ${channel.type}`}>{channel.type === 'bark' ? 'Bark' : 'Telegram'}</span>
                  <span className={`deploy-status-pill ${enabled ? 'ok' : 'warn'}`}>{enabled ? '已启用' : '已停用'}</span>
                </div>
                <div className="notification-event-chips">
                  {events.length ? events.map(event => (
                    <span key={event} className="notification-event-chip">{eventOptions.find(x => x.value === event)?.label || event}</span>
                  )) : <span className="muted">未选择事件</span>}
                </div>
                {events.some(event => userScopedNotificationEvents.has(event)) && <small className="notification-target-summary">关注用户：{isAdmin ? (channel.user_ids || []).map(id => users.find(user => user.id === id)?.nickname || users.find(user => user.id === id)?.username || `#${id}`).join('、') || '本人' : '本人'}</small>}
              </div>
            </div>
            <div className="notification-channel-actions">
              <button type="button" className="ghost" disabled={testingID === channel.id} onClick={() => void testChannel(channel)}>
                {testingID === channel.id ? '测试中…' : '发送测试'}
              </button>
              <button type="button" className="ghost" onClick={() => void toggleEnabled(channel)}>{enabled ? '停用' : '启用'}</button>
              <button type="button" className="ghost" onClick={() => openEdit(channel)}><Edit3 size={14} /><span>编辑</span></button>
              <button type="button" className="ghost danger-text" onClick={() => void remove(client, `/notification-channels/${channel.id}`, load, dialogs, channel)}><Trash2 size={14} /><span>删除</span></button>
            </div>
          </MotionCard>
        })}
      </MotionList>
    )}

    <AnimatePresence>
      {editor && (
        <NotificationChannelDialog
          draft={editor}
          setDraft={setEditor}
          saving={saving}
          testing={testingID === 'draft' || (editor.id > 0 && testingID === editor.id)}
          onCancel={() => setEditor(null)}
          onSave={() => void saveChannel()}
          onTest={() => void testChannel(editor.id ? { id: editor.id, name: editor.name, type: editor.type, enabled: editor.enabled, events: editor.events.join(','), config_json: notificationPayloadFromDraft(editor).config_json } as NotificationChannel : undefined, editor)}
          eventOptions={eventOptions}
          defaultTemplates={defaultTemplates}
          users={users}
          isAdmin={isAdmin}
          ownerUserID={ownerUserID}
        />
      )}
    </AnimatePresence>
  </Panel>
}

function NotificationChannelDialog({
  draft,
  setDraft,
  saving,
  testing,
  onCancel,
  onSave,
  onTest,
  eventOptions,
  defaultTemplates,
  users,
  isAdmin,
  ownerUserID,
}: {
  draft: NotificationDraft
  setDraft: React.Dispatch<React.SetStateAction<NotificationDraft | null>>
  saving: boolean
  testing: boolean
  onCancel: () => void
  onSave: () => void
  onTest: () => void
  eventOptions: NotificationEventDefinition[]
  defaultTemplates: Record<string, NotificationTemplate>
  users: User[]
  isAdmin: boolean
  ownerUserID: number
}) {
  const update = (patch: Partial<NotificationDraft>) => setDraft(prev => prev ? { ...prev, ...patch } : prev)
  const toggleEvent = (event: string) => {
    setDraft(prev => {
      if (!prev) return prev
      const has = prev.events.includes(event)
      return { ...prev, events: has ? prev.events.filter(x => x !== event) : [...prev.events, event] }
    })
  }
  const switchType = (type: 'telegram' | 'bark') => {
    setDraft(prev => prev ? { ...prev, type } : prev)
  }
  return <MotionDialogPanel onCancel={onCancel} className="notification-channel-dialog">
    <header className="dialog-head">
      <div>
        <h2 id="notification-channel-title">{draft.id ? '编辑通知通道' : '新建通知通道'}</h2>
        <p className="muted">设置通知方式和接收事件。</p>
      </div>
      <button className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><XIcon /></button>
    </header>
    <div className="dialog-body">
      <div className="form notification-channel-form">
        <FormField label="通道名称" required hint="用于识别此通道。">
          <input value={draft.name} onChange={e => update({ name: e.target.value })} placeholder="通道名称" autoFocus />
        </FormField>

        <FormField label="通道类型" required>
          <Select variant="segmented" value={draft.type} onChange={event => switchType(event.target.value as 'telegram' | 'bark')} aria-label="通道类型">
            <option value="telegram">Telegram</option>
            <option value="bark">Bark</option>
          </Select>
        </FormField>

        <FormField label="启用通知">
          <label className="notification-enable-row">
            <input type="checkbox" checked={draft.enabled} onChange={e => update({ enabled: e.target.checked })} aria-label="启用通知" />
          </label>
        </FormField>

        <FormField label="通知事件" required hint="可多选。">
          <div className="notification-event-options">
            {eventOptions.map(option => (
              <label key={option.value} className={`notification-event-option ${draft.events.includes(option.value) ? 'active' : ''}`}>
                <input type="checkbox" checked={draft.events.includes(option.value)} onChange={() => toggleEvent(option.value)} />
                <span>
                  <strong>{option.label}</strong>
                  <small>{option.description}</small>
                </span>
              </label>
            ))}
          </div>
        </FormField>

        {draft.events.some(event => userScopedNotificationEvents.has(event)) && <FormField label="关注用户" required hint={isAdmin ? '流量和异常使用提醒会发送这些用户的情况。' : '只接收与你本人有关的提醒。'}>
          <div className="notification-user-options">
            {(isAdmin ? users.filter(user => user.status === 'active') : users.filter(user => user.id === ownerUserID)).map(user => {
              const active = draft.user_ids.includes(user.id)
              return <button key={user.id} type="button" className={`sub-chip ${active ? 'active' : ''}`} disabled={!isAdmin} onClick={() => isAdmin && update({ user_ids: active ? draft.user_ids.filter(id => id !== user.id) : [...draft.user_ids, user.id] })}>{user.nickname || user.username}{user.id === ownerUserID ? '（本人）' : ''}</button>
            })}
            {!isAdmin && !users.some(user => user.id === ownerUserID) && <span className="notification-event-chip">本人</span>}
          </div>
        </FormField>}

        <FormField label="通知模板" hint="按需修改标题和正文。">
          <div className="notification-template-list">
            {draft.events.map(event => {
              const option = eventOptions.find(item => item.value === event)
              const current = draft.templates[event] || defaultTemplates[event] || { title: '', body: '' }
              return <details key={event} className="notification-template-editor">
                <summary><span>{option?.label || event}</span><small>{current.title}</small></summary>
                <div className="notification-template-fields">
                  <label><span>标题</span><input value={current.title} onChange={e => update({ templates: { ...draft.templates, [event]: { ...current, title: e.target.value } } })} /></label>
                  <label><span>正文</span><textarea rows={4} value={current.body} onChange={e => update({ templates: { ...draft.templates, [event]: { ...current, body: e.target.value } } })} /></label>
                  <div className="notification-template-variables"><span>可用变量</span>{(option?.variables || []).map(variable => <code key={variable}>{`{{.${variable}}}`}</code>)}</div>
                  <button type="button" className="ghost" onClick={() => update({ templates: { ...draft.templates, [event]: { ...(defaultTemplates[event] || current) } } })}>恢复默认模板</button>
                </div>
              </details>
            })}
          </div>
        </FormField>

        {draft.type === 'telegram' ? <>
          <FormField label="Bot Token" required hint="从 @BotFather 获取。">
            <input value={draft.bot_token} onChange={e => update({ bot_token: e.target.value })} placeholder="123456:ABC-DEF..." autoComplete="off" />
          </FormField>
          <FormField label="Chat ID" required hint="个人、群组或频道 ID。">
            <input value={draft.chat_id} onChange={e => update({ chat_id: e.target.value })} placeholder="-1001234567890" autoComplete="off" />
          </FormField>
        </> : <>
          <FormField label="Device Key" required hint="Bark 设备 Key。">
            <input value={draft.device_key} onChange={e => update({ device_key: e.target.value })} placeholder="device key" autoComplete="off" />
          </FormField>
          <FormField label="Server URL" hint="留空使用 Bark 官方地址。">
            <input value={draft.server_url} onChange={e => update({ server_url: e.target.value })} placeholder="https://api.day.app" autoComplete="off" />
          </FormField>
        </>}
      </div>
    </div>
    <footer className="dialog-actions">
      <button type="button" className="ghost" onClick={onCancel} disabled={saving}>取消</button>
      <button type="button" className="ghost" onClick={onTest} disabled={saving || testing}>{testing ? '测试中…' : '发送测试'}</button>
      <button type="button" onClick={onSave} disabled={saving || testing}>{saving ? '保存中…' : draft.id ? '保存修改' : '创建通道'}</button>
    </footer>
  </MotionDialogPanel>
}

function Tasks({ data, client, loading: pageLoading }: any) {
  const [rows, setRows] = useState<any[]>(data.agent_tasks || [])
  const [manualRefreshing, setManualRefreshing] = useState(false)
  const [backgroundRefreshing, setBackgroundRefreshing] = useState(false)
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null)
  const [refreshFailed, setRefreshFailed] = useState(false)
  const requestInFlightRef = useRef(false)
  const mountedRef = useRef(false)
  const hasActiveTasks = useMemo(() => rows.some(task => ['pending', 'running'].includes(String(task.status || ''))), [rows])
  const hasActiveTasksRef = useRef(hasActiveTasks)
  hasActiveTasksRef.current = hasActiveTasks

  useEffect(() => { setRows(data.agent_tasks || []) }, [data.agent_tasks])

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  const loadTasks = React.useCallback(async (mode: 'manual' | 'background' = 'manual') => {
    if (requestInFlightRef.current) return
    requestInFlightRef.current = true
    if (mode === 'manual') setManualRefreshing(true)
    else setBackgroundRefreshing(true)
    try {
      const res = await client.request('/agent-tasks?limit=300')
      if (!mountedRef.current) return
      setRows(res.tasks || [])
      setLastRefreshedAt(new Date())
      setRefreshFailed(false)
    } catch (error) {
      if (mountedRef.current) setRefreshFailed(true)
      console.warn('Task refresh failed:', error)
    } finally {
      requestInFlightRef.current = false
      if (mountedRef.current) {
        if (mode === 'manual') setManualRefreshing(false)
        else setBackgroundRefreshing(false)
      }
    }
  }, [client])

  useEffect(() => {
    let cancelled = false
    let timer: number | undefined

    const scheduleNext = () => {
      if (cancelled || document.visibilityState !== 'visible') return
      timer = window.setTimeout(runRefresh, hasActiveTasksRef.current ? 3000 : 15000)
    }
    const runRefresh = async () => {
      if (cancelled || document.visibilityState !== 'visible') return
      await loadTasks('background')
      scheduleNext()
    }
    const handleVisibilityChange = () => {
      if (timer !== undefined) window.clearTimeout(timer)
      timer = undefined
      if (document.visibilityState === 'visible') void runRefresh()
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    if (document.visibilityState === 'visible') void runRefresh()
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [loadTasks])

  const busy = manualRefreshing || pageLoading
  const refreshing = manualRefreshing || backgroundRefreshing
  const refreshedTime = lastRefreshedAt?.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
  return <Panel title="部署 / Agent 任务">
    <div className="section-toolbar">
      <div>
        <h3>任务中心</h3>
        <p className="muted">一次下发、批量更新等操作会合并为一条记录。先看整体状态，再点开服务器，最后查看该服务器的具体子任务。</p>
      </div>
      <div className="section-actions">
        <div className={`live-refresh-status ${refreshFailed ? 'is-error' : hasActiveTasks ? 'is-active' : ''}`} title={hasActiveTasks ? '进行中的任务每 3 秒更新' : '任务状态每 15 秒更新'}>
          <span className="live-refresh-dot" aria-hidden="true" />
          <span>{refreshFailed ? '自动刷新暂时失败' : refreshing ? '正在更新任务' : '自动刷新已开启'}</span>
          {refreshedTime ? <time dateTime={lastRefreshedAt?.toISOString()}>更新于 {refreshedTime}</time> : null}
        </div>
        <button className="ghost" onClick={() => void loadTasks('manual')} disabled={refreshing}>{refreshing ? '刷新中…' : '刷新'}</button>
      </div>
    </div>
    {busy && !rows.length ? <TableSkeleton /> : <TaskTimeline rows={rows} data={data} />}
  </Panel>
}

type TaskGroupKind = 'deployment' | 'batch' | 'single'
type TaskGroup = {
  kind: TaskGroupKind
  id: string | number
  title: string
  subtitle?: string
  version?: number
  batchType?: string
  tasks: any[]
  updated_at: string
}

const BATCHABLE_TASK_TYPES = new Set([
  'update_agent', 'update_agent_config', 'diagnose_network', 'detect_mtu',
  'probe_inbounds', 'probe_inbounds_external', 'probe_port_forwards', 'collect_logs', 'manage_logs',
])

function TaskTimeline({ rows, data }: { rows: any[]; data: any }) {
  const groups = groupTasksForTimeline(rows)
  if (!groups.length) return <p className="muted">暂无任务</p>
  return <MotionList className="task-card-list">{groups.map(group => (
    <TaskGroupCard key={`${group.kind}-${group.id}`} group={group} data={data} />
  ))}</MotionList>
}

function isDeploymentBundle(tasks: any[]) {
  const types = new Set(tasks.map(t => String(t.type || '')))
  return types.has('apply_deployment')
}

function groupTasksForTimeline(rows: any[]): TaskGroup[] {
  const byVersion = new Map<number, any[]>()
  const leftover: any[] = []

  ;(rows || []).forEach(task => {
    const version = Number(task.config_version || 0)
    if (version > 0) byVersion.set(version, [...(byVersion.get(version) || []), task])
    else leftover.push(task)
  })

  const groups: TaskGroup[] = []

  byVersion.forEach((tasks, version) => {
    if (isDeploymentBundle(tasks)) {
      groups.push({
        kind: 'deployment',
        id: `deploy-${version}`,
        title: '下发配置',
        subtitle: `版本 ${version}`,
        version,
        tasks,
        updated_at: maxTaskTime(tasks),
      })
      return
    }
    // Isolated versioned tasks (MTU / probe / WARP / etc.) fall through to type batching.
    leftover.push(...tasks)
  })

  const batches = new Map<string, any[]>()
  leftover.forEach(task => {
    const type = String(task.type || 'task')
    const key = BATCHABLE_TASK_TYPES.has(type)
      ? `${type}:${taskBatchBucket(task)}`
      : `single:${task.id}`
    batches.set(key, [...(batches.get(key) || []), task])
  })

  batches.forEach((tasks, key) => {
    const type = String(tasks[0]?.type || 'task')
    if (key.startsWith('single:')) {
      groups.push({
        kind: 'single',
        id: key,
        title: labelValue(type),
        tasks,
        updated_at: maxTaskTime(tasks),
      })
      return
    }
    const serverCount = new Set(tasks.map(t => t.server_id)).size
    groups.push({
      kind: tasks.length > 1 || serverCount > 1 ? 'batch' : 'single',
      id: `batch-${key}`,
      title: batchTitleForType(type),
      subtitle: serverCount > 1 ? `${serverCount} 台服务器` : undefined,
      batchType: type,
      tasks,
      updated_at: maxTaskTime(tasks),
    })
  })

  return groups.sort((a, b) => String(b.updated_at || '').localeCompare(String(a.updated_at || '')) || String(b.id).localeCompare(String(a.id)))
}

function taskBatchBucket(task: any) {
  const raw = String(task.created_at || task.updated_at || '')
  const ms = Date.parse(raw)
  if (!Number.isFinite(ms)) return raw || 'unknown'
  // 2-minute window groups multi-server actions kicked off together.
  return String(Math.floor(ms / (2 * 60 * 1000)))
}

function batchTitleForType(type: string) {
  switch (type) {
    case 'update_agent': return '更新 Agent'
    case 'update_agent_config': return '同步 Agent 配置'
    case 'detect_mtu': return 'MTU 检测'
    case 'diagnose_network': return '网络诊断'
    case 'probe_inbounds': return '入口监听探测'
    case 'probe_inbounds_external': return '公网端口探测'
    case 'probe_port_forwards': return '端口转发探测'
    case 'collect_logs': return '拉取日志'
    case 'manage_logs': return '管理日志'
    default: return labelValue(type || 'task')
  }
}

function maxTaskTime(tasks: any[]) {
  return tasks.map(t => String(t.updated_at || t.created_at || '')).sort().pop() || ''
}

function taskServerLabel(data: any, serverID: number) {
  const server = (data?.servers || []).find((s: Server) => Number(s.id) === Number(serverID))
  return server?.name || `服务器 #${serverID}`
}

function TaskGroupCard({ group, data }: { group: TaskGroup; data: any }) {
  const [expanded, setExpanded] = useState(false)
  const [openServerID, setOpenServerID] = useState<number | null>(null)
  const summary = taskStatusSummary(group.tasks)
  const status = deploymentStatusFromSummary(summary)
  const serverIDs = Array.from(new Set(group.tasks.map(t => Number(t.server_id || 0)))).filter(Boolean).sort((a, b) => a - b)
  const skipped = group.tasks.filter(t => parseJSONLoose(t.result_json)?.skipped || parseJSONLoose(t.payload_json)?.skipped).length
  const createdAt = String(group.tasks.map(t => t.created_at).filter(Boolean).sort()[0] || '')

  const byServer = new Map<number, any[]>()
  group.tasks.forEach(task => {
    const sid = Number(task.server_id || 0)
    byServer.set(sid, [...(byServer.get(sid) || []), task])
  })

  const metaBits = [
    group.subtitle,
    serverIDs.length ? `${serverIDs.length} 台服务器` : '',
    `${group.tasks.length} 项任务`,
  ].filter(Boolean)

  // Single-server single-task groups can open details directly without an extra empty layer.
  const isFlatSingle = group.kind === 'single' && group.tasks.length === 1

  return <MotionCard tag="article" className={`task-card task-group-card ${group.kind === 'deployment' ? 'deployment-task-card' : ''}`}>
    <button type="button" className="task-group-toggle" onClick={() => setExpanded(v => !v)} aria-expanded={expanded}>
      <div className="task-group-title-block">
        <strong>{group.title}</strong>
        <span>{metaBits.join(' · ')}</span>
      </div>
      <div className="task-summary">
        <span className="task-stat"><em>{summary.succeeded}</em> 成功</span>
        <span className="task-stat"><em>{summary.pending}</em> 等待</span>
        <span className="task-stat"><em>{summary.running}</em> 执行中</span>
        <span className={`task-stat ${summary.failed ? 'is-fail' : ''}`}><em>{summary.failed}</em> 失败</span>
        {skipped ? <span className="task-stat"><em>{skipped}</em> 跳过</span> : null}
      </div>
      <div className="task-group-head-right">
        {cell(status, 'status')}
        <ChevronRight size={16} className={expanded ? 'task-chevron open' : 'task-chevron'} />
      </div>
      <div className="task-meta">
        <span>创建 {formatTableTime(createdAt)}</span>
        <span>更新 {formatTableTime(maxTaskTime(group.tasks))}</span>
      </div>
    </button>

    {expanded && (
      <div className="task-group-body">
        {isFlatSingle ? (
          <TaskDetailList tasks={group.tasks} data={data} />
        ) : (
          <div className="task-server-list">
            {serverIDs.map(serverID => {
              const tasks = byServer.get(serverID) || []
              const serverSummary = taskStatusSummary(tasks)
              const serverStatus = deploymentStatusFromSummary(serverSummary)
              const open = openServerID === serverID
              return <div key={serverID} className={`task-server-row ${open ? 'open' : ''}`}>
                <button type="button" className="task-server-toggle" onClick={() => setOpenServerID(open ? null : serverID)} aria-expanded={open}>
                  <div className="task-group-title-block">
                    <strong>{taskServerLabel(data, serverID)}</strong>
                    <span>{tasks.length} 项 · 成功 {serverSummary.succeeded} · 失败 {serverSummary.failed} · 进行中 {serverSummary.pending + serverSummary.running}</span>
                  </div>
                  <div className="task-group-head-right">
                    {cell(serverStatus, 'status')}
                    <ChevronRight size={15} className={open ? 'task-chevron open' : 'task-chevron'} />
                  </div>
                </button>
                {open && <TaskDetailList tasks={tasks} data={data} />}
              </div>
            })}
            {(byServer.get(0) || []).length > 0 && (
              <div className="task-server-row open">
                <div className="task-server-toggle static">
                  <div><strong>未绑定服务器</strong><span>{(byServer.get(0) || []).length} 项</span></div>
                </div>
                <TaskDetailList tasks={byServer.get(0) || []} data={data} />
              </div>
            )}
          </div>
        )}
      </div>
    )}
  </MotionCard>
}

function TaskDetailList({ tasks, data }: { tasks: any[]; data: any }) {
  const sorted = [...tasks].sort((a, b) => Number(a.id || 0) - Number(b.id || 0))
  return <div className="task-detail-list">
    {sorted.map(task => <TaskDetailCard key={task.id} task={task} data={data} />)}
  </div>
}

function TaskDetailCard({ task, data }: { task: any; data?: any }) {
  const [open, setOpen] = useState(false)
  const result = parseJSONLoose(task.result_json)
  const payload = parseJSONLoose(task.payload_json)
  const error = String(result?.error || '')
  const message = String(result?.message || '')
  const status = result?.timeout ? 'timeout' : task.status
  const summary = error || message || taskSummaryFromPayload(task.type, payload)
  return <article className="task-detail-card">
    <button type="button" className="task-detail-toggle" onClick={() => setOpen(v => !v)} aria-expanded={open}>
      <div>
        <strong>{labelValue(task.type || 'task')}</strong>
        <span className={error ? 'error-text' : ''}>{summary}</span>
      </div>
      <div className="task-group-head-right">
        {cell(status, 'status')}
        <ChevronRight size={14} className={open ? 'task-chevron open' : 'task-chevron'} />
      </div>
    </button>
    {open && (
      <div className="task-detail-body">
        <div className="task-meta">
          <span>任务 #{task.id}</span>
          <span>创建 {formatTableTime(String(task.created_at || ''))}</span>
          <span>更新 {formatTableTime(String(task.updated_at || ''))}</span>
          {task.completed_at && <span>完成 {formatTableTime(String(task.completed_at))}</span>}
          {task.config_version ? <span>版本 {task.config_version}</span> : null}
        </div>
        {task.type === 'apply_deployment' && Array.isArray(result?.steps) ? (
          <div className="deployment-step-list">
            {result.steps.map((step: any, index: number) => (
              <div className="deployment-step-row" key={`${step?.key || 'step'}-${index}`}>
                <div>
                  <strong>{String(step?.label || step?.key || `步骤 ${index + 1}`)}</strong>
                  <span className={step?.error ? 'error-text' : ''}>{String(step?.error || step?.message || '')}</span>
                </div>
                <div className="task-group-head-right">
                  {cell(step?.status || 'succeeded', 'status')}
                  <span className="muted">{Number(step?.duration_ms || 0)} ms</span>
                </div>
              </div>
            ))}
          </div>
        ) : null}
        <pre>{JSON.stringify({ payload: redactTaskJSON(payload), result: redactTaskJSON(result) }, null, 2)}</pre>
      </div>
    )}
  </article>
}

function deploymentStatusFromSummary(summary: { total: number; pending: number; running: number; succeeded: number; failed: number }) {
  if (summary.total === 0) return 'pending'
  if (summary.failed > 0) return summary.failed >= summary.total ? 'failed' : 'partial_failed'
  if (summary.running) return 'running'
  if (summary.pending) return 'pending'
  return 'succeeded'
}

function taskStatusSummary(tasks: any[]) {
  const out = { total: tasks.length, pending: 0, running: 0, succeeded: 0, failed: 0 }
  tasks.forEach(task => {
    const result = parseJSONLoose(task.result_json)
    const status = result?.timeout ? 'timeout' : String(task.status || '')
    if (status === 'pending') out.pending++
    else if (status === 'running') out.running++
    else if (status === 'succeeded') out.succeeded++
    else if (status.includes('fail') || status === 'timeout') out.failed++
  })
  return out
}

function parseJSONLoose(raw: any) {
  if (!raw) return null
  if (typeof raw === 'object') return raw
  try { return JSON.parse(String(raw)) } catch { return String(raw) }
}

function redactTaskJSON(value: any): any {
  if (value == null) return value
  if (Array.isArray(value)) return value.map(redactTaskJSON)
  if (typeof value !== 'object') return value
  const out: Record<string, any> = {}
  Object.entries(value).forEach(([key, item]) => {
    const lower = key.toLowerCase()
    if (lower.includes('token') || lower.includes('password') || lower.includes('secret') || lower.includes('private_key') || lower === 'config') {
      if (lower === 'config' && typeof item === 'string') {
        out[key] = `[config ${formatBytes(item.length)}]`
      } else {
        out[key] = '***'
      }
      return
    }
    out[key] = redactTaskJSON(item)
  })
  return out
}

function taskSummaryFromPayload(type: string, payload: any) {
  if (type === 'apply_deployment') {
    const count = [payload?.time_sync, payload?.config, payload?.port_forwards, payload?.inbound_probe, payload?.port_forward_probe, payload?.tunnels, payload?.dns_benchmark, payload?.mtu_detection].filter(Boolean).length
    return `${count || 1} 个部署步骤`
  }
  if (type === 'apply_core_config' && payload?.skipped) return '配置未变化，已跳过'
  if (type === 'apply_core_config' && payload?.config) return `配置体积 ${formatBytes(String(payload.config).length)}`
  if (type === 'update_agent') return payload?.source ? `来源 ${labelValue(payload.source)}` : '更新 Agent 与内核'
  if (type === 'update_agent_config') return '同步 Agent 本机配置'
  if (type === 'detect_mtu') return payload?.mode ? `模式 ${labelValue(payload.mode)}` : 'MTU 检测'
  if (type === 'probe_inbounds' || type === 'probe_inbounds_external') return payload?.entry_targets?.length ? `${payload.entry_targets.length} 个入口` : '入口端口探测'
  if (type === 'probe_port_forwards') return payload?.rules?.length ? `${payload.rules.length} 条规则` : '端口转发探测'
  if (type === 'collect_logs') return payload?.services ? `服务 ${payload.services}` : '拉取日志'
  if (type === 'manage_logs') return `${payload?.action === 'clear' ? '清空' : '轮转'} ${payload?.services || 'all'} 日志`
  if (payload && typeof payload === 'object') return '等待 Agent 执行'
  return '暂无详情'
}

function Panel({ title, children, className = '', actions = null }: any) { return <section className={`panel${className ? ` ${className}` : ''}`}><div className="panel-head"><h2>{title}</h2>{actions}</div><div className="panel-body">{children}</div></section> }

type ProtocolAuth = { username: string; uuid: string; password: string; method: string }

type InboundPreset = { id: string; protocol: Protocol; label: string; description: string; defaultPort: number }

const inboundPresets: InboundPreset[] = [
  { id: 'vless-tls-vision', protocol: 'vless', label: 'VLESS TLS Vision', description: 'TCP + TLS + Vision', defaultPort: 443 },
  { id: 'vless-reality', protocol: 'vless', label: 'VLESS Reality Vision', description: 'TCP + Reality + Vision', defaultPort: 443 },
  { id: 'vless-ws', protocol: 'vless', label: 'VLESS WebSocket', description: 'WebSocket + TLS', defaultPort: 443 },
  { id: 'vless-tcp', protocol: 'vless', label: 'VLESS TCP', description: '无 TLS，适合内网或测试', defaultPort: 443 },
  { id: 'hy2-tls', protocol: 'hy2', label: 'HY2', description: 'HY2 标准配置', defaultPort: 443 },
  { id: 'anytls-basic', protocol: 'anytls', label: 'AnyTLS', description: 'AnyTLS 标准配置', defaultPort: 443 },
  { id: 'ss-aes-128-gcm', protocol: 'shadowsocks', label: 'SS 128', description: 'AES-128-GCM，单用户', defaultPort: 8388 },
  { id: 'ss-aes-256-gcm', protocol: 'shadowsocks', label: 'SS 256', description: 'AES-256-GCM，单用户', defaultPort: 8388 },
  { id: 'ss-2022-128', protocol: 'shadowsocks', label: 'SS 2022-128', description: 'AES-128-GCM，多用户', defaultPort: 8388 },
  { id: 'ss-2022-256', protocol: 'shadowsocks', label: 'SS 2022-256', description: 'AES-256-GCM，多用户', defaultPort: 8388 },
  { id: 'ssh-restricted', protocol: 'ssh', label: 'SSH 受限代理', description: '公钥认证，仅支持本地/动态转发', defaultPort: 2222 },
]

const shadowsocksMethods = [
  { value: 'aes-128-gcm', label: 'SS 128' },
  { value: 'aes-256-gcm', label: 'SS 256' },
  { value: '2022-blake3-aes-128-gcm', label: 'SS 2022-128' },
  { value: '2022-blake3-aes-256-gcm', label: 'SS 2022-256' },
]

function parseConfig(raw: string): Record<string, any> | null {
  try {
    const parsed = JSON.parse(raw || '{}')
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}
  } catch {
    return null
  }
}

function randomToken(length = 24) {
  const chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_'
  const bytes = new Uint8Array(length)
  if (globalThis.crypto?.getRandomValues) globalThis.crypto.getRandomValues(bytes)
  else for (let i = 0; i < length; i++) bytes[i] = Math.floor(Math.random() * 256)
  return Array.from(bytes, b => chars[b % chars.length]).join('')
}

function randomBase64(byteLength: number) {
  const bytes = new Uint8Array(byteLength)
  if (globalThis.crypto?.getRandomValues) globalThis.crypto.getRandomValues(bytes)
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  let binary = ''
  bytes.forEach(b => { binary += String.fromCharCode(b) })
  return btoa(binary)
}

function randomHex(length = 8) {
  const bytes = new Uint8Array(Math.ceil(length / 2))
  if (globalThis.crypto?.getRandomValues) globalThis.crypto.getRandomValues(bytes)
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('').slice(0, length)
}

function inboundPreset(id: string) {
  return inboundPresets.find(x => x.id === id) || inboundPresets[0]
}

function inboundPresetsForProtocol(protocol: Protocol) {
  return inboundPresets.filter(x => x.protocol === protocol)
}

function defaultInboundPreset(protocol: Protocol) {
  const defaults: Record<Protocol, string> = {
    vless: 'vless-reality',
    hy2: 'hy2-tls',
    anytls: 'anytls-basic',
    shadowsocks: 'ss-2022-128',
	ssh: 'ssh-restricted',
  }
  return defaults[protocol]
}

function presetRequiresCertificate(presetID: string) {
  return presetID === 'vless-tls-vision' || presetID === 'vless-ws' || presetID === 'hy2-tls' || presetID === 'anytls-basic'
}

function inferInboundPreset(protocol: Protocol, configJson: string) {
  const cfg = parseConfig(configJson) || {}
  if (protocol === 'shadowsocks') {
    const method = String(cfg.method || '')
    if (method === 'aes-128-gcm') return 'ss-aes-128-gcm'
    if (method === 'aes-256-gcm') return 'ss-aes-256-gcm'
    if (method === '2022-blake3-aes-128-gcm') return 'ss-2022-128'
    if (method === '2022-blake3-aes-256-gcm') return 'ss-2022-256'
  }
  if (protocol === 'vless') {
    const tls = objectConfig(cfg.tls)
    const transport = objectConfig(cfg.transport)
    if (objectConfig(tls.reality).enabled) return 'vless-reality'
    if (String(transport.type || '').toLowerCase() === 'ws') return 'vless-ws'
    if (tls.enabled) return 'vless-tls-vision'
    return 'vless-tcp'
  }
  if (protocol === 'hy2') return 'hy2-tls'
  if (protocol === 'anytls') return 'anytls-basic'
	if (protocol === 'ssh') return 'ssh-restricted'
  return defaultInboundPreset(protocol)
}

function buildInboundPresetConfig(id: string) {
  const preset = inboundPreset(id)
  const cfg: Record<string, any> = {}
	if (preset.id === 'ssh-restricted') {
		cfg.exposure_confirmed = false
		cfg.exposure_confirmation_version = 'ssh-inbound-v1'
		cfg.access_mode = 'restricted_proxy'
	}
  if (preset.protocol === 'shadowsocks') {
    if (preset.id === 'ss-aes-128-gcm') cfg.method = 'aes-128-gcm'
    if (preset.id === 'ss-aes-256-gcm') cfg.method = 'aes-256-gcm'
    if (preset.id === 'ss-2022-128') {
      cfg.method = '2022-blake3-aes-128-gcm'
      cfg.password = randomBase64(16)
    }
    if (preset.id === 'ss-2022-256') {
      cfg.method = '2022-blake3-aes-256-gcm'
      cfg.password = randomBase64(32)
    }
  }
  if (preset.id === 'vless-tls-vision') {
    cfg.flow = 'xtls-rprx-vision'
    cfg.tls = { enabled: true, server_name: 'example.com' }
  }
  if (preset.id === 'vless-reality') {
    cfg.flow = 'xtls-rprx-vision'
    cfg.tls = {
      enabled: true,
      server_name: 'cdn.icloud-content.com',
      reality: {
        enabled: true,
        handshake: { server: 'cdn.icloud-content.com', server_port: 443 },
        private_key: '',
        public_key: '',
        short_id: randomHex(8),
      },
    }
  }
  if (preset.id === 'vless-ws') {
    cfg.tls = { enabled: true, server_name: 'example.com' }
    cfg.transport = { type: 'ws', path: '/vless', headers: { Host: 'example.com' } }
  }
  if (preset.id === 'hy2-tls') {
    cfg.tls = { enabled: true, server_name: 'example.com' }
    cfg.up_mbps = 100
    cfg.down_mbps = 100
  }
  if (preset.id === 'anytls-basic') cfg.tls = { enabled: true, server_name: 'example.com' }
  return JSON.stringify(cfg, null, 2)
}

function makeUUID() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  const bytes = new Uint8Array(16)
  if (globalThis.crypto?.getRandomValues) globalThis.crypto.getRandomValues(bytes)
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

function defaultAuth(protocol: Protocol): ProtocolAuth {
  return {
    username: `node-${randomToken(6)}`,
    uuid: protocol === 'vless' ? makeUUID() : '',
    password: protocol === 'hy2' || protocol === 'anytls' || protocol === 'shadowsocks' ? randomToken(24) : '',
    method: protocol === 'shadowsocks' ? '2022-blake3-aes-128-gcm' : '',
  }
}

function readAuth(configJson: string, protocol: Protocol): ProtocolAuth {
  const cfg = parseConfig(configJson) || {}
  const meta = cfg._oboard && typeof cfg._oboard === 'object' ? cfg._oboard : {}
  return {
    username: typeof meta.username === 'string' ? meta.username : '',
    uuid: protocol === 'vless' && typeof cfg.uuid === 'string' ? cfg.uuid : '',
    password: (protocol === 'hy2' || protocol === 'anytls' || protocol === 'shadowsocks') && typeof cfg.password === 'string' ? cfg.password : '',
    method: protocol === 'shadowsocks' && typeof cfg.method === 'string' ? cfg.method : '2022-blake3-aes-128-gcm',
  }
}

function writeAuth(configJson: string, protocol: Protocol, auth: ProtocolAuth) {
  const cfg = parseConfig(configJson) || {}
  const meta = cfg._oboard && typeof cfg._oboard === 'object' ? cfg._oboard : {}
  meta.username = auth.username
  meta.auth_auto = true
  cfg._oboard = meta
  delete cfg.username
  if (protocol === 'vless') cfg.uuid = auth.uuid
  if (protocol === 'hy2' || protocol === 'anytls') cfg.password = auth.password
  if (protocol === 'shadowsocks') {
    cfg.method = auth.method || '2022-blake3-aes-128-gcm'
    cfg.password = auth.password
  }
  return JSON.stringify(cfg, null, 2)
}

function ensureAuthConfig(configJson: string, protocol: Protocol) {
  const cfg = parseConfig(configJson)
  if (!cfg) return configJson
  const current = readAuth(configJson, protocol)
  const defaults = defaultAuth(protocol)
  return writeAuth(configJson, protocol, {
    username: current.username || defaults.username,
    uuid: current.uuid || defaults.uuid,
    password: current.password || defaults.password,
    method: current.method || defaults.method,
  })
}

function regenerateAuthConfig(configJson: string, protocol: Protocol) {
  return writeAuth(configJson, protocol, defaultAuth(protocol))
}

function AuthFields({ value, setValue }: any) {
  const protocol = value.protocol as Protocol
  const auth = readAuth(value.config_json, protocol)
  const setAuth = (patch: Partial<ProtocolAuth>) => setValue({ ...value, config_json: writeAuth(value.config_json, protocol, { ...auth, ...patch }) })
  return <div className="auth-fields">
    <div className="auth-title"><span>认证信息</span><button className="ghost" onClick={() => setValue({ ...value, config_json: regenerateAuthConfig(value.config_json, protocol) })}>重新生成</button></div>
    <FormField label="用户名 / 标签"><input value={auth.username} onChange={e => setAuth({ username: e.target.value })} /></FormField>
    {protocol === 'vless' && <FormField label="UUID" required><input value={auth.uuid} onChange={e => setAuth({ uuid: e.target.value })} /></FormField>}
    {(protocol === 'hy2' || protocol === 'anytls' || protocol === 'shadowsocks') && <FormField label="密码" required><input value={auth.password} onChange={e => setAuth({ password: e.target.value })} /></FormField>}
    {protocol === 'shadowsocks' && <FormField label="加密方法"><input value={auth.method} onChange={e => setAuth({ method: e.target.value })} /></FormField>}
  </div>
}

function ProtocolForm({ value, setValue, servers, submit, outbound }: any) {
  useEffect(() => {
    const protocol = value.protocol as Protocol
    const next = ensureAuthConfig(value.config_json, protocol)
    if (next !== value.config_json) setValue({ ...value, config_json: next })
  }, [value.protocol])
  return <div className="form"><Select value={value.server_id} onChange={e => setValue({ ...value, server_id: Number(e.target.value) })}><option value={0}>选择服务器</option>{servers.map((s: Server) => <option value={s.id} key={s.id}>{s.name}</option>)}</Select><input value={value.name} onChange={e => setValue({ ...value, name: e.target.value })} placeholder="名称" /><Select value={value.protocol} onChange={e => setValue({ ...value, protocol: e.target.value as Protocol, config_json: ensureAuthConfig(value.config_json, e.target.value as Protocol) })}>{(outbound ? proxyProtocols : protocols).map(p => <option key={p} value={p}>{labelProtocol(p)}</option>)}</Select>{outbound ? <><input value={value.target_address} onChange={e => setValue({ ...value, target_address: e.target.value })} placeholder="目标地址" /><input value={value.target_port} onChange={e => setValue({ ...value, target_port: Number(e.target.value) })} placeholder="目标端口" /></> : <><input value={value.listen_ip} onChange={e => setValue({ ...value, listen_ip: e.target.value })} placeholder="监听 IP" /><input value={value.port} onChange={e => setValue({ ...value, port: Number(e.target.value) })} placeholder="监听端口" /></>}<AuthFields value={value} setValue={setValue} /><textarea value={value.config_json} onChange={e => setValue({ ...value, config_json: e.target.value })} placeholder="JSON 配置" /><button onClick={submit}>创建</button></div>
}

function Table({ rows, actions, loading: propLoading }: any) {
  const contextLoading = React.useContext(LoadingContext)
  const loading = propLoading !== undefined ? propLoading : contextLoading
  if (loading && !rows?.length) return <TableSkeleton />
  if (!rows?.length) return <p className="muted">暂无数据</p>
  const keys = Object.keys(rows[0]).filter(k => !k.startsWith('_') && !String(rows[0][k]).startsWith('argon2id'))
  return <div className="table-wrap"><table><thead><tr>{keys.map(k => <th key={k}>{humanLabel(k)}</th>)}{actions && <th>操作</th>}</tr></thead><tbody>{rows.map((r: any, i: number) => <tr key={i}>{keys.map(k => <td key={k}>{cell(r[k], k)}</td>)}{actions && <td className="actions"><TableActions>{actions(r)}</TableActions></td>}</tr>)}</tbody></table></div>
}

function TableActions({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  const [menuStyle, setMenuStyle] = useState<React.CSSProperties | null>(null)
  const moreRef = useRef<HTMLButtonElement | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const items = flattenActionNodes(children)

  const placeMenu = () => {
    const rect = moreRef.current?.getBoundingClientRect()
    if (!rect) return
    const menuWidth = 176
    const estimatedHeight = Math.min(Math.max(48, items.length * 40 + 14), window.innerHeight - 24)
    const below = rect.bottom + 8 + estimatedHeight <= window.innerHeight - 12
    const left = Math.max(12, Math.min(window.innerWidth - menuWidth - 12, rect.right - menuWidth))
    setMenuStyle({
      position: 'fixed',
      zIndex: 3000,
      width: menuWidth,
      left,
      right: 'auto',
      top: below ? rect.bottom + 8 : Math.max(12, rect.top - estimatedHeight - 8),
      bottom: 'auto',
      maxHeight: Math.max(96, window.innerHeight - 24),
    })
  }

  useEffect(() => {
    if (!open) return
    placeMenu()
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as globalThis.Node
      if (moreRef.current?.contains(target) || menuRef.current?.contains(target)) return
      setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    window.addEventListener('resize', placeMenu)
    window.addEventListener('scroll', placeMenu, true)
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('resize', placeMenu)
      window.removeEventListener('scroll', placeMenu, true)
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open, items.length])

  if (items.length <= 1) return <>{items.map((item, index) => cloneAction(item, index, itemIsDanger(item) ? 'danger-ghost' : 'ghost'))}</>
  const menu = open && menuStyle ? createPortal(
    <div ref={menuRef} className="action-menu-popover action-menu-portal" role="menu" style={menuStyle}>
      {items.map((item, index) => cloneAction(item, index, itemIsDanger(item) ? 'ghost danger-ghost' : 'ghost', () => setOpen(false)))}
    </div>,
    document.body
  ) : null
  return <div className="action-menu action-menu-single">
    <button
      ref={moreRef}
      className="ghost action-trigger"
      onClick={() => {
        if (!open) placeMenu()
        setOpen(v => !v)
      }}
      aria-label="操作"
      title="操作"
      aria-haspopup="menu"
      aria-expanded={open}
    >
      <span>操作</span><ChevronDownIcon />
    </button>
    {menu}
  </div>
}

function flattenActionNodes(children: React.ReactNode): React.ReactElement<any>[] {
  const items: React.ReactElement<any>[] = []
  React.Children.forEach(children, child => {
    if (!React.isValidElement(child)) return
    if (child.type === React.Fragment) {
      items.push(...flattenActionNodes((child.props as { children?: React.ReactNode }).children))
      return
    }
    items.push(child as React.ReactElement<any>)
  })
  return items
}

function nodeText(node: React.ReactNode): string {
  if (node === null || node === undefined || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(nodeText).join('')
  if (React.isValidElement(node)) return nodeText((node.props as { children?: React.ReactNode }).children)
  return ''
}

function itemIsDanger(item: React.ReactElement<any>) {
  const className = String(item.props.className || '')
  return /danger|删除|吊销|撤销|重置|revoke|delete|remove/i.test(className + ' ' + nodeText(item.props.children))
}

function cloneAction(item: React.ReactElement<any>, key: React.Key, className = '', afterClick?: () => void) {
  const props = item.props as { className?: string; onClick?: (event: React.MouseEvent<HTMLElement>) => void | Promise<void> }
  const mergedClassName = [props.className, className].filter(Boolean).join(' ')
  return React.cloneElement(item, {
    key,
    className: mergedClassName || undefined,
    onClick: async (event: React.MouseEvent<HTMLElement>) => {
      afterClick?.()
      await props.onClick?.(event)
    },
  })
}

function cell(v: any, key = '') {
  if (v === undefined || v === null || v === '') return <span className="empty">—</span>
  if (React.isValidElement(v)) return v
  if (typeof v === 'boolean') return <span className={v ? 'badge success' : 'badge neutral'}>{v ? '已启用' : '已禁用'}</span>
  if (/bytes$/i.test(key) && typeof v === 'number') return formatBytes(v)
  if (typeof v === 'object') return <code>{JSON.stringify(v)}</code>
  const s = String(v)
  if (isTimeField(key)) return formatTableTime(s)
  if (isSensitiveField(key)) return <code className="masked-value">{maskSensitiveValue(s, key)}</code>
  const lower = s.toLowerCase()
  if (lower === 'online') return <span style={{ width: '10px', height: '10px', borderRadius: '50%', backgroundColor: 'var(--color-success)', display: 'inline-block', boxShadow: '0 0 6px var(--color-success)', verticalAlign: 'middle' }} title="在线" />
  if (lower === 'offline') return <span style={{ width: '10px', height: '10px', borderRadius: '50%', backgroundColor: 'var(--color-danger)', display: 'inline-block', boxShadow: '0 0 6px var(--color-danger)', verticalAlign: 'middle' }} title="离线" />
  if (['active', 'ready', 'succeeded', 'healthy', 'enabled'].includes(lower)) return <span className="badge success">{labelValue(s)}</span>
  if (['failed', 'error', 'disabled', 'rollback_failed', 'unhealthy'].includes(lower)) return <span className="badge danger">{labelValue(s)}</span>
  if (['partial_failed', 'timeout', 'pending', 'running', 'requested', 'needed', 'detect', 'apply', 'unknown', 'periodic', 'warning', 'skipped'].includes(lower)) return <span className="badge warning">{labelValue(s)}</span>
  if (s === '<redacted>') return <code>已脱敏</code>
  if (s.startsWith('{') || s.startsWith('[') || s.length > 64) return <code>{s}</code>
  return s
}

function isTimeField(key: string) {
  return /(^|_)(created|updated|completed|checked|synced)_at$/i.test(key) || /_time$/i.test(key)
}

function formatTableTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function isSensitiveField(key: string) {
  return /(^|_)(password|token|secret|uuid|key)$/i.test(key) || ['proxy_uuid', 'proxy_password', 'subscription_token'].includes(key)
}

function maskSensitiveValue(value: string, key: string) {
  if (!value || value === '<redacted>') return '已脱敏'
  if (/uuid/i.test(key)) {
    if (value.length <= 13) return value
    return value.slice(0, 8) + '…' + value.slice(-4)
  }
  if (value.length <= 10) return '••••'
  return value.slice(0, 6) + '••••' + value.slice(-4)
}

function labelValue(v: any) { return valueLabels[String(v)] || String(v) }
function labelProtocol(p: Protocol | string) {
	if (p === 'shadowsocks') return 'SS'
	if (p === 'hy2') return 'HY2'
	if (p === 'anytls') return 'AnyTLS'
	if (p === 'vless') return 'VLESS'
	if (p === 'ssh') return 'SSH'
	if (p === 'socks') return 'SOCKS'
	return String(p)
}
function humanLabel(k: string) {
  if (fieldLabels[k]) return fieldLabels[k]
  const tokens: Record<string, string> = {
    server: '服务器', servers: '服务器', agent: 'Agent', agents: 'Agent', user: '用户', users: '用户', inbound: '入口', inbounds: '入口', outbounds: '出口', outbound: '出口', chain: '链路', chains: '链路', route: '路由', routing: '分流', rule: '规则', rules: '规则', source: '源', target: '目标',
    traffic: '流量', upload: '上传', download: '下载', bytes: '字节', total: '总数', active: '活跃', online: '在线', offline: '离线', degraded: '异常', healthy: '健康', unhealthy: '异常',
    pending: '等待中', running: '执行中', failed: '失败', succeeded: '成功', task: '任务', tasks: '任务', config: '配置', version: '版本', last: '最新', current: '当前', result: '结果', payload: '内容', json: 'JSON',
    created: '创建', updated: '更新', completed: '完成', at: '时间', id: 'ID', token: '令牌', status: '状态', type: '类型', mode: '模式', enabled: '启用', disabled: '禁用', latency: '延迟', ms: '毫秒', error: '错误', message: '消息', count: '数量'
  }
  return k.split('_').map(x => tokens[x] || x).join('')
}
function formatBytes(v: number) {
  if (!v) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let n = Number(v)
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n >= 10 || i === 0 ? n.toFixed(0) : n.toFixed(1)} ${units[i]}`
}
function formatByteRate(v: number) { return `${formatBytes(Math.max(0, Number(v) || 0))}/s` }
function formatSpeedLimit(v: number) { return v > 0 ? `${v} Mbps` : '不限速' }
function formatTrafficLimit(v: number) { return v > 0 ? formatBytes(v) : '不限量' }
function formatDate(v: string) {
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? v : d.toLocaleString()
}
function trafficQuotaLabel(v?: string) {
  return v === 'quota_exceeded' ? '已达量暂停' : '正常'
}
function userLimitMode(user: Pick<User, 'speed_limit_mbps' | 'traffic_limit_bytes'>): LimitMode {
  return (user.speed_limit_mbps || user.traffic_limit_bytes) ? 'custom' : 'inherit'
}
function enabledGroupsForUser(data: any, userID: number) {
  const groups: UserGroup[] = data.user_groups || []
  const members: UserGroupMember[] = data.user_group_members || []
  const groupByID = new Map(groups.filter(g => g.enabled !== false).map(g => [g.id, g]))
  return members
    .filter(m => m.user_id === userID && m.enabled !== false)
    .map(m => groupByID.get(m.group_id))
    .filter(Boolean) as UserGroup[]
}
function strictestPositive(values: number[]) {
  const positives = values.filter(v => Number(v) > 0)
  return positives.length ? Math.min(...positives) : 0
}
function effectiveUserLimits(data: any, user: User) {
  const groups = enabledGroupsForUser(data, user.id)
  const inheritedSpeed = strictestPositive(groups.map(g => g.speed_limit_mbps || 0))
  const inheritedTraffic = strictestPositive(groups.map(g => g.traffic_limit_bytes || 0))
  const userSpeed = user.speed_limit_mbps || 0
  const userTraffic = user.traffic_limit_bytes || 0
  const speed = userSpeed > 0 ? userSpeed : inheritedSpeed
  const traffic = userTraffic > 0 ? userTraffic : inheritedTraffic
  const source = userSpeed > 0 || userTraffic > 0 ? '用户单独设置' : groups.length && (inheritedSpeed || inheritedTraffic) ? '跟随用户组' : '不限速'
  return { speed, traffic, source, groups }
}
function userLimitSummary(data: any, user: User) {
  const limits = effectiveUserLimits(data, user)
  const groupText = limits.groups.length ? ` · ${limits.groups.map(g => g.name).join('、')}` : ''
  return `${limits.source} · ${formatSpeedLimit(limits.speed)} · ${formatTrafficLimit(limits.traffic)}${groupText}`
}
function groupLimitSummary(group: Pick<UserGroup, 'speed_limit_mbps' | 'traffic_limit_bytes'>) {
  return `${formatSpeedLimit(group.speed_limit_mbps || 0)} · ${formatTrafficLimit(group.traffic_limit_bytes || 0)}`
}
function trafficResetSummary(item: Pick<UserGroup, 'traffic_reset_mode' | 'traffic_reset_day'> | Pick<User, 'traffic_reset_mode' | 'traffic_reset_day'>) {
  return (item.traffic_reset_mode || 'monthly') === 'month_day' ? `每月 ${item.traffic_reset_day || 1} 日重置` : '自然月重置'
}

function describeAuditLog(log: AuditLog, data: any) {
  const action = String(log.action || '')
  const target = String(log.target || '')
  const actor = auditActorLabel(log, data)
  const actionLabel = auditActionLabel(action)
  const targetInfo = auditTargetInfo(log, data)
  const detail = auditDetailText(log, targetInfo.label)
  const tone = auditActionTone(action)
  return {
    title: auditTitle(log, actor, targetInfo),
    actionLabel,
    actor,
    targetType: targetInfo.type,
    targetLabel: targetInfo.label || auditTargetTypeLabel(target),
    detail,
    tone,
    ip: auditIPLabel(log.ip),
  }
}

function auditTitle(log: AuditLog, actor: string, target: { type: string; label: string }) {
  const action = String(log.action || '')
  const detail = String(log.detail || '').trim()
  if (action === 'login') return `${actor} 登录成功`
  if (action === 'login_totp') return `${actor} 通过双重认证登录成功`
  if (action === 'login_passkey') return `${actor} 使用通行密钥登录成功`
  if (action === 'bootstrap') return `${actor} 创建了首个管理员`
  if (action === 'change_password') return `${actor} 修改了登录密码`
  if (action === 'enable' && log.target === 'totp') return `${actor} 开启了双重认证`
  if (action === 'disable' && log.target === 'totp') return `${actor} 停用了双重认证`
  if (action === 'rotate' && log.target === 'totp-recovery-codes') return `${actor} 生成了新的双重认证恢复码`
  if (action === 'create' && log.target === 'passkey') return `${actor} 添加了通行密钥`
  if (action === 'delete' && log.target === 'passkey') return `${actor} 移除了通行密钥`
  if (action === 'agent_enroll') return `Agent 接入了服务器 ${target.label}`
  if (action === 'notify') return `系统发送了通知：${target.label}`
  if (action === 'notify_failed') return `通知发送失败：${target.label}`
  if (action === 'apply' && log.target === 'deployment') return `${actor} 下发了配置版本 ${detail || target.label}`
  if (action === 'dismiss' && log.target === 'deployment') return `${actor} 忽略了配置版本 ${detail || target.label} 的失败提醒`
  if (action === 'grant' && log.target === 'inbound-user') return `${actor} 授权了 ${target.label}`
  if (action === 'grant' && log.target === 'user-group-member') return `${actor} 加入了 ${target.label}`
  if (action === 'revoke' && log.target === 'user-group-member') return `${actor} 移除了 ${target.label}`
  if (action === 'revoke' && log.target === 'inbound-user') return `${actor} 撤销了 ${target.label}`
  if (action === 'grant' && log.target === 'inbound-access') return `${actor} 新增了入口权限：${target.label}`
  if (action === 'revoke' && log.target === 'inbound-access') return `${actor} 撤销了入口权限：${target.label}`
  if (action === 'update' && log.target === 'agent-config') return `${actor} 更新了 ${target.label} 的 Agent 设置`
  if (action === 'diagnose') return `${actor} 创建了 ${target.label} 的诊断任务`
  if (action === 'detect' && log.target === 'mtu') return `${actor} 发起了 ${target.label} 的 MTU 检测`
  if (action === 'create' && log.target === 'enroll-token') return `${actor} 生成了 ${target.label} 的 Agent 安装令牌`
  if (action === 'rotate' && log.target === 'subscription-token') return `${actor} 轮换了 ${target.label} 的订阅令牌`
  if (action === 'revoke' && log.target === 'subscription-token') return `${actor} 吊销了 ${target.label} 的订阅令牌`
  if (action === 'update' && log.target === 'subscription-age') return `${actor} 更新了 ${target.label} 的 Age 订阅设置`
  return `${actor}${auditActionVerb(action)}${target.label}`
}

function auditActorLabel(log: AuditLog, data: any) {
  if (log.actor_id) {
    const user = findByID<User>(data.users, log.actor_id)
    return user?.username ? `用户 ${user.username}` : `用户 #${log.actor_id}`
  }
  if (log.action === 'agent_enroll') return 'Agent'
  if (String(log.ip || '').toLowerCase() === 'controller') return '系统'
  return '系统'
}

function auditTargetInfo(log: AuditLog, data: any) {
  const target = String(log.target || '')
  const detail = String(log.detail || '').trim()
  const type = auditTargetTypeLabel(target)
  if (target === 'settings') return { type, label: '面板设置' }
  if (target === 'deployment') return { type, label: detail ? `配置版本 ${detail}` : '配置下发' }
  if (target === 'user' && detail && !numberFromString(detail)) return { type, label: detail }
  if (target === 'server' && detail && !numberFromString(detail)) return { type, label: detail }
  if (target === 'inbound-user') return { type, label: auditInboundUserLabel(detail, data) }
  if (target === 'user-group-member') return { type, label: auditGroupMemberLabel(detail, data) }
  if (target === 'inbound-access') return { type, label: auditInboundAccessLabel(detail, data) }
  if (target === 'notification_channel') return { type, label: auditNotificationLabel(detail, data) }
  if (target === 'agent-config' || target === 'mtu' || target === 'enroll-token') return { type, label: auditServerLabel(numberFromString(detail), data) }
  if (target === 'subscription-token' || target === 'subscription-age') return { type, label: auditUserLabel(numberFromString(detail), data) }
  if (target === 'totp' || target === 'totp-recovery-codes') return { type, label: auditUserLabel(numberFromString(detail), data) }
  const id = numberFromString(detail)
  const row = id ? auditResourceByTarget(target, id, data) : null
  if (row) return { type, label: resourceLabel(row, `${type} #${id}`) }
  if (id) return { type, label: `${type} #${id}` }
  return { type, label: detail || type }
}

function auditDetailText(log: AuditLog, targetLabel: string) {
  const detail = String(log.detail || '').trim()
  if (!detail) return ''
  if (log.target === 'settings') return detail.split(',').map(x => humanLabel(x.trim())).filter(Boolean).join('、')
  if (log.target === 'notification_channel') {
    const event = detail.split(':').slice(1).join(':')
    return event ? auditEventLabel(event) : ''
  }
  if (targetLabel && (detail === targetLabel || targetLabel.includes(`#${detail}`))) return ''
  if (/^\d+$/.test(detail) || /^\d+:\d+$/.test(detail)) return ''
  return detail
}

function auditActionLabel(action: string) {
  const labels: Record<string, string> = {
    bootstrap: '初始化', login: '登录', login_totp: '双重认证登录', login_passkey: '通行密钥登录', change_password: '改密',
    create: '创建', update: '更新', delete: '删除',
    grant: '授权', revoke: '撤销', rotate: '轮换', enable: '开启', disable: '停用',
    apply: '下发', dismiss: '忽略', diagnose: '诊断', detect: '检测',
    notify: '通知', notify_failed: '通知失败', agent_enroll: 'Agent',
  }
  return labels[action] || labelValue(action)
}

function auditActionVerb(action: string) {
  const verbs: Record<string, string> = {
    create: '创建了', update: '更新了', delete: '删除了',
    grant: '授权了', revoke: '撤销了', rotate: '轮换了',
    apply: '下发了', dismiss: '忽略了', diagnose: '诊断了', detect: '检测了',
    notify: '通知了', notify_failed: '通知失败：',
    bootstrap: '初始化了', login: '登录了', login_totp: '登录了', login_passkey: '登录了', change_password: '修改了', enable: '开启了', disable: '停用了',
  }
  return verbs[action] || `${auditActionLabel(action)}了`
}

function auditActionTone(action: string): AuditTone {
  if (['delete', 'notify_failed'].includes(action)) return 'danger'
  if (['update', 'apply', 'dismiss', 'diagnose', 'detect', 'rotate', 'revoke', 'disable'].includes(action)) return 'warning'
  if (['create', 'grant', 'bootstrap', 'login', 'login_totp', 'login_passkey', 'enable', 'agent_enroll', 'notify'].includes(action)) return 'success'
  return 'neutral'
}

function auditTargetTypeLabel(target: string) {
  const labels: Record<string, string> = {
    settings: '设置', user: '用户', server: '服务器', 'agent-config': 'Agent 设置',
    mtu: 'MTU', 'enroll-token': 'Agent 命令', inbound: '入口节点', 'inbound-user': '入口用户',
    'user-group': '用户组', 'user-group-member': '用户组成员', 'inbound-access': '入口权限',
    routing_rule: '分流规则', notification_channel: '通知渠道', port_forward: '端口转发',
    tunnel: '隧道', deployment: '配置下发', 'subscription-token': '订阅令牌', 'subscription-age': 'Age 订阅',
    totp: '双重认证', 'totp-recovery-codes': '恢复码', passkey: '通行密钥',
    'subscription-profile': '订阅配置', 'subscription-assignment': '订阅分配',
  }
  return labels[target] || humanLabel(target)
}

function auditResourceByTarget(target: string, id: number, data: any) {
  const collections: Record<string, string> = {
    server: 'servers', inbound: 'inbounds', outbound: 'outbounds', user: 'users',
    'user-group': 'user_groups', routing_rule: 'routing_rules', notification_channel: 'notification_channels',
    port_forward: 'port_forwards', tunnel: 'tunnels', 'subscription-profile': 'subscription_profiles',
    'subscription-assignment': 'subscription_assignments', 'inbound-access': 'inbound_access_grants',
  }
  return findByID(data[collections[target]], id)
}

function auditInboundUserLabel(detail: string, data: any) {
  const [inboundID, userID] = numericPair(detail)
  if (inboundID && userID) return `${auditUserLabel(userID, data)} 使用 ${auditInboundLabel(inboundID, data)}`
  const id = numberFromString(detail)
  const row = id ? findByID<InboundUser>(data.inbound_users, id) : null
  if (row) return `${auditUserLabel(row.user_id, data)} 使用 ${auditInboundLabel(row.inbound_id, data)}`
  return id ? `入口用户授权 #${id}` : detail || '入口用户授权'
}

function auditGroupMemberLabel(detail: string, data: any) {
  const [groupID, userID] = numericPair(detail)
  if (groupID && userID) return `${auditUserLabel(userID, data)} 到 ${auditGroupLabel(groupID, data)}`
  const id = numberFromString(detail)
  const row = id ? findByID<UserGroupMember>(data.user_group_members, id) : null
  if (row) return `${auditUserLabel(row.user_id, data)} 从 ${auditGroupLabel(row.group_id, data)}`
  return id ? `用户组成员 #${id}` : detail || '用户组成员'
}

function auditInboundAccessLabel(detail: string, data: any) {
  const id = numberFromString(detail)
  const grant = id ? findByID<InboundAccessGrant>(data.inbound_access_grants, id) : null
  if (!grant) return id ? `入口权限 #${id}` : detail || '入口权限'
  const subject = grant.subject_type === 'group' ? auditGroupLabel(grant.subject_id, data) : auditUserLabel(grant.subject_id, data)
  if (grant.scope_type === 'global') return `${subject} 使用全部入口`
  if (grant.scope_type === 'server') return `${subject} 使用 ${auditServerLabel(grant.server_id, data)} 的全部入口`
  return `${subject} 使用 ${auditInboundLabel(grant.inbound_id, data)}`
}

function auditNotificationLabel(detail: string, data: any) {
  const [idText, eventText = ''] = detail.split(':')
  const id = numberFromString(idText)
  const channel = id ? resourceLabel(findByID<NotificationChannel>(data.notification_channels, id), `通知渠道 #${id}`) : '通知渠道'
  const event = auditEventLabel(eventText)
  return event ? `${channel} · ${event}` : channel
}

function auditEventLabel(event: string) {
  const labels: Record<string, string> = {
    server_offline: '服务器离线',
    server_recovered: '服务器恢复',
  }
  return labels[event] || event
}

function auditServerLabel(id: number | undefined, data: any) {
  if (!id) return '服务器'
  return resourceLabel(findByID<Server>(data.servers, id), `服务器 #${id}`)
}

function auditUserLabel(id: number | undefined, data: any) {
  if (!id) return '用户'
  return resourceLabel(findByID<User>(data.users, id), `用户 #${id}`)
}

function auditInboundLabel(id: number | undefined, data: any) {
  if (!id) return '入口'
  return resourceLabel(findByID<Inbound>(data.inbounds, id), `入口 #${id}`)
}

function auditGroupLabel(id: number | undefined, data: any) {
  if (!id) return '用户组'
  return resourceLabel(findByID<UserGroup>(data.user_groups, id), `用户组 #${id}`)
}

function findByID<T extends { id: number }>(rows: T[] | undefined, id?: number) {
  if (!id || !Array.isArray(rows)) return undefined
  return rows.find(x => Number(x.id) === Number(id))
}

function numericPair(value: string) {
  const match = String(value || '').match(/^(\d+):(\d+)$/)
  return match ? [Number(match[1]), Number(match[2])] : [undefined, undefined]
}

function numberFromString(value: string | number | undefined) {
  const text = String(value ?? '').trim()
  return /^\d+$/.test(text) ? Number(text) : undefined
}

function auditIPLabel(value: string) {
  const raw = String(value || '').trim()
  if (!raw) return '—'
  if (raw === 'controller') return '系统内部'
  if (raw.startsWith('[')) return raw.slice(1, raw.indexOf(']') > 0 ? raw.indexOf(']') : undefined)
  const ipv4Port = raw.match(/^(\d{1,3}(?:\.\d{1,3}){3}):\d+$/)
  return ipv4Port ? ipv4Port[1] : raw
}

function resourceLabel(row: any, fallback: string) {
  if (!row || typeof row !== 'object') return fallback
  const label = row.name || row.username || row.group_name || row.entry_address || row.id
  return label ? String(label) : fallback
}

async function remove(client: ReturnType<typeof api>, path: string, load: () => Promise<void>, dialogs: DialogApi, row?: any) {
  const label = resourceLabel(row, path)
  const confirmed = await dialogs.confirm({
    title: '确认删除',
    tone: 'danger',
    confirmText: '删除',
    message: <div>
      <p>即将删除：<strong>{label}</strong></p>
      <p className="muted">删除后相关配置可能在下一次下发时从 Agent 配置中移除。请确认该资源不再被其它规则引用。</p>
    </div>,
  })
  if (!confirmed) return
  await client.request(path, { method: 'DELETE' })
  await load()
}

async function probeForwardNow(client: ReturnType<typeof api>, row: PortForward, load: () => Promise<void>, notify?: (message: string, tone?: ToastKind) => void) {
  await client.request(`/port-forwards/${row.id}/probe`, { method: 'POST', body: '{}' })
  await load()
  notify?.(`已创建「${row.name || '端口转发'}」探测，请在任务中心查看结果`, 'success')
}

async function setSubscriptionBurnPolicy(client: ReturnType<typeof api>, user: User, enabled: boolean, load: () => Promise<void>, dialogs: DialogApi, notify?: (message: string, tone?: ToastKind) => void) {
  const label = resourceLabel(user, '用户 #' + user.id)
  if (enabled) {
    const confirmed = await dialogs.confirm({
      title: '开启阅后即焚',
      confirmText: '开启',
      message: <div>
        <p>为用户 <strong>{label}</strong> 开启一次性订阅链接？</p>
        <p className="muted">复制链接不会销毁；客户端首次成功获取订阅内容后，链接立即失效，后续自动更新也会停止。需要再次使用时请重新签发。</p>
      </div>,
    })
    if (!confirmed) return
  }
  await client.request('/users/' + user.id + '/subscription-token/policy', {
    method: 'PATCH',
    body: JSON.stringify({ burn_after_read: enabled }),
  })
  await load()
  notify?.(`${label} 已${enabled ? '开启' : '关闭'}阅后即焚`, 'success')
}

async function rotateSub(client: ReturnType<typeof api>, u: Pick<User, 'id'> & Partial<Pick<User, 'username'>>, load: () => Promise<void>, dialogs: DialogApi, notify?: (message: string, tone?: ToastKind) => void) {
  const label = resourceLabel(u, '用户 #' + u.id)
  const confirmed = await dialogs.confirm({
    title: '确认轮换订阅令牌',
    tone: 'danger',
    confirmText: '轮换令牌',
    message: <div>
      <p>用户 <strong>{label}</strong> 的旧订阅链接会立即失效。</p>
      <p className="muted">确认后会生成新令牌，可在用户列表中复制新链接。</p>
    </div>,
  })
  if (!confirmed) return
  await client.request('/users/' + u.id + '/subscription-token/rotate', { method: 'POST', body: '{}' })
  await load()
  notify?.(`${label} 的订阅令牌已轮换，请重新复制链接`, 'success')
}

async function revokeSub(client: ReturnType<typeof api>, u: Pick<User, 'id'> & Partial<Pick<User, 'username'>>, load: () => Promise<void>, dialogs: DialogApi, notify?: (message: string, tone?: ToastKind) => void) {
  const label = resourceLabel(u, '用户 #' + u.id)
  const confirmed = await dialogs.confirm({
    title: '确认吊销订阅令牌',
    tone: 'danger',
    confirmText: '吊销令牌',
    message: <div>
      <p>即将吊销用户 <strong>{label}</strong> 的订阅令牌。</p>
      <p className="muted">吊销后现有订阅链接不可继续使用，通常需要重新下发配置清理关联节点。</p>
    </div>,
  })
  if (!confirmed) return
  await client.request('/users/' + u.id + '/subscription-token/revoke', { method: 'POST', body: '{}' })
  await load()
  notify?.(`${label} 的订阅令牌已吊销`, 'success')
}

createRoot(document.getElementById('root')!).render(
  <LazyMotion features={domAnimation}>
    <App />
  </LazyMotion>
)
