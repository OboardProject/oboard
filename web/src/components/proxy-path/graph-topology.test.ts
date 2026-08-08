import { describe, expect, it } from 'vitest'
import { buildSharedProxyPathTopology, canonicalProxyPathStep, graphPathEdgeLabels, graphPathFocusState, mergeGraphPathIDs } from './graph-topology'
import type { ProxyPath, ProxyPathStep } from './types'

const path = (id: number, inboundID = 10): ProxyPath => ({
  id,
  inbound_id: inboundID,
  kind: 'chain',
  name: `path-${id}`,
  name_mode: 'auto',
  name_template: [],
  exit_region_mode: 'auto',
  exit_region_code: '',
  enabled: true,
})

const step = (id: number, pathID: number, position: number, serverID: number, configJSON = '{}'): ProxyPathStep => ({
  id,
  path_id: pathID,
  position,
  node_type: 'server_inbound',
  transport_mode: 'singbox',
  server_id: serverID,
  config_json: configJSON,
})

describe('proxy graph shared topology', () => {
  it('renders an identical server prefix once before different branches', () => {
    const hytronA = step(101, 1, 1, 20)
    const hytronB = step(201, 2, 1, 20)
    const warp: ProxyPathStep = { id: 102, path_id: 1, position: 2, node_type: 'warp', transport_mode: 'singbox', config_json: '{}' }
    const nextServer = step(202, 2, 2, 30)
    const topology = buildSharedProxyPathTopology([path(1), path(2)], [hytronA, warp, hytronB, nextServer])

    expect(canonicalProxyPathStep(topology, hytronA).id).toBe(101)
    expect(canonicalProxyPathStep(topology, hytronB).id).toBe(101)
    expect(topology.stepsByCanonicalID.get(101)?.map(item => item.id)).toEqual([101, 201])
    expect(canonicalProxyPathStep(topology, warp).id).toBe(102)
    expect(canonicalProxyPathStep(topology, nextServer).id).toBe(202)
  })

  it('does not merge matching steps from different entry nodes', () => {
    const left = step(101, 1, 1, 20)
    const right = step(201, 2, 1, 20)
    const topology = buildSharedProxyPathTopology([path(1, 10), path(2, 11)], [left, right])

    expect(canonicalProxyPathStep(topology, left).id).toBe(101)
    expect(canonicalProxyPathStep(topology, right).id).toBe(201)
  })

  it('treats equivalent structured config as the same prefix', () => {
    const left = step(101, 1, 1, 20, '{"method":"a","nested":{"b":2,"a":1}}')
    const right = step(201, 2, 1, 20, '{"nested":{"a":1,"b":2},"method":"a"}')
    const topology = buildSharedProxyPathTopology([path(1), path(2)], [left, right])

    expect(canonicalProxyPathStep(topology, right).id).toBe(101)
  })

  it('merges multiple nested shared prefixes and splits only at membership changes', () => {
    const steps = [
      step(101, 1, 1, 20), step(102, 1, 2, 30), step(103, 1, 3, 40),
      step(201, 2, 1, 20), step(202, 2, 2, 30), step(203, 2, 3, 50),
      step(301, 3, 1, 20), step(302, 3, 2, 60), step(303, 3, 3, 70),
      step(401, 4, 1, 20), step(402, 4, 2, 60), step(403, 4, 3, 80),
    ]
    const topology = buildSharedProxyPathTopology([path(1), path(2), path(3), path(4)], steps)

    expect(topology.stepsByCanonicalID.get(101)?.map(item => item.path_id)).toEqual([1, 2, 3, 4])
    expect(topology.stepsByCanonicalID.get(102)?.map(item => item.path_id)).toEqual([1, 2])
    expect(topology.stepsByCanonicalID.get(302)?.map(item => item.path_id)).toEqual([3, 4])
    expect(canonicalProxyPathStep(topology, steps[4]).id).toBe(102)
    expect(canonicalProxyPathStep(topology, steps[10]).id).toBe(302)
    expect(canonicalProxyPathStep(topology, steps[2]).id).toBe(103)
    expect(canonicalProxyPathStep(topology, steps[5]).id).toBe(203)
  })

  it('keeps shared edge path membership sorted and unique', () => {
    expect(mergeGraphPathIDs(mergeGraphPathIDs([9, 2], 5), 2)).toEqual([2, 5, 9])
  })

  it('labels a shared trunk and each branch only where membership changes', () => {
    const labels = graphPathEdgeLabels([
      { id: 'trunk', source: 'root', target: 'shared', pathIDs: [1, 2] },
      { id: 'left', source: 'shared', target: 'left-node', pathIDs: [1] },
      { id: 'left-tail', source: 'left-node', target: 'left-exit', pathIDs: [1] },
      { id: 'right', source: 'shared', target: 'right-node', pathIDs: [2] },
    ], pathID => `path-${pathID}`)

    expect(labels.get('trunk')).toEqual({ text: '2 条路径共享', title: 'path-1\npath-2' })
    expect(labels.get('left')).toEqual({ text: 'path-1', title: 'path-1', pathID: 1 })
    expect(labels.has('left-tail')).toBe(false)
    expect(labels.get('right')).toEqual({ text: 'path-2', title: 'path-2', pathID: 2 })
  })

  it('keeps shared trunks active while muting unrelated branches', () => {
    expect(graphPathFocusState([1, 2], [2])).toBe('active')
    expect(graphPathFocusState([1], [2])).toBe('muted')
    expect(graphPathFocusState([], [2])).toBe('context')
    expect(graphPathFocusState([1], [])).toBeUndefined()
  })
})
