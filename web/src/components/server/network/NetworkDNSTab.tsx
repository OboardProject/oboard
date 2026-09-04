import React, { useEffect, useState } from 'react'
import { Select } from '../../ui/select'
import { Switch } from '../../ui/switch'
import { FormField } from '../../ui/form-field'
import { dnsPolicyErrorText } from '../../../dns-status'
import type { Server } from '../../proxy-path/types'

type DNSList = { id:number; name:string; kind:string; revision:number; candidates:any[]; enabled:boolean }
type ServerDNSPolicy = { server_id:number; encrypted_list_id:number; bootstrap_list_id:number; revision:number; strategy:string; auto_test:string; test_interval_seconds:number; encrypted_selected:any[]|null; bootstrap_selected:any[]|null; last_success_at?:string; last_error:string }
type DNSBenchmarkResult = { encrypted:any; bootstrap:any; status:string; error:string; created_at:string }

function labelValue(v:any){ const m:Record<string,string>={ auto:'跟随服务器', prefer_ipv4:'优先 IPv4', prefer_ipv6:'优先 IPv6', ipv4_only:'仅 IPv4', ipv6_only:'仅 IPv6'}; return m[String(v)]||String(v) }
function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }
function dnsTransportLabel(t:string){ const m:Record<string,string>={ udp:'UDP', tcp:'TCP', dot:'DoT', doh:'DoH', doq:'DoQ'}; return m[t]||t }

function isDNSPolicyStale(policy?: ServerDNSPolicy, lists: DNSList[]=[]){
  if(!policy) return false
  const p:any = policy
  const enc = lists.find(l=> l.id===p.encrypted_list_id)
  const boot = lists.find(l=> l.id===p.bootstrap_list_id)
  if(enc && enc.revision !== p.encrypted_selection_revision) return true
  if(boot && boot.revision !== p.bootstrap_selection_revision) return true
  return Boolean(p.needs_benchmark)
}
function dnsPolicyDraft(policy?: ServerDNSPolicy, lists: DNSList[]=[]){
  // pick first enabled lists as fallback
  const encFallback = lists.find(l=> l.kind==='encrypted' && l.enabled)?.id || lists.find(l=> l.kind==='encrypted')?.id || 0
  const bootFallback = lists.find(l=> l.kind==='bootstrap' && l.enabled)?.id || lists.find(l=> l.kind==='bootstrap')?.id || 0
  return {
    // 0 是「不使用加密解析」，不能回落到默认列表。
    encryptedListID: policy ? policy.encrypted_list_id : encFallback,
    bootstrapListID: policy?.bootstrap_list_id || bootFallback,
    strategy: policy?.strategy || 'auto',
    hourlyTest: policy?.auto_test==='periodic',
  }
}
function DNSGroupStatus({ title, selected, group }: { title:string; selected:any[]|null; group?: any }){
  const best = group?.best_tags?.join(' · ') || '—'
  return <div className="dns-group-status"><strong>{title}</strong><span>当前首选：{selected && selected.length ? selected.map((c:any)=>c.tag||c.server).join(' · ') : '—'}</span><span>测试推荐：{best}</span></div>
}

