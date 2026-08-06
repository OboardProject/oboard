import { describe, expect, it } from 'vitest'
import { buildSharedProxyPathTopology, canonicalProxyPathStep, graphBranchRouteOffsets, graphPathEdgeLabels, graphPathFocusState, mergeGraphPathIDs } from './graph-topology'
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

  it('keeps route offsets stable in visual target order', () => {
    const offsets = graphBranchRouteOffsets([
      { id: 'right', source: 'shared', target: 'warp' },
      { id: 'left', source: 'shared', target: 'hytron' },
      { id: 'other', source: 'other', target: 'leaf' },
    ], nodeID => ({ hytron: 100, warp: 500, leaf: 0 }[nodeID] || 0))

    expect(offsets.get('left')).toBe(-10)
    expect(offsets.get('right')).toBe(10)
    expect(offsets.has('other')).toBe(false)
  })

  it('bounds route tracks when one shared node has many branches', () => {
    const edges = Array.from({ length: 47 }, (_, index) => ({
      id: `edge-${index}`,
      source: 'shared',
      target: `target-${index}`,
    }))
    const offsets = graphBranchRouteOffsets(edges, nodeID => Number(nodeID.slice(7)))
    const values = Array.from(offsets.values())

    expect(Math.min(...values)).toBe(-90)
    expect(Math.max(...values)).toBe(90)
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
