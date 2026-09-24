import React from 'react'
import { Select } from '../../ui/select'
import { FormField } from '../../ui/form-field'
import { customDNSCandidatesText, parseCustomDNSCandidates, type DNSCandidate, type DNSListKind } from '../../../dns-candidate'

// CUSTOM_DNS_LIST marks a resolver group that uses this server's own
// resolvers instead of a shared list.
export const CUSTOM_DNS_LIST = -1

type ResolverList = { id: number; name: string; kind: string; candidates: unknown[]; enabled: boolean; protected?: boolean; owner_server_id?: number }
type ResolverPolicy = {
  encrypted_list_id: number
  bootstrap_list_id: number
  encrypted_source?: string
  bootstrap_source?: string
  encrypted_candidates?: DNSCandidate[] | null
  bootstrap_candidates?: DNSCandidate[] | null
  strategy?: string
  auto_test?: string
}

export type DNSResolverDraft = {
  encryptedListID: number
  bootstrapListID: number
  encryptedCustom: string
  bootstrapCustom: string
  strategy: string
  hourlyTest: boolean
}

export function isSharedDNSList(list: ResolverList) {
  return !list.owner_server_id
}

export function dnsResolverDraft(policy: ResolverPolicy | undefined, lists: readonly ResolverList[]): DNSResolverDraft {
  const fallbackListID = (kind: DNSListKind) => {
    const shared = lists.filter(list => list.kind === kind && list.enabled && isSharedDNSList(list))
    return shared.find(list => list.protected)?.id || shared[0]?.id || 0
  }
  const encryptedCustom = policy?.encrypted_source === 'custom'
  const bootstrapCustom = policy?.bootstrap_source === 'custom'
  return {
    // 0 是「不使用加密解析」，已保存的策略不能回落到默认列表。
    encryptedListID: encryptedCustom ? CUSTOM_DNS_LIST : policy ? Number(policy.encrypted_list_id) : fallbackListID('encrypted'),
    bootstrapListID: bootstrapCustom ? CUSTOM_DNS_LIST : Number(policy?.bootstrap_list_id || fallbackListID('bootstrap')),
    encryptedCustom: encryptedCustom ? customDNSCandidatesText(policy?.encrypted_candidates) : '',
    bootstrapCustom: bootstrapCustom ? customDNSCandidatesText(policy?.bootstrap_candidates) : '',
    strategy: policy?.strategy || 'auto',
    hourlyTest: policy?.auto_test === 'periodic',
  }
}

// dnsResolverPayload builds the PUT body. A custom group sends its resolvers
// with list id 0; the Controller keeps them as this server's own list.
export function dnsResolverPayload(draft: DNSResolverDraft) {
  const encryptedCustom = draft.encryptedListID === CUSTOM_DNS_LIST
  const bootstrapCustom = draft.bootstrapListID === CUSTOM_DNS_LIST
  return {
    encrypted_list_id: encryptedCustom ? 0 : draft.encryptedListID,
    bootstrap_list_id: bootstrapCustom ? 0 : draft.bootstrapListID,
    ...(encryptedCustom ? { encrypted_candidates: parseCustomDNSCandidates(draft.encryptedCustom, 'encrypted') } : {}),
    ...(bootstrapCustom ? { bootstrap_candidates: parseCustomDNSCandidates(draft.bootstrapCustom, 'bootstrap') } : {}),
    strategy: draft.strategy,
    auto_test: draft.hourlyTest ? 'periodic' : 'first_apply',
    test_interval_seconds: 3600,
  }
}

export function DNSResolverFields({ draft, setDraft, lists, policy, disabled }: {
  draft: DNSResolverDraft
  setDraft: (next: DNSResolverDraft) => void
  lists: readonly ResolverList[]
  policy?: ResolverPolicy
  disabled?: boolean
}) {
  const options = (kind: DNSListKind, currentID?: number) => lists
    .filter(list => list.kind === kind && isSharedDNSList(list) && (list.enabled || list.id === currentID))
    .map(list => <option key={list.id} value={list.id}>{list.name} · {list.candidates.length} 项</option>)
  return <>
    <FormField label="加密解析服务" full>
      <Select value={draft.encryptedListID} onChange={event => setDraft({ ...draft, encryptedListID: Number(event.target.value) })} disabled={disabled}>
        <option value={0}>不使用加密解析（仅普通解析）</option>
        {options('encrypted', policy?.encrypted_source === 'shared' ? policy.encrypted_list_id : undefined)}
        <option value={CUSTOM_DNS_LIST}>自定义（仅此服务器）</option>
      </Select>
      {draft.encryptedListID === 0 && <small className="muted">服务器将只用普通解析查询域名，查询内容在链路上不加密。</small>}
      {draft.encryptedListID === CUSTOM_DNS_LIST && <>
        <textarea rows={3} value={draft.encryptedCustom} onChange={event => setDraft({ ...draft, encryptedCustom: event.target.value })} disabled={disabled} placeholder={'https://dns.example.com/dns-query\ntls://1.1.1.1'} aria-label="自定义加密解析服务" spellCheck={false} />
        <small className="muted">每行一个，支持 DoH（https://）、DoT（tls://）和 DoQ（quic://），可用内网地址，按顺序使用，最多 32 个。</small>
      </>}
    </FormField>
    <FormField label="基础解析服务" full>
      <Select value={draft.bootstrapListID} onChange={event => setDraft({ ...draft, bootstrapListID: Number(event.target.value) })} disabled={disabled}>
        {options('bootstrap', policy?.bootstrap_source === 'shared' ? policy.bootstrap_list_id : undefined)}
        <option value={CUSTOM_DNS_LIST}>自定义（仅此服务器）</option>
      </Select>
      {draft.bootstrapListID === CUSTOM_DNS_LIST && <>
        <textarea rows={3} value={draft.bootstrapCustom} onChange={event => setDraft({ ...draft, bootstrapCustom: event.target.value })} disabled={disabled} placeholder={'223.5.5.5\ntcp://8.8.8.8'} aria-label="自定义基础解析服务" spellCheck={false} />
        <small className="muted">每行一个 IP，可填内网或本机地址，默认 UDP 53，可写 tcp:// 或自定义端口，按顺序使用，最多 32 个。</small>
      </>}
    </FormField>
  </>
}
