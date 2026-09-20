// Pure geometry and browser-local persistence for the proxy-path canvas.
// Nothing here touches server state: node coordinates and the toolbox position
// are per-browser preferences, so clearing them never changes a stored path.

import { removeStoredValue, writeStoredJSON } from '../../browser-storage'

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

// v6 coordinates were restored unconditionally over the computed layout, so a
// canvas whose topology had changed since the last visit always reopened with
// stale ranks and detoured edges. v7 started from the layout and only restored
// pinned nodes, but the structural signature that decides whether to relayout
// does not cover per-node width — so when the server-card width formula itself
// changed (slot-count based instead of fixed), v7 coordinates kept being reused
// under a now-different width and cards drifted off their connection points.
// v8 forces one full recompute for that formula change; the drift is not a
// bug an operator can fix by dragging, so it has to be an automatic reset.
const POSITIONS_KEY = 'oboard.proxyGraph.positions.v9'
const LEGACY_POSITIONS_KEYS = ['oboard.proxyGraph.positions.v6', 'oboard.proxyGraph.positions.v7', 'oboard.proxyGraph.positions.v8']
const PINNED_KEY = 'oboard.proxyGraph.pinnedNodes.v1'
const SIGNATURE_KEY = 'oboard.proxyGraph.layoutSignature.v1'
const TOOLBOX_KEY = 'oboard.proxyGraph.toolboxPosition.v1'
const DIRECT_EXITS_KEY = 'oboard.proxyGraph.directExitInstances.v2'
const LEGACY_DIRECT_EXITS_KEY = 'oboard.proxyGraph.directExitInstances.v1'

export function loadGraphPositions(): Record<string, GraphPosition> {
  try {
    LEGACY_POSITIONS_KEYS.forEach(key => removeStoredValue(key))
    return JSON.parse(localStorage.getItem(POSITIONS_KEY) || '{}')
  } catch {
    return {}
  }
}

export function saveGraphPositions(positions: Record<string, GraphPosition>) {
  writeStoredJSON(POSITIONS_KEY, positions)
}

/** Node IDs the operator dragged. Auto layout leaves these where they are. */
export function loadPinnedGraphNodes(): string[] {
  try {
    const value = JSON.parse(localStorage.getItem(PINNED_KEY) || '[]')
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
  } catch {
    return []
  }
}

export function savePinnedGraphNodes(nodeIDs: Iterable<string>) {
  writeStoredJSON(PINNED_KEY, Array.from(new Set(nodeIDs)).sort())
}

export function loadGraphLayoutSignatures(): Record<string, string> {
  try {
    const value = JSON.parse(localStorage.getItem(SIGNATURE_KEY) || '{}')
    return value && typeof value === 'object' && !Array.isArray(value) ? value : {}
  } catch {
    return {}
  }
}

export function saveGraphLayoutSignature(rootServerID: number, signature: string) {
  const current = loadGraphLayoutSignatures()
  writeStoredJSON(SIGNATURE_KEY, { ...current, [String(rootServerID)]: signature })
}

// Bump whenever the layout maths changes shape — card widths, slot pitch, layer
// spacing. Stored signatures then stop matching and every canvas recomputes
// once. A purely structural fingerprint cannot notice this on its own: the
// topology is identical, only the geometry it should produce has changed.
export const GRAPH_LAYOUT_ALGORITHM_VERSION = 3

/** Structural fingerprint of one canvas: which nodes exist and how the primary
 *  chain connects them. Coordinates deliberately do not participate, so moving
 *  a card never asks for a relayout while adding a hop always does. */
export function graphLayoutSignature(
  nodeIDs: Iterable<string>,
  primaryEdges: Iterable<{ source: string; target: string; sourceHandle?: string }>,
): string {
  const nodes = Array.from(new Set(nodeIDs)).sort()
  const edges = Array.from(new Set(
    Array.from(primaryEdges).map(edge => `${edge.source}${edge.sourceHandle || ''}>${edge.target}`),
  )).sort()
  return `v${GRAPH_LAYOUT_ALGORITHM_VERSION}|${nodes.join(',')}|${edges.join(',')}`
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
  writeStoredJSON(TOOLBOX_KEY, position)
}

export function loadGraphDirectExitInstances(): GraphDirectExitInstance[] {
  try {
    removeStoredValue(LEGACY_DIRECT_EXITS_KEY)
    const value = JSON.parse(localStorage.getItem(DIRECT_EXITS_KEY) || '[]')
    if (!Array.isArray(value)) return []
    return value.filter(item => typeof item?.instance_id === 'string' && Number.isFinite(item?.root_server_id))
  } catch {
    return []
  }
}

export function saveGraphDirectExitInstances(instances: GraphDirectExitInstance[]) {
  writeStoredJSON(DIRECT_EXITS_KEY, instances)
}

