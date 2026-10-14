export type Client = { request<T = any>(path: string, init?: RequestInit): Promise<T> }
export type Notify = (message: string, tone?: 'success' | 'error' | 'warning') => void

export type NodeGroup = { id: number; kind: 'oboard' | 'remote' | 'manual'; system_key?: string; name: string; node_count: number }
export type NodeSource = { id: number; group_id: number; url_display: string; status: string; last_error?: string; last_success_at?: string | null }
export type FilterRule = { type: string; value: string }
export type SubscriptionOutput = { id: number; name: string; is_default: boolean; enabled: boolean; group_ids: number[]; filters?: FilterRule[] }
export type LibraryNode = { id: string; group_id: number; name: string; protocol: string; source: string; copyable: boolean; editable: boolean }
export type ImportPreview = { nodes: Array<{ name: string; protocol: string; fingerprint: string }>; issues: Array<{ index: number; name?: string; message: string }> }
export type Workspace = { subject: { id: number; username: string; nickname?: string }; node_groups: NodeGroup[]; node_sources: NodeSource[]; subscription_outputs: SubscriptionOutput[] }

export const groupKindLabels: Record<NodeGroup['kind'], string> = { oboard: '系统组', remote: '远程订阅', manual: '手动节点' }

export const sourceStatusLabels: Record<string, string> = { ready: '已同步', error: '同步失败' }

export const subscriptionFormats = ['auto', 'mihomo', 'stash', 'surge', 'surge-mac', 'loon', 'egern', 'shadowrocket', 'qx', 'surfboard', 'sing-box', 'v2ray', 'v2ray-uri']

export const filterTypeLabels: Record<string, string> = {
  keep_name: '名字正则保留', drop_name: '名字正则排除',
  keep_protocol: '协议保留', drop_protocol: '协议排除',
  keep_region: '地区保留', drop_region: '地区排除',
  keep_group: '分组保留', drop_group: '分组排除',
}

export const filterProtocols = ['vless', 'vmess', 'trojan', 'tuic', 'hysteria2', 'anytls', 'shadowsocks', 'socks5', 'socks', 'snell', 'mieru', 'ssh']

export function errorText(error: unknown) {
  return error instanceof Error ? error.message : String((error as { message?: string })?.message || error)
}
