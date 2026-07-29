// Wire types shared between the proxy-path canvas components and the main app.
// These mirror the Controller JSON contracts; keep them in sync with
// oboard/internal/model/types.go.

export type Protocol = 'vless' | 'hy2' | 'anytls' | 'shadowsocks' | 'ssh'
export type ExternalProtocol = Exclude<Protocol, 'ssh'> | 'socks'
export type EntryIPMode = 'auto' | 'ipv4' | 'ipv6' | 'custom'
export type RegionMode = 'auto' | 'manual'
export type DNSRecordTypes = 'auto' | 'a' | 'aaaa' | 'both'
export type CertificateMode = 'external' | 'auto' | 'exact' | 'wildcard' | 'explicit'

export type Server = {
  id: number
  name: string
  entry_address: string
  public_ipv4: string
  public_ipv6: string
  region_code: string
  detected_region_code: string
  region_mode: RegionMode
  entry_ip_mode: EntryIPMode
  listen_ip: string
  ip_stack: string
  udp_inbound_mode: string
  mtu_mode: string
  mtu_value: number
  mtu_probe_host: string
  mtu_probe_port: number
  mtu_overhead_bytes: number
  bbr_enabled: boolean
  port_range_start: number
  port_range_end: number
  ssh_port: number
  status: string
  os: string
  distro_id: string
  distro_version: string
  distro_name: string
  libc: string
  service_manager: string
  package_manager: string
  arch: string
  cpu: string
  cpu_usage_percent: number
  memory_used_bytes: number
  memory_total_bytes: number
  agent_memory_bytes: number
  agent_id?: string
  agent_version: string
  agent_build: string
  sing_box_version: string
  monitoring_mode: 'lightweight' | 'standard'
  traffic_reset_mode: string
  traffic_reset_day: number
  network_upload_bps: number
  network_download_bps: number
  traffic_upload_bytes: number
  traffic_download_bytes: number
  traffic_period_start?: string
  traffic_period_end?: string
  connectivity_probe_enabled: boolean
  connection_audit_enabled: boolean
  connectivity_status: string
  connectivity_latency_ms: number
  connectivity_checked_at?: string
  connectivity_error?: string
  telemetry_updated_at?: string
}

export type Inbound = {
  id: number
  server_id: number
  name: string
  protocol: Protocol
  listen_ip: string
  port: number
  entry_ip_mode: EntryIPMode
  external_ip: string
  dns_sync_enabled: boolean
  dns_credential_id?: number
  dns_domain: string
  dns_proxy_enabled: boolean
  dns_record_types: DNSRecordTypes
  ddns_enabled: boolean
  ddns_interval_seconds: number
  dns_sync_status: string
  dns_sync_error: string
  dns_last_synced_at?: string
  tls: boolean
  certificate_mode?: CertificateMode
  certificate_id?: number
  certificate_domain?: string
  config_json: string
  enabled: boolean
}

export type ExternalOutbound = {
  id: number
  server_id?: number
  name: string
  protocol: ExternalProtocol
  scope: 'global' | 'server'
  target_address: string
  target_port: number
  config_json: string
  expose_to_users: boolean
  enabled: boolean
}

export type ProxyPathNamePart =
  | { kind: 'literal'; value: string }
  | { kind: 'server'; server_id: number }
  | { kind: 'external_outbound'; external_outbound_id: number }

export type ProxyPath = {
  id: number
  kind: 'chain' | 'direct'
  branch_source_step_id?: number
  name: string
  name_mode: 'auto' | 'custom'
  name_template: ProxyPathNamePart[]
  inbound_id: number
  enabled: boolean
}

export type ProxyPathTransportMode = 'singbox' | 'port_forward' | 'tunnel'

export type ProxyPathStep = {
  id: number
  path_id: number
  position: number
  node_type: 'server_inbound' | 'imported' | 'warp'
  transport_mode?: ProxyPathTransportMode
  processing_role?: boolean
  server_id?: number
  inbound_id?: number
  external_outbound_id?: number
  config_json?: string
}
