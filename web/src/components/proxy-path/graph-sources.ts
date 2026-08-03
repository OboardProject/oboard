import type { ProxyPathReuseSource } from './TransportDialog'

export const SERVER_GRAPH_SOURCE_HANDLE = 'server-source'

export type GraphEntrySource = { id: number; label: string; title: string }
export type GraphPathSource = { step_id: number; label: string; title: string }
export type GraphSourceOption = { key: string; label: string; detail: string; source: ProxyPathReuseSource }

export function graphServerSourceOptions(entries: GraphEntrySource[], paths: GraphPathSource[]): GraphSourceOption[] {
  return [
    ...entries.map(entry => ({
      key: `inbound:${entry.id}`,
      label: entry.title,
      detail: entry.label,
      source: { inbound_id: entry.id },
    })),
    ...paths.map(path => ({
      key: `step:${path.step_id}`,
      label: path.title.split(' / ')[0] || path.title,
      detail: path.label,
      source: { step_id: path.step_id },
    })),
  ]
}
