import React, { useId, useState } from 'react'
import { X } from 'lucide-react'

import { FormField } from './ui/form-field'
import { MotionDialogPanel } from './ui/motion'
import { Select } from './ui/select'
import { Switch } from './ui/switch'

export type DNSRecordDraft = {
  type: string
  name: string
  content: string
  comment: string
  ttl: number
  proxied: boolean
}

type DNSRecordSource = Omit<DNSRecordDraft, 'comment' | 'proxied'> & {
  comment?: string
  proxied?: boolean
}

type DNSRecordZoneOption = {
  credential: { name: string; provider: string }
  zone: { id: number; zone_name: string; server_id?: number }
}

export function emptyDNSRecordDraft(): DNSRecordDraft {
  return { type: 'A', name: '', content: '', comment: '', ttl: 300, proxied: false }
}

export function dnsRecordDraftFromRecord(record: DNSRecordSource): DNSRecordDraft {
  const proxied = Boolean(record.proxied)
  return {
    type: record.type.trim().toUpperCase(),
    name: record.name,
    content: record.content,
    comment: record.comment || '',
    ttl: proxied ? 1 : (record.ttl > 0 ? record.ttl : 300),
    proxied,
  }
}

export function dnsRecordPayload<T extends { enabled?: boolean }>(draft: DNSRecordDraft, existing?: T) {
  return {
    ...(existing || {} as T),
    ...draft,
    type: (draft.type || 'A').trim().toUpperCase(),
    enabled: existing?.enabled ?? true,
  }
}

