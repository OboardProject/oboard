// Pure geometry and browser-local persistence for the proxy-path canvas.
// Nothing here touches server state: node coordinates and the toolbox position
// are per-browser preferences, so clearing them never changes a stored path.

export type GraphPosition = { x: number; y: number }
export type GraphDirectExitInstance = { instance_id: string; root_server_id: number }
export type GraphLayerNode = { id: string; width: number; terminal: boolean }
export type GraphLayerLayout = { positions: Record<string, GraphPosition>; extraHeight: number }
export type GraphLayoutEdge = { source: string; target: string }

export const GRAPH_ENTRY_NODE_WIDTH = 260
export const GRAPH_LAYER_SIBLING_GAP = 100
export const GRAPH_LAYER_COMPACT_WIDTH = GRAPH_ENTRY_NODE_WIDTH * 4 + GRAPH_LAYER_SIBLING_GAP * 3
export const GRAPH_LAYER_SECONDARY_OFFSET_Y = 190

const POSITIONS_KEY = 'oboard.proxyGraph.positions.v5'
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

function graphLayerRowWidth(nodes: GraphLayerNode[], siblingGap: number) {
  return nodes.reduce((sum, node) => sum + node.width, 0) + Math.max(0, nodes.length - 1) * siblingGap
}

function placeGraphLayerRow(nodes: GraphLayerNode[], centerX: number, y: number, siblingGap: number) {
  const positions: Record<string, GraphPosition> = {}
  let cursorX = centerX - graphLayerRowWidth(nodes, siblingGap) / 2
  nodes.forEach(node => {
    positions[node.id] = snapGraphPosition({ x: cursorX, y })
    cursorX += node.width + siblingGap
  })
  return positions
}

