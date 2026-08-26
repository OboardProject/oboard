// Pure geometry and browser-local persistence for the proxy-path canvas.
// Nothing here touches server state: node coordinates and the toolbox position
// are per-browser preferences, so clearing them never changes a stored path.

export type GraphPosition = { x: number; y: number }
export type GraphDirectExitInstance = { instance_id: string; root_server_id: number }
export type GraphLayoutEdge = { source: string; target: string }

export type GraphLayoutNode = {
  id: string
  width: number
  height: number
  rank: number
}

export type GraphBranchBand = {
  nodeID: string
  left: number
  right: number
  centerX: number
  rank: number
}

export type GraphLayerChannel = {
  sourceRank: number
  targetRank: number
  top: number
  bottom: number
  tracks: Record<string, number>
}

export type ProxyGraphLayoutResult = {
  positions: Record<string, GraphPosition>
  bands: Record<string, GraphBranchBand>
  ranks: Record<string, number>
  layerChannels: GraphLayerChannel[]
}

export type ProxyLayoutEdge = {
  id: string
  source: string
  target: string
  sourceHandle?: string
  targetHandle?: string
  pathIDs: number[]
}

export type ProxyLayoutNode = {
  id: string
  width: number
  height: number
  handles?: Record<string, GraphPosition>
}

export type ProxyLayoutOptions = {
  centerX?: number
  originY?: number
  subtreeGap?: number
  trackGap?: number
  channelPadding?: number
}

export const GRAPH_ENTRY_NODE_WIDTH = 260
export const GRAPH_LAYER_SIBLING_GAP = 72
export const GRAPH_LAYER_SECONDARY_OFFSET_Y = 150
export const PRIMARY_SUBTREE_GAP = 48
export const ROUTING_TRACK_GAP = 14
export const ROUTING_CHANNEL_PADDING = 24
export const ROUTING_MIN_CHANNEL_HEIGHT = 90
export const GRAPH_LAYOUT_DEFAULT_NODE_HEIGHT = 180
export const GRAPH_LAYOUT_EXIT_NODE_HEIGHT = 130

const POSITIONS_KEY = 'oboard.proxyGraph.positions.v6'
const TOOLBOX_KEY = 'oboard.proxyGraph.toolboxPosition.v1'
const DIRECT_EXITS_KEY = 'oboard.proxyGraph.directExitInstances.v2'
const LEGACY_DIRECT_EXITS_KEY = 'oboard.proxyGraph.directExitInstances.v1'

export function loadGraphPositions(): Record<string, GraphPosition> {
  try {
    return JSON.parse(localStorage.getItem(POSITIONS_KEY) || '{}')
  } catch {
    return {}
  }
}

export function saveGraphPositions(positions: Record<string, GraphPosition>) {
  localStorage.setItem(POSITIONS_KEY, JSON.stringify(positions))
}

export function loadGraphToolboxPosition(): GraphPosition {
  try {
    const value = JSON.parse(localStorage.getItem(TOOLBOX_KEY) || '{}')
    return { x: Number.isFinite(value.x) ? value.x : 12, y: Number.isFinite(value.y) ? value.y : 12 }
  } catch {
    return { x: 12, y: 12 }
  }
}

export function saveGraphToolboxPosition(position: GraphPosition) {
  localStorage.setItem(TOOLBOX_KEY, JSON.stringify(position))
}

export function loadGraphDirectExitInstances(): GraphDirectExitInstance[] {
  try {
    localStorage.removeItem(LEGACY_DIRECT_EXITS_KEY)
    const value = JSON.parse(localStorage.getItem(DIRECT_EXITS_KEY) || '[]')
    if (!Array.isArray(value)) return []
    return value.filter(item => typeof item?.instance_id === 'string' && Number.isFinite(item?.root_server_id))
  } catch {
    return []
  }
}

export function saveGraphDirectExitInstances(instances: GraphDirectExitInstance[]) {
  localStorage.setItem(DIRECT_EXITS_KEY, JSON.stringify(instances))
}

