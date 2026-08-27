export type AuditLogRow = {
  id: number
  actor_id?: number
  action: string
  target: string
  detail: string
  ip: string
  created_at: string
}

export type AuditTone = 'success' | 'warning' | 'danger' | 'neutral'

export type DescribedAuditLog = {
  log: AuditLogRow
  title: string
  actionLabel: string
  actor: string
  targetType: string
  targetLabel: string
  detail: string
  tone: AuditTone
  ip: string
}

export type AuditLogGroup = {
  id: string
  logs: DescribedAuditLog[]
  title: string
  actionLabel: string
  actor: string
  targetType: string
  targetLabel: string
  detail: string
  tone: AuditTone
  ip: string
}

const CJK = /[\u3400-\u9fff]/
const LATIN_OR_DIGIT = /[A-Za-z0-9]/

export function mixCopy(text: string) {
  let value = String(text || '').replace(/\s+/g, ' ').trim()
  if (!value) return ''
  let out = value.slice(0, 1)
  for (let i = 1; i < value.length; i++) {
    const prev = out[out.length - 1]
    const next = value[i]
    if (prev !== ' ' && next !== ' ' && ((CJK.test(prev) && LATIN_OR_DIGIT.test(next)) || (LATIN_OR_DIGIT.test(prev) && CJK.test(next)))) {
      out += ' '
    }
    out += next
  }
  return out.replace(/([\u3400-\u9fff])\s+([\u3400-\u9fff])/g, '$1$2').replace(/\s+/g, ' ').trim()
}

export function describeAuditLog(log: AuditLogRow, data: any = {}): DescribedAuditLog {
  const actor = auditActorLabel(log, data)
  const targetInfo = auditTargetInfo(log, data)
  return {
    log,
    title: auditTitle(log, actor, targetInfo),
    actionLabel: auditActionLabel(log.action),
    actor,
    targetType: targetInfo.type,
    targetLabel: targetInfo.label || auditTargetTypeLabel(log.target),
    detail: auditDetailText(log, targetInfo.label),
    tone: auditActionTone(log.action),
    ip: auditIPLabel(log.ip),
  }
}

export function groupConsecutiveAuditLogs(items: DescribedAuditLog[]): AuditLogGroup[] {
  const groups: DescribedAuditLog[][] = []
  for (const item of items) {
    const current = groups[groups.length - 1]
    if (current && sameAuditGroup(current[0], item)) {
      current.push(item)
      continue
    }
    groups.push([item])
  }
  return groups.map(logs => {
    const first = logs[0]
    const ips = unique(logs.map(item => item.ip).filter(value => value && value !== '—'))
    return {
      id: `audit-${first.log.id}-${logs.length}`,
      logs,
      title: first.title,
      actionLabel: first.actionLabel,
      actor: first.actor,
      targetType: first.targetType,
      targetLabel: first.targetLabel,
      detail: logs.length === 1 ? first.detail : '',
      tone: first.tone,
      ip: ips.length <= 1 ? (ips[0] || first.ip) : `${ips.length} 个来源`,
    }
  })
}

export function formatAuditTimeRange(from: string, to: string, formatTime: (value: string) => string) {
  const start = formatTime(from)
  const end = formatTime(to)
  if (!start) return end
  if (!end || start === end) return start
  const [startDay, startClock] = splitDateTime(start)
  const [endDay, endClock] = splitDateTime(end)
  if (startDay && startClock && endDay && endClock && startDay === endDay) {
    return `${startDay} ${startClock} – ${endClock}`
  }
  return `${start} – ${end}`
}

