import React, { useEffect, useRef, useState } from 'react'
import { RefreshCw } from 'lucide-react'

import { CustomSelect } from './ui/CustomSelect'

export type NetworkInterfaceInfo = {
  name: string
  up: boolean
  running: boolean
  loopback: boolean
  addresses: string[]
}

type TaskSnapshot = {
  id?: number
  status?: string
  result_json?: string
}

export type NetworkInterfacePickerClient = {
  request: (path: string, init?: RequestInit) => Promise<any>
}

export type NetworkInterfacePickerProps = {
  serverID: number
  value: string
  onChange: (value: string) => void
  client: NetworkInterfacePickerClient
  mode?: 'interface' | 'source-prefix'
  pollIntervalMS?: number
  maxPollAttempts?: number
}

type ParsedAddress = {
  bits: 32 | 128
  value: bigint
  prefixLength: number
}

type SourcePrefixCandidate = {
  id: string
  interfaceName: string
  address: string
  prefix: string
  family: 'IPv4' | 'IPv6'
}

const terminalTaskStatuses = new Set(['succeeded', 'failed', 'rollback_failed'])

function taskResult(task: TaskSnapshot) {
  try {
    return JSON.parse(String(task.result_json || '{}'))
  } catch {
    return {}
  }
}

function interfaceStateLabel(iface: NetworkInterfaceInfo) {
  const states = [iface.up ? '已启用' : '未启用']
  if (iface.running) states.push('运行中')
  if (iface.loopback) states.push('回环')
  return states.join(' · ')
}

function interfaceAddressLabel(addresses: string[]) {
  if (!addresses.length) return '无 IP 地址'
  return addresses.join(' · ')
}

function interfaceTooltip(iface: NetworkInterfaceInfo) {
  if (!iface.addresses.length) return '无 IP 地址'
  return iface.addresses.join('\n')
}

function normalizeInterfaces(value: unknown): NetworkInterfaceInfo[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is NetworkInterfaceInfo => Boolean(
    item && typeof item === 'object' && typeof item.name === 'string' && Array.isArray(item.addresses),
  ))
}

function parseIPv4(value: string): bigint | null {
  const octets = value.split('.')
  if (octets.length !== 4) return null
  let result = 0n
  for (const octet of octets) {
    if (!/^\d{1,3}$/.test(octet)) return null
    const part = Number(octet)
    if (part > 255) return null
    result = (result << 8n) | BigInt(part)
  }
  return result
}

function parseIPv6(value: string): bigint | null {
  const normalized = value.toLowerCase().split('%', 1)[0]
  if (!normalized || normalized.split('::').length > 2) return null

  const expandIPv4Tail = (parts: string[]) => {
    if (!parts.length || !parts[parts.length - 1].includes('.')) return parts
    const ipv4 = parseIPv4(parts[parts.length - 1])
    if (ipv4 == null) return null
    return [...parts.slice(0, -1), (ipv4 >> 16n).toString(16), (ipv4 & 0xffffn).toString(16)]
  }

  const halves = normalized.split('::')
  const left = expandIPv4Tail(halves[0] ? halves[0].split(':') : [])
  const right = expandIPv4Tail(halves.length === 2 && halves[1] ? halves[1].split(':') : [])
  if (!left || !right) return null
  const missing = 8 - left.length - right.length
  if ((halves.length === 1 && missing !== 0) || (halves.length === 2 && missing < 1)) return null
  const parts = [...left, ...Array(missing).fill('0'), ...right]
  if (parts.length !== 8 || parts.some(part => !/^[0-9a-f]{1,4}$/.test(part))) return null
  return parts.reduce((result, part) => (result << 16n) | BigInt(`0x${part}`), 0n)
}

function formatIPv4(value: bigint) {
  return [24n, 16n, 8n, 0n].map(shift => Number((value >> shift) & 0xffn)).join('.')
}

