import { describe, expect, it } from 'vitest'
import { buildProxyPathReuseRequest } from './TransportDialog'

describe('buildProxyPathReuseRequest', () => {
  it('keeps generated VLESS on the default no-copy branch mode', () => {
    expect(buildProxyPathReuseRequest({
      sources: [{ inbound_id: 10 }],
      targetServerID: 20,
      targetKind: 'generated',
      targetInboundID: 0,
      chainProtocol: 'vless',
      chainMethod: '2022-blake3-aes-128-gcm',
      realityServer: 'cdn.icloud-content.com',
      realityPort: 443,
      mode: 'singbox',
      tunnelKind: 'ssh',
      sshPort: 0,
      keepalive: 25,
      copyMode: 'all',
      branchPathID: 0,
    })).toEqual({
      sources: [{ inbound_id: 10 }],
      target_server_id: 20,
      target_kind: 'generated',
      chain_protocol: 'vless',
      reality_handshake_server: 'cdn.icloud-content.com',
      reality_handshake_port: 443,
      transport_mode: 'singbox',
      copy_mode: 'none',
    })
  })

  it('encodes one existing branch and tunnel settings', () => {
    expect(buildProxyPathReuseRequest({
      sources: [{ step_id: 30 }, { inbound_id: 31 }],
      targetServerID: 40,
      targetKind: 'existing',
      targetInboundID: 41,
      chainProtocol: 'shadowsocks',
      chainMethod: '2022-blake3-aes-256-gcm',
      realityServer: '',
      realityPort: 0,
      mode: 'tunnel',
      tunnelKind: 'wireguard',
      sshPort: 0,
      keepalive: 15,
      copyMode: 'single',
      branchPathID: 42,
    })).toEqual({
      sources: [{ step_id: 30 }, { inbound_id: 31 }],
      target_server_id: 40,
      target_kind: 'existing',
      target_inbound_id: 41,
      transport_mode: 'tunnel',
      tunnel_type: 'wireguard',
      persistent_keepalive: 15,
      copy_mode: 'single',
      branch_path_id: 42,
    })
  })
})