export function snapGraphPosition(position: GraphPosition): GraphPosition {
  return { x: Math.round(position.x), y: Math.round(position.y) }
}

/** Pointer drags land on arbitrary sub-pixel offsets. Quantising them keeps a
 *  card that looks aligned actually aligned, so the router can still draw the
 *  straight vertical segment instead of a two-bend jog. */
export const GRAPH_DRAG_SNAP_GRID = 4

export function snapDraggedGraphPosition(position: GraphPosition, grid = GRAPH_DRAG_SNAP_GRID): GraphPosition {
  const size = grid > 0 ? grid : 1
  return { x: Math.round(position.x / size) * size, y: Math.round(position.y / size) * size }
}

export type GraphHopFallbackInput = {
  /** Top-left of the card this hop continues from. */
  parent: GraphPosition
  /** Distance from the parent's left edge to the handle the edge leaves from. */
  parentHandleOffsetX: number
  parentHeight?: number
  childWidth: number
  /** Nth hop already placed under this same parent. */
  siblingIndex?: number
}

/** Where a hop goes before anything has positioned it: centred on the handle it
 *  leaves from and a full layer below the parent, so it never lands on the card
 *  it continues from and the router can draw one straight segment. */
export function graphHopFallbackPosition({
  parent,
  parentHandleOffsetX,
  parentHeight = GRAPH_LAYOUT_DEFAULT_NODE_HEIGHT,
  childWidth,
  siblingIndex = 0,
}: GraphHopFallbackInput): GraphPosition {
  const anchorX = parent.x + parentHandleOffsetX
  return snapGraphPosition({
    x: anchorX - childWidth / 2 + Math.max(0, siblingIndex) * (childWidth + GRAPH_LAYER_SIBLING_GAP),
    y: parent.y + parentHeight + ROUTING_MIN_CHANNEL_HEIGHT,
  })
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
  outgoing.forEach(nodeEdges => nodeEdges.sort((left, right) =>
    getNodeHandleOffsetX(nodeByID.get(left.source), left.sourceHandle)
      - getNodeHandleOffsetX(nodeByID.get(right.source), right.sourceHandle)
      || compareLayoutEdges(left, right)))

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

  function getNodeHandleOffsetX(node: ProxyLayoutNode | undefined, handleID?: string): number {
    if (!node) return 0
    if (handleID && node.handles?.[handleID] && Number.isFinite(node.handles[handleID].x)) {
      return node.handles[handleID].x
    }
    return node.width / 2
  }

  const bands: Record<string, GraphBranchBand> = {}
  const nodeLeft = (nodeID: string) => bands[nodeID].centerX - nodeByID.get(nodeID)!.width / 2
  const mean = (values: number[]) => values.reduce((sum, value) => sum + value, 0) / values.length
  const rankOrder = [...new Set(Object.values(ranks))].sort((a, b) => a - b)
  for (const rank of rankOrder) {
    const layer = orderedNodes.filter(node => ranks[node.id] === rank).map(node => {
      const parents = (incoming.get(node.id) || []).filter(edge => bands[edge.source] && ranks[edge.source] < rank)
      const anchors = parents.map(edge => nodeLeft(edge.source) + getNodeHandleOffsetX(nodeByID.get(edge.source), edge.sourceHandle))
      return {
        node,
        width: subtreeSpans.get(node.id) || node.width,
        order: anchors.length ? mean(anchors) : centerX,
        desired: anchors.length
          ? mean(parents.map((edge, index) => anchors[index] - getNodeHandleOffsetX(node, edge.targetHandle) + node.width / 2))
          : centerX,
        edge: parents[0],
      }
    }).sort((a, b) => a.order - b.order
      || (a.edge && b.edge ? compareLayoutEdges(a.edge, b.edge) : 0)
      || (a.node.id === rootNodeID ? -1 : b.node.id === rootNodeID ? 1 : a.node.id.localeCompare(b.node.id)))

    // Project desired centres onto non-overlapping subtree slots. Pooling adjacent
    // violations preserves port order and shares displacement instead of pushing
    // every later sibling right. All incoming handles contribute at a DAG merge.
    const offsets: number[] = []
    const blocks: { start: number; end: number; sum: number; count: number }[] = []
    layer.forEach((item, index) => {
      offsets[index] = index === 0 ? 0 : offsets[index - 1] + (layer[index - 1].width + item.width) / 2 + subtreeGap
      blocks.push({ start: index, end: index, sum: item.desired - offsets[index], count: 1 })
      while (blocks.length > 1) {
        const right = blocks[blocks.length - 1]
        const left = blocks[blocks.length - 2]
        if (left.sum / left.count <= right.sum / right.count) break
        left.end = right.end
        left.sum += right.sum
        left.count += right.count
        blocks.pop()
      }
    })
    for (const block of blocks) {
      for (let index = block.start; index <= block.end; index++) {
        const { node, width } = layer[index]
        const center = block.sum / block.count + offsets[index]
        bands[node.id] = { nodeID: node.id, left: center - width / 2, right: center + width / 2, centerX: center, rank }
      }
    }
  }

  const maxRank = Math.max(0, ...Object.values(ranks))
  const nodesByRank = new Map<number, ProxyLayoutNode[]>()
  orderedNodes.forEach(node => nodesByRank.set(ranks[node.id] || 0, [...(nodesByRank.get(ranks[node.id] || 0) || []), node]))
  const layerY: number[] = [originY]
  const layerChannels: GraphLayerChannel[] = []
  for (let sourceRank = 0; sourceRank < maxRank; sourceRank++) {
    const crossingEdges = edges.filter(edge => (ranks[edge.source] || 0) <= sourceRank && (ranks[edge.target] || 0) > sourceRank)
      .sort((left, right) => {
        const leftSource = nodeLeft(left.source) + getNodeHandleOffsetX(nodeByID.get(left.source), left.sourceHandle)
        const rightSource = nodeLeft(right.source) + getNodeHandleOffsetX(nodeByID.get(right.source), right.sourceHandle)
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
      x: (bands[node.id]?.centerX ?? centerX) - node.width / 2,
      y: layerY[ranks[node.id] || 0] ?? originY,
    }),
  ]))
  return { positions, bands, ranks, layerChannels }
}

