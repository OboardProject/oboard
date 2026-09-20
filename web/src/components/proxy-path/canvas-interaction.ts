import { useCallback, useState, type Dispatch, type SetStateAction } from 'react'
import type { GraphPosition } from './layout'

type CanvasConnection = { source: string | null; target: string | null; sourceHandle?: string | null; targetHandle?: string | null }
type CanvasEdge = { source: string; target: string; sourceHandle?: string | null; targetHandle?: string | null; data?: { routingClass?: string } }

/** Canvas checks are advisory; the Controller remains the topology authority. */
export function graphConnectionIssue(connection: CanvasConnection, edges: readonly CanvasEdge[], busy = false): string {
  if (busy) return '上一条连接仍在处理中，请完成或取消后再连接。'
  if (!connection.source || !connection.target) return '请选择起点和目标连接点。'
  if (connection.source === connection.target) return '不能连接节点自身。请添加另一个目标节点。'
  if (edges.some(edge => edge.source === connection.source && edge.target === connection.target
    && (edge.sourceHandle || '') === (connection.sourceHandle || '')
    && (edge.targetHandle || '') === (connection.targetHandle || ''))) return '这两个连接点已经相连，可双击连线修改传递方式。'
  const outgoing = new Map<string, string[]>()
  for (const edge of edges) {
    if (edge.data?.routingClass !== 'primary') continue
    outgoing.set(edge.source, [...(outgoing.get(edge.source) || []), edge.target])
  }
  const pending = [connection.target]
  const visited = new Set<string>()
  while (pending.length) {
    const node = pending.pop()!
    if (node === connection.source) return '这条连接会返回上游节点，形成循环。请选择下游节点。'
    if (visited.has(node)) continue
    visited.add(node)
    pending.push(...(outgoing.get(node) || []))
  }
  return ''
}

export function useCanvasScope<T>(rootID: number, empty: T): [T, Dispatch<SetStateAction<T>>] {
  const [scopes, setScopes] = useState<Record<number, T>>({})
  const update = useCallback<Dispatch<SetStateAction<T>>>(value => {
    setScopes(current => ({
      ...current,
      [rootID]: typeof value === 'function'
        ? (value as (previous: T) => T)(current[rootID] ?? empty)
        : value,
    }))
  }, [rootID, empty])
  return [scopes[rootID] ?? empty, update]
}

export type CanvasRect = GraphPosition & { width: number; height: number }

export function findCanvasPlacement(preferred: GraphPosition, size: { width: number; height: number }, occupied: readonly CanvasRect[], visible?: CanvasRect): GraphPosition {
  const open = (position: GraphPosition) => occupied.every(rect =>
    position.x + size.width + 24 <= rect.x || rect.x + rect.width + 24 <= position.x
    || position.y + size.height + 24 <= rect.y || rect.y + rect.height + 24 <= position.y)
  const candidates: GraphPosition[] = [preferred]
  if (visible) {
    const start = { x: visible.x + 16, y: visible.y + 16 }
    for (let y = start.y; y + size.height <= visible.y + visible.height; y += size.height + 32) {
      for (let x = start.x; x + size.width <= visible.x + visible.width; x += size.width + 32) candidates.push({ x, y })
    }
    const fits = (point: GraphPosition) => point.x >= visible.x && point.y >= visible.y
      && point.x + size.width <= visible.x + visible.width && point.y + size.height <= visible.y + visible.height
    const visibleOpen = candidates.filter(fits).filter(open).sort((a, b) => Math.hypot(a.x - preferred.x, a.y - preferred.y) - Math.hypot(b.x - preferred.x, b.y - preferred.y))[0]
    if (visibleOpen) return visibleOpen
  }
  if (open(preferred)) return preferred
  // A finite, collision-free fallback, even when the visible canvas is full.
  return { x: preferred.x, y: Math.max(preferred.y, ...occupied.map(rect => rect.y + rect.height + 48)) }
}
