// Pure geometry and browser-local persistence for the proxy-path canvas.
// Nothing here touches server state: node coordinates and the toolbox position
// are per-browser preferences, so clearing them never changes a stored path.

export type GraphPosition = { x: number; y: number }

export const GRAPH_ENTRY_NODE_WIDTH = 260

const POSITIONS_KEY = 'oboard.proxyGraph.positions.v5'
const TOOLBOX_KEY = 'oboard.proxyGraph.toolboxPosition.v1'

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

export function snapGraphPosition(position: GraphPosition): GraphPosition {
  return { x: Math.round(position.x), y: Math.round(position.y) }
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
