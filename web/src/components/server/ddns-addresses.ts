export type DDNSInterface = {
  name: string
  up: boolean
  running: boolean
  loopback: boolean
  addresses: string[]
}

export type DDNSAddressChoice = {
  interfaceName: string
  address: string
  family: 'ipv4' | 'ipv6'
  internal: boolean
}

export function ddnsAddressChoices(interfaces: DDNSInterface[], family: '' | 'ipv4' | 'ipv6', excludePrivate: boolean): DDNSAddressChoice[] {
  return interfaces.flatMap(iface => {
    if (iface.loopback || !iface.up || !iface.running) return []
    return iface.addresses.flatMap(address => {
      const parsed = classifyDDNSAddress(address)
      if (!parsed) return []
      if (family && parsed.family !== family) return []
      if (excludePrivate && parsed.internal) return []
      return [{ interfaceName: iface.name, address, family: parsed.family, internal: parsed.internal }]
    })
  })
}

export function classifyDDNSAddress(value: string): { family: 'ipv4' | 'ipv6'; internal: boolean } | null {
  const address = value.trim().split('/')[0]?.replace(/^\[|\]$/g, '') || ''
  if (!address) return null
  if (address.includes(':')) {
    const expanded = expandIPv6(address)
    if (!expanded) return null
    return { family: 'ipv6', internal: internalIPv6(expanded) }
  }
  const parts = address.split('.')
  if (parts.length !== 4 || parts.some(part => !/^\d{1,3}$/.test(part) || Number(part) > 255)) return null
  const octets = parts.map(Number)
  return { family: 'ipv4', internal: internalIPv4(octets) }
}

function internalIPv4(octets: number[]) {
  const [a, b] = octets
  return a === 0 || a === 10 || a === 127 || (a === 100 && (b & 0xc0) === 64) || (a === 169 && b === 254) || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168)
}

function internalIPv6(parts: number[]) {
  const head = parts[0]
  if (parts.every(part => part === 0)) return true
  if (parts.slice(0, 7).every(part => part === 0) && parts[7] === 1) return true
  if ((head & 0xffc0) === 0xfe80) return true
  if ((head & 0xfe00) === 0xfc00) return true
  return false
}

function expandIPv6(value: string): number[] | null {
  const raw = value.toLowerCase().split('%', 1)[0]
  if (!raw || raw.split('::').length > 2) return null
  const halves = raw.split('::')
  const parse = (text: string) => text ? text.split(':').map(part => /^[0-9a-f]{1,4}$/.test(part) ? Number.parseInt(part, 16) : NaN) : []
  const left = parse(halves[0])
  const right = parse(halves[1] || '')
  if (left.some(Number.isNaN) || right.some(Number.isNaN)) return null
  const missing = 8 - left.length - right.length
  if ((halves.length === 1 && missing !== 0) || (halves.length === 2 && missing < 1)) return null
  return [...left, ...Array(missing).fill(0), ...right]
}

export function ddnsPrefixFromAddress(value: string): string | null {
  const parsed = classifyDDNSAddress(value)
  if (!parsed) return null
  const [address, mask, ...extra] = value.trim().split('/')
  const width = parsed.family === 'ipv4' ? 32 : 128
  if (extra.length || (mask !== undefined && !/^\d+$/.test(mask))) return null
  const bits = mask === undefined ? width : Number(mask)
  if (bits > width) return null
  const unit = parsed.family === 'ipv4' ? 8 : 16
  const parts = parsed.family === 'ipv4' ? address.split('.').map(Number) : expandIPv6(address.replace(/^\[|\]$/g, ''))!
  const network = parts.map((part, index) => {
    const kept = Math.max(0, Math.min(unit, bits - index * unit))
    return Math.floor(part / 2 ** (unit - kept)) * 2 ** (unit - kept)
  })
  if (parsed.family === 'ipv4') return network.join('.') + '/' + bits
  let bestStart = -1
  let bestLength = 1
  for (let i = 0; i < network.length;) {
    if (network[i] !== 0) { i++; continue }
    let end = i
    while (end < network.length && network[end] === 0) end++
    if (end - i > bestLength) { bestStart = i; bestLength = end - i }
    i = end
  }
  const hex = network.map(part => part.toString(16))
  const prefix = bestStart < 0 ? hex.join(':') : hex.slice(0, bestStart).join(':') + '::' + hex.slice(bestStart + bestLength).join(':')
  return prefix + '/' + bits
}
