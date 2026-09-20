import React from 'react'
import type { TimeCorrectionMode } from '../components/proxy-path/types'

const valueLabels: Record<string, string> = {
  admin: '管理员', operator: '操作员', viewer: '只读', active: '活跃', online: '在线', offline: '离线', unknown: '未知', healthy: '健康', unhealthy: '异常',
  enabled: '已启用', disabled: '已禁用', true: '启用', false: '禁用', succeeded: '成功', success: '成功', skipped: '已跳过', stale: '已过期', warning: '需关注', failed: '失败', partial_failed: '部分失败', timeout: '超时', error: '错误', pending: '等待中', running: '执行中', requested: '已请求', needed: '需要申请', ready: '就绪',
  rollback_failed: '回滚失败', direct: '直连', block: '阻断', outbound: '出口', external: '导入节点', proxy_path: '代理链路', family_split: 'IPv4 / IPv6 分离合并', chain: '链式代理', warp: 'WARP', interface: '指定网卡', source_prefix: '地址前缀', socks: 'SOCKS',
  singbox_source: 'sing-box JSON', singbox_binary: 'sing-box SRS', mihomo_domain: 'Mihomo domain', mihomo_ipcidr: 'Mihomo IP-CIDR', mihomo_classical: 'Mihomo classical', blackmatrix_classical: 'Blackmatrix7 规则',
  global: '全局', server: '服务器', auto: '自动（IPv4 优先）', ipv4: 'IPv4', ipv6: 'IPv6', custom: '自定义', ipv4_only: '仅 IPv4', ipv6_only: '仅 IPv6', dual_stack: '双栈', prefer_ipv4: '优先 IPv4', prefer_ipv6: '优先 IPv6',
  a: 'A', aaaa: 'AAAA', both: 'A + AAAA',
  allow: '允许', uot: 'UoT', never: '从不', first_apply: '首次下发', periodic: '定期', always: '每次', sampled: '实际连接采样', periodic_sampled: '定期+采样',
  detect: '仅检测', apply: '检测并应用', tcp_udp: 'TCP+UDP', builtin: '内置', wireguard: 'WireGuard', ssh: 'SSH', doh: 'DoH', dot: 'DoT', udp: 'UDP', tcp: 'TCP',
  cloudflare: 'Cloudflare', google: 'Google', quad9: 'Quad9', alidns: '阿里 DNS', dnspod: 'DNSPod', remote: '远程', local: '本地',
  apply_deployment: '应用部署', apply_core_config: '下发核心配置', probe_inbounds: '检查入口监听', probe_inbounds_external: '检查公网端口', probe_port_forwards: '探测端口转发', probe_external_egress: '探测第三方出口',
  benchmark_dns: '解析服务检查', detect_mtu: 'MTU 检测', check_time: '时间检测', update_agent_config: '同步 Agent 配置', diagnose_network: '网络诊断', list_network_interfaces: '读取网卡',
  collect_logs: '拉取日志', manage_logs: '管理日志',
  install_agent: '安装 Agent', update_agent: '更新 Agent', uninstall_agent: '卸载 Agent',
}

export function cell(v: any, key = '') {
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
  if (['partial_failed', 'timeout', 'pending', 'running', 'requested', 'needed', 'detect', 'apply', 'unknown', 'periodic', 'warning', 'skipped', 'stale'].includes(lower)) return <span className="badge warning">{labelValue(s)}</span>
  if (s === '<redacted>') return <code>已脱敏</code>
  if (s.startsWith('{') || s.startsWith('[') || s.length > 64) return <code>{s}</code>
  return s
}

export function isTimeField(key: string) {
  return /(^|_)(created|updated|completed|checked|synced)_at$/i.test(key) || /_time$/i.test(key)
}

export function formatTableTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function isSensitiveField(key: string) {
  return /(^|_)(password|token|secret|uuid|key)$/i.test(key) || ['proxy_uuid', 'proxy_password', 'subscription_token'].includes(key)
}

export function maskSensitiveValue(value: string, key: string) {
  if (!value || value === '<redacted>') return '已脱敏'
  if (/uuid/i.test(key)) {
    if (value.length <= 13) return value
    return value.slice(0, 8) + '…' + value.slice(-4)
  }
  if (value.length <= 10) return '••••'
  return value.slice(0, 6) + '••••' + value.slice(-4)
}

export function labelValue(v: any) { return valueLabels[String(v)] || String(v) }
export function formatBytes(v: number) {
  if (!v) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let n = Number(v)
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n >= 10 || i === 0 ? n.toFixed(0) : n.toFixed(1)} ${units[i]}`
}
export function formatDate(v: string) {
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? v : d.toLocaleString()
}
export function timeCorrectionModeLabel(mode?: TimeCorrectionMode) {
  if (mode === 'auto') return '自动校时'
  if (mode === 'ntp') return '逻辑校时'
  return '仅检测'
}
