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

/**
 * Expands a direct branch's membership through the parent path prefix ending
 * at branch_source_step_id. The graph still renders the parent semantic edges
 * once, while focus and labels recognize that the direct exit traverses them.
 */
export function graphExpandedPathIDsByStep(paths: ProxyPath[], steps: ProxyPathStep[]) {
  const visiblePathByID = new Map(paths.filter(path => path.enabled !== false).map(path => [path.id, path]))
  const stepByID = new Map(steps.map(step => [step.id, step]))
  const stepsByPath = new Map<number, ProxyPathStep[]>()
  steps.forEach(step => {
    if (!visiblePathByID.has(step.path_id)) return
    stepsByPath.set(step.path_id, [...(stepsByPath.get(step.path_id) || []), step])
  })
  stepsByPath.forEach(pathSteps => pathSteps.sort((left, right) => left.position - right.position || left.id - right.id))

  const pathIDsByStepID = new Map<number, number[]>()
  stepsByPath.forEach(pathSteps => pathSteps.forEach(step => pathIDsByStepID.set(step.id, [step.path_id])))
  Array.from(visiblePathByID.values()).sort((left, right) => left.id - right.id).forEach(path => {
    if (path.kind !== 'direct' || !path.branch_source_step_id) return
    const source = stepByID.get(path.branch_source_step_id)
    const sourcePath = source ? visiblePathByID.get(source.path_id) : undefined
    if (!source || !sourcePath || sourcePath.inbound_id !== path.inbound_id) return
    const sourceSteps = stepsByPath.get(source.path_id) || []
    const sourceIndex = sourceSteps.findIndex(step => step.id === source.id)
    if (sourceIndex < 0) return
    sourceSteps.slice(0, sourceIndex + 1).forEach(step => {
      pathIDsByStepID.set(step.id, mergeGraphPathIDs(pathIDsByStepID.get(step.id), path.id))
    })
  })
  return pathIDsByStepID
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