function formatIPv6(value: bigint) {
  const parts = Array.from({ length: 8 }, (_, index) => Number((value >> BigInt((7 - index) * 16)) & 0xffffn).toString(16))
  let bestStart = -1
  let bestLength = 0
  for (let start = 0; start < parts.length;) {
    if (parts[start] !== '0') {
      start += 1
      continue
    }
    let end = start
    while (end < parts.length && parts[end] === '0') end += 1
    if (end - start > bestLength) {
      bestStart = start
      bestLength = end - start
    }
    start = end
  }
  if (bestLength < 2) return parts.join(':')
  const before = parts.slice(0, bestStart).join(':')
  const after = parts.slice(bestStart + bestLength).join(':')
  return `${before}::${after}`
}

function parseCIDR(value: string): ParsedAddress | null {
  const slash = value.lastIndexOf('/')
  if (slash <= 0 || slash === value.length - 1) return null
  const address = value.slice(0, slash)
  const rawPrefix = value.slice(slash + 1)
  if (!/^\d{1,3}$/.test(rawPrefix)) return null
  const prefixLength = Number(rawPrefix)
  if (address.includes(':')) {
    const parsed = parseIPv6(address)
    return parsed != null && prefixLength <= 128 ? { bits: 128, value: parsed, prefixLength } : null
  }
  const parsed = parseIPv4(address)
  return parsed != null && prefixLength <= 32 ? { bits: 32, value: parsed, prefixLength } : null
}

function isLocalOnlyAddress(parsed: ParsedAddress) {
  if (parsed.bits === 32) {
    return parsed.value === 0n
      || (parsed.value >> 24n) === 127n
      || (parsed.value >> 16n) === 0xa9fen
  }
  return parsed.value === 0n
    || parsed.value === 1n
    || (parsed.value >> 118n) === 0x3fan
}

export function sourcePrefixSuggestion(value: string): string | null {
  const parsed = parseCIDR(value.trim())
  if (!parsed || isLocalOnlyAddress(parsed)) return null
  const prefixLength = parsed.bits === 128 && parsed.prefixLength === 128 ? 64 : parsed.prefixLength
  const hostBits = BigInt(parsed.bits - prefixLength)
  const network = hostBits ? (parsed.value >> hostBits) << hostBits : parsed.value
  const address = parsed.bits === 32 ? formatIPv4(network) : formatIPv6(network)
  return `${address}/${prefixLength}`
}

function sourcePrefixCandidates(interfaces: NetworkInterfaceInfo[]): SourcePrefixCandidate[] {
  return interfaces.flatMap(iface => {
    if (iface.loopback) return []
    return iface.addresses.flatMap(address => {
      const prefix = sourcePrefixSuggestion(address)
      if (!prefix) return []
      return [{
        id: `${iface.name}\u001f${address}`,
        interfaceName: iface.name,
        address,
        prefix,
        family: address.includes(':') ? 'IPv6' : 'IPv4',
      } satisfies SourcePrefixCandidate]
    })
  })
}