export function formatDurationSeconds(seconds: number) {
  const value = Math.max(0, Math.floor(Number(seconds) || 0))
  if (value < 60) return `${value} 秒`
  const minutes = Math.floor(value / 60)
  const remain = value % 60
  if (minutes < 60) return remain ? `${minutes} 分 ${remain} 秒` : `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  if (!restMinutes) return `${hours} 小时`
  return remain ? `${hours} 小时 ${restMinutes} 分` : `${hours} 小时 ${restMinutes} 分钟`
}

function sameAuditGroup(a: DescribedAuditLog, b: DescribedAuditLog) {
  return a.log.action === b.log.action
    && a.log.target === b.log.target
    && Number(a.log.actor_id || 0) === Number(b.log.actor_id || 0)
    && a.actor === b.actor
    && a.targetLabel === b.targetLabel
}

function auditTitle(log: AuditLogRow, actor: string, target: { type: string; label: string }) {
  const action = String(log.action || '')
  const parsed = parseAuditDetail(log.detail)
  const client = oauthClientLabel(parsed)
  if (action === 'login') return copy(`${actor} 登录成功`)
  if (action === 'login_totp') return copy(`${actor} 通过双重认证登录成功`)
  if (action === 'login_passkey') return copy(`${actor} 使用通行密钥登录成功`)
  if (action === 'logout') return copy(`${actor} 退出了登录`)
  if (action === 'register') return copy(`${actor} 注册了账户`)
  if (action === 'bootstrap') return copy(`${actor} 创建了首个管理员`)
  if (action === 'auto_admin') return '系统自动创建了管理员账户'
  if (action === 'change_password') return copy(`${actor} 修改了登录密码`)
  if (action === 'enable' && log.target === 'totp') return copy(`${actor} 开启了双重认证`)
  if (action === 'disable' && log.target === 'totp') return copy(`${actor} 停用了双重认证`)
  if (action === 'rotate' && log.target === 'totp-recovery-codes') return copy(`${actor} 生成了新的双重认证恢复码`)
  if (action === 'create' && log.target === 'passkey') return copy(`${actor} 添加了通行密钥`)
  if (action === 'delete' && log.target === 'passkey') return copy(`${actor} 移除了通行密钥`)
  if (action === 'agent_enroll') return copy(`Agent 接入了服务器 ${target.label}`)
  if (action === 'notify') return copy(`系统发送了通知：${target.label}`)
  if (action === 'notify_failed') return copy(`通知发送失败：${target.label}`)
  if (action === 'notification_broadcast') return copy(`${actor} 群发了通知`)
  if (action === 'apply' && log.target === 'deployment') return copy(`${actor} 下发了配置版本 ${String(log.detail || '').trim() || target.label}`)
  if (action === 'dismiss' && log.target === 'deployment') return copy(`${actor} 忽略了配置版本 ${String(log.detail || '').trim() || target.label} 的失败提醒`)
  if (action === 'grant' && log.target === 'inbound-user') return copy(`${actor} 授权了 ${target.label}`)
  if (action === 'grant' && log.target === 'user-group-member') return copy(`${actor} 加入了 ${target.label}`)
  if (action === 'revoke' && log.target === 'user-group-member') return copy(`${actor} 移除了 ${target.label}`)
  if (action === 'revoke' && log.target === 'inbound-user') return copy(`${actor} 撤销了 ${target.label}`)
  if (action === 'grant' && log.target === 'inbound-access') return copy(`${actor} 新增了入口权限：${target.label}`)
  if (action === 'revoke' && log.target === 'inbound-access') return copy(`${actor} 撤销了入口权限：${target.label}`)
  if (action === 'update' && log.target === 'agent-config') return copy(`${actor} 更新了 ${target.label} 的 Agent 设置`)
  if (action === 'diagnose') return copy(`${actor} 创建了 ${target.label} 的诊断任务`)
  if (action === 'detect' && log.target === 'mtu') return copy(`${actor} 发起了 ${target.label} 的 MTU 检测`)
  if (action === 'create' && log.target === 'enroll-token') return copy(`${actor} 生成了 ${target.label} 的 Agent 安装令牌`)
  if (action === 'rotate' && log.target === 'subscription-token') return copy(`${actor} 轮换了 ${target.label} 的订阅令牌`)
  if (action === 'revoke' && log.target === 'subscription-token') return copy(`${actor} 吊销了 ${target.label} 的订阅令牌`)
  if (action === 'update' && log.target === 'subscription-age') return copy(`${actor} 更新了 ${target.label} 的 Age 订阅设置`)
  if (action === 'renew') return copy(`${actor} 续期了 ${target.label}`)
  if (action === 'oauth_token_refreshed') return copy(`${actor} 刷新了${possessiveClient(client)}访问令牌`)
  if (action === 'oauth_token_issued') return copy(`${actor} 签发了${possessiveClient(client)}访问令牌`)
  if (action === 'oauth_token_revoked') return copy(`${actor} 撤销了 OAuth 令牌`)
  if (action === 'oauth_token_denied') {
    const reason = oauthReasonLabel(String(parsed?.reason || ''))
    return copy(reason ? `${actor} 的令牌请求被拒绝：${reason}` : `${actor} 的令牌请求被拒绝`)
  }
  if (action === 'oauth_authorization_granted') return copy(`${actor} 授权了${possessiveClient(client)}访问`)
  if (action === 'oauth_authorization_denied') {
    const reason = oauthReasonLabel(String(parsed?.reason || ''))
    return copy(reason ? `${actor} 拒绝了${possessiveClient(client)}授权：${reason}` : `${actor} 拒绝了${possessiveClient(client)}授权`)
  }
  if (action === 'oauth_client_created') return copy(`${actor} 创建了 ${client || 'OAuth 应用'}`)
  if (action === 'oauth_refresh_reuse') return copy(`${actor} 的刷新令牌被重复使用，相关授权已撤销`)
  if (action === 'node_incident_resolved') return copy(`${actor} 恢复了节点故障`)
  if (action === 'node_incident_action_succeeded') return copy(`${actor} 完成了节点故障处置`)
  if (action === 'node_incident_action_failed') return copy(`${actor} 的节点故障处置失败`)
  if (action === 'node_publication_isolate') return copy(`${actor} 隔离了故障节点的发布`)
  if (action === 'node_publication_restore') return copy(`${actor} 恢复了故障节点的发布`)
  return copy(`${actor}${auditActionVerb(action)}${target.label}`)
}

function auditActorLabel(log: AuditLogRow, data: any) {
  if (log.actor_id) {
    const user = findByID(data.users, log.actor_id)
    return user?.username ? mixCopy(`用户 ${user.username}`) : `用户 #${log.actor_id}`
  }
  if (log.action === 'agent_enroll') return 'Agent'
  if (String(log.ip || '').toLowerCase() === 'controller') return '系统'
  return '系统'
}