export function snapGraphPosition(position: GraphPosition): GraphPosition {
  return { x: Math.round(position.x), y: Math.round(position.y) }
}

function compareLayoutEdges(left: ProxyLayoutEdge, right: ProxyLayoutEdge) {
  const leftPathID = left.pathIDs.length ? Math.min(...left.pathIDs) : Number.POSITIVE_INFINITY
  const rightPathID = right.pathIDs.length ? Math.min(...right.pathIDs) : Number.POSITIVE_INFINITY
  return leftPathID - rightPathID
    || left.target.localeCompare(right.target)
    || left.id.localeCompare(right.id)
}

export function layoutProxyGraphTopology(
  nodes: ProxyLayoutNode[],
  primaryEdges: ProxyLayoutEdge[],
  rootNodeID: string,
  options: ProxyLayoutOptions = {},
): ProxyGraphLayoutResult {
  const centerX = options.centerX ?? 760
  const originY = options.originY ?? 300
  const subtreeGap = options.subtreeGap ?? PRIMARY_SUBTREE_GAP
  const trackGap = options.trackGap ?? ROUTING_TRACK_GAP
  const channelPadding = options.channelPadding ?? ROUTING_CHANNEL_PADDING
  const orderedNodes = nodes.slice().sort((left, right) => left.id.localeCompare(right.id))
  const nodeByID = new Map(orderedNodes.map(node => [node.id, node]))
  const edges = primaryEdges
    .filter(edge => nodeByID.has(edge.source) && nodeByID.has(edge.target) && edge.source !== edge.target)
    .slice()
    .sort((left, right) => left.source.localeCompare(right.source) || compareLayoutEdges(left, right))
  const incoming = new Map<string, ProxyLayoutEdge[]>()
  const outgoing = new Map<string, ProxyLayoutEdge[]>()
  const indegree = new Map(orderedNodes.map(node => [node.id, 0]))
  edges.forEach(edge => {
    incoming.set(edge.target, [...(incoming.get(edge.target) || []), edge])
    outgoing.set(edge.source, [...(outgoing.get(edge.source) || []), edge])
    indegree.set(edge.target, (indegree.get(edge.target) || 0) + 1)
  })
  incoming.forEach(nodeEdges => nodeEdges.sort(compareLayoutEdges))
  outgoing.forEach(nodeEdges => nodeEdges.sort(compareLayoutEdges))

  const ready = orderedNodes.filter(node => indegree.get(node.id) === 0).map(node => node.id).sort()
  const topological: string[] = []
  while (ready.length) {
    const nodeID = ready.shift()!
    topological.push(nodeID)
    for (const edge of outgoing.get(nodeID) || []) {
      const next = (indegree.get(edge.target) || 0) - 1
      indegree.set(edge.target, next)
      if (next === 0) {
        ready.push(edge.target)
        ready.sort()
      }
    }
  }
  const cyclicNodeIDs = orderedNodes.map(node => node.id).filter(nodeID => !topological.includes(nodeID))
  topological.push(...cyclicNodeIDs)

  const ranks: Record<string, number> = Object.fromEntries(orderedNodes.map(node => [node.id, 0]))
  topological.forEach(nodeID => {
    for (const edge of outgoing.get(nodeID) || []) {
      ranks[edge.target] = Math.max(ranks[edge.target] || 0, (ranks[nodeID] || 0) + 1)
    }
  })
  if (nodeByID.has(rootNodeID)) ranks[rootNodeID] = 0

  if (import.meta.env.DEV) {
    const multiParent = orderedNodes.filter(node => (incoming.get(node.id)?.length || 0) > 1).map(node => node.id)
    if (multiParent.length) console.warn('[proxy-layout] primary DAG nodes have multiple parents', multiParent)
    if (cyclicNodeIDs.length) console.warn('[proxy-layout] primary topology contains a cycle', cyclicNodeIDs)
  }

  const subtreeSpans = new Map<string, number>()
  const spanningChildren = new Map<string, ProxyLayoutEdge[]>()
  outgoing.forEach((nodeEdges, nodeID) => {
    spanningChildren.set(nodeID, nodeEdges.filter(edge => (incoming.get(edge.target)?.[0]?.id || '') === edge.id))
  })
  const spanFor = (nodeID: string, visiting = new Set<string>()): number => {
    const cached = subtreeSpans.get(nodeID)
    if (cached !== undefined) return cached
    const node = nodeByID.get(nodeID)
    if (!node) return 0
    if (visiting.has(nodeID)) return node.width
    const nextVisiting = new Set(visiting).add(nodeID)
    const children = spanningChildren.get(nodeID) || []
    const childrenWidth = children.reduce((sum, edge) => sum + spanFor(edge.target, nextVisiting), 0)
      + Math.max(0, children.length - 1) * subtreeGap
    const span = Math.max(node.width, childrenWidth)
    subtreeSpans.set(nodeID, span)
    return span
  }
  orderedNodes.forEach(node => spanFor(node.id))

  const rootCandidates = orderedNodes
    .filter(node => !(incoming.get(node.id)?.length))
    .map(node => node.id)
    .sort((left, right) => (left === rootNodeID ? -1 : right === rootNodeID ? 1 : left.localeCompare(right)))
  const roots = rootCandidates.length ? rootCandidates : orderedNodes.map(node => node.id)
  const totalWidth = roots.reduce((sum, nodeID) => sum + (subtreeSpans.get(nodeID) || 0), 0)
    + Math.max(0, roots.length - 1) * subtreeGap
  let rootCursor = centerX - totalWidth / 2
  function getNodeHandleOffsetX(node: ProxyLayoutNode | undefined, handleID?: string): number {
    if (!node) return 0
    if (handleID && node.handles?.[handleID] && Number.isFinite(node.handles[handleID].x)) {
      return node.handles[handleID].x
    }
    return node.width / 2
  }

  const bands: Record<string, GraphBranchBand> = {}
  const assignBand = (nodeID: string, left: number, right: number) => {
    if (bands[nodeID]) return
    const center = (left + right) / 2
    bands[nodeID] = { nodeID, left, right, centerX: center, rank: ranks[nodeID] || 0 }
    const parentNode = nodeByID.get(nodeID)
    const children = spanningChildren.get(nodeID) || []
    if (!children.length) return

    if (children.length === 1) {
      const edge = children[0]
      const childWidth = subtreeSpans.get(edge.target) || nodeByID.get(edge.target)?.width || 0
      const anchorX = left + getNodeHandleOffsetX(parentNode, edge.sourceHandle)
      const childLeft = anchorX - childWidth / 2
      assignBand(edge.target, childLeft, childLeft + childWidth)
      return
    }

    const initialPositions = children.map(edge => {
      const width = subtreeSpans.get(edge.target) || nodeByID.get(edge.target)?.width || 0
      const anchorX = left + getNodeHandleOffsetX(parentNode, edge.sourceHandle)
      return { edge, width, anchorX, left: anchorX - width / 2 }
    })

    for (let i = 1; i < initialPositions.length; i++) {
      const prevRight = initialPositions[i - 1].left + initialPositions[i - 1].width
      if (initialPositions[i].left < prevRight + subtreeGap) {
        initialPositions[i].left = prevRight + subtreeGap
      }
    }

    const totalGroupWidth = (initialPositions[initialPositions.length - 1].left + initialPositions[initialPositions.length - 1].width) - initialPositions[0].left
    const avgAnchorX = (initialPositions[0].anchorX + initialPositions[initialPositions.length - 1].anchorX) / 2
    const groupCenter = initialPositions[0].left + totalGroupWidth / 2
    const centerShift = avgAnchorX - groupCenter
    initialPositions.forEach(item => {
      item.left += centerShift
    })

    initialPositions.forEach(item => {
      assignBand(item.edge.target, item.left, item.left + item.width)
    })
  }
  roots.forEach(nodeID => {
    const width = subtreeSpans.get(nodeID) || nodeByID.get(nodeID)?.width || 0
    assignBand(nodeID, rootCursor, rootCursor + width)
    rootCursor += width + subtreeGap
  })
  orderedNodes.forEach(node => {
    if (bands[node.id]) return
    const width = subtreeSpans.get(node.id) || node.width
    assignBand(node.id, rootCursor, rootCursor + width)
    rootCursor += width + subtreeGap
  })

  const maxRank = Math.max(0, ...Object.values(ranks))
  const nodesByRank = new Map<number, ProxyLayoutNode[]>()
  orderedNodes.forEach(node => nodesByRank.set(ranks[node.id] || 0, [...(nodesByRank.get(ranks[node.id] || 0) || []), node]))
  const layerY: number[] = [originY]
  const layerChannels: GraphLayerChannel[] = []
  for (let sourceRank = 0; sourceRank < maxRank; sourceRank++) {
    const crossingEdges = edges.filter(edge => (ranks[edge.source] || 0) <= sourceRank && (ranks[edge.target] || 0) > sourceRank)
      .sort((left, right) => {
        const leftSource = bands[left.source]?.left || 0
        const rightSource = bands[right.source]?.left || 0
        if (leftSource !== rightSource) return leftSource - rightSource
        return compareLayoutEdges(left, right)
      })
    const sourceKey = (edge: ProxyLayoutEdge) => `${edge.source}\u001f${edge.sourceHandle || ''}`
    const sourceIndex = new Map<string, number>()
    crossingEdges.forEach(edge => {
      const key = sourceKey(edge)
      if (!sourceIndex.has(key)) sourceIndex.set(key, sourceIndex.size)
    })
    const channelHeight = Math.max(
      ROUTING_MIN_CHANNEL_HEIGHT,
      channelPadding * 2 + Math.max(0, sourceIndex.size - 1) * trackGap,
    )
    const currentHeight = Math.max(GRAPH_LAYOUT_DEFAULT_NODE_HEIGHT, ...(nodesByRank.get(sourceRank) || []).map(node => node.height))
    const top = layerY[sourceRank] + currentHeight
    const bottom = top + channelHeight
    const tracks = Object.fromEntries(crossingEdges.map(edge => [
      edge.id,
      top + channelPadding + (sourceIndex.get(sourceKey(edge)) || 0) * trackGap,
    ]))
    layerChannels.push({ sourceRank, targetRank: sourceRank + 1, top, bottom, tracks })
    layerY[sourceRank + 1] = bottom
  }

  const positions = Object.fromEntries(orderedNodes.map(node => [
    node.id,
    snapGraphPosition({
      x: (bands[node.id]?.centerX || centerX) - node.width / 2,
      y: layerY[ranks[node.id] || 0] ?? originY,
    }),
  ]))
  return { positions, bands, ranks, layerChannels }
}

