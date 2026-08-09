import { describe, expect, it } from 'vitest'
import { relatedProxyPaths } from './graph-relations'
import type { Inbound, ProxyPath, ProxyPathStep } from './types'

const inbounds: Inbound[] = [
  { id: 10, server_id: 1, name: 'entry-a', protocol: 'vless', listen_ip: '::', port: 443, entry_ip_mode: 'auto', external_ip: '', dns_sync_enabled: false, dns_domain: '', dns_proxy_enabled: false, dns_record_types: 'auto', ddns_enabled: false, ddns_interval_seconds: 0, dns_sync_status: '', dns_sync_error: '', tls: false, config_json: '{}', enabled: true },
  { id: 11, server_id: 2, name: 'entry-b', protocol: 'shadowsocks', listen_ip: '::', port: 8443, entry_ip_mode: 'auto', external_ip: '', dns_sync_enabled: false, dns_domain: '', dns_proxy_enabled: false, dns_record_types: 'auto', ddns_enabled: false, ddns_interval_seconds: 0, dns_sync_status: '', dns_sync_error: '', tls: false, config_json: '{}', enabled: true },
]
const path = (id: number, enabled = true, kind: 'chain' | 'direct' = 'chain'): ProxyPath => ({ id, kind, name: `path-${id}`, name_mode: 'auto', name_template: [], inbound_id: 10, exit_region_mode: 'auto', exit_region_code: '', enabled })
const step = (id: number, pathID: number, position: number, patch: Partial<ProxyPathStep> = {}): ProxyPathStep => ({ id, path_id: pathID, position, node_type: 'server_inbound', transport_mode: 'singbox', server_id: 2, config_json: '{}', ...patch })

describe('proxy graph related paths', () => {
  it('finds enabled and disabled paths using a server as a root or hop', () => {
    const data = { inbounds, proxy_paths: [path(1), path(2, false)], proxy_path_steps: [step(101, 1, 1), step(201, 2, 1)] }
    expect(relatedProxyPaths(data, { kind: 'server', id: 1 }).map(item => item.path.id)).toEqual([1, 2])
    expect(relatedProxyPaths(data, { kind: 'server', id: 2 }).map(item => item.path.id)).toEqual([1, 2])
  })

  it('finds root and reused inbound references', () => {
    const data = { inbounds, proxy_paths: [path(1), path(2)], proxy_path_steps: [step(101, 1, 1, { inbound_id: 11, server_id: undefined })] }
    expect(relatedProxyPaths(data, { kind: 'entry', id: 10 }).map(item => item.path.id)).toEqual([1, 2])
    expect(relatedProxyPaths(data, { kind: 'entry', id: 11 }).map(item => item.path.id)).toEqual([1])
  })

  it('matches shared step prefixes across disabled paths and direct branches', () => {
    const direct = { ...path(3, true, 'direct'), branch_source_step_id: 101 }
    const data = {
      inbounds,
      proxy_paths: [path(1), path(2, false), direct],
      proxy_path_steps: [step(101, 1, 1), step(201, 2, 1)],
    }
    expect(relatedProxyPaths(data, { kind: 'step', id: 101, pathIDs: [1, 3] }).map(item => item.path.id)).toEqual([1, 3, 2])
  })

  it('finds every path using an imported node', () => {
    const data = {
      inbounds,
      proxy_paths: [path(1), path(2, false)],
      proxy_path_steps: [step(101, 1, 1, { node_type: 'imported', server_id: undefined, external_outbound_id: 9 }), step(201, 2, 1, { node_type: 'imported', server_id: undefined, external_outbound_id: 9 })],
    }
    expect(relatedProxyPaths(data, { kind: 'external', id: 9 }).map(item => item.path.id)).toEqual([1, 2])
  })
})