function auditTargetInfo(log: AuditLogRow, data: any) {
  const target = String(log.target || '')
  const detail = String(log.detail || '').trim()
  const parsed = parseAuditDetail(detail)
  const type = auditTargetTypeLabel(target)
  if (target === 'settings') return { type, label: '面板设置' }
  if (target === 'deployment') return { type, label: detail && !looksRaw(detail) ? `配置版本 ${detail}` : '配置下发' }
  if (target === 'user' && detail && !numberFromString(detail) && !looksRaw(detail)) return { type, label: detail }
  if (target === 'server' && detail && !numberFromString(detail) && !looksRaw(detail)) return { type, label: detail }
  if (target === 'inbound-user') return { type, label: auditInboundUserLabel(detail, data) }
  if (target === 'user-group-member') return { type, label: auditGroupMemberLabel(detail, data) }
  if (target === 'inbound-access') return { type, label: auditInboundAccessLabel(detail, data) }
  if (target === 'notification_channel') return { type, label: auditNotificationLabel(detail, data) }
  if (target === 'agent-config' || target === 'mtu' || target === 'enroll-token') return { type, label: auditServerLabel(numberFromString(detail), data) }
  if (target === 'subscription-token' || target === 'subscription-age') return { type, label: auditUserLabel(numberFromString(detail), data) }
  if (target === 'totp' || target === 'totp-recovery-codes') return { type, label: auditUserLabel(numberFromString(detail), data) }
  if (target === 'oauth_grant' || target === 'oauth_client' || target === 'oauth_token') {
    return { type, label: oauthClientLabel(parsed) || type }
  }
  if (target === 'node_incident' || target === 'node_incident_action') {
    const [incidentID] = numericPair(detail)
    return { type, label: incidentID ? `故障 #${incidentID}` : type }
  }
  const id = numberFromString(detail)
  const row = id ? auditResourceByTarget(target, id, data) : null
  if (row) return { type, label: resourceLabel(row, `${type} #${id}`) }
  if (id) return { type, label: `${type} #${id}` }
  if (looksRaw(detail)) return { type, label: type }
  return { type, label: detail || type }
}