export function minimizeGraphLayerCrossings(
  layers: string[][],
  edges: GraphLayoutEdge[],
  compareNodes: (left: string, right: string) => number,
): string[][] {
  const ordered = layers.map(layer => layer.slice().sort(compareNodes))
  const layerByNode = new Map<string, number>()
  ordered.forEach((layer, layerIndex) => layer.forEach(nodeID => layerByNode.set(nodeID, layerIndex)))

  const incoming = new Map<string, string[]>()
  const outgoing = new Map<string, string[]>()
  edges.forEach(edge => {
    const sourceLayer = layerByNode.get(edge.source)
    const targetLayer = layerByNode.get(edge.target)
    if (sourceLayer === undefined || targetLayer === undefined || sourceLayer === targetLayer) return
    incoming.set(edge.target, [...(incoming.get(edge.target) || []), edge.source])
    outgoing.set(edge.source, [...(outgoing.get(edge.source) || []), edge.target])
  })

  const reorder = (layerIndex: number, neighbors: Map<string, string[]>) => {
    const layer = ordered[layerIndex]
    const previousIndex = new Map(layer.map((nodeID, index) => [nodeID, index]))
    const ranks = new Map<string, number>()
    ordered.forEach(nodes => {
      const denominator = Math.max(1, nodes.length - 1)
      nodes.forEach((nodeID, index) => ranks.set(nodeID, index / denominator))
    })
    const barycenter = (nodeID: string) => {
      const linkedRanks = (neighbors.get(nodeID) || [])
        .map(neighborID => ranks.get(neighborID))
        .filter((rank): rank is number => rank !== undefined)
      if (!linkedRanks.length) return undefined
      return linkedRanks.reduce((sum, rank) => sum + rank, 0) / linkedRanks.length
    }
    layer.sort((left, right) => {
      const leftCenter = barycenter(left)
      const rightCenter = barycenter(right)
      if (leftCenter !== undefined && rightCenter !== undefined && Math.abs(leftCenter - rightCenter) > 1e-9) {
        return leftCenter - rightCenter
      }
      if (leftCenter !== undefined && rightCenter === undefined) return -1
      if (leftCenter === undefined && rightCenter !== undefined) return 1
      return (previousIndex.get(left) || 0) - (previousIndex.get(right) || 0) || compareNodes(left, right)
    })
  }

  // Alternating downward and upward sweeps is the standard barycentric pass
  // used by layered graph layouts. A few bounded passes are enough for this
  // small operator-facing graph and keep the result deterministic.
  for (let pass = 0; pass < 4; pass++) {
    for (let layerIndex = 1; layerIndex < ordered.length; layerIndex++) reorder(layerIndex, incoming)
    for (let layerIndex = ordered.length - 2; layerIndex > 0; layerIndex--) reorder(layerIndex, outgoing)
  }
  return ordered
}

