import { describe, expect, it } from 'vitest'
import type { ProxyPathStep } from './types'
import { detachedPathSuffix, detachedStepCreateRequest, disconnectPathCandidates } from './detached-chain'

const steps: ProxyPathStep[] = [
  { id: 11, path_id: 1, position: 1, node_type: 'server_inbound', server_id: 101, transport_mode: 'port_forward', config_json: '{"listen_port":31001}' },
  { id: 12, path_id: 1, position: 2, node_type: 'imported', external_outbound_id: 201, transport_mode: 'singbox', config_json: '{"password":"secret"}' },
  { id: 21, path_id: 2, position: 1, node_type: 'server_inbound', server_id: 101, transport_mode: 'port_forward', config_json: '{"listen_port":31001}' },
  { id: 22, path_id: 2, position: 2, node_type: 'warp' },
]

describe('detached proxy path chains', () => {
  it('maps a shared canonical edge to the matching step in each real path', () => {
    expect(disconnectPathCandidates(11, [1, 2, 3], steps)).toEqual([
      { pathID: 1, step: steps[0] },
      { pathID: 2, step: steps[2] },
    ])
  })

  it('keeps the selected step and every downstream step in order', () => {
    expect(detachedPathSuffix(1, 12, steps)).toEqual([steps[1]])
    expect(detachedPathSuffix(1, 11, steps)).toEqual([steps[0], steps[1]])
  })

  it('copies transport configuration while assigning the new path position', () => {
    expect(detachedStepCreateRequest(steps[1], 9, 4)).toEqual({
      path_id: 9,
      position: 4,
      node_type: 'imported',
      transport_mode: 'singbox',
      processing_role: false,
      server_id: undefined,
      inbound_id: undefined,
      external_outbound_id: 201,
      config_json: '{"password":"secret"}',
    })
  })
})
