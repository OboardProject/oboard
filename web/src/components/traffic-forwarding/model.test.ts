import { describe, expect, it } from 'vitest'

import type { Server } from '../proxy-path/types'
import {
  REDACTED,
  buildForwardingItems,
  emptyTrafficForwardDraft,
  trafficForwardFieldErrors,
  trafficForwardPayload,
  tunnelDraftFromTunnel,
  tunnelFieldErrors,
  tunnelPayload,
  type TrafficForward,
  type TrafficForwardProbe,
  type TrafficTunnel,
} from './model'

const servers = [
  { id: 1, name: '香港入口', status: 'online', port_range_start: 10000, port_range_end: 10010 },
  { id: 2, name: '东京应用', status: 'offline', port_range_start: 20000, port_range_end: 20010 },
] as unknown as Server[]

const forward: TrafficForward = {
  id: 11, name: '转发-A', source_server_id: 1, target_server_id: 2, listen_ip: '', listen_port: 10000, target_address: '', target_port: 443,
  protocol: 'tcp', backend: 'realm', probe_mode: 'periodic', probe_interval_seconds: 300, priority: 100, config_json: '{}', enabled: true,
  updated_at: '2026-08-24T09:00:00Z',
}

const tunnel: TrafficTunnel = {
  id: 7, name: 'wg', source_server_id: 1, target_server_id: 2, type: 'wireguard', local_address: '10.10.0.1/30', peer_address: '10.10.0.2/30',
  listen_port: 0, target_endpoint: '', target_port: 0, priority: 50,
  config_json: JSON.stringify({ private_key: REDACTED, peer_public_key: REDACTED, allowed_ips: ['10.10.0.0/30'], persistent_keepalive: 25, mtu: 1380 }), enabled: true,
}

describe('traffic forwarding model', () => {
  it('starts a forward on the next free port of the chosen entry and names it automatically', () => {
    const draft = emptyTrafficForwardDraft(servers, [forward], 1)
    expect(draft.listen_port).toBe(10001)
    expect(draft.target_server_id).toBe(0)
    expect(trafficForwardFieldErrors(draft).target_address).toBeTruthy()
    const payload = trafficForwardPayload({ ...draft, target_address: ' 203.0.113.80 ' }, servers)
    expect(payload.name).toBe('香港入口:10001 → 203.0.113.80')
    expect(payload.target_address).toBe('203.0.113.80')
  })

  it('merges forwards and tunnels into one list ordered by priority with health', () => {
    const lateProbe: TrafficForwardProbe = { id: 1, port_forward_id: 11, server_id: 1, mode: 'periodic', available: true, latency_ms: 38, sample_count: 5, error: '', result_json: '{"forward_updated_at":"2026-08-24T08:00:00Z"}', created_at: '2026-08-24T10:00:00Z' }
    const items = buildForwardingItems([forward], [lateProbe], [tunnel])
    expect(items.map(item => item.key)).toEqual(['tunnel-7', 'forward-11'])
    expect(items[1].health.label).toBe('等待检查')
    expect(items[0].health.detail).toBe('保活 25 秒')
  })

  it('round-trips a redacted tunnel without losing unedited config keys', () => {
    const draft = tunnelDraftFromTunnel(tunnel)
    expect(tunnelFieldErrors(draft)).toEqual({})
    const payload = tunnelPayload({ ...draft, persistent_keepalive: 10 }, servers)
    expect(JSON.parse(payload.config_json)).toEqual({ mtu: 1380, private_key: REDACTED, peer_public_key: REDACTED, allowed_ips: ['10.10.0.0/30'], persistent_keepalive: 10 })
  })

  it('requires both WireGuard keys for a new tunnel', () => {
    const draft = { ...tunnelDraftFromTunnel(tunnel), private_key: '', peer_public_key: '' }
    expect(Object.keys(tunnelFieldErrors(draft))).toEqual(['private_key', 'peer_public_key'])
  })
})