export function graphServerNodeWidth(_entryCount: number) {
	return GRAPH_ENTRY_NODE_WIDTH
}

export function graphPathHandleLeft(index: number, count: number) {
  if (count <= 1) return '50%'
  return `${20 + (index * 60) / (count - 1)}%`
}

export function graphEntryHandleLeft(index: number, count: number, _reserveCenter = false) {
  return graphPathHandleLeft(index, count)
}

export function defaultServerGraphPosition(index: number): GraphPosition {
  return { x: 630, y: 300 + index * 370 }
}

export function defaultImportedGraphPosition(index: number): GraphPosition {
  return { x: 630, y: 670 + index * 370 }
}

// Entry cards fan out above a fixed-width server card.
export function defaultEntryGraphPosition(
  serverPosition: GraphPosition,
  index: number,
  total = 1,
  serverWidth = graphServerNodeWidth(total),
): GraphPosition {
	const centerX = serverPosition.x + serverWidth / 2 + (index - (total - 1) / 2) * (GRAPH_ENTRY_NODE_WIDTH + 40)
  return { x: Math.round(centerX - GRAPH_ENTRY_NODE_WIDTH / 2), y: serverPosition.y - 170 }
}

export type GraphEntryOrderItem = { id: number; port: number }

function entryGraphPositionX(
  entry: GraphEntryOrderItem,
  positions: Record<string, GraphPosition>,
  portIndex: Map<number, number>,
  serverPosition: GraphPosition,
  entryCount: number,
  serverWidth: number,
) {
  const saved = positions[`entry-${entry.id}`]
  if (saved && Number.isFinite(saved.x)) return saved.x
  return defaultEntryGraphPosition(serverPosition, portIndex.get(entry.id) ?? 0, entryCount, serverWidth).x
}

// Server inbound handles must follow the cards above them. Port order and
// creation order often disagree, and that is what draws the belongs-to X.
export function sortServerEntriesForGraph<T extends GraphEntryOrderItem>(
  entries: T[],
  positions: Record<string, GraphPosition>,
  serverPosition: GraphPosition,
  serverWidth = graphServerNodeWidth(entries.length),
): T[] {
  const count = Math.max(1, entries.length)
  const portIndex = new Map(
    entries
      .slice()
      .sort((left, right) => left.port - right.port || left.id - right.id)
      .map((entry, index) => [entry.id, index]),
  )
  return entries.slice().sort((left, right) => {
    const leftX = entryGraphPositionX(left, positions, portIndex, serverPosition, count, serverWidth)
    const rightX = entryGraphPositionX(right, positions, portIndex, serverPosition, count, serverWidth)
    return leftX - rightX || left.port - right.port || left.id - right.id
  })
}
