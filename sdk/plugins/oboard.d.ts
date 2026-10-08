// Type declarations for OBoard plugins (runtime "oboard-js").
//
// A plugin is one main.js that declares `function main(run)` (optionally
// async). The runtime has no require, import, process, fs, net, timers,
// fetch or WebAssembly. Everything outside pure computation goes through the
// `oboard` SDK, and every SDK call is checked against the manifest, the
// administrator grant and the resource scope before OBoard performs it.

export {}

declare global {
  type ServerID = string
  type IPFamily = 'auto' | 'ipv4' | 'ipv6'

  /** Structured failure raised by every SDK call. `code` is stable. */
  class OBoardError extends Error {
    constructor(code: OBoardErrorCode | string, message?: string)
    readonly name: 'OBoardError'
    readonly code: OBoardErrorCode | string
  }

  type OBoardErrorCode =
    | 'CAPABILITY_DENIED' | 'RESOURCE_DENIED' | 'INVALID_ENVIRONMENT' | 'CONFIGURATION_REQUIRED'
    | 'PERMISSION_REVIEW_REQUIRED' | 'SERVER_NOT_FOUND' | 'SERVER_OFFLINE' | 'SERVER_BUSY'
    | 'AGENT_POLICY_DENIED' | 'UNSUPPORTED_CAPABILITY' | 'TARGET_NOT_ALLOWED'
    | 'HTTP_HOST_DENIED' | 'HTTP_PRIVATE_ADDRESS_DENIED' | 'HTTP_TIMEOUT' | 'HTTP_FAILED' | 'HTTP_RESPONSE_TOO_LARGE'
    | 'OPERATION_TIMEOUT' | 'OPERATION_FAILED' | 'RUN_TIMEOUT' | 'RATE_LIMITED' | 'LIMIT_EXCEEDED'
    | 'STATE_QUOTA_EXCEEDED' | 'STATE_CONFLICT' | 'SECRET_NOT_CONFIGURED' | 'PLUGIN_DISABLED'
    | 'RUNTIME_UNAVAILABLE' | 'INVALID_ARGUMENT' | 'CANCELLED' | 'SCRIPT_ERROR' | 'RESOURCE_LIMIT' | 'INTERNAL_ERROR'

  /**
   * Opaque reference to an instance secret. It never contains the secret; it
   * can only be placed in an HTTP header, basic/bearer auth, or used as an
   * HMAC key, where OBoard resolves it outside the plugin.
   */
  class SecretRef {
    private constructor()
    readonly name: string
    withPrefix(prefix: string): SecretRef
    withSuffix(suffix: string): SecretRef
    toString(): string
  }

  interface RunContext {
    readonly run_id: string
    readonly plugin_id: string
    readonly plugin_version: string
    readonly instance_id: string
    readonly trigger: 'manual' | 'interval' | 'cron' | 'event' | 'ui' | 'action'
    readonly scheduled_at?: string
    readonly page?: string
    readonly action?: string
    readonly event?: { type: 'server.online' | 'server.offline'; server_id: ServerID; occurred_at: string }
  }

  interface Env {
    /** Typed value of a declared or custom variable; secrets are SecretRef. */
    readonly [name: string]: unknown
    get<T = unknown>(name: string, fallback?: T): T
    /** The saved text form of a variable; secrets render as "[SecretRef NAME]". */
    raw(name: string): string | undefined
    has(name: string): boolean
    names(): string[]
    type(name: string): EnvType | undefined
  }

  type EnvType = 'string' | 'text' | 'integer' | 'number' | 'boolean' | 'select' | 'multi_select'
    | 'server' | 'servers' | 'secret' | 'url' | 'duration' | 'json'

  interface ServerView {
    server_id: ServerID
    name: string
    status: string
    region_code: string
    public_ipv4: string
    public_ipv6: string
    enrolled: boolean
    online: boolean
    agent_version: string
    last_seen_at?: string
  }

  interface ServerHealth {
    server_id: ServerID
    status: string
    agent_connected: boolean
    last_seen_at: string
    stale: boolean
    config_sync: string
    connectivity_status: string
    agent_version: string
    core_version: string
  }

  interface ServerMetrics {
    server_id: ServerID
    exists: boolean
    stale: boolean
    observed_at?: string
    cpu_usage_percent?: number
    memory_used_bytes?: number
    memory_total_bytes?: number
    disk_used_bytes?: number
    disk_total_bytes?: number
    tcp_connections?: number
    udp_connections?: number
    network_upload_bps?: number
    network_download_bps?: number
  }

  interface PingOptions { server_id: ServerID; target: string; ip_family?: IPFamily; count?: number; interval_ms?: number; timeout_ms?: number; packet_size?: number }
  interface PingResult {
    operation_id: string; target: string; resolved_ip: string; ip_family: 'ipv4' | 'ipv6'
    sent: number; received: number; loss_percent: number; min_rtt_ms: number; avg_rtt_ms: number; max_rtt_ms: number
    samples: Array<{ seq: number; rtt_ms?: number; timeout: boolean }>
  }

  interface TraceOptions { server_id: ServerID; target: string; mode?: 'icmp' | 'udp' | 'tcp'; port?: number; ip_family?: IPFamily; max_hops?: number; queries_per_hop?: number; per_hop_timeout_ms?: number }
  interface TraceResult {
    operation_id: string; target: string; resolved_ip: string; ip_family: 'ipv4' | 'ipv6'; mode: 'icmp' | 'udp' | 'tcp'; port?: number
    started_at: string; duration_ms: number; reached: boolean; truncated: boolean
    hops: Array<{ hop: number; addresses: string[]; probes: Array<{ address?: string; rtt_ms?: number; timeout: boolean }> }>
  }

  interface TCPProbeOptions { server_id: ServerID; host: string; port: number; ip_family?: IPFamily; timeout_ms?: number }
  interface TCPProbeResult { operation_id: string; host: string; port: number; resolved_ip?: string; ip_family?: string; connected: boolean; connect_ms?: number; error_class?: string }

  interface DNSLookupOptions { server_id: ServerID; name: string; record_types?: Array<'A' | 'AAAA'>; timeout_ms?: number }
  interface DNSLookupResult { operation_id: string; name: string; records: Array<{ type: 'A' | 'AAAA'; address: string }>; duration_ms: number; error_class?: string }

  interface HTTPProbeOptions { server_id: ServerID; url: string; method?: 'GET' | 'HEAD'; ip_family?: IPFamily; timeout_ms?: number; follow_redirects?: boolean }
  interface HTTPProbeResult {
    operation_id: string; url: string; final_url?: string; resolved_ip?: string; status_code?: number; redirects: number; body_bytes: number
    timings: { dns_ms: number; connect_ms: number; tls_ms: number; ttfb_ms: number; total_ms: number }
    tls?: { version: string; server_name: string; cert_not_after: string }
    error_class?: string
  }

  type ViewTone = 'neutral' | 'success' | 'warning' | 'danger'
  type ViewNode =
    | { type: 'stack'; children: ViewNode[] }
    | { type: 'heading'; text: string }
    | { type: 'text'; text: string }
    | { type: 'metric'; label: string; value: string; tone?: ViewTone }
    | { type: 'badge'; text: string; tone?: ViewTone }
    | { type: 'table'; columns: Array<{ label: string }>; rows: string[][] }
    | { type: 'binding'; source: 'servers.get' | 'servers.health' | 'servers.metrics'; server: { $env: string } }
    | { type: 'button'; action: string; label: string }
    | { type: 'empty'; text: string }

  interface ViewDocument { title?: string; body: ViewNode[] }

  type HeaderValue = string | SecretRef

  interface HTTPRequestOptions {
    url: string
    method?: 'GET' | 'HEAD' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
    headers?: Record<string, HeaderValue>
    query?: Record<string, string | SecretRef>
    body?: string
    body_encoding?: 'utf8' | 'base64'
    /** Serialized as the body with Content-Type application/json. */
    json?: unknown
    auth?: { type: 'bearer'; secret: SecretRef } | { type: 'basic'; username: string; secret: SecretRef }
    timeout_ms?: number
    /** Ignored whenever the request carries a secret. */
    follow_redirects?: boolean
    max_response_bytes?: number
  }

  interface HTTPResponse {
    readonly ok: boolean
    readonly status: number
    readonly headers: Record<string, string>
    readonly redirects: number
    readonly final_url: string
    readonly body?: string
    readonly body_base64?: string
    readonly json?: unknown
  }

  interface StateEntry<T = unknown> { key: string; exists: boolean; version: number; value?: T; updated_at?: string }

  interface DigestOptions { input?: 'utf8' | 'hex' | 'base64'; output?: 'hex' | 'base64' | 'base64url' }

  interface OBoardSDK {
    readonly servers: {
      /** servers.read */
      get(serverId: ServerID): ServerView
      /** servers.read: every granted server. */
      list(): ServerView[]
      /** servers.health.read */
      health(serverId: ServerID): ServerHealth
      /** servers.metrics.read */
      metrics(serverId: ServerID): ServerMetrics
    }
    /** Executed natively by the Agent on the chosen server; public targets only. */
    readonly network: {
      ping(options: PingOptions): PingResult
      trace(options: TraceOptions): TraceResult
      tcpProbe(options: TCPProbeOptions): TCPProbeResult
      dnsLookup(options: DNSLookupOptions): DNSLookupResult
      httpProbe(options: HTTPProbeOptions): HTTPProbeResult
    }
    /** http.request: HTTPS to granted hosts through the Controller gateway. */
    readonly http: { request(options: HTTPRequestOptions): HTTPResponse }
    /** state.read / state.write: private to this instance, with quotas. */
    readonly state: {
      get<T = unknown>(key: string): StateEntry<T>
      list(prefix?: string, options?: { limit?: number }): Array<StateEntry>
      set(key: string, value: unknown): { key: string; version: number }
      delete(key: string, options?: { expected_version?: number }): { key: string; deleted: boolean }
      /** Writes only if the stored version equals expectedVersion (0 = absent). */
      compareAndSwap(key: string, expectedVersion: number, value: unknown): { key: string; version: number }
    }
    /** notifications.send: granted channels only. */
    readonly notifications: { send(options: { title: string; body?: string; channel_ids?: string[] }): { sent: string[] } }
    /** ui.page: publish a closed view document for one declared page. */
    readonly ui: { publish(options: { page: string; document: ViewDocument }): { page: string; published_at: string } }
    /** Pure computation. HMAC with a SecretRef key requires secrets.use. */
    readonly crypto: {
      sha256(data: string, options?: DigestOptions): string
      sha1(data: string, options?: DigestOptions): string
      hmacSha256(key: string | SecretRef, data: string, options?: DigestOptions & { keyEncoding?: 'utf8' | 'hex' | 'base64' }): string
      hmacSha1(key: string | SecretRef, data: string, options?: DigestOptions & { keyEncoding?: 'utf8' | 'hex' | 'base64' }): string
      base64Encode(data: string, options?: { input?: 'utf8' | 'hex' | 'base64'; url?: boolean }): string
      base64Decode(text: string, options?: { output?: 'utf8' | 'hex' | 'base64'; url?: boolean }): string
      hexEncode(data: string, options?: { input?: 'utf8' | 'hex' | 'base64' }): string
      hexDecode(text: string, options?: { output?: 'utf8' | 'hex' | 'base64' }): string
      randomBytes(length: number, encoding?: 'hex' | 'base64' | 'base64url'): string
      uuid(): string
    }
  }

  interface Logger {
    debug(...values: unknown[]): void
    info(...values: unknown[]): void
    warn(...values: unknown[]): void
    error(...values: unknown[]): void
  }

  const oboard: OBoardSDK
  const env: Env
  const log: Logger
  const console: Logger & { log(...values: unknown[]): void }
}
