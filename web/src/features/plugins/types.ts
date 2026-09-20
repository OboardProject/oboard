export type PluginStatus = 'draft' | 'enabled' | 'disabled' | 'archived'

export interface Plugin {
  id: number
  name: string
  description: string
  owner_user_id: number
  status: PluginStatus
  created_at: string
  updated_at: string
}

export interface PluginRevision {
  id: number
  plugin_id: number
  revision_number: number
  status: string
  runtime: string
  sdk_version: string
  source?: string
  source_digest: string
  manifest?: any
  created_at: string
}

export interface PluginTrigger {
  id: number
  plugin_id: number
  revision_id: number
  name: string
  enabled: boolean
  kind: string
  spec?: any
  binding_revision: number
}

export interface PluginRun {
  id: number
  uuid: string
  plugin_id: number
  revision_id: number
  status: string
  mode: string
  trigger_kind: string
  error_code?: string
  skip_reason?: string
  created_at: string
  finished_at?: string
  result?: unknown
}

export interface PluginGrant {
  id: number
  plugin_id: number
  revision_id: number
  capabilities: string[] | any
  source_digest: string
  revoked_at?: string | null
}

export type PluginPackageInput = { package_zip: string; source?: never } | { source: { repository_url: string; ref: string }; package_zip?: never }
export type PluginPackageInstallInput = { package_zip: string; source?: never } | { source: { repository_url: string; commit: string }; package_zip?: never }
export interface PluginPackagePreview {
  metadata: { plugin_id: string; name: string; version: string; description: string }
  sha256: string
  existing_plugin_id: number
  active_revision_id: number
  capabilities: { added: string[]; removed: string[]; unchanged: string[] }
  source?: { kind: string; repository?: string; commit?: string }
  has_ui: boolean
}
export interface PluginInstallation {
  plugin_id: number; package_id: string; active_revision_id: number; installed: boolean
  config: Record<string, unknown>; updated_at: string
}
export interface PluginPackageVersion {
  plugin_id: number; revision_id: number; version: string; sha256: string
  manifest: { config_schema?: ConfigSchema; params_schema?: ConfigSchema; capabilities?: string[]; secrets?: Array<{ name: string; purpose?: string }>; network?: PluginNetworkPolicy; [key: string]: unknown }
  ui: PluginUIDocument | null; source_kind: string; source_repository: string; source_commit: string; created_at: string
}
export interface PluginNetworkPolicy {
  allowed_origins: string[]; allowed_methods: string[]; max_request_bytes?: number; max_response_bytes?: number
}
export interface ConfigSchema {
  type?: string; title?: string; description?: string; default?: unknown; enum?: unknown[]
  properties?: Record<string, ConfigSchema>; required?: string[]; items?: ConfigSchema
  minimum?: number; maximum?: number; minLength?: number; maxLength?: number; pattern?: string
}
export interface PluginUIField { name: string; label: string; type: 'text' | 'number' | 'boolean' | 'select'; required?: boolean; options?: string[] }
export interface PluginUIComponent {
  id: string; type: 'text' | 'stat' | 'table' | 'form' | 'button'; title?: string; text?: string
  columns?: Array<{ key: string; label: string }>; fields?: PluginUIField[]
  action?: { label: string; params?: Record<string, unknown> }
}
export interface PluginUIDocument { pages: Array<{ id: string; title: string; description?: string; components: PluginUIComponent[] }> }

export type ToastTone = 'error' | 'success' | 'warning' | 'info'

export interface PluginsWorkspaceProps {
  tab: 'plugins' | 'plugin-triggers' | 'plugin-runs'
  data: any
  client: {
    requestV2: (path: string, init?: RequestInit) => Promise<any>
  }
  notify: (message: string, tone?: ToastTone) => void
  onNavigate: (tab: 'plugins' | 'plugin-triggers' | 'plugin-runs') => void
}
