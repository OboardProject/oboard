import type { ProxyPathReuseSource } from './TransportDialog'

export const SERVER_GRAPH_SOURCE_HANDLE = 'server-source'

export function isGenericServerSourceHandle(handle?: string | null) {
  return handle === SERVER_GRAPH_SOURCE_HANDLE
}

export function serverEntryHandleID(inboundID: number) {
  return `server-entry-${inboundID}`
}

export function serverEntryTargetHandleID(inboundID: number) {
  return `server-entry-target-${inboundID}`
}

export function inboundIDFromServerHandle(handle?: string | null) {
  const match = /^server-entry-(\d+)$/.exec(handle || '')
  return match ? Number(match[1]) : 0
}

export type GraphEntrySource = { id: number; label: string; title: string }
export type GraphPathSource = { step_id: number; label: string; title: string }
export type GraphSourceOption = { key: string; label: string; detail: string; source: ProxyPathReuseSource }

export function graphServerEntrySourceOptions(entries: GraphEntrySource[]): GraphSourceOption[] {
  return entries.map(entry => ({
    key: `inbound:${entry.id}`,
    label: entry.title,
    detail: entry.label,
    source: { inbound_id: entry.id },
  }))
}

export type GraphConnectionSourceIntent =
  | { kind: 'resolved'; sources: ProxyPathReuseSource[] }
  | { kind: 'choose-entry'; options: GraphSourceOption[] }
  | { kind: 'unsupported' }

export function graphConnectionSourceIntent(entityType: string | undefined, entityID: number, options: GraphSourceOption[]): GraphConnectionSourceIntent {
  if (entityType === 'proxy-path-step' && entityID > 0) {
    return { kind: 'resolved', sources: [{ step_id: entityID }] }
  }
  if (entityType === 'server') {
    return { kind: 'choose-entry', options: options.filter(option => Boolean(option.source.inbound_id)) }
  }
  return { kind: 'unsupported' }
}
