const DEFAULT_CHAIN_METHOD = '2022-blake3-aes-128-gcm'
const DEFAULT_REALITY_SERVER = 'cdn.icloud-content.com'
const DEFAULT_REALITY_PORT = 443

export function proxyPathGeneratedReuseCountKey(config: Record<string, unknown>): string | null {
  const protocol = String(config.chain_protocol || 'shadowsocks').toLowerCase()
  if (protocol === 'shadowsocks') return `shadowsocks:${String(config.chain_method || DEFAULT_CHAIN_METHOD)}`
  if (protocol === 'vless') {
    return `vless:${String(config.reality_handshake_server || DEFAULT_REALITY_SERVER).toLowerCase()}:${Number(config.reality_handshake_port || DEFAULT_REALITY_PORT)}`
  }
  if (protocol === 'mieru' || protocol === 'socks') return protocol
  return null
}
