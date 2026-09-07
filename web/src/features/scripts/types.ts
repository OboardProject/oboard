export type ScriptStatus = 'draft' | 'enabled' | 'disabled' | 'archived'

export interface Script {
  id: number
  name: string
  description: string
  owner_user_id: number
  status: ScriptStatus
  created_at: string
  updated_at: string
}

export interface ScriptRevision {
  id: number
  script_id: number
  revision_number: number
  status: string
  runtime: string
  sdk_version: string
  source?: string
  source_digest: string
  manifest?: any
  created_at: string
}

export interface ScriptTrigger {
  id: number
  script_id: number
  revision_id: number
  name: string
  enabled: boolean
  kind: string
  spec?: any
  binding_revision: number
}

export interface ScriptRun {
  id: number
  uuid: string
  script_id: number
  revision_id: number
  status: string
  mode: string
  trigger_kind: string
  error_code?: string
  skip_reason?: string
  created_at: string
  finished_at?: string
}

export interface ScriptGrant {
  id: number
  script_id: number
  revision_id: number
  capabilities: string[] | any
  source_digest: string
  revoked_at?: string | null
}

export type ToastTone = 'error' | 'success' | 'warning' | 'info'

export interface ScriptsWorkspaceProps {
  tab: 'scripts' | 'script-triggers' | 'script-runs'
  data: any
  client: {
    requestV2: (path: string, init?: RequestInit) => Promise<any>
  }
  notify: (message: string, tone?: ToastTone) => void
  onNavigate: (tab: 'scripts' | 'script-triggers' | 'script-runs') => void
}
