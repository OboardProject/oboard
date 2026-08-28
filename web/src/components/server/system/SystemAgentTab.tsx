import React, { useState } from 'react'
import { Copy, Check, Terminal } from 'lucide-react'
import type { Server } from '../../proxy-path/types'

function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }
async function copyText(v:string){ const t=String(v||''); if(!t) return false; try{ if(navigator.clipboard?.writeText && window.isSecureContext){ await navigator.clipboard.writeText(t); return true}}catch{} const ta=document.createElement('textarea'); ta.value=t; ta.style.position='fixed'; ta.style.left='-9999px'; document.body.appendChild(ta); ta.select(); try{return document.execCommand('copy')}catch{return false}finally{document.body.removeChild(ta)} }
function shellQuote(v:string){ return `'${String(v).replace(/'/g,`'\\''`)}'` }
function agentScriptCommand(base:string, action:'install'|'update'|'uninstall', token='', server?: Pick<Server,'bbr_enabled'>){
  const b=base.replace(/\/+$/,''); const dl=`curl -fsSL ${shellQuote(`${b}/install/agent.sh`)}`
  if(action==='install') return `${dl} | OBOARD_ENROLL_TOKEN=${shellQuote(token)} OBOARD_INSTALL_BBR=${server?.bbr_enabled?'1':'0'} sh`
  return `${dl} | sh -s -- ${action}`
}

function CopyButton({ value, label }:{ value:string; label?:string }){
  const [copied,setCopied]=useState(false)
  return <button type="button" className="ghost" onClick={async()=>{ const ok=await copyText(value); if(ok){setCopied(true); setTimeout(()=>setCopied(false),1500)}}}>{copied ? <Check size={14}/>: <Copy size={14}/>} {copied? '已复制': label||'复制'}</button>
}

export function SystemAgentTab({ server, controllerURL, expectedBuild, onEnroll, onUpdateAgent, disabled, disabledReason, notify }: { server: Server; controllerURL:string; expectedBuild?: string; onEnroll: ()=>Promise<string>; onUpdateAgent: ()=>Promise<void>; disabled?: boolean; disabledReason?:string; notify?:(m:string,t?:string)=>void }) {
  const isOnline = String(server.status||'').toLowerCase()==='online'
  const enrolled = Boolean(String(server.agent_id||'').trim())
  const [token, setToken]=useState('')
  const [loading, setLoading]=useState(false)
  const [updating, setUpdating]=useState(false)
  const currentBuild = String(server.agent_build||'').trim()
  const needUpdate = Boolean(expectedBuild && currentBuild && expectedBuild!==currentBuild)

  const handleEnroll=async()=>{
    if(disabled) return
    setLoading(true)
    try{
      const t = await onEnroll()
      setToken(t)
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
          <div className="server-operation-card" style={{flexDirection:'column', alignItems:'stretch'}}>
            <button type="button" onClick={()=>void handleEnroll()} disabled={loading || disabled}>{loading? '生成中...':'生成接入命令'}</button>
            {token ? (
              <div style={{marginTop:12}}>
                <pre style={{whiteSpace:'pre-wrap', wordBreak:'break-all', background:'var(--surface-2)', padding:12, borderRadius:8}}>{agentScriptCommand(controllerURL,'install', token, server)}</pre>
                <div style={{marginTop:8, display:'flex', gap:8}}>
                  <CopyButton value={agentScriptCommand(controllerURL,'install', token, server)} label="复制接入命令" />
                </div>
              </div>
            ) : null}
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
          <div className="server-about-item"><span className="server-about-label">状态</span><span className="server-about-value">{isOnline ? '● 已连接' : '○ 离线'}</span></div>
          <div className="server-about-item"><span className="server-about-label">当前版本</span><span className="server-about-value">{server.agent_version||'—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">当前 Build</span><span className="server-about-value">{currentBuild || '—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">Controller 期望</span><span className="server-about-value">{expectedBuild||'—'}</span></div>
        </dl>
        {needUpdate ? (
          <div className="access-note warning">
            <strong>有新版本可用</strong>
            <span>当前 {currentBuild} · 目标 {expectedBuild}</span>
            <button type="button" onClick={()=>void handleUpdate()} disabled={updating || !isOnline || disabled} title={!isOnline? 'Agent 离线' : undefined} style={{marginTop:8}}>{updating? '更新中...':'更新 Agent'}</button>
            {!isOnline && <small className="muted">Agent 离线时无法通过面板更新，请在服务器上执行命令</small>}
          </div>
        ) : (
          <div className="access-note"><strong>已是最新版本</strong><span>当前构建与主控期望一致</span></div>
        )}
        <div style={{marginTop:12, display:'flex', gap:8, flexWrap:'wrap'}}>
          <button type="button" className="ghost" onClick={()=>void handleUpdate()} disabled={updating || !isOnline || disabled}>{updating? '更新中...':'更新 Agent'}</button>
          {disabled && <small className="muted">{disabledReason}</small>}
        </div>
      </section>

      <section className="server-detail-section">
        <h3>Agent 接入</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><span className="server-about-label">Agent ID</span><span className="server-about-value">{server.agent_id||'—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">最后连接</span><span className="server-about-value">{server.last_seen_at ? formatTableTime(server.last_seen_at) : server.telemetry_updated_at ? formatTableTime(server.telemetry_updated_at) : '—'}</span></div>
        </dl>
        <div style={{marginTop:12, display:'flex', gap:8, flexWrap:'wrap'}}>
          <button type="button" className="ghost" onClick={()=>void handleEnroll()} disabled={loading || disabled}><Terminal size={14}/> {loading? '生成中...':'重新生成接入 Token'}</button>
        </div>
        {token ? (
          <div style={{marginTop:12}}>
            <pre style={{whiteSpace:'pre-wrap', wordBreak:'break-all', background:'var(--surface-2)', padding:12, borderRadius:8}}>{agentScriptCommand(controllerURL,'install', token, server)}</pre>
            <CopyButton value={agentScriptCommand(controllerURL,'install', token, server)} label="复制接入命令" />
          </div>
        ) : null}
      </section>
    </div>
  )
}
