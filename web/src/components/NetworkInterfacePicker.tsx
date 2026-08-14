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
  pollIntervalMS?: number
  maxPollAttempts?: number
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

export function NetworkInterfacePicker({
  serverID,
  value,
  onChange,
  client,
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
  }, [serverID])

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
    setStatus(next.length ? `已读取 ${next.length} 个网卡` : 'Agent 未返回网卡')
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

  const selectedValue = interfaces.some(iface => iface.name === value) ? value : ''
  const options = interfaces.map(iface => {
    const tooltip = interfaceTooltip(iface)
    return {
      value: iface.name,
      label: <span className="network-interface-option" title={tooltip}>
        <span><strong>{iface.name}</strong><small>{interfaceStateLabel(iface)}</small></span>
        <small title={tooltip}>{interfaceAddressLabel(iface.addresses)}</small>
      </span>,
    }
  })

  return <div className="network-interface-picker">
    <div className="network-interface-input">
      <input value={value} onChange={event => onChange(event.target.value)} placeholder="eth1" autoComplete="off" />
      <button
        type="button"
        className="ghost icon-button"
        onClick={() => void readInterfaces()}
        disabled={!serverID || loading}
        aria-label="读取网卡"
        title="读取网卡"
      >
        <RefreshCw size={15} className={loading ? 'spin' : ''} />
      </button>
    </div>
    {status ? <small className={interfaces.length ? 'network-interface-status' : 'network-interface-status error-text'} role={interfaces.length ? 'status' : 'alert'}>{status}</small> : null}
    {interfaces.length ? <CustomSelect
      value={selectedValue}
      onChange={onChange}
      options={options}
      placeholder="从 Agent 返回的网卡中选择"
      ariaLabel="选择 Agent 网卡"
      className="network-interface-select"
    /> : null}
  </div>
}