export function NetworkInterfacePicker({
  serverID,
  value,
  onChange,
  client,
  mode = 'interface',
  pollIntervalMS = 1000,
  maxPollAttempts = 30,
}: NetworkInterfacePickerProps) {
  const [loading, setLoading] = useState(false)
  const [interfaces, setInterfaces] = useState<NetworkInterfaceInfo[]>([])
  const [status, setStatus] = useState('')
  const requestGeneration = useRef(0)

  useEffect(() => {
    requestGeneration.current += 1
    setLoading(false)
    setInterfaces([])
    setStatus('')
  }, [mode, serverID])

  useEffect(() => () => {
    requestGeneration.current += 1
  }, [])

  const applyTerminalTask = (task: TaskSnapshot) => {
    const result = taskResult(task)
    if (task.status !== 'succeeded') {
      throw new Error(String(result?.error || result?.message || 'Agent 读取网卡失败'))
    }
    const next = normalizeInterfaces(result?.interfaces)
    setInterfaces(next)
    if (mode === 'source-prefix') {
      const candidates = sourcePrefixCandidates(next)
      setStatus(candidates.length ? `已读取 ${candidates.length} 个可用 IP` : 'Agent 未返回可用 IP')
    } else {
      setStatus(next.length ? `已读取 ${next.length} 个网卡` : 'Agent 未返回网卡')
    }
  }

  const readInterfaces = async () => {
    if (!serverID || loading) return
    const generation = ++requestGeneration.current
    setLoading(true)
    setInterfaces([])
    setStatus('')
    try {
      const created = await client.request(`/servers/${serverID}/network-interfaces`, { method: 'POST', body: '{}' })
      let task = (created?.task || {}) as TaskSnapshot
      if (!task.id) throw new Error('主控未返回网卡读取任务')
      for (let attempt = 0; attempt <= maxPollAttempts; attempt += 1) {
        if (generation !== requestGeneration.current) return
        if (terminalTaskStatuses.has(String(task.status || ''))) {
          applyTerminalTask(task)
          return
        }
        if (attempt === maxPollAttempts) throw new Error('读取网卡超时，请重试或手工填写')
        await new Promise(resolve => window.setTimeout(resolve, pollIntervalMS))
        if (generation !== requestGeneration.current) return
        const response = await client.request(`/agent-tasks/${task.id}`)
        task = (response?.task || {}) as TaskSnapshot
      }
    } catch (error) {
      if (generation === requestGeneration.current) {
        setStatus(error instanceof Error ? error.message : String(error))
      }
    } finally {
      if (generation === requestGeneration.current) setLoading(false)
    }
  }

  const interfaceOptions = interfaces.map(iface => {
    const tooltip = interfaceTooltip(iface)
    return {
      value: iface.name,
      label: <span className="network-interface-option" title={tooltip}>
        <span><strong>{iface.name}</strong><small>{interfaceStateLabel(iface)}</small></span>
        <small title={tooltip}>{interfaceAddressLabel(iface.addresses)}</small>
      </span>,
    }
  })
  const prefixCandidates = sourcePrefixCandidates(interfaces)
  const prefixOptions = prefixCandidates.map(candidate => {
    const detail = `${candidate.address}\n${candidate.prefix}`
    return {
      value: candidate.id,
      label: <span className="network-interface-option network-prefix-option" title={detail}>
        <span><strong>{candidate.interfaceName}</strong><small>{candidate.family} · {candidate.prefix.slice(candidate.prefix.lastIndexOf('/'))}</small></span>
        <small title={candidate.address}>{candidate.address}</small>
        <small className="network-prefix-result" title={candidate.prefix}>{candidate.prefix}</small>
      </span>,
    }
  })
  const isPrefixMode = mode === 'source-prefix'
  const selectedValue = isPrefixMode
    ? prefixCandidates.find(candidate => candidate.prefix === value)?.id || ''
    : interfaces.some(iface => iface.name === value) ? value : ''
  const options = isPrefixMode ? prefixOptions : interfaceOptions
  const handleSelect = (selected: string) => {
    if (!isPrefixMode) {
      onChange(selected)
      return
    }
    const candidate = prefixCandidates.find(item => item.id === selected)
    if (candidate) onChange(candidate.prefix)
  }

  return <div className="network-interface-picker">
    <div className="network-interface-input">
      <input
        value={value}
        onChange={event => onChange(event.target.value)}
        placeholder={isPrefixMode ? '2001:b011:b000:8e73::/64' : 'eth1'}
        autoComplete="off"
        inputMode={isPrefixMode ? 'text' : undefined}
        autoCapitalize={isPrefixMode ? 'none' : undefined}
        spellCheck={isPrefixMode ? false : undefined}
        aria-label={isPrefixMode ? '源地址前缀' : '出口网卡'}
      />
      <button
        type="button"
        className="ghost icon-button"
        onClick={() => void readInterfaces()}
        disabled={!serverID || loading}
        aria-label={isPrefixMode ? '读取网卡 IP' : '读取网卡'}
        title={isPrefixMode ? '读取网卡 IP' : '读取网卡'}
      >
        <RefreshCw size={15} className={loading ? 'spin' : ''} />
      </button>
    </div>
    {status ? <small className={options.length ? 'network-interface-status' : 'network-interface-status error-text'} role={options.length ? 'status' : 'alert'}>{status}</small> : null}
    {options.length ? <CustomSelect
      value={selectedValue}
      onChange={handleSelect}
      options={options}
      placeholder={isPrefixMode ? '从 Agent 返回的 IP 中选择' : '从 Agent 返回的网卡中选择'}
      ariaLabel={isPrefixMode ? '选择 Agent 网卡 IP' : '选择 Agent 网卡'}
      className="network-interface-select"
    /> : null}
  </div>
}