function auditDetailText(log: AuditLogRow, targetLabel: string) {
  const detail = String(log.detail || '').trim()
  if (!detail || looksRaw(detail)) {
    if (log.target === 'node_incident' || log.action === 'node_incident_resolved') {
      const [, seconds] = numericPair(detail)
      return seconds ? `中断 ${formatDurationSeconds(seconds)}` : ''
    }
    const parsed = parseAuditDetail(detail)
    const reason = oauthReasonLabel(String(parsed?.reason || ''))
    return reason
  }
  if (log.target === 'settings') return detail.split(',').map(item => humanField(item.trim())).filter(Boolean).join('、')
  if (log.target === 'notification_channel') {
    const event = detail.split(':').slice(1).join(':')
    return event ? auditEventLabel(event) : ''
  }
  if (targetLabel && (detail === targetLabel || targetLabel.includes(`#${detail}`))) return ''
  if (/^\d+$/.test(detail) || /^\d+:\d+$/.test(detail) || /^\d+:[^:]+:/.test(detail)) {
    if (log.action === 'node_incident_resolved' || log.target === 'node_incident') {
      const [, seconds] = numericPair(detail)
      return seconds ? `中断 ${formatDurationSeconds(seconds)}` : ''
    }
    return ''
  }
  return mixCopy(detail)
}

export function auditActionLabel(action: string) {
  const labels: Record<string, string> = {
    bootstrap: '初始化', auto_admin: '初始化', login: '登录', login_totp: '双重认证登录', login_passkey: '通行密钥登录', logout: '退出',
    register: '注册', change_password: '改密',
    create: '创建', update: '更新', delete: '删除',
    grant: '授权', revoke: '撤销', rotate: '轮换', enable: '开启', disable: '停用', renew: '续期',
    apply: '下发', dismiss: '忽略', diagnose: '诊断', detect: '检测',
    notify: '通知', notify_failed: '通知失败', notification_broadcast: '群发通知', agent_enroll: '接入',
    oauth_token_refreshed: '刷新令牌', oauth_token_issued: '签发令牌', oauth_token_denied: '拒绝令牌',
    oauth_token_revoked: '撤销令牌', oauth_authorization_granted: '授权应用', oauth_authorization_denied: '拒绝授权',
    oauth_client_created: '创建应用', oauth_refresh_reuse: '令牌复用',
    node_incident_resolved: '节点恢复', node_incident_action_succeeded: '处置完成', node_incident_action_failed: '处置失败',
    node_publication_isolate: '隔离发布', node_publication_restore: '恢复发布',
  }
  return labels[action] || readableAction(action)
}

function auditActionVerb(action: string) {
  const verbs: Record<string, string> = {
    create: '创建了', update: '更新了', delete: '删除了',
    grant: '授权了', revoke: '撤销了', rotate: '轮换了', renew: '续期了',
    apply: '下发了', dismiss: '忽略了', diagnose: '诊断了', detect: '检测了',
    notify: '通知了', notify_failed: '通知失败：',
    bootstrap: '初始化了', auto_admin: '初始化了', login: '登录了', login_totp: '登录了', login_passkey: '登录了',
    logout: '退出了', register: '注册了', change_password: '修改了', enable: '开启了', disable: '停用了',
  }
  if (verbs[action]) return verbs[action]
  const label = auditActionLabel(action)
  return label.endsWith('了') ? label : `${label}了`
}

function auditActionTone(action: string): AuditTone {
  if (['delete', 'notify_failed', 'oauth_token_denied', 'oauth_authorization_denied', 'oauth_refresh_reuse', 'node_incident_action_failed'].includes(action)) return 'danger'
  if (['update', 'apply', 'dismiss', 'diagnose', 'detect', 'rotate', 'revoke', 'disable', 'oauth_token_revoked', 'node_publication_isolate'].includes(action)) return 'warning'
  if ([
    'create', 'grant', 'bootstrap', 'auto_admin', 'login', 'login_totp', 'login_passkey', 'logout', 'enable', 'agent_enroll', 'notify',
    'oauth_token_issued', 'oauth_token_refreshed', 'oauth_authorization_granted', 'oauth_client_created',
    'node_incident_resolved', 'node_incident_action_succeeded', 'node_publication_restore', 'register', 'renew',
  ].includes(action)) return 'success'
  return 'neutral'
}

