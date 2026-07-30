// Pure geometry and browser-local persistence for the proxy-path canvas.
// Nothing here touches server state: node coordinates and the toolbox position
// are per-browser preferences, so clearing them never changes a stored path.

export type GraphPosition = { x: number; y: number }
export type GraphDirectExitInstance = { instance_id: string; root_server_id: number }
export type GraphLayerNode = { id: string; width: number; terminal: boolean }
export type GraphLayerLayout = { positions: Record<string, GraphPosition>; extraHeight: number }

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

// A server card widens with its entry count so every entry handle keeps a
// readable slot along the bottom edge.
export function graphServerNodeWidth(entryCount: number) {
  return Math.max(GRAPH_ENTRY_NODE_WIDTH, Math.max(1, entryCount) * GRAPH_ENTRY_NODE_WIDTH)
}

// Horizontal position of one entry handle, as a fraction of the card width.
// reserveCenter splits the handles around the middle so the shared "all entries"
// handle has room of its own.
export function graphEntryHandleRatio(index: number, count: number, reserveCenter = false) {
  if (reserveCenter) {
    const leftCount = Math.ceil(count / 2)
    if (index < leftCount) return ((index + 1) / (leftCount + 1)) * 0.42
    const rightCount = count - leftCount
    return 0.58 + ((index - leftCount + 1) / (rightCount + 1)) * 0.42
  }
  return (index + 0.5) / Math.max(1, count)
}

export function graphEntryHandleLeft(index: number, count: number, reserveCenter = false) {
  return `${graphEntryHandleRatio(index, count, reserveCenter) * 100}%`
}

export function graphPathHandleLeft(index: number, count: number) {
  if (count <= 1) return '50%'
  return `${15 + (index * 70) / (count - 1)}%`
}

export function defaultServerGraphPosition(index: number): GraphPosition {
  return { x: 630, y: 300 + index * 370 }
}

export function defaultImportedGraphPosition(index: number): GraphPosition {
  return { x: 630, y: 670 + index * 370 }
}

// Entries sit above their server card, centered on their own handle.
export function defaultEntryGraphPosition(
  serverPosition: GraphPosition,
  index: number,
  total = 1,
  serverWidth = graphServerNodeWidth(total),
): GraphPosition {
  const centerX = serverPosition.x + serverWidth * graphEntryHandleRatio(index, total)
  return { x: Math.round(centerX - GRAPH_ENTRY_NODE_WIDTH / 2), y: serverPosition.y - 170 }
}
