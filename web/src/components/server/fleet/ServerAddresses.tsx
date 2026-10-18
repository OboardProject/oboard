import { useEffect, useRef, useState } from 'react'
import { Check } from 'lucide-react'
import type { Server } from '../../proxy-path/types'

export async function copyText(value: string) {
  const text = String(value || '')
  if (!text) return false
  try {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Plain-HTTP panels and denied clipboard permissions fall back to a textarea.
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  textarea.select()
  try {
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(textarea)
  }
}

export function serverCustomEntry(server: Pick<Server, 'entry_ip_mode' | 'entry_address'>) {
  if (String(server.entry_ip_mode || 'auto') !== 'custom') return ''
  return String(server.entry_address || '').trim()
}

export function ipv6DisplayParts(value: string) {
  const text = String(value || '').trim()
  const last = text.lastIndexOf(':')
  const prev = last > 0 ? text.lastIndexOf(':', last - 1) : -1
  if (prev <= 0) return { head: text, tail: '' }
  return { head: text.slice(0, prev), tail: text.slice(prev) }
}

type Kind = 'v4' | 'v6' | 'entry'

function AddressChip({ kind, value, copied, onCopy }: { kind: Kind; value: string; copied: boolean; onCopy: () => void }) {
  const label = kind === 'v4' ? '出口 IPv4' : kind === 'v6' ? '出口 IPv6' : '入口'
  const parts = kind === 'v6' ? ipv6DisplayParts(value) : null
  return (
    <button
      type="button"
      className={`fleet-address${copied ? ' is-copied' : ''}`}
      data-kind={kind}
      aria-label={`复制${label} ${value}`}
      title={copied ? '已复制' : `复制${label}`}
      onClick={event => { event.stopPropagation(); onCopy() }}
    >
      {kind === 'entry' ? <span className="fleet-address-kicker">入口</span> : null}
      {parts?.tail
        ? <span className="fleet-address-value is-middle"><span className="fleet-address-head">{parts.head}</span><span>{parts.tail}</span></span>
        : <span className="fleet-address-value">{value}</span>}
      {copied ? <Check size={12} aria-hidden="true" className="fleet-address-check" /> : null}
    </button>
  )
}

// Egress addresses and a custom entry address, each copyable in place without
// opening whatever surface the line sits in.
export function ServerAddresses({ server, stacked = false }: { server: Server; stacked?: boolean }) {
  const v4 = String(server.public_ipv4 || '').trim()
  const v6 = String(server.public_ipv6 || '').trim()
  const entry = serverCustomEntry(server)
  const [copied, setCopied] = useState<Kind | null>(null)
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const copy = async (kind: Kind, value: string) => {
    if (!(await copyText(value))) return
    window.clearTimeout(timer.current)
    setCopied(kind)
    timer.current = window.setTimeout(() => setCopied(null), 1200)
  }
  return (
    <div className={`fleet-addresses${stacked ? ' is-stacked' : ''}`}>
      {v4 ? <AddressChip kind="v4" value={v4} copied={copied === 'v4'} onCopy={() => void copy('v4', v4)} /> : null}
      {v6 ? <AddressChip kind="v6" value={v6} copied={copied === 'v6'} onCopy={() => void copy('v6', v6)} /> : null}
      {!v4 && !v6 ? <span className="fleet-address-empty">出口待检测</span> : null}
      {entry ? <AddressChip kind="entry" value={entry} copied={copied === 'entry'} onCopy={() => void copy('entry', entry)} /> : null}
    </div>
  )
}
