export type DNSTransport = 'udp' | 'tcp' | 'dot' | 'doh' | 'doq'
export type DNSListKind = 'encrypted' | 'bootstrap'
export type DNSCandidate = { tag: string; transport: DNSTransport; server: string; port: number; path?: string; tls_name?: string }

export function dnsDefaultPort(transport: DNSTransport) {
  if (transport === 'doh') return 443
  if (transport === 'dot' || transport === 'doq') return 853
  return 53
}

export function dnsCandidateInput(candidate?: DNSCandidate) {
  if (!candidate?.server) return ''
  const scheme = ({ udp: 'udp', tcp: 'tcp', dot: 'tls', doh: 'https', doq: 'quic' } as Record<DNSTransport, string>)[candidate.transport]
  const defaultPort = dnsDefaultPort(candidate.transport)
  const port = candidate.port && candidate.port !== defaultPort ? `:${candidate.port}` : ''
  const host = candidate.server.includes(':') && !candidate.server.startsWith('[') ? `[${candidate.server}]` : candidate.server
  return `${scheme}://${host}${port}${candidate.transport === 'doh' ? candidate.path || '/dns-query' : ''}`
}

export function parseDNSCandidate(value: string, fallback: DNSTransport, tag: string): DNSCandidate | null {
  const raw = value.trim()
  if (!raw) return null
  const withScheme = /^[a-z]+:\/\//i.test(raw) ? raw : `${fallback === 'dot' ? 'tls' : fallback === 'doh' ? 'https' : fallback === 'doq' ? 'quic' : fallback}://${raw}`
  let parsed: URL
  try { parsed = new URL(withScheme) } catch { throw new Error(`DNS 地址无效：${raw}`) }
  const transport = ({ udp: 'udp', tcp: 'tcp', tls: 'dot', dot: 'dot', https: 'doh', doh: 'doh', quic: 'doq', doq: 'doq' } as Record<string, DNSTransport>)[parsed.protocol.replace(':', '')]
  if (!transport || !parsed.hostname) throw new Error(`DNS 地址无效：${raw}`)
  const server = parsed.hostname.replace(/^\[|\]$/g, '')
  return {
    tag,
    transport,
    server,
    port: parsed.port ? Number(parsed.port) : dnsDefaultPort(transport),
    ...(transport === 'doh' ? { path: parsed.pathname || '/dns-query' } : {}),
    ...(['dot', 'doh', 'doq'].includes(transport) ? { tls_name: server } : {}),
  }
}

// Custom resolvers are entered one address per line; each gets a positional
// tag so the operator never has to name them.
export function parseCustomDNSCandidates(text: string, kind: DNSListKind): DNSCandidate[] {
  const lines = text.split('\n').map(line => line.trim()).filter(Boolean)
  const label = kind === 'encrypted' ? '加密解析' : '基础解析'
  if (!lines.length) throw new Error(`请填写至少 1 个自定义${label}服务`)
  if (lines.length > 32) throw new Error(`自定义${label}服务最多 32 个`)
  return lines.map((line, index) => {
    const candidate = parseDNSCandidate(line, kind === 'encrypted' ? 'doh' : 'udp', `custom-${index + 1}`)!
    if (kind === 'encrypted' && !['doh', 'dot', 'doq'].includes(candidate.transport)) throw new Error(`加密解析服务只支持 DoH、DoT 或 DoQ：${line}`)
    if (kind === 'bootstrap' && !['udp', 'tcp'].includes(candidate.transport)) throw new Error(`基础解析服务只支持 UDP 或 TCP：${line}`)
    return candidate
  })
}

export function customDNSCandidatesText(candidates: readonly DNSCandidate[] | null | undefined) {
  return (candidates || []).map(candidate => dnsCandidateInput(candidate)).join('\n')
}