export function DNSRecordDialog({ zoneOptions, zoneID, setZoneID, draft, setDraft, serverName, editing, saving, onCancel, onSubmit }: {
  zoneOptions: DNSRecordZoneOption[]
  zoneID: number
  setZoneID: (zoneID: number) => void
  draft: DNSRecordDraft
  setDraft: React.Dispatch<React.SetStateAction<DNSRecordDraft>>
  serverName: (serverID?: number) => string
  editing: boolean
  saving: boolean
  onCancel: () => void
  onSubmit: () => Promise<void>
}) {
  const formID = useId()
  const selectedOption = zoneOptions.find(item => item.zone.id === zoneID)
  const selectedZoneName = selectedOption?.zone.zone_name?.trim() || ''
  const update = (patch: Partial<DNSRecordDraft>) => setDraft(current => ({ ...current, ...patch }))
  const [hostPrefix, setHostPrefix] = useState(() => {
    if (!draft.name || !selectedZoneName) return draft.name || ''
    if (draft.name === selectedZoneName || draft.name === '@') return '@'
    if (draft.name.endsWith(`.${selectedZoneName}`)) return draft.name.slice(0, -(selectedZoneName.length + 1))
    return draft.name
  })

  const formatOptionLabel = (option: DNSRecordZoneOption) => {
    const accountName = option.credential.name?.trim()
    const zoneName = option.zone.zone_name?.trim()
    const namePart = accountName && accountName !== zoneName ? `${zoneName} (${accountName})` : zoneName
    const serverPart = option.zone.server_id ? ` · ${serverName(option.zone.server_id)}` : ''
    return `${namePart}${serverPart}`
  }

  const handlePrefixChange = (rawPrefix: string) => {
    let prefix = rawPrefix.trim()
    if (selectedZoneName && prefix.endsWith(`.${selectedZoneName}`)) {
      prefix = prefix.slice(0, -(selectedZoneName.length + 1))
    } else if (selectedZoneName && prefix === selectedZoneName) {
      prefix = '@'
    }
    setHostPrefix(prefix)
    update({ name: prefix === '@' || !prefix ? selectedZoneName : `${prefix}.${selectedZoneName}` })
  }

  const handleZoneChange = (nextZoneID: number) => {
    setZoneID(nextZoneID)
    const nextZoneName = zoneOptions.find(item => item.zone.id === nextZoneID)?.zone.zone_name?.trim() || ''
    update({ name: hostPrefix === '@' || !hostPrefix ? nextZoneName : `${hostPrefix}.${nextZoneName}` })
  }

  const cloudflare = selectedOption?.credential.provider === 'cloudflare'
  const canSubmit = Boolean(zoneID && hostPrefix.trim() && draft.content.trim())
  const submitHint = canSubmit ? undefined : '请填写域名、主机记录和记录值'

  return <MotionDialogPanel onCancel={onCancel} className="dns-record-dialog" ariaLabel={editing ? '编辑解析记录' : '添加解析记录'}>
    <header className="dialog-head"><div><h2>{editing ? '编辑解析记录' : '添加解析记录'}</h2><p className="muted">{editing ? `所属域名：${selectedZoneName}` : '为指定域名创建一条子域名解析。'}</p></div><button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><X aria-hidden="true" /></button></header>
    <div className="dialog-body">
      <form id={formID} className="form server-dialog-form labeled-form" onSubmit={event => { event.preventDefault(); if (canSubmit) void onSubmit() }}>
        <FormField label="域名" required><Select aria-label="域名" required disabled={editing} value={zoneID} onChange={event => handleZoneChange(Number(event.target.value))}><option value={0}>选择域名</option>{zoneOptions.map(option => <option key={option.zone.id} value={option.zone.id}>{formatOptionLabel(option)}</option>)}</Select></FormField>
        <FormField label="记录类型" required><Select aria-label="记录类型" required value={draft.type || 'A'} onChange={event => update({ type: event.target.value, proxied: event.target.value === 'TXT' ? false : draft.proxied })}>{['A', 'AAAA', 'CNAME', 'TXT'].map(type => <option key={type} value={type}>{type}</option>)}</Select></FormField>
        <FormField label="主机记录" required hint="支持填写子域名前缀（例如 hkp 或 *）；如需解析主域名请填写 @。">
          <div className="dns-domain-input">
            <input aria-label="主机记录" required value={hostPrefix} onChange={event => handlePrefixChange(event.target.value)} placeholder="例如：hkp 或 @" autoCapitalize="none" autoCorrect="off" spellCheck={false} disabled={!selectedZoneName} />
            <span className="dns-domain-suffix">{selectedZoneName ? `.${selectedZoneName}` : '请先选择域名'}</span>
          </div>
        </FormField>
        <FormField label="记录值" required><input aria-label="记录值" required value={draft.content} onChange={event => update({ content: event.target.value })} autoCapitalize="none" autoCorrect="off" spellCheck={false} placeholder={draft.type === 'AAAA' ? '例如：2001:db8::1' : draft.type === 'CNAME' ? '例如：target.example.com' : draft.type === 'TXT' ? '例如：v=spf1...' : '例如：1.2.3.4'} /></FormField>
        <FormField label="TTL" required hint={draft.proxied ? 'Cloudflare 代理开启时使用自动 TTL。' : '记录在解析缓存中的有效时间，单位为秒。'}><input aria-label="TTL" required type="number" min={1} step={1} disabled={draft.proxied} value={draft.ttl} onChange={event => update({ ttl: Math.max(1, Number(event.target.value) || 1) })} /></FormField>
        <FormField label="解析备注" hint="备注属于这条子域名解析；自动创建时会写入入口和服务器信息。"><input aria-label="解析备注" value={draft.comment} maxLength={100} onChange={event => update({ comment: event.target.value })} placeholder="例如：东京入口" /></FormField>
        {cloudflare && <div className="switch-form-row"><span className="switch-form-label">Cloudflare 代理</span><Switch checked={draft.proxied} disabled={draft.type === 'TXT'} onChange={checked => update({ proxied: checked, ttl: checked ? 1 : draft.ttl })} ariaLabel="Cloudflare 代理" /></div>}
      </form>
    </div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={onCancel}>取消</button><button type="submit" form={formID} disabled={saving || !canSubmit} title={submitHint}>{saving ? (editing ? '保存中...' : '创建中...') : (editing ? '保存修改' : '添加记录')}</button></footer>
  </MotionDialogPanel>
}
