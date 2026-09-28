type DNSCandidateTag = {
  tag?: string | null
}

export function dnsTagListLabel(tags: readonly string[] | null | undefined, fallback: string) {
  const visible = Array.isArray(tags) ? tags.filter(tag => typeof tag === 'string' && tag.length > 0) : []
  return visible.join(' · ') || fallback
}

export function dnsSelectionLabel(candidates: readonly DNSCandidateTag[] | null | undefined) {
  const tags = Array.isArray(candidates) ? candidates.map(candidate => candidate?.tag || '') : []
  return dnsTagListLabel(tags, '等待测试')
}

export function dnsRecordDetail(comment: string | null | undefined, ttl: number, linkedServerName = '') {
  const detail = comment || `TTL ${ttl}`
  const managed = /^OBoard:\s*入口\s+.*\s*\/\s*服务器\s+(.+)$/i.exec(detail.trim())
  if (managed) return `服务器 ${linkedServerName || managed[1].trim()}`
  return linkedServerName && !detail.toLocaleLowerCase().includes(linkedServerName.toLocaleLowerCase())
    ? `${detail} · 服务器 ${linkedServerName}`
    : detail
}
