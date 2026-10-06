import React, { useState } from 'react'
import { Select } from '../../ui/select'
import type { Server } from '../../proxy-path/types'

function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }

export function SystemLogsTab({ server, data, client, disabled, disabledReason }: { server: Server; data:any; client:any; disabled?:boolean; disabledReason?:string }) {
  const [lines, setLines]=useState(120)
  const [services, setServices]=useState('all')
  const [loading, setLoading]=useState(false)
  const [task, setTask]=useState<any>(null)
  const [result, setResult]=useState<any>(null)
  const [operation, setOperation]=useState('')

  const pull=async()=>{
    if(loading|| disabled) return
    setLoading(true); setTask(null); setResult(null)
    try{
      const created = await client.request(`/servers/${server.id}/logs`, { method:'POST', body: JSON.stringify({ lines, services }) })
      const taskID = created.task?.id
      setTask(created.task)
      if(!taskID) return
      for(let i=0;i<30;i++){
        await new Promise(r=> setTimeout(r,1000))
        const res = await client.request(`/servers/${server.id}/tasks?limit=30`)
        const next = (res.tasks||[]).find((x:any)=> Number(x.id)===Number(taskID))
        if(next){
          setTask(next)
          if(['succeeded','failed','rollback_failed'].includes(String(next.status))){
            try{ setResult(JSON.parse(next.result_json||'{}')) }catch{ setResult({ raw: next.result_json })}
            return
          }
        }
      }
    } finally{ setLoading(false) }
  }

  const control=async(action:'rotate'|'clear')=>{
    if(disabled) return
    if(action==='clear'){
      const ok = confirm(`确定清空 ${services==='all'? 'Agent 和内核' : services==='agent'? 'Agent':'内核'} 日志及轮转备份？`)
      if(!ok) return
    }
    setOperation(action)
    try{
      const created = await client.request(`/servers/${server.id}/logs/control`, { method:'POST', body: JSON.stringify({ action, services }) })
      const taskID = created.task?.id
      if(!taskID) return
      for(let i=0;i<30;i++){
        await new Promise(r=> setTimeout(r,1000))
        const res = await client.request(`/servers/${server.id}/tasks?limit=30`)
        const next = (res.tasks||[]).find((x:any)=> Number(x.id)===Number(taskID))
        if(next && ['succeeded','failed','rollback_failed'].includes(String(next.status))){
          if(next.status!=='succeeded') throw new Error('日志操作失败')
          await pull()
          return
        }
      }
      throw new Error('日志操作超时')
    } catch(e:any){ alert(e?.message||String(e)) } finally{ setOperation('') }
  }

  const raw = result ? JSON.stringify(result,null,2) : ''
  const logs = result?.logs || {}
  const policy = result?.policy || {}

  return (
    <div className="server-logs-tab">
      <div className="server-logs-toolbar">
        <div className="server-logs-filters">
          <Select value={services} onChange={e=> setServices(e.target.value)} aria-label="日志服务" disabled={disabled || loading || Boolean(operation)}>
            <option value="all">全部服务</option>
            <option value="agent">Agent</option>
            <option value="core">oboard-sb</option>
          </Select>
          <Select value={lines} onChange={e=> setLines(Number(e.target.value)||120)} aria-label="日志行数" disabled={disabled || loading || Boolean(operation)}>
            <option value={60}>60 行</option>
            <option value={120}>120 行</option>
            <option value={300}>300 行</option>
            <option value={600}>600 行</option>
          </Select>
        </div>
        <button type="button" onClick={()=>void pull()} disabled={loading || disabled || Boolean(operation)}>{loading ? '拉取中…' : '刷新'}</button>
      </div>
      {disabled && <small className="muted">{disabledReason}</small>}

      <div className="server-logs-content" aria-busy={loading}>
        {task && <p className="muted">状态：{String(task.status)} {task.completed_at ? `· ${formatTableTime(String(task.completed_at))}` : '· 执行中'}</p>}
        {result ? (
          <div className="agent-log-result">
            <div className="log-summary">
              <span>Agent：{result?.agent_version||'—'} #{result?.agent_build||'—'}</span>
              <span>时间：{result?.time ? formatTableTime(String(result.time)) : '—'}</span>
              <span>行数：{result?.lines||'—'}</span>
              {policy.agent && <span>Agent 日志：{policy.agent.max_mb} MB · {policy.agent.backups} 个备份</span>}
              {policy.core && <span>内核日志：{policy.core.max_mb} MB · {policy.core.backups} 个备份</span>}
            </div>
            {logs.agent && (
              <section className="service-log-block">
                <h3>Agent 日志</h3>
                <pre>{logs.agent?.log_file?.content || logs.agent?.journal?.output || logs.agent?.log_file?.error || logs.agent?.journal?.error || '暂无日志'}</pre>
              </section>
            )}
            {logs.core && (
              <section className="service-log-block">
                <h3>内核日志</h3>
                <pre>{logs.core?.log_file?.content || logs.core?.journal?.output || logs.core?.log_file?.error || logs.core?.journal?.error || '暂无日志'}</pre>
              </section>
            )}
            <details className="task-details"><summary>原始 JSON</summary><pre style={{whiteSpace:'pre-wrap', wordBreak:'break-all'}}>{raw}</pre></details>
          </div>
        ) : <div className="server-logs-empty"><strong>{loading ? '正在获取日志…' : '尚未获取日志'}</strong><span>{loading ? '等待 Agent 返回最近日志' : '点击刷新，获取最近日志（自动脱敏）'}</span></div>}
      </div>

      <div className="server-logs-actions">
        <div className="agent-log-actions">
          <button className="ghost" onClick={()=>void control('rotate')} disabled={Boolean(operation)|| loading|| disabled}>{operation==='rotate'? '轮转中...':'轮转日志'}</button>
          <button className="ghost danger-text" onClick={()=>void control('clear')} disabled={Boolean(operation)|| loading|| disabled}>{operation==='clear'? '清空中...':'清空日志'}</button>
        </div>
        <small className="muted">清空将删除日志及轮转备份</small>
      </div>
    </div>
  )
}