function auditTargetTypeLabel(target: string) {
  const labels: Record<string, string> = {
    settings: '设置', user: '用户', server: '服务器', 'agent-config': 'Agent 设置',
    mtu: 'MTU', 'enroll-token': 'Agent 命令', inbound: '入口节点', 'inbound-user': '入口用户',
    'user-group': '用户组', 'user-group-member': '用户组成员', 'inbound-access': '入口权限',
    routing_rule: '分流规则', notification_channel: '通知渠道', port_forward: '端口转发',
    tunnel: '隧道', deployment: '配置下发', 'subscription-token': '订阅令牌', 'subscription-age': 'Age 订阅',
    'subscription-custom-path': '自定义订阅路径', 'subscription-custom-path-policy': '自定义路径权限',
    totp: '双重认证', 'totp-recovery-codes': '恢复码', passkey: '通行密钥',
    'subscription-profile': '订阅配置', 'subscription-assignment': '订阅分配',
    oauth_grant: 'OAuth 授权', oauth_client: 'OAuth 应用', oauth_token: 'OAuth 令牌',
    node_incident: '节点故障', node_incident_action: '节点处置', notification_broadcast: '群发通知',
  }
  return labels[target] || humanField(target)
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
  if (inboundID && userID) return copy(`${auditUserLabel(userID, data)} 使用 ${auditInboundLabel(inboundID, data)}`)
  const id = numberFromString(detail)
  const row = id ? findByID(data.inbound_users, id) : null
  if (row) return copy(`${auditUserLabel(row.user_id, data)} 使用 ${auditInboundLabel(row.inbound_id, data)}`)
  return id ? `入口用户授权 #${id}` : '入口用户授权'
}

function auditGroupMemberLabel(detail: string, data: any) {
  const [groupID, userID] = numericPair(detail)
  if (groupID && userID) return copy(`${auditUserLabel(userID, data)} 到 ${auditGroupLabel(groupID, data)}`)
  const id = numberFromString(detail)
  const row = id ? findByID(data.user_group_members, id) : null
  if (row) return copy(`${auditUserLabel(row.user_id, data)} 从 ${auditGroupLabel(row.group_id, data)}`)
  return id ? `用户组成员 #${id}` : '用户组成员'
}

function auditInboundAccessLabel(detail: string, data: any) {
  const id = numberFromString(detail)
  const grant = id ? findByID(data.inbound_access_grants, id) : null
  if (!grant) return id ? `入口权限 #${id}` : '入口权限'
  const subject = grant.subject_type === 'group' ? auditGroupLabel(grant.subject_id, data) : auditUserLabel(grant.subject_id, data)
  if (grant.scope_type === 'global') return copy(`${subject} 使用全部入口`)
  if (grant.scope_type === 'server') return copy(`${subject} 使用 ${auditServerLabel(grant.server_id, data)} 的全部入口`)
  return copy(`${subject} 使用 ${auditInboundLabel(grant.inbound_id, data)}`)
}

function auditNotificationLabel(detail: string, data: any) {
  const [idText, eventText = ''] = detail.split(':')
  const id = numberFromString(idText)
  const channel = id ? resourceLabel(findByID(data.notification_channels, id), `通知渠道 #${id}`) : '通知渠道'
  const event = auditEventLabel(eventText)
  return event ? copy(`${channel} · ${event}`) : channel
}

function auditEventLabel(event: string) {
  const labels: Record<string, string> = {
    server_offline: '服务器离线',
    server_recovered: '服务器恢复',
  }
  return labels[event] || readableAction(event)
}

function auditServerLabel(id: number | undefined, data: any) {
  if (!id) return '服务器'
  return resourceLabel(findByID(data.servers, id), `服务器 #${id}`)
}

function auditUserLabel(id: number | undefined, data: any) {
  if (!id) return '用户'
  return resourceLabel(findByID(data.users, id), `用户 #${id}`)
}