export function NetworkDNSTab({ server, policy, lists, benchmarks, client, notify, disabled, disabledReason }: { server: Server; policy?: ServerDNSPolicy; lists: DNSList[]; benchmarks: DNSBenchmarkResult[]; client:any; notify?:(m:string,t?:string)=>void; disabled?:boolean; disabledReason?:string }) {
  const [draft, setDraft]=useState(()=> dnsPolicyDraft(policy, lists))
  const [working, setWorking]=useState('')
  const latest = benchmarks[0]
  const encryptedList = lists.find(l=> l.id===draft.encryptedListID)
  const bootstrapList = lists.find(l=> l.id===draft.bootstrapListID)
  const stale = isDNSPolicyStale(policy as any, lists)

  useEffect(()=> setDraft(dnsPolicyDraft(policy, lists)), [policy?.revision, lists.length])

  const save = async()=>{
    const response = await client.request(`/servers/${server.id}/dns-policy`, { method:'PUT', body: JSON.stringify({
      encrypted_list_id: draft.encryptedListID,
      bootstrap_list_id: draft.bootstrapListID,
      strategy: draft.strategy,
      auto_test: draft.hourlyTest ? 'periodic':'first_apply',
      test_interval_seconds: 3600,
    })})
    return response.dns_policy as ServerDNSPolicy
  }
  const run = async(action:'save'|'test'|'test_and_apply')=>{
    if(working || disabled) return
    setWorking(action)
    try{
      await save()
      if(action!=='save'){
        const result = await client.request(`/servers/${server.id}/dns-test`, { method:'POST', body: JSON.stringify({ action })})
        if(result.task?.status==='failed'){
          const f = JSON.parse(result.task.result_json||'{}')
          throw new Error(f?.error||f?.message||'暂时无法测试解析服务')
        }
        notify?.(action==='test' ? '解析测试已开始':'解析测试已开始，成功后会自动应用配置','success')
      } else {
        notify?.('服务器 DNS 策略已保存','success')
      }
    } catch(e:any){ notify?.(e?.message||String(e),'error') } finally{ setWorking('') }
  }

  return (
    <div className="server-dns-tab">
      <div className="dns-status-strip">
        <span><strong>{stale ? '等待重新测试' : policy?.last_success_at ? '正常' : '等待首次测试'}</strong><small>{policy?.last_success_at ? `最后成功 ${formatTableTime(policy.last_success_at)}` : '保存后会按列表顺序使用'}</small></span>
        <span><strong>{draft.hourlyTest ? '每小时自动测试' : '关闭自动测试'}</strong><small>自动测试只更新测试结果，不会修改配置</small></span>
        <span className={policy?.last_error ? 'has-error':''}><strong>{policy?.last_error ? '最近一次测试失败' : latest?.status==='stale' ? '测试结果已过期':'无异常'}</strong><small>{dnsPolicyErrorText(policy?.last_error, policy) || dnsPolicyErrorText(latest?.error, policy) || '—'}</small></span>
      </div>
      {stale && <div className="access-note warning"><strong>解析服务列表已更新，需要重新测试</strong><span>旧的测试结果已停止使用。</span></div>}
      <div className="dns-group-grid">{policy?.encrypted_list_id ? <DNSGroupStatus title="加密解析" selected={policy?.encrypted_selected||[]} group={latest?.encrypted} /> : <div className="dns-group-status"><strong>加密解析</strong><span>未启用</span><span>仅使用普通解析</span></div>}<DNSGroupStatus title="基础解析" selected={policy?.bootstrap_selected||[]} group={latest?.bootstrap} /></div>
      <div className="form dns-settings-form labeled-form">
        <FormField label="加密解析服务列表" full>
          <Select value={draft.encryptedListID} onChange={e=> setDraft({...draft, encryptedListID:Number(e.target.value)})} disabled={disabled}>
            <option value={0}>不使用加密解析（仅普通解析）</option>
            {lists.filter(l=> l.kind==='encrypted' && (l.enabled || l.id===policy?.encrypted_list_id)).map(l=> <option key={l.id} value={l.id}>{l.name} · {l.candidates.length} 项</option>)}
          </Select>
          {draft.encryptedListID===0 && <small className="muted">服务器将只用普通解析查询域名，查询内容在链路上不加密。</small>}
        </FormField>
        <FormField label="基础解析服务列表" full>
          <Select value={draft.bootstrapListID} onChange={e=> setDraft({...draft, bootstrapListID:Number(e.target.value)})} disabled={disabled}>
            {lists.filter(l=> l.kind==='bootstrap' && (l.enabled || l.id===policy?.bootstrap_list_id)).map(l=> <option key={l.id} value={l.id}>{l.name} · {l.candidates.length} 项</option>)}
          </Select>
        </FormField>
        <FormField label="IP 类型">
          <Select value={draft.strategy} onChange={e=> setDraft({...draft, strategy: e.target.value})} disabled={disabled}>
            <option value="auto">跟随服务器</option>
            <option value="prefer_ipv4">优先 IPv4</option>
            <option value="prefer_ipv6">优先 IPv6</option>
            <option value="ipv4_only">仅 IPv4</option>
            <option value="ipv6_only">仅 IPv6</option>
          </Select>
        </FormField>
        <FormField label="每小时自动测试">
          <Switch checked={draft.hourlyTest} onChange={checked=> setDraft({...draft, hourlyTest: checked})} ariaLabel="每小时自动测试" />
        </FormField>
        <div className="dns-list-preview"><span>{encryptedList?.candidates.map((c:any)=> dnsTransportLabel(c.transport)).join(' · ')}</span><span>{bootstrapList?.candidates.map((c:any)=> dnsTransportLabel(c.transport)).join(' · ')}</span></div>
        {disabled && <small className="muted">{disabledReason}</small>}
      </div>
      <div className="server-workspace-actions">
        <button className="ghost" disabled={Boolean(working) || disabled} onClick={()=> void run('save')}>{working==='save'? '保存中...':'仅保存'}</button>
        <button className="ghost" disabled={Boolean(working) || disabled} onClick={()=> void run('test')}>{working==='test'? '测试中...':'重新测试'}</button>
        <button disabled={Boolean(working) || disabled} onClick={()=> void run('test_and_apply')}>{working==='test_and_apply'? '测试中...':'测试并应用'}</button>
      </div>
    </div>
  )
}
