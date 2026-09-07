import * as React from 'react'
import { Badge } from '../ui/badge'

export type AuthorizationDeliveryStatus =
  | 'authorized'
  | 'preparing'
  | 'revoking'
  | 'waiting_offline'
  | 'failed'
  | 'upgrade_required'
  | 'external_credential'

const labels: Record<AuthorizationDeliveryStatus, string> = {
  authorized: '已授权',
  preparing: '准备中',
  revoking: '正在撤销',
  waiting_offline: '等待离线节点',
  failed: '失败',
  upgrade_required: '需升级',
  external_credential: '外部凭据',
}

const variants: Record<AuthorizationDeliveryStatus, React.ComponentProps<typeof Badge>['variant']> = {
  authorized: 'success',
  preparing: 'secondary',
  revoking: 'warning',
  waiting_offline: 'warning',
  failed: 'destructive',
  upgrade_required: 'warning',
  external_credential: 'outline',
}

export function AuthorizationStatusBadge({
  status,
  className,
}: {
  status: AuthorizationDeliveryStatus
  className?: string
}) {
  return (
    <Badge variant={variants[status] || 'soft'} className={className} title={labels[status]}>
      {labels[status]}
    </Badge>
  )
}

export function deriveAccessChangeStatus(input: {
  status?: string
  kind?: string
  completion?: string
  pending_reason?: string
  retryable?: boolean
}): AuthorizationDeliveryStatus {
  if (input.completion === 'upgrade_required' || input.pending_reason === 'agent_upgrade_required') return 'upgrade_required'
  if (input.completion === 'waiting_offline' || input.pending_reason === 'agent_offline') return 'waiting_offline'
  if (input.completion === 'failed' || input.status === 'failed') return 'failed'
  if (input.kind === 'revoke' && (input.status === 'preparing' || input.status === 'activating' || input.status === 'finalizing' || input.completion === 'pending')) {
    return 'revoking'
  }
  if (input.status === 'preparing' || input.status === 'activating' || input.status === 'finalizing' || input.completion === 'pending') {
    return 'preparing'
  }
  return 'authorized'
}

export function deriveServerDeliveryStatus(server: {
  authorization_confirmed?: boolean
  users_confirmed?: boolean
  authorization_pending_reason?: string
  users_pending_reason?: string
  users_fallback?: string
  status?: string
}): AuthorizationDeliveryStatus {
  const reason = server.authorization_pending_reason || server.users_pending_reason || ''
  if (reason === 'agent_upgrade_required') return 'upgrade_required'
  if (reason === 'agent_offline' || String(server.status || '').toLowerCase() === 'offline') {
    if (server.authorization_confirmed === false || server.users_confirmed === false) return 'waiting_offline'
  }
  if (reason === 'delivery_failed' || reason === 'runtime_unavailable') return 'failed'
  if (server.authorization_confirmed === false || server.users_confirmed === false) return 'preparing'
  return 'authorized'
}

export function ServerDeliveryBadge({ server }: { server: Parameters<typeof deriveServerDeliveryStatus>[0] }) {
  const status = deriveServerDeliveryStatus(server)
  if (status === 'authorized') return null
  return <AuthorizationStatusBadge status={status} />
}
