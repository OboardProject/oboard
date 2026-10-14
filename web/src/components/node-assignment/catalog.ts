import type { ScopeNode } from './NodeScopeMenu'

export type CatalogPlanRef = { plan_id: number; name: string; display_group?: string; display_name: string; has_display_name_override: boolean }

export type CatalogNode = ScopeNode & {
  name: string
  source_name: string
  global_name_override?: string | null
  has_global_name_override: boolean
  effective_global_name: string
  metadata_lock_version: number
  entry_server_name?: string
  entry_protocol?: string
  entry_port?: number
  path_summary?: string[]
  enabled: boolean
  status: 'ok' | 'offline' | 'disabled'
  group?: string
  plans: CatalogPlanRef[]
  effective_users: number
  allow_exceptions: number
  deny_exceptions: number
}

export const catalogProtocols = ['vless', 'hysteria2', 'anytls', 'shadowsocks', 'mieru', 'socks', 'wireguard']

export const nodeStatusLabels: Record<string, string> = { ok: '正常', offline: '离线', disabled: '已禁用' }

export function nodeStatusVariant(status: string) {
  return status === 'ok' ? 'success' as const : status === 'disabled' ? 'secondary' as const : 'warning' as const
}

export function nodeTypeLabel(type: string) {
  if (type === 'proxy_path') return '代理拓扑'
  if (type === 'external_outbound') return '导入节点'
  if (type === 'inbound') return '独立入口'
  return type
}
