import React, { useState } from 'react'
import { Select } from '../../ui/select'
import { Switch } from '../../ui/switch'
import { FormField } from '../../ui/form-field'
import type { Server } from '../../proxy-path/types'

function formatTimeOffset(ms:number){
  if(!Number.isFinite(ms)) return '—'
  const s=Number(ms)/1000
  if(Math.abs(s)<0.05) return '0 秒'
  return `${s>0?'+':''}${s.toFixed(Math.abs(s)>=10?1:2)} 秒`
}

export function SystemSettingsTab({ server, onSave, onCheckTime, disabled, disabledReason }: { server: Server; onSave:(patch:any)=>Promise<void>; onCheckTime?:()=>Promise<void>; disabled?:boolean; disabledReason?:string }) {
  const [bbr, setBbr]=useState(Boolean(server.bbr_enabled))
  const [mode, setMode]=useState((server.time_correction_mode||'off') as string)
  const [audit, setAudit]=useState(Boolean(server.connection_audit_enabled))
  const [saving, setSaving]=useState(false)
  const [checking, setChecking]=useState(false)

  const save=async()=>{
    if(saving|| disabled) return
    setSaving(true)
    try{
      await onSave({ bbr_enabled: bbr, time_correction_mode: mode, connection_audit_enabled: audit })
    } finally{ setSaving(false) }
  }

  const checkNow=async()=>{
    if(checking|| disabled || !onCheckTime) return
    setChecking(true)
    try{ await onCheckTime() } finally{ setChecking(false) }
  }

  const timeStatus = (()=>{
    switch(server.time_check_status){
      case 'ok': return '时间正常'
      case 'corrected': return (server as any).time_logical_active ? '逻辑校时生效':'系统时间已校准'
      case 'skewed': return '偏差过大'
      case 'unavailable': return '检测失败'
      case 'pending': return '等待检测'
      default: return '尚未检测'
    }
  })()

  return (
    <div className="server-system-settings-tab">
      <section className="server-detail-section">
        <h3>性能</h3>
        <FormField label="BBR + FQ" hint="仅在首次 Agent 安装时生效，切换后不会立即修改内核">
          <Switch checked={bbr} onChange={setBbr} ariaLabel="BBR" />
        </FormField>
      </section>

      <section className="server-detail-section">
        <h3>时间</h3>
        <FormField label="时间校正模式" hint="自动：优先系统校时；逻辑校时：不修改系统时钟">
          <Select value={mode} onChange={e=> setMode(e.target.value)} disabled={disabled}>
            <option value="off">关闭</option>
            <option value="auto">自动</option>
            <option value="ntp">逻辑校时</option>
          </Select>
        </FormField>
        <dl className="server-detail-grid">
          <div className="server-about-item"><span className="server-about-label">Host Offset</span><span className="server-about-value">{server.time_checked_at ? formatTimeOffset(server.time_offset_ms) : '—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">Effective Offset</span><span className="server-about-value">{server.time_checked_at ? formatTimeOffset(server.time_effective_offset_ms) : '—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">状态</span><span className="server-about-value">{timeStatus}</span></div>
        </dl>
        <div style={{display:'flex', gap:8, marginTop:12}}>
          <button type="button" className="ghost" onClick={()=>void checkNow()} disabled={checking|| disabled}>{checking? '检测中...':'立即检查时间'}</button>
        </div>
        {disabled && <small className="muted">{disabledReason}</small>}
      </section>

      <section className="server-detail-section">
        <h3>Agent 功能</h3>
        <FormField label="连接审计" hint="采集连接来源与审计摘要">
          <Switch checked={audit} onChange={setAudit} ariaLabel="连接审计" />
        </FormField>
      </section>

      <div className="server-workspace-actions">
        <button type="button" onClick={()=>void save()} disabled={saving|| disabled}>{saving? '保存中...':'保存系统设置'}</button>
      </div>
    </div>
  )
}
