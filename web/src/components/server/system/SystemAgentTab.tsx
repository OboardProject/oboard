import React, { useState } from 'react'
import { Copy, Check, Terminal } from 'lucide-react'
import type { Server } from '../../proxy-path/types'

function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }
async function copyText(v:string){ const t=String(v||''); if(!t) return false; try{ if(navigator.clipboard?.writeText && window.isSecureContext){ await navigator.clipboard.writeText(t); return true}}catch{} const ta=document.createElement('textarea'); ta.value=t; ta.style.position='fixed'; ta.style.left='-9999px'; document.body.appendChild(ta); ta.select(); try{return document.execCommand('copy')}catch{return false}finally{document.body.removeChild(ta)} }

function CopyButton({ value, label }:{ value:string; label?:string }){
  const [copied,setCopied]=useState(false)
  return <button type="button" className="ghost" onClick={async()=>{ const ok=await copyText(value); if(ok){setCopied(true); setTimeout(()=>setCopied(false),1500)}}}>{copied ? <Check size={14}/>: <Copy size={14}/>} {copied? '已复制': label||'复制'}</button>
}

export type EnrollCommands = { command: string; windowsCommand?: string }

const commandStyle: React.CSSProperties = {whiteSpace:'pre-wrap', wordBreak:'break-all', background:'var(--surface-2)', padding:12, borderRadius:'var(--radius-sm)'}

function EnrollCommandBlocks({ commands }:{ commands: EnrollCommands }){
  return <>
    <div style={{marginTop:12}}>
      {commands.windowsCommand ? <small className="muted">Linux · 在 root SSH 中执行</small> : null}
      <pre style={commandStyle}>{commands.command}</pre>
      <div style={{marginTop:8, display:'flex', gap:8}}>
        <CopyButton value={commands.command} label="复制接入命令" />
      </div>
    </div>
    {commands.windowsCommand ? (
      <div style={{marginTop:12}}>
        <small className="muted">Windows · 在“以管理员身份运行”的 PowerShell 中执行</small>
        <pre style={commandStyle}>{commands.windowsCommand}</pre>
        <div style={{marginTop:8, display:'flex', gap:8}}>
          <CopyButton value={commands.windowsCommand} label="复制 Windows 接入命令" />
        </div>
      </div>
    ) : null}
  </>
}

export function SystemAgentTab({ server, expectedBuild, onEnroll, onUpdateAgent, disabled, disabledReason, notify }: { server: Server; expectedBuild?: string; onEnroll: ()=>Promise<EnrollCommands>; onUpdateAgent: ()=>Promise<void>; disabled?: boolean; disabledReason?:string; notify?:(m:string,t?:string)=>void }) {
  const isOnline = String(server.status||'').toLowerCase()==='online'
  const enrolled = Boolean(String(server.agent_id||'').trim())
  const [commands, setCommands]=useState<EnrollCommands|null>(null)
  const [loading, setLoading]=useState(false)
  const [updating, setUpdating]=useState(false)
  const currentBuild = String(server.agent_build||'').trim()
  const buildNeedsUpdate = (current: string, target: string): boolean => {
    const c = String(current || '').trim()
    const t = String(target || '').trim()
    if (!c || !t) return !c && !!t
    if (c.length === t.length && /^[0-9]+$/.test(c) && /^[0-9]+$/.test(t)) return c < t
    return c !== t
  }
  const needUpdate = Boolean(expectedBuild && String(expectedBuild).trim() && String(expectedBuild).trim().toLowerCase() !== 'dev' && buildNeedsUpdate(currentBuild, String(expectedBuild)) )
  const stealthActive = Boolean(server.kernel_capabilities?.includes?.('stealth_active_v1'))

  const handleEnroll=async()=>{
    if(disabled) return
    setLoading(true)
    try{
      setCommands(await onEnroll())
    } catch(e:any){ notify?.(e?.message||String(e),'error') } finally{ setLoading(false) }
  }
  const handleUpdate=async()=>{
    if(disabled) return
    setUpdating(true)
    try{ await onUpdateAgent() } finally{ setUpdating(false) }
  }

  if(!enrolled){
    return (
      <div className="server-agent-tab">
        <section className="server-detail-section">
          <h3>尚未接入 OBoard Agent</h3>
          <p className="muted">需要在目标服务器执行接入命令以完成注册。</p>
          <div className="server-agent-enroll">
            <button type="button" onClick={()=>void handleEnroll()} disabled={loading || disabled}>{loading? '生成中...':'生成接入命令'}</button>
            {commands ? <EnrollCommandBlocks commands={commands} /> : null}
            {disabled && <small className="muted">{disabledReason}</small>}
          </div>
        </section>
      </div>
    )
  }

  return (
    <div className="server-agent-tab">
      <section className="server-detail-section">
        <h3>OBoard Agent</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><dt className="server-about-label">状态</dt><dd className="server-about-value">{isOnline ? '● 已连接' : '○ 离线'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">当前版本</dt><dd className="server-about-value">{server.agent_version||'—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">当前构建</dt><dd className="server-about-value">{currentBuild || '—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">目标构建</dt><dd className="server-about-value">{expectedBuild||'—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">安全进程</dt><dd className="server-about-value">{stealthActive ? '已启用' : (server.stealth_enabled ? '待重新安装' : '未启用')}</dd></div>
        </dl>
        <div className="server-agent-update-row">
          <span className="server-agent-update-status">{needUpdate ? '有新版本可用' : currentBuild && expectedBuild && expectedBuild !== 'dev' ? '已是最新版本' : '版本信息待确认'}</span>
          {needUpdate && <button type="button" onClick={()=>void handleUpdate()} disabled={updating || !isOnline || disabled}>{updating ? '更新中…' : '更新 Agent'}</button>}
        </div>
        {disabled ? <small className="muted">{disabledReason}</small> : needUpdate && !isOnline ? <small className="muted">Agent 离线，恢复连接后可更新。</small> : null}
      </section>

      <section className="server-detail-section">
        <h3>Agent 接入</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><dt className="server-about-label">Agent ID</dt><dd className="server-about-value">{server.agent_id||'—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">最后连接</dt><dd className="server-about-value">{server.last_seen_at ? formatTableTime(server.last_seen_at) : server.telemetry_updated_at ? formatTableTime(server.telemetry_updated_at) : '—'}</dd></div>
        </dl>
        <div style={{marginTop:12, display:'flex', gap:8, flexWrap:'wrap'}}>
          <button type="button" className="ghost" onClick={()=>void handleEnroll()} disabled={loading || disabled}><Terminal size={14}/> {loading? '生成中...':'重新生成接入 Token'}</button>
        </div>
        {commands ? <EnrollCommandBlocks commands={commands} /> : null}
      </section>
    </div>
  )
}