export function layoutGraphLayer(
  nodes: GraphLayerNode[],
  centerX: number,
  y: number,
  siblingGap = GRAPH_LAYER_SIBLING_GAP,
  compactWidth = GRAPH_LAYER_COMPACT_WIDTH,
): GraphLayerLayout {
  const totalWidth = graphLayerRowWidth(nodes, siblingGap)
  const terminalCount = nodes.filter(node => node.terminal).length
  if (totalWidth <= compactWidth || terminalCount < 2) {
    return { positions: placeGraphLayerRow(nodes, centerX, y, siblingGap), extraHeight: 0 }
  }

  const primaryIDs = new Set(nodes.filter(node => !node.terminal).map(node => node.id))
  let primaryWidth = graphLayerRowWidth(nodes.filter(node => primaryIDs.has(node.id)), siblingGap)
  let primaryCount = primaryIDs.size
  let secondaryWidth = 0
  let secondaryCount = 0

  nodes.filter(node => node.terminal).forEach(node => {
    const nextPrimaryWidth = primaryWidth + (primaryCount ? siblingGap : 0) + node.width
    const nextSecondaryWidth = secondaryWidth + (secondaryCount ? siblingGap : 0) + node.width
    if (nextPrimaryWidth <= nextSecondaryWidth) {
      primaryIDs.add(node.id)
      primaryWidth = nextPrimaryWidth
      primaryCount++
      return
    }
    secondaryWidth = nextSecondaryWidth
    secondaryCount++
  })

  const primary = nodes.filter(node => primaryIDs.has(node.id))
  const secondary = nodes.filter(node => !primaryIDs.has(node.id))
  if (!secondary.length) {
    return { positions: placeGraphLayerRow(nodes, centerX, y, siblingGap), extraHeight: 0 }
  }

  const staggerX = primary.length % 2 === secondary.length % 2
    ? (GRAPH_ENTRY_NODE_WIDTH + siblingGap) / 2
    : 0
  const positions = {
    ...placeGraphLayerRow(primary, centerX, y, siblingGap),
    ...placeGraphLayerRow(secondary, centerX + staggerX, y + GRAPH_LAYER_SECONDARY_OFFSET_Y, siblingGap),
  }
  let minX = Number.POSITIVE_INFINITY
  let maxX = Number.NEGATIVE_INFINITY
  nodes.forEach(node => {
    const position = positions[node.id]
    minX = Math.min(minX, position.x)
    maxX = Math.max(maxX, position.x + node.width)
  })
  const shiftX = centerX - (minX + maxX) / 2
  Object.keys(positions).forEach(nodeID => {
    positions[nodeID] = snapGraphPosition({ x: positions[nodeID].x + shiftX, y: positions[nodeID].y })
  })

  return { positions, extraHeight: GRAPH_LAYER_SECONDARY_OFFSET_Y }
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

export function layoutGraphLanes(
  layers: GraphLayerNode[][],
  edges: GraphLayoutEdge[],
  centerX: number,
  originY: number,
  layerGap: number,
  siblingGap = GRAPH_LAYER_SIBLING_GAP,
): Record<string, GraphPosition> {
  const nodeByID = new Map(layers.flat().map(node => [node.id, node]))
  const layerByNode = new Map<string, number>()
  layers.forEach((layer, layerIndex) => layer.forEach(node => layerByNode.set(node.id, layerIndex)))
  const incoming = new Map<string, string[]>()
  edges.forEach(edge => {
    const sourceLayer = layerByNode.get(edge.source)
    const targetLayer = layerByNode.get(edge.target)
    if (sourceLayer === undefined || targetLayer === undefined || sourceLayer >= targetLayer) return
    incoming.set(edge.target, [...(incoming.get(edge.target) || []), edge.source])
  })

  const maxWidth = Math.max(GRAPH_ENTRY_NODE_WIDTH, ...Array.from(nodeByID.values(), node => node.width))
  const columnStep = maxWidth + siblingGap
  const laneByNode = new Map<string, number>()
  const rawPositions: Record<string, GraphPosition> = {}
  layers.forEach((layer, layerIndex) => {
    const layerY = originY + layerIndex * layerGap

    // Wide sibling groups use two interleaved rows. The visual order still
    // follows the crossing-minimized layer order, while the half-column
    // stagger keeps a lower node's incoming edge clear of the node above it.
    if (graphLayerRowWidth(layer, siblingGap) > GRAPH_LAYER_COMPACT_WIDTH) {
      const primary = layer.filter((_, index) => index % 2 === 0)
      const secondary = layer.filter((_, index) => index % 2 === 1)
      const secondaryCenterX = layer.length % 2 === 0 ? columnStep / 2 : 0
      const compactPositions = {
        ...placeGraphLayerRow(primary, 0, layerY, siblingGap),
        ...placeGraphLayerRow(secondary, secondaryCenterX, layerY + GRAPH_LAYER_SECONDARY_OFFSET_Y, siblingGap),
      }
      const compactMinX = Math.min(...layer.map(node => compactPositions[node.id].x))
      const compactMaxX = Math.max(...layer.map(node => compactPositions[node.id].x + node.width))
      const compactShiftX = -(compactMinX + compactMaxX) / 2
      layer.forEach(node => {
        const position = { x: compactPositions[node.id].x + compactShiftX, y: compactPositions[node.id].y }
        rawPositions[node.id] = position
        laneByNode.set(node.id, (position.x + node.width / 2) / columnStep)
      })
      return
    }

    const used = new Set<number>()
    const preferredLane = (nodeID: string) => {
      const parentLanes = (incoming.get(nodeID) || [])
        .map(parentID => laneByNode.get(parentID))
        .filter((lane): lane is number => lane !== undefined)
      if (!parentLanes.length) return undefined
      return parentLanes.reduce((sum, lane) => sum + lane, 0) / parentLanes.length
    }
    const nearestFreeLane = (preferred: number) => {
      const center = Math.round(preferred)
      if (!used.has(center)) return center
      for (let offset = 1; offset <= layer.length + used.size; offset++) {
        if (!used.has(center + offset)) return center + offset
        if (!used.has(center - offset)) return center - offset
      }
      return center + used.size + 1
    }

    if (layerIndex === 0) {
      layer.forEach((node, index) => {
        const lane = index
        laneByNode.set(node.id, lane)
        used.add(lane)
        rawPositions[node.id] = { x: lane * columnStep - node.width / 2, y: layerY }
      })
      return
    }

    // Reserve each upstream node's lane for its first child. This keeps the
    // main continuation vertical before secondary branches occupy side lanes.
    const primaryChildren = new Set<string>()
    const claimedParents = new Set<string>()
    layer.forEach(node => {
      for (const parentID of incoming.get(node.id) || []) {
        if (claimedParents.has(parentID) || laneByNode.get(parentID) === undefined) continue
        claimedParents.add(parentID)
        primaryChildren.add(node.id)
      }
    })
    const place = (node: GraphLayerNode) => {
      const preferred = preferredLane(node.id)
      const fallback = used.size ? Math.max(...used) + 1 : 0
      const lane = nearestFreeLane(preferred ?? fallback)
      laneByNode.set(node.id, lane)
      used.add(lane)
      rawPositions[node.id] = { x: lane * columnStep - node.width / 2, y: layerY }
    }
    layer.filter(node => primaryChildren.has(node.id)).forEach(place)
    layer.filter(node => !primaryChildren.has(node.id)).forEach(place)
  })

  const nodes = Array.from(nodeByID.values())
  const minX = Math.min(...nodes.map(node => rawPositions[node.id].x))
  const maxX = Math.max(...nodes.map(node => rawPositions[node.id].x + node.width))
  const shiftX = centerX - (minX + maxX) / 2
  return Object.fromEntries(nodes.map(node => [
    node.id,
    snapGraphPosition({ x: rawPositions[node.id].x + shiftX, y: rawPositions[node.id].y }),
  ]))
}

export function graphServerNodeWidth(_entryCount: number) {
	return GRAPH_ENTRY_NODE_WIDTH
}

export function graphPathHandleLeft(index: number, count: number) {
  if (count <= 1) return '50%'
  return `${15 + (index * 70) / (count - 1)}%`
}

export function graphEntryHandleLeft(index: number, count: number, reserveCenter = false) {
  if (!reserveCenter) return graphPathHandleLeft(index, count)
  const leftCount = Math.ceil(count / 2)
  if (index < leftCount) return `${((index + 1) / (leftCount + 1)) * 42}%`
  const rightCount = count - leftCount
  return `${58 + ((index - leftCount + 1) / (rightCount + 1)) * 42}%`
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
