type DNSCandidateTag = {
  tag?: string | null
}

export function dnsTagListLabel(tags: readonly string[] | null | undefined, fallback: string) {
  const visible = Array.isArray(tags) ? tags.filter(tag => typeof tag === 'string' && tag.length > 0) : []
  return visible.join(' · ') || fallback
}

export function dnsSelectionLabel(candidates: readonly DNSCandidateTag[] | null | undefined) {
  const tags = Array.isArray(candidates) ? candidates.map(candidate => candidate?.tag || '') : []
  return dnsTagListLabel(tags, '等待检查')
}
