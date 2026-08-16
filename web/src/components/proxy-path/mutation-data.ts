type Entity = { id: number }

const topologyCollections = [
  ['servers', 'server'],
  ['inbounds', 'inbound'],
  ['external_outbounds', 'external_outbound'],
  ['proxy_paths', 'proxy_path'],
  ['proxy_path_steps', 'proxy_path_step'],
  ['routing_rules', 'routing_rule'],
  ['port_forwards', 'port_forward'],
  ['tunnels', 'tunnel'],
  ['warp_profiles', 'warp_profile'],
] as const

function upsertEntities(current: Entity[] | undefined, incoming: Entity[]) {
  const merged = new Map((current || []).map(item => [item.id, item]))
  incoming.forEach(item => merged.set(item.id, { ...merged.get(item.id), ...item }))
  return Array.from(merged.values())
}

// Topology mutation endpoints return only the rows they changed. Merge those
// rows immediately so the graph can render the confirmed result while a full
// page-data request reconciles derived names and cross-resource state.
export function mergeTopologyMutation<T extends Record<string, any>>(current: T, payload: Record<string, any>): T {
  let next: Record<string, any> = current
  topologyCollections.forEach(([collection, singular]) => {
    const many = Array.isArray(payload[collection]) ? payload[collection] : []
    const one = payload[singular]?.id ? [payload[singular]] : []
    if (!many.length && !one.length) return
    next = { ...next, [collection]: upsertEntities(next[collection], [...many, ...one]) }
  })
  return next as T
}

export function removeTopologyRows<T extends Record<string, any>>(current: T, removals: Partial<Record<string, readonly number[]>>): T {
  let next: Record<string, any> = current
  Object.entries(removals).forEach(([collection, ids]) => {
    if (!ids?.length || !Array.isArray(next[collection])) return
    const removed = new Set(ids)
    next = { ...next, [collection]: next[collection].filter((item: Entity) => !removed.has(item.id)) }
  })
  return next as T
}
