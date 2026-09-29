export type ToastTone = 'error' | 'success' | 'warning' | 'info'
export type Risk = 'low' | 'medium' | 'high'

export type EnvType =
  | 'string' | 'text' | 'integer' | 'number' | 'boolean' | 'select' | 'multi_select'
  | 'server' | 'servers' | 'secret' | 'url' | 'duration' | 'json'

export interface EnvOption { label: string; value: string }

export interface EnvField {
  name: string
  type: EnvType
  label: string
  description?: string
  required?: boolean
  default?: unknown
  placeholder?: string
  min_length?: number
  max_length?: number
  pattern?: string
  min?: number
  max?: number
  min_duration?: string
  max_duration?: string
  options?: EnvOption[]
  min_items?: number
  max_items?: number
  filter?: string[]
  schemes?: string[]
  depends_on?: { field: string; equals: unknown }
}

export interface Manifest {
  id: string
  name: string
  version: string
  description: string
  runtime: string
  entry: string
  capabilities: string[]
  http?: { hosts: string[]; methods: string[] }
  resources?: { servers?: { min: number; max?: number; reason?: string } }
  environment?: EnvField[]
  triggers: { schedule: boolean; events?: string[] }
  limits: Record<string, unknown>
}

export interface PermissionLine {
  capability: string
  group: string
  label: string
  description: string
  risk: Risk
  resource?: string
}

export interface FieldIssue { field: string; code: string; message: string }

export interface InstanceSummary {
  id: number
  name: string
  enabled: boolean
  status: string
  config_status: string
  auto_paused: boolean
  permission_review_required: boolean
  failure_streak: number
  last_run_at?: string
  last_success_at?: string
  last_error_code?: string
}

export interface Installation {
  id: number
  plugin_id: string
  name: string
  description: string
  enabled: boolean
  published: boolean
  has_draft: boolean
  version?: string
  package_id?: number
  publisher_identity: string
  publisher_name?: string
  signature_state?: string
  source_kind?: string
  source_repository?: string
  source_commit?: string
  icon?: string
  instances: InstanceSummary[]
  created_at: string
  updated_at: string
}

export interface VersionView {
  package_id: number
  version: string
  sha256: string
  publisher_identity: string
  publisher_name?: string
  signature_state: string
  source_kind: string
  source_repository?: string
  source_commit?: string
  active: boolean
  created_at: string
}

export interface CapabilityGrant { servers?: number[]; hosts?: string[]; channels?: number[] }
export interface Grant { capabilities: Record<string, CapabilityGrant> }

export interface GrantView extends Grant {
  package_id: number
  current: boolean
  revision: number
  approved_at: string
}

export interface CustomVar { name: string; type: EnvType; value?: unknown }

export interface Schedule {
  id: number
  instance_id: number
  kind: 'interval' | 'cron' | 'event'
  interval?: string
  cron?: string
  timezone?: string
  event?: string
  enabled: boolean
  next_due_at?: string
  last_fired_at?: string
  last_skipped_at?: string
  last_skip_reason?: string
}

export interface InstanceDetail extends InstanceSummary {
  installation_id: number
  revision: number
  issues: FieldIssue[]
  values: Record<string, unknown>
  custom: CustomVar[]
  secrets: Record<string, { configured: boolean; updated_at?: string }>
  grant?: GrantView
  schedules: Schedule[]
  state: { keys: number; bytes: number; max_keys: number; max_bytes: number }
  created_at: string
  updated_at: string
}

export interface InstallationDetail extends Installation {
  manifest?: Manifest
  permissions: PermissionLine[]
  http_hosts: string[]
  forbidden: string[]
  readme?: string
  license?: string
  versions: VersionView[]
  draft?: { manifest: string; source: string; updated_at?: string }
  source?: string
  can_develop: boolean
  can_install: boolean
  instance_details: InstanceDetail[]
}

export interface PermissionDiff {
  added_capabilities: string[]
  removed_capabilities: string[]
  added_hosts: string[]
  removed_hosts: string[]
  added_methods: string[]
  added_events: string[]
  added_secrets: string[]
  added_environment: string[]
  removed_environment: string[]
  changed_environment: string[]
  new_required: string[]
  resources_expanded: boolean
  expanded: boolean
}

export interface PackagePreview {
  plugin_id: string
  name: string
  version: string
  description: string
  sha256: string
  publisher_identity: string
  publisher_name: string
  signature_state: string
  source_kind: string
  source_repository?: string
  source_commit?: string
  manifest: Manifest
  permissions: PermissionLine[]
  http_hosts: string[]
  forbidden: string[]
  existing?: { installation_id: number; version: string; publisher_identity: string }
  diff: PermissionDiff
  action: string
  blocked?: string
}

export interface PackageSource {
  kind: 'upload' | 'github'
  package_base64?: string
  repository_url?: string
  ref?: string
  commit?: string
}

export interface EditorDiagnostics {
  valid: boolean
  manifest?: Manifest
  issues: FieldIssue[]
  undeclared_capabilities: string[]
  unused_environment: string[]
  permissions: PermissionLine[]
}

export interface Run {
  id: number
  uuid: string
  installation_id: number
  instance_id: number
  package_id: number
  plugin_key: string
  plugin_version: string
  trigger: string
  trigger_detail?: unknown
  caller_principal?: string
  status: string
  error_code?: string
  error_message?: string
  cancel_requested: boolean
  queued_at: string
  started_at?: string
  finished_at?: string
  capability_call_count: number
  http_call_count: number
  agent_operation_count: number
  result?: unknown
}

export interface RunLog { run_id: number; seq: number; level: string; message: string; created_at: string }

export interface StateEntry { instance_id: number; key: string; value: unknown; version: number; updated_at: string }

export interface AuditEvent {
  id: number
  created_at: string
  run_uuid: string
  capability: string
  resource: string
  result: string
  error_code?: string
  duration_ms: number
  detail?: unknown
}

export interface RuntimeStatus {
  enabled: boolean
  scheduler_paused: boolean
  runtime_installed: boolean
  install_command?: string
  worker_connected: boolean
  isolation_available: boolean
  isolation_mode: string
  isolation_reason?: string
  active_runs: number
  queued_runs: number
  max_concurrency: number
  max_timeout_seconds: number
  log_retention_days: number
  run_retention_days: number
}

export interface ServerOption {
  id: string
  name: string
  region_code?: string
  enrolled: boolean
  online: boolean
  ipv4: boolean
  ipv6: boolean
}

export interface CapabilitySpec {
  name: string
  group: string
  label: string
  description: string
  resource?: string
  risk: Risk
  agent_task: boolean
  audited: boolean
  methods: string[]
  rate_per_minute: number
}

export interface Catalog {
  capabilities: CapabilitySpec[]
  forbidden: string[]
  environment_types: EnvType[]
  custom_types: EnvType[]
  events: string[]
  runtime: string
}

export interface NotificationChannelOption { id: number; name?: string; kind?: string; type?: string }

export type RequestFn = <T = any>(path: string, init?: RequestInit) => Promise<T>

export interface PluginsWorkspaceProps {
  tab: 'plugins' | 'plugin-runs'
  data: any
  client: { requestV2: RequestFn }
  notify: (message: string, tone?: ToastTone) => void
  onNavigate: (tab: 'plugins' | 'plugin-runs') => void
}
