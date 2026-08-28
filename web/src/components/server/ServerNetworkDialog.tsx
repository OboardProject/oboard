import React, { useState } from 'react'
import { ServerWorkspaceDialog } from './shared/ServerWorkspaceDialog'
import { NetworkOverviewTab } from './network/NetworkOverviewTab'
import { NetworkTrafficTab } from './network/NetworkTrafficTab'
import { NetworkSettingsTab } from './network/NetworkSettingsTab'
import { NetworkDNSTab } from './network/NetworkDNSTab'
import { NetworkMTUTab } from './network/NetworkMTUTab'
import { NetworkDiagnosticsTab } from './network/NetworkDiagnosticsTab'
import type { Server } from '../proxy-path/types'

type Tab = 'overview'|'traffic'|'settings'|'dns'|'mtu'|'diagnostics'

export function ServerNetworkDialog({ server, initialTab='overview', data, client, notify, onClose, onUpdated, role='viewer' }: { server: Server; initialTab?: Tab; data:any; client:any; notify?:(m:string,t?:string)=>void; onClose:()=>void; onUpdated?:()=>void; role?:string }) {
  const [tab, setTab]=useState<Tab>(initialTab)
  const enrolled = Boolean(String(server.agent_id||'').trim())
  const isOnline = String(server.status||'').toLowerCase()==='online'
  const offline = enrolled && !isOnline
  const neverEnrolled = !enrolled
  const isViewer = String(role)==='viewer'

  const tabs: Array<{id:Tab,label:string; disabled?:boolean; hint?:string}> = [
    { id:'overview', label:'概览' },
    { id:'traffic', label:'流量' },
    { id:'settings', label:'网络设置' },
    { id:'dns', label:'DNS' },
    { id:'mtu', label:'MTU' },
    { id:'diagnostics', label:'诊断', disabled: isViewer || !enrolled || !isOnline, hint: isViewer? '需要管理员权限' : !enrolled ? '未接入 Agent' : !isOnline ? 'Agent 离线' : undefined },
  ]

  const policy = (data.server_dns_policies||[]).find((p:any)=> Number(p.server_id)===Number(server.id))
  const lists = data.dns_lists||[]
  const benchmarks = (data.dns_benchmarks||[]).filter((x:any)=> Number(x.server_id)===Number(server.id))

  const handleSaveNetwork=async(patch:any)=>{
    const result = await client.request(`/servers/${server.id}`, { method:'PATCH', body: JSON.stringify({ ...server, ...patch }) })
    onUpdated?.()
    notify?.('网络设置已保存','success')
    return result
  }
  const handleResetTraffic=async()=>{
    notify?.('正在清零流量...','info')
    // caller should implement reset, but we do inline
    try{
      await client.request(`/servers/${server.id}/reset-traffic`, { method:'POST', body:'{}' })
      onUpdated?.()
      notify?.('已用流量已清零','success')
    } catch(e:any){ notify?.(e?.message||String(e),'error') }
  }

  return (
    <ServerWorkspaceDialog server={server} title="网络" tabs={tabs as any} activeTab={tab} onTabChange={(id)=>setTab(id as Tab)} onClose={onClose}>
      {tab==='overview' && <NetworkOverviewTab server={server} />}
      {tab==='traffic' && <NetworkTrafficTab server={server} onResetTraffic={()=>void handleResetTraffic()} disabled={false} />}
      {tab==='settings' && <NetworkSettingsTab server={server} onSave={handleSaveNetwork} disabled={false} />}
      {tab==='dns' && <NetworkDNSTab server={server} policy={policy} lists={lists} benchmarks={benchmarks} client={client} notify={notify} />}
      {tab==='mtu' && <NetworkMTUTab server={server} client={client} onSaved={onUpdated} />}
      {tab==='diagnostics' && <NetworkDiagnosticsTab server={server} client={client} notify={notify} disabled={offline||neverEnrolled} disabledReason={neverEnrolled? '未接入 Agent，无法执行诊断' : offline? 'Agent 当前离线' : undefined} />}
    </ServerWorkspaceDialog>
  )
}
