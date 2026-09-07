import * as React from 'react'
import {
  deriveAccessChangeStatus,
  type AuthorizationDeliveryStatus,
} from '../components/authorization/AuthorizationStatusBadge'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

export type AccessChangeStatusSnapshot = {
  change_id: number
  status?: string
  kind?: string
  completion?: string
  pending_reason?: string
  pending_servers?: number[]
  retryable?: boolean
  last_error?: string
  applied_authorization_revision?: number
  applied_users_revision?: number
  desired_state?: string
  effective_state?: string
}

export function useAccessChangeStatus(client: AnyClient, changeID?: number | null) {
  const [snapshot, setSnapshot] = React.useState<AccessChangeStatusSnapshot | null>(null)
  const latestID = React.useRef(0)

  const reload = React.useCallback(async (id = changeID) => {
    if (!id) {
      setSnapshot(null)
      return null
    }
    const requestID = ++latestID.current
    const res = await client.request<AccessChangeStatusSnapshot & { access_change?: { id: number; status?: string; kind?: string } }>(`/access-changes/${id}`)
    if (requestID !== latestID.current) return null
    const next: AccessChangeStatusSnapshot = {
      change_id: res.change_id || res.access_change?.id || id,
      status: res.status || res.access_change?.status,
      kind: res.kind || res.access_change?.kind,
      completion: res.completion,
      pending_reason: res.pending_reason,
      pending_servers: res.pending_servers,
      retryable: res.retryable,
      last_error: res.last_error,
      applied_authorization_revision: res.applied_authorization_revision,
      applied_users_revision: res.applied_users_revision,
      desired_state: res.desired_state,
      effective_state: res.effective_state,
    }
    setSnapshot(next)
    return next
  }, [changeID, client])

  React.useEffect(() => {
    if (!changeID) {
      setSnapshot(null)
      return
    }
    void reload(changeID)
    const timer = window.setInterval(() => {
      void reload(changeID)
    }, 2500)
    return () => window.clearInterval(timer)
  }, [changeID, reload])

  const status: AuthorizationDeliveryStatus = snapshot
    ? deriveAccessChangeStatus(snapshot)
    : 'authorized'

  return { snapshot, status, reload }
}
