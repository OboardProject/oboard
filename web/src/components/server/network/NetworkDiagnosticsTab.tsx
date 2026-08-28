import React, { useState } from 'react'
import type { Server } from '../../proxy-path/types'

export function NetworkDiagnosticsTab({ server, client, notify, disabled, disabledReason }: { server: Server; client:any; notify?:(m:string,t?:string)=>void; disabled?:boolean; disabledReason?:string }) {
  const [running, setRunning]=useState(false)
  const [task, setTask]=useState<any>(null)
  const [result, setResult]=useState<any>(null)
  const [error, setError]=useState('')

  const start=async()=>{
    if(running|| disabled) return
    setRunning(true); setTask(null); setResult(null); setError('')
    try{
      const created = await client.request(`/servers/${server.id}/diagnose`, { method:'POST', body:'{}' })
      const taskID = created.task?.id
      setTask(created.task)
      if(!taskID){ setRunning(false); return }
      for(let i=0;i<60;i++){
        await new Promise(r=> setTimeout(r,1000))
        const res = await client.request(`/servers/${server.id}/tasks?limit=30`)
        const next = (res.tasks||[]).find((x:any)=> Number(x.id)===Number(taskID))
        if(next){
          setTask(next)
          if(['succeeded','failed','rollback_failed'].includes(String(next.status))){
            let parsed: any=null
            try{ parsed=JSON.parse(next.result_json||'{}')}catch{}
            setResult(parsed)
            if(String(next.status)!=='succeeded'){
              setError(parsed?.error||'诊断未完全成功')
            } else {
              notify?.('网络诊断已完成','success')
            }
            break
          }
        }
      }
    } catch(e:any){
      setError(e?.message||String(e))
      notify?.(e?.message||String(e),'error')
    } finally{ setRunning(false) }
  }

  const items: Array<{key:string; label:string}> = [
    { key:'controller', label:'Controller 连接' },
    { key:'ipv4', label:'IPv4' },
    { key:'ipv6', label:'IPv6' },
    { key:'default_route', label:'默认路由' },
    { key:'listeners', label:'入口监听' },
    { key:'dns', label:'DNS' },
    { key:'firewall', label:'防火墙' },
    { key:'entry_probe', label:'入口公网探测' },
  ]

  const getStatus=(key:string)=>{
    if(!result) return running ? '检查中' : '—'
    // result may have fields like checks, results, etc.
    const checks = result.checks || result.results || result.items || {}
    if(checks[key]?.available !== undefined) return checks[key].available ? '✓' : '✗'
    if(checks[key]?.status) return checks[key].status
    // generic fallback: try to find in raw
    const raw = JSON.stringify(result).toLowerCase()
    if(raw.includes(key) && raw.includes('fail')) return '✗'
    if(raw.includes(key)) return '✓'
    return result[key]?.available ? '✓' : result[key] ? '✓' : '—'
  }

  return (
    <div className="server-diagnostics-tab">
      <section className="server-detail-section">
        <h3>网络诊断</h3>
        <p className="muted">对目标服务器执行完整的网络与入口检查，结果通过 Agent 任务返回。</p>
        <button type="button" onClick={()=>void start()} disabled={running|| disabled} title={disabled? disabledReason: '开始完整诊断'}>
          {running ? '正在诊断…' : '开始完整诊断'}
        </button>
        {disabled && <small className="muted" style={{display:'block', marginTop:8}}>{disabledReason}</small>}
        {task && <p className="muted" style={{marginTop:10}}>任务状态：{String(task.status)} {task.completed_at ? `· 完成于 ${new Date(task.completed_at).toLocaleString()}` : '· 执行中'}</p>}
        {error && <div className="access-note warning"><strong>提示</strong><span>{error}</span></div>}
      </section>

      <section className="server-detail-section">
        <h3>诊断项目</h3>
        <div className="server-diagnostics-grid">
          {items.map(item=> (
            <div key={item.key} className="server-diagnostics-card">
              <span>{item.label}</span>
              <span className={`server-diagnostics-status ${getStatus(item.key)==='✓' ? 'ok' : getStatus(item.key)==='✗' ? 'fail' : ''}`}>{getStatus(item.key)}</span>
            </div>
          ))}
        </div>
        {result ? (
          <details className="task-details" style={{marginTop:12}} open>
            <summary>详细输出</summary>
            <pre style={{whiteSpace:'pre-wrap', wordBreak:'break-all', maxHeight:420, overflow:'auto', background:'var(--surface-2)', padding:12, borderRadius:8}}>{JSON.stringify(result, null, 2)}</pre>
            {result?.ip_addr || result?.ip_route ? (
              <div style={{marginTop:10}}>
                {result.ip_addr && <><strong>ip addr</strong><pre>{typeof result.ip_addr==='string'? result.ip_addr : JSON.stringify(result.ip_addr,null,2)}</pre></>}
                {result.ip_route && <><strong>ip route</strong><pre>{typeof result.ip_route==='string'? result.ip_route : JSON.stringify(result.ip_route,null,2)}</pre></>}
              </div>
            ) : null}
          </details>
        ) : <small className="muted">点击“开始完整诊断”后，这里会显示各项检查的详细输出</small>}
      </section>

      {result && result.entry_probes && (
        <section className="server-detail-section">
          <h3>入口监听</h3>
          <div className="server-entry-probe-list">
            {(Array.isArray(result.entry_probes) ? result.entry_probes : Object.entries(result.entry_probes)).map((entry:any, idx:number)=>{
              const label = Array.isArray(result.entry_probes) ? `监听 ${idx+1}` : entry[0]
              const val = Array.isArray(result.entry_probes) ? entry : entry[1]
              const available = val?.available ?? val?.status==='ok'
              return <div key={idx} className="server-entry-probe-row"><span>{label}</span><span className={available? 'ok':'fail'}>{available? '正常':'异常'}</span><small>{val?.latency_ms ? `${val.latency_ms} ms` : ''}</small></div>
            })}
          </div>
        </section>
      )}
    </div>
  )
}
