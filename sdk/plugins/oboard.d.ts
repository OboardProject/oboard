/** Editor-only globals for synchronous oboard-js-v1. No Node/browser/TS runtime is implied. */
declare namespace OBoard {
  type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };
  type ID = string;
  interface SDKResult<T> { ok: true; result: T; operation_id?: string }
  interface SDKError { error_code: string; message: string }
  interface Simulated { simulated: true; capability: string; operation_id: string }
  interface Server {
    server_id: ID; name: string; status: string; region_code: string;
    public_ipv4: string; public_ipv6: string; agent_version: string;
    agent_build: string; sing_box_version: string; last_seen_at: string;
  }
  interface ServerStatus {
    server_id: ID; control_connected: true | 'unknown'; agent_connected: true | 'unknown';
    last_trusted_communication_at: string; observed_at: string; status_version: string;
    stale: boolean; maintenance: boolean; capabilities: string[] | null; core_running: string;
    inbound_available: string; config_applied: 'true' | 'false' | 'unknown'; reason: string;
  }
  interface Metrics {
    server_id: ID; exists: boolean; stale: boolean; observed_at: string;
    cpu_usage_percent?: number; memory_used_bytes?: number; memory_total_bytes?: number;
  }
  interface Incident {
    incident_id: ID; subject_server_id: ID; status: string; kind: string;
    first_offline_at: string; detected_at: string; resolved_at: string; flap_count: number;
  }
  type Service = 'oboard-agent' | 'oboard-sb' | 'all';
  interface ServiceRequest { server_id: ID; service: Service }
  interface PowerRequest { server_id: ID; reason: string }
  interface Accepted {
    accepted: true; operation_id: string; task_id?: ID; run_id?: string;
    action_key?: string; expires_at?: string; stage?: string; channel_id?: ID;
  }
  interface Operation {
    operation_id: string; status: string; capability: string; error_code: string;
    task_id?: ID; task_status?: string; waited?: boolean;
  }
  type StateRead<T = JSONValue> = { key: string; exists: false; version: '0' } | { key: string; exists: true; version: string; value: T };
  interface StateWrite<T = JSONValue> { key: string; version: string; value: T }
  type HTTPMethod = 'GET' | 'HEAD' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'OPTIONS';
  interface NetworkRequest {
    request: { url: string; method: HTTPMethod; headers?: Record<string, string>; body?: string; timeout_seconds?: number };
    /** Declared and granted host-injected secret reference, never plaintext. */
    secret?: string;
  }
  /** Secret-bearing requests expose status only: headers is null and body is empty. */
  interface NetworkResponse { status: number; headers: Record<string, string> | null; body: string }
  type ManagementCapability = 'inventory.read' | 'servers.list' | 'servers.get' | 'servers.metrics.read' |
    'servers.latency_probes.read' | 'servers.connectivity.read' | 'servers.connectivity.sla' |
    'servers.connectivity.events' | 'servers.dns_policy.get' | 'deployments.plan' | 'deployments.apply' |
    'servers.update' | 'inbounds.create' | 'inbounds.update' | 'inbounds.delete' |
    'proxy_paths.create' | 'proxy_paths.update' | 'proxy_paths.delete';
  interface ManagementRequest {
    management: { capability: ManagementCapability; input: Record<string, JSONValue>; reason?: string; expected_revisions?: Record<string, string> };
  }
  interface ManagementApplied { changeset_id: string; status: string; approval_required: boolean; operation_id?: string }
  interface SDK {
    servers: {
      get(args: { server_id: ID }): SDKResult<Server>;
      list(args?: { limit?: number }): SDKResult<{ servers: Server[] }>;
      status(args: { server_id: ID }): SDKResult<ServerStatus>;
    };
    metrics: { latest(args: { server_id: ID }): SDKResult<Metrics> };
    incidents: { get(args: { incident_id: ID }): SDKResult<Incident> };
    services: {
      status(args: ServiceRequest): SDKResult<Accepted | Simulated>;
      restart(args: ServiceRequest): SDKResult<Accepted | Simulated>;
    };
    host: { poweroff(args: PowerRequest): SDKResult<Accepted | Simulated>; reboot(args: PowerRequest): SDKResult<Accepted | Simulated> };
    notifications: { send(args: { channel_id: ID; title: string; body: string }): SDKResult<Accepted | Simulated> };
    operations: {
      get(args: { operation_id: string }): SDKResult<Operation>;
      wait(args: { operation_id: string; wait_seconds?: number }): SDKResult<Operation>;
    };
    state: {
      get<T = JSONValue>(args: { key: string }): SDKResult<StateRead<T>>;
      compareAndSet<T extends JSONValue>(args: { key: string; expected_version: number; value: T }): SDKResult<StateWrite<T> | Simulated>;
    };
    config: { get(args?: Record<string, never>): SDKResult<{ config: Record<string, JSONValue> }> };
    network: { request(args: NetworkRequest): SDKResult<NetworkResponse | Simulated> };
    management: {
      query(args: ManagementRequest): SDKResult<{ data: JSONValue }>;
      preview(args: ManagementRequest): SDKResult<{ preview: JSONValue } | Simulated>;
      apply(args: ManagementRequest): SDKResult<ManagementApplied | Simulated>;
    };
  }
}
/** Narrow to the plugin's params_schema in author code; no automatic default injection. */
declare const params: Record<string, OBoard.JSONValue>;
declare const env: Readonly<Record<string, string>>;
declare const oboard: OBoard.SDK;
declare const log: { info(message: string): void; warn(message: string): void; error(message: string): void };