/** Keep pins fixed; move other rectangles the shortest horizontal distance into
 * free space. Overlapping pins remain untouched because operator intent wins. */
export function resolveGraphNodeCollisions(
  nodes: Pick<ProxyLayoutNode, 'id' | 'width' | 'height'>[],
  positions: Record<string, GraphPosition>,
  pinnedIDs: Iterable<string>,
): Record<string, GraphPosition> {
  const result = Object.fromEntries(Object.entries(positions).map(([id, position]) => [id, { ...position }]))
  const pinned = new Set(pinnedIDs)
  const placed: Pick<ProxyLayoutNode, 'id' | 'width' | 'height'>[] = []
  const ordered = nodes.filter(node => Number.isFinite(result[node.id]?.x) && Number.isFinite(result[node.id]?.y))
    .slice().sort((a, b) => Number(pinned.has(b.id)) - Number(pinned.has(a.id))
      || result[a.id].y - result[b.id].y || result[a.id].x - result[b.id].x || a.id.localeCompare(b.id))
  for (const node of ordered) {
    const position = result[node.id]
    if (!pinned.has(node.id)) {
      const intervals = placed.filter(other => {
        const otherPosition = result[other.id]
        return position.y < otherPosition.y + other.height && otherPosition.y < position.y + node.height
      }).map(other => ({
        left: result[other.id].x - node.width - PRIMARY_SUBTREE_GAP,
        right: result[other.id].x + other.width + PRIMARY_SUBTREE_GAP,
      }))
      const candidates = [position.x, ...intervals.flatMap(interval => [interval.left, interval.right])]
        .filter(x => intervals.every(interval => x <= interval.left || x >= interval.right))
        .sort((a, b) => Math.abs(a - position.x) - Math.abs(b - position.x) || a - b)
      position.x = candidates[0]
    }
    placed.push(node)
  }
  return result
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

// Every source on a server card owns one slot as wide as the card that hangs
// under it. That makes the entry card above a handle, the handle itself and the
// first hop below it share one vertical line; anything narrower forces the
// router into a jog no layout pass can remove, because the children are wider
// than the span the handles are squeezed into.
export const GRAPH_SERVER_SLOT_WIDTH = GRAPH_ENTRY_NODE_WIDTH + PRIMARY_SUBTREE_GAP

export function graphServerNodeWidth(sourceCount: number) {
  return Math.max(1, sourceCount) * GRAPH_SERVER_SLOT_WIDTH
}

/** Horizontal centre of one source slot, as a CSS percentage of card width. */
export function graphPathHandleLeft(index: number, count: number) {
  const slots = Math.max(1, count)
  const slot = Math.max(0, Math.min(index, slots - 1))
  return `${((slot + 0.5) / slots) * 100}%`
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

// An entry card sits centred on its own slot, directly above the handle it
// feeds, so the belongs-to edge is a straight drop. `slotCount` is the number of
// sources the card carries — entries plus path continuations — not just entries.
export function defaultEntryGraphPosition(
  serverPosition: GraphPosition,
  index: number,
  slotCount = 1,
  serverWidth = graphServerNodeWidth(slotCount),
): GraphPosition {
  const slots = Math.max(1, slotCount)
  const slot = Math.max(0, Math.min(index, slots - 1))
  const centerX = serverPosition.x + ((slot + 0.5) / slots) * serverWidth
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