function auditInboundLabel(id: number | undefined, data: any) {
  if (!id) return '入口'
  return resourceLabel(findByID(data.inbounds, id), `入口 #${id}`)
}

function auditGroupLabel(id: number | undefined, data: any) {
  if (!id) return '用户组'
  return resourceLabel(findByID(data.user_groups, id), `用户组 #${id}`)
}

function oauthClientLabel(parsed: Record<string, any> | null) {
  const raw = String(parsed?.client_name || parsed?.client_id || '').trim()
  if (!raw) return ''
  if (raw === 'oboard-web') return '面板'
  return mixCopy(`应用 ${raw}`)
}

function possessiveClient(client: string) {
  if (!client) return ''
  if (client === '面板') return '面板'
  return `${client} 的`
}

function oauthReasonLabel(reason: string) {
  const labels: Record<string, string> = {
    invalid_request: '请求无效',
    login_required: '需要登录',
    scope_denied: '权限不足',
    user_denied: '用户拒绝',
    invalid_consent: '授权无效',
    code_issue_failed: '授权码签发失败',
    token_issue_failed: '令牌签发失败',
    invalid_grant: '授权无效',
    token_family_revoked: '令牌族已撤销',
    inactive_grant: '授权已失效',
    disabled_client: '应用已停用',
    user_not_authorized: '用户无权访问',
  }
  return labels[reason] || ''
}

function parseAuditDetail(detail: string) {
  const text = String(detail || '').trim()
  if (!text.startsWith('{')) return null
  try {
    const parsed = JSON.parse(text)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, any> : null
  } catch {
    return null
  }
}

function looksRaw(value: string) {
  const text = String(value || '').trim()
  if (!text) return false
  if (text.startsWith('{') || text.startsWith('[')) return true
  if (text.length > 96) return true
  return /["{}[\]=]/.test(text) && /[_a-z]{3,}/.test(text)
}

function readableAction(value: string) {
  return mixCopy(String(value || '').split('_').filter(Boolean).join(' '))
}

function humanField(key: string) {
  const labels: Record<string, string> = {
    audit_enabled: '审计总开关',
    subscription_audit_enabled: '订阅审计',
    connection_audit_enabled: '连接审计',
    audit_action: '审计动作',
    audit_policy: '审计策略',
    oauth_grant: 'OAuth 授权',
    oauth_client: 'OAuth 应用',
    oauth_token: 'OAuth 令牌',
    node_incident: '节点故障',
  }
  if (labels[key]) return labels[key]
  return readableAction(key)
}

function findByID<T extends { id: number }>(rows: T[] | undefined, id?: number) {
  if (!id || !Array.isArray(rows)) return undefined
  return rows.find(item => Number(item.id) === Number(id))
}

function resourceLabel(row: any, fallback: string) {
  if (!row || typeof row !== 'object') return fallback
  const label = row.name || row.username || row.group_name || row.entry_address || row.id
  return label ? String(label) : fallback
}

function numericPair(value: string) {
  const match = String(value || '').match(/^(\d+):(\d+)$/)
  return match ? [Number(match[1]), Number(match[2])] : [undefined, undefined]
}

function numberFromString(value: string | number | undefined) {
  const text = String(value ?? '').trim()
  return /^\d+$/.test(text) ? Number(text) : undefined
}

export function auditIPLabel(value: string) {
  const raw = String(value || '').trim()
  if (!raw) return '—'
  if (raw === 'controller' || raw === 'automation') return '系统内部'
  if (raw.startsWith('[')) return raw.slice(1, raw.indexOf(']') > 0 ? raw.indexOf(']') : undefined)
  const ipv4Port = raw.match(/^(\d{1,3}(?:\.\d{1,3}){3}):\d+$/)
  return ipv4Port ? ipv4Port[1] : raw
}

function unique(values: string[]) {
  return Array.from(new Set(values))
}

function splitDateTime(value: string) {
  const match = String(value || '').match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}(?::\d{2})?)$/)
  return match ? [match[1], match[2]] : ['', value]
}

function copy(text: string) {
  return mixCopy(text)
}
