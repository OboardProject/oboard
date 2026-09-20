import React, { useState } from 'react'
import { ServerWorkspaceDialog } from './shared/ServerWorkspaceDialog'
import { SystemOverviewTab } from './system/SystemOverviewTab'
import { SystemAgentTab } from './system/SystemAgentTab'
import { SystemSettingsTab } from './system/SystemSettingsTab'
import { SystemLogsTab } from './system/SystemLogsTab'
import type { Server } from '../proxy-path/types'

type Tab = 'overview'|'agent'|'settings'|'logs'

export function ServerSystemDialog({ server, initialTab='overview', data, client, onClose, onUpdated, notify, controllerURL, role='viewer' }: { server: Server; initialTab?: Tab; data:any; client:any; onClose:()=>void; onUpdated?:()=>void; notify?:(m:string,t?:string)=>void; controllerURL:string; role?: string }) {
  const [tab, setTab]=useState<Tab>(initialTab)
  const enrolled = Boolean(String(server.agent_id||'').trim())
  const isOnline = String(server.status||'').toLowerCase()==='online'
  const offline = enrolled && !isOnline
  const isViewer = role !== 'admin' && role !== 'operator'
  const tabs: Array<{id:Tab,label:string; disabled?:boolean; hint?:string}> = [
    { id:'overview', label:'主机信息' },
    { id:'agent', label:'接入与更新', disabled: isViewer, hint: isViewer? '需要管理员权限': undefined },
    { id:'logs', label:'日志', disabled: isViewer || !enrolled || !isOnline, hint: isViewer? '需要管理员权限' : !enrolled? '未接入 Agent' : !isOnline? 'Agent 离线' : undefined },
  ]

  const expectedBuild = data.version?.agent_expected_build || data.version?.build || ''

  const handleEnroll=async()=>{
    const res = await client.request(`/servers/${server.id}/enroll-token`, { method:'POST', body:'{}' })
    return res.install_command as string
  }
  const handleUpdateAgent=async()=>{
    try{
      const res = await client.request(`/servers/${server.id}/agent-update`, { method:'POST', body:'{}' })
      notify?.(res.existing ? '已有更新任务进行中' : '已创建 Agent 更新任务','success')
      onUpdated?.()
    } catch(e:any){
      notify?.(e?.message||String(e),'error')
      throw e
    }
  }
  const handleSaveSystem=async(patch:any)=>{
    if (isViewer) return
    const payload: any = {}
    if(patch.bbr_enabled!==undefined) payload.bbr_enabled = patch.bbr_enabled
    if(patch.time_correction_mode) payload.time_correction_mode = patch.time_correction_mode
    if(patch.connection_audit_enabled!==undefined) payload.connection_audit_enabled = patch.connection_audit_enabled
    await client.request(`/servers/${server.id}`, { method:'PATCH', body: JSON.stringify(payload) })
    onUpdated?.()
    notify?.('系统设置已保存','success')
  }
  const handleCheckTime=async()=>{
    notify?.('已触发时间检查','info')
  }

  return (
    <ServerWorkspaceDialog server={server} title="Agent 维护与日志" tabs={tabs as any} activeTab={tab} onTabChange={(id)=> setTab(id as Tab)} onClose={onClose}>
      {tab==='overview' && <SystemOverviewTab server={server} />}
      {tab==='agent' && <SystemAgentTab server={server} expectedBuild={expectedBuild} onEnroll={handleEnroll} onUpdateAgent={handleUpdateAgent} notify={notify} disabled={isViewer} disabledReason="当前账号只有查看权限" />}
      {tab==='settings' && <SystemSettingsTab server={server} onSave={handleSaveSystem} onCheckTime={handleCheckTime} disabled={isViewer} disabledReason="当前账号只有查看权限" />}
      {tab==='logs' && <SystemLogsTab server={server} data={data} client={client} disabled={isViewer||offline||!enrolled} disabledReason={isViewer ? '当前账号只有查看权限' : !enrolled? '未接入 Agent，无法获取日志' : 'Agent 当前离线'} />}
    </ServerWorkspaceDialog>
  )
}
