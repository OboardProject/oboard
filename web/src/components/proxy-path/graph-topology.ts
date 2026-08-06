import type { ProxyPath, ProxyPathStep } from './types'

export type SharedProxyPathTopology = {
  canonicalStepByID: Map<number, ProxyPathStep>
  stepsByCanonicalID: Map<number, ProxyPathStep[]>
}

function canonicalJSON(raw?: string) {
  const normalize = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(normalize)
    if (value && typeof value === 'object') {
      return Object.fromEntries(Object.keys(value).sort().map(key => [key, normalize((value as Record<string, unknown>)[key])]))
    }
    return value
  }
  try {
    return JSON.stringify(normalize(JSON.parse(raw || '{}')))
  } catch {
    return (raw || '').trim()
  }
}

function graphStepSignature(step: ProxyPathStep) {
  return JSON.stringify([
    step.position,
    step.node_type,
    step.transport_mode || 'singbox',
    step.processing_role === true,
    step.server_id || 0,
    step.inbound_id || 0,
    step.external_outbound_id || 0,
    canonicalJSON(step.config_json),
  ])
}

/**
 * Groups path steps only while their complete prefix is identical. The store
 * keeps one step row per path, but the graph presents that shared prefix as one
 * trunk and fans out only where the paths actually diverge.
 */
export function buildSharedProxyPathTopology(paths: ProxyPath[], steps: ProxyPathStep[]): SharedProxyPathTopology {
  const visiblePathByID = new Map(paths.filter(path => path.enabled !== false).map(path => [path.id, path]))
  const stepsByPath = new Map<number, ProxyPathStep[]>()
  steps.forEach(step => {
    if (!visiblePathByID.has(step.path_id)) return
    stepsByPath.set(step.path_id, [...(stepsByPath.get(step.path_id) || []), step])
  })
  stepsByPath.forEach(pathSteps => pathSteps.sort((left, right) => (left.position - right.position) || (left.id - right.id)))

  const groupsByPrefix = new Map<string, ProxyPathStep[]>()
  Array.from(visiblePathByID.values())
    .sort((left, right) => left.id - right.id)
    .forEach(path => {
      let prefix = `entry:${path.inbound_id}`
      ;(stepsByPath.get(path.id) || []).forEach(step => {
        prefix += `\u001f${graphStepSignature(step)}`
        groupsByPrefix.set(prefix, [...(groupsByPrefix.get(prefix) || []), step])
      })
    })

  const canonicalStepByID = new Map<number, ProxyPathStep>()
  const stepsByCanonicalID = new Map<number, ProxyPathStep[]>()
  groupsByPrefix.forEach(group => {
    const ordered = group.slice().sort((left, right) => left.id - right.id)
    const canonical = ordered[0]
    stepsByCanonicalID.set(canonical.id, ordered)
    ordered.forEach(step => canonicalStepByID.set(step.id, canonical))
  })
  return { canonicalStepByID, stepsByCanonicalID }
}

export function canonicalProxyPathStep(topology: SharedProxyPathTopology, step: ProxyPathStep) {
  return topology.canonicalStepByID.get(step.id) || step
}

export type GraphRouteEdge = {
  id: string
  source: string
  target: string
  auxiliary?: boolean
}

export type GraphPathMembershipEdge = {
  id: string
  source: string
  target: string
  pathIDs: number[]
}

export type GraphPathEdgeLabel = {
  text: string
  title: string
  pathID?: number
}

export type GraphPathFocusState = 'active' | 'muted' | 'context'

export function mergeGraphPathIDs(pathIDs: number[] | undefined, pathID: number) {
  return Array.from(new Set([...(pathIDs || []), pathID])).sort((left, right) => left - right)
}

export function graphPathFocusState(pathIDs: number[] | undefined, activePathIDs: number[]) {
  if (!activePathIDs.length) return undefined
  if (!pathIDs?.length) return 'context' as const
  const active = new Set(activePathIDs)
  return pathIDs.some(pathID => active.has(pathID)) ? 'active' as const : 'muted' as const
}

function samePathIDs(left: number[], right: number[]) {
  return left.length === right.length && left.every((pathID, index) => pathID === right[index])
}

/** Labels a route once, then labels it again only where shared membership changes. */
export function graphPathEdgeLabels(
  edges: GraphPathMembershipEdge[],
  pathLabel: (pathID: number) => string,
) {
  const incomingByNode = new Map<string, GraphPathMembershipEdge[]>()
  edges.forEach(edge => incomingByNode.set(edge.target, [...(incomingByNode.get(edge.target) || []), edge]))

  const labels = new Map<string, GraphPathEdgeLabel>()
  edges.forEach(edge => {
    const pathIDs = edge.pathIDs.slice().sort((left, right) => left - right)
    if (!pathIDs.length) return
    const continuesSameMembership = (incomingByNode.get(edge.source) || [])
      .some(incoming => samePathIDs(incoming.pathIDs, pathIDs))
    if (continuesSameMembership) return
    if (pathIDs.length === 1) {
      const pathID = pathIDs[0]
      const label = pathLabel(pathID)
      labels.set(edge.id, { text: label, title: label, pathID })
      return
    }
    const names = pathIDs.map(pathLabel)
    labels.set(edge.id, {
      text: `${pathIDs.length} 条路径共享`,
      title: names.join('\n'),
    })
  })
  return labels
}

/** Assigns separate horizontal tracks to edges that fan out from one node. */
export function graphBranchRouteOffsets(
  edges: GraphRouteEdge[],
  targetX: (nodeID: string) => number,
  spacing = 20,
  maxSpan = 180,
) {
  const offsets = new Map<string, number>()
  const bySource = new Map<string, GraphRouteEdge[]>()
  edges.forEach(edge => {
    if (edge.auxiliary) return
    bySource.set(edge.source, [...(bySource.get(edge.source) || []), edge])
  })
  bySource.forEach(sourceEdges => {
    if (sourceEdges.length < 2) return
    const ordered = sourceEdges.slice().sort((left, right) => (
      targetX(left.target) - targetX(right.target) || left.target.localeCompare(right.target) || left.id.localeCompare(right.id)
    ))
    const routeSpacing = Math.min(spacing, maxSpan / (ordered.length - 1))
    ordered.forEach((edge, index) => offsets.set(edge.id, (index - (ordered.length - 1) / 2) * routeSpacing))
  })
  return offsets
}
