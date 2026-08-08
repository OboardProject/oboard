export type AccessLevel = 'read' | 'operate'

export interface OAuthClient {
  id: string
  name: string
  redirect_uris: string[]
  identity_type: 'cimd' | 'preregistered' | string
  metadata_uri: string
  metadata_hash: string
  metadata_etag: string
  metadata_fetched_at: string | null
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface ResourceBoundarySelection {
  selection: 'all' | 'selected' | 'none'
  ids?: string[]
  include_future?: boolean
  allow_create?: boolean
}

export interface ResourceBoundary {
  version: number
  resources?: Record<string, ResourceBoundarySelection>
  global_capabilities?: string[]
  destructive_operations?: boolean
}

export interface OAuthGrant {
  id: string
  client_id: string
  client_name?: string
  user_id: number
  username?: string
  access_level: AccessLevel
  resource_boundary?: ResourceBoundary
  approval_profile?: { auto_approve_risk: number } | null
  offline_access: boolean
  policy_version: number
  role_version: number
  consent_version: number
  status: string
  created_at: string
  last_used_at: string | null
  expires_at: string | null
  revoked_at: string | null
  revoke_reason?: string
}

export type ToastTone = 'error' | 'success' | 'warning' | 'info'

export interface MCPAccessPageProps {
  requestV2: (path: string, init?: RequestInit) => Promise<any>
  notify: (message: string, tone?: ToastTone) => void
  confirm: (options: {
    title: string
    message: string
    confirmText?: string
    tone?: 'danger'
  }) => Promise<boolean>
}
