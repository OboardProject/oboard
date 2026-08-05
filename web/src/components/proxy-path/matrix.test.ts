import { describe, expect, it } from 'vitest'
import { buildProxyPathMatrix, proxyPathMatrixStepScope } from './matrix'
import type { Inbound, ProxyPath, ProxyPathStep } from './types'

const entry = (id: number, port: number, enabled = true) => ({ id, server_id: 1, port, enabled } as Inbound)
const path = (id: number, inboundID: number, kind: ProxyPath['kind'] = 'chain', branchSourceStepID?: number) => ({
  id,
  inbound_id: inboundID,
  kind,
  branch_source_step_id: branchSourceStepID,
  enabled: true,
} as ProxyPath)
const step = (id: number, pathID: number, position: number, serverID: number) => ({
  id,
  path_id: pathID,
  position,
  node_type: 'server_inbound',
  server_id: serverID,
} as ProxyPathStep)

describe('proxy path matrix', () => {
  it('orders entries by port and keeps branches beside their source path', () => {
    const matrix = buildProxyPathMatrix({
      inbounds: [entry(20, 8443), entry(10, 443)],
      proxy_paths: [path(100, 10), path(103, 10, 'direct', 333), path(102, 10, 'direct', 322), path(200, 20)],
      proxy_path_steps: [
        step(301, 100, 1, 2), step(302, 100, 2, 3), step(303, 100, 3, 4),
        step(321, 102, 1, 2), step(322, 102, 2, 3),
        step(331, 103, 1, 2), step(332, 103, 2, 3), step(333, 103, 3, 4),
      ],
    }, 1)

    expect(matrix.groups.map(group => group.entry.id)).toEqual([10, 20])
    expect(matrix.groups[0].columns.map(column => column.path?.id)).toEqual([100, 102, 103])
  })

  it('leaves prefix rows empty and moves deeper branches farther right', () => {
    const matrix = buildProxyPathMatrix({
      inbounds: [entry(10, 443)],
      proxy_paths: [path(100, 10), path(102, 10, 'direct', 322), path(103, 10, 'direct', 333)],
      proxy_path_steps: [
        step(301, 100, 1, 2), step(302, 100, 2, 3), step(303, 100, 3, 4),
        step(321, 102, 1, 2), step(322, 102, 2, 3),
        step(331, 103, 1, 2), step(332, 103, 2, 3), step(333, 103, 3, 4),
      ],
    }, 1)
    const [, shallowBranch, deepBranch] = matrix.groups[0].columns

    expect(shallowBranch.branchDepth).toBe(2)
    expect(Array.from(shallowBranch.cells.keys())).toEqual([3])
    expect(shallowBranch.cells.get(3)?.kind).toBe('direct')
    expect(deepBranch.branchDepth).toBe(3)
    expect(Array.from(deepBranch.cells.keys())).toEqual([4])
    expect(deepBranch.cells.get(4)?.kind).toBe('direct')
    expect(matrix.rows).toEqual([0, 1, 2, 3, 4])
  })

  it('hides repeated entry and step cells for paths without explicit branch metadata', () => {
    const matrix = buildProxyPathMatrix({
      inbounds: [entry(10, 443)],
      proxy_paths: [path(100, 10), path(101, 10, 'direct'), path(102, 10)],
      proxy_path_steps: [
        step(301, 100, 1, 2), step(302, 100, 2, 3),
        step(311, 101, 1, 2),
        step(321, 102, 1, 4), step(322, 102, 2, 5),
      ],
    }, 1)
    const [primary, directBranch, entryBranch] = matrix.groups[0].columns

    expect(Array.from(primary.cells.keys())).toEqual([0, 1, 2])
    expect(directBranch.branchDepth).toBe(1)
    expect(Array.from(directBranch.cells.keys())).toEqual([2])
    expect(directBranch.cells.get(2)?.kind).toBe('direct')
    expect(entryBranch.branch).toBe(true)
    expect(entryBranch.branchDepth).toBe(0)
    expect(Array.from(entryBranch.cells.keys())).toEqual([1, 2])
  })

  it('filters disabled data and keeps an empty entry visible', () => {
    const disabledPath = path(101, 10)
    disabledPath.enabled = false
    const matrix = buildProxyPathMatrix({
      inbounds: [entry(10, 443), entry(20, 8443, false), entry(30, 9443)],
      proxy_paths: [disabledPath, path(200, 20)],
      proxy_path_steps: [step(301, 101, 1, 2), step(401, 200, 1, 3)],
    }, 1)

    expect(matrix.groups.map(group => group.entry.id)).toEqual([10, 30])
    expect(matrix.pathCount).toBe(0)
    expect(matrix.groups.every(group => group.columns.length === 1)).toBe(true)
    expect(matrix.groups[0].columns[0].cells.get(0)?.kind).toBe('entry')
  })

  it('returns no groups for a server without enabled entries', () => {
    expect(buildProxyPathMatrix({ inbounds: [entry(10, 443, false)] }, 1)).toEqual({ groups: [], rows: [0], pathCount: 0 })
  })

  it('matches complete node prefixes across multiple entries', () => {
    const data = {
      inbounds: [entry(10, 443), entry(20, 8443)],
      proxy_paths: [path(100, 10), path(101, 10), path(200, 20), path(201, 20)],
      proxy_path_steps: [
        step(301, 100, 1, 2), step(302, 100, 2, 3),
        step(311, 101, 1, 2), step(312, 101, 2, 4),
        step(401, 200, 1, 2), step(402, 200, 2, 3),
        step(411, 201, 1, 5), step(412, 201, 2, 3),
      ],
    }

    expect(proxyPathMatrixStepScope(data, 1, 100, 1).proxyPathIDs).toEqual([100, 101, 200])
    expect(proxyPathMatrixStepScope(data, 1, 100, 2).proxyPathIDs).toEqual([100, 200])
  })
})
