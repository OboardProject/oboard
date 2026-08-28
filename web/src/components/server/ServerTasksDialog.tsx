import React, { useEffect, useState } from 'react'
import { MotionDialogPanel } from '../ui/motion'
import type { Server } from '../proxy-path/types'

function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return String(v); const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }
function labelValue(v:any){ const m:Record<string,string>={ pending:'等待中', running:'执行中', succeeded:'成功', failed:'失败', rollback_failed:'回滚失败'}; return m[String(v)]||String(v) }

export function ServerTasksDialog({ server, client, onClose }: { server: Server; client:any; onClose:()=>void }) {
  const [filter, setFilter]=useState<'all'|'running'|'failed'|'succeeded'>('all')
  const [tasks, setTasks]=useState<any[]>([])
  const [loading, setLoading]=useState(true)
  const [error, setError]=useState('')
  const [selected, setSelected]=useState<any>(null)

  const load=async()=>{
    setLoading(true); setError('')
    try{
      const res = await client.request(`/servers/${server.id}/tasks?limit=100`)
      setTasks(Array.isArray(res.tasks)? res.tasks: [])
    } catch(e:any){ setError(e?.message||String(e)) } finally{ setLoading(false) }
  }
  useEffect(()=>{ void load() }, [server.id])

  const filtered = tasks.filter(t=>{
    if(filter==='all') return true
    if(filter==='running') return ['pending','running'].includes(String(t.status))
    return String(t.status)===filter
  })

  return (
    <MotionDialogPanel onCancel={onClose} className="server-tasks-dialog server-workspace-dialog">
      <header className="dialog-head">
        <div><h2>任务记录 · {server.name || `服务器 #${server.id}`}</h2><p className="muted">Agent 任务历史 · 共 {tasks.length} 条</p></div>
        <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭">×</button>
      </header>
      <div className="server-workspace-tabs" role="tablist">
        {(['all','running','failed','succeeded'] as const).map(f=> (
          <button key={f} type="button" role="tab" aria-selected={filter===f} className={filter===f? 'active':''} onClick={()=>setFilter(f)}>{f==='all'? '全部' : f==='running'? '运行中' : f==='failed'? '失败':'成功'}</button>
        ))}
        <button type="button" className="ghost" onClick={()=>void load()} style={{marginLeft:'auto'}}>刷新</button>
      </div>
      <div className="dialog-body" style={{display:'grid', gridTemplateColumns: selected? '1fr 360px':'1fr', gap:16, minHeight:380}}>
        <div className="server-tasks-list">
          {loading ? <p className="muted">正在加载…</p> : error ? <div className="access-note warning"><span>{error}</span></div> : !filtered.length ? <p className="muted">暂无任务</p> : (
            <table className="server-tasks-table" style={{width:'100%', borderCollapse:'collapse'}}>
              <thead><tr><th style={{textAlign:'left', padding:'8px 6px'}}>更新时间</th><th style={{textAlign:'left', padding:'8px 6px'}}>类型</th><th style={{textAlign:'left', padding:'8px 6px'}}>状态</th></tr></thead>
              <tbody>
                {filtered.map((t:any)=> (
                  <tr key={t.id} onClick={()=> setSelected(t)} style={{cursor:'pointer', background: selected?.id===t.id? 'var(--surface-2)':'transparent'}}>
                    <td style={{padding:'8px 6px', fontVariantNumeric:'tabular-nums'}}>{t.updated_at||t.created_at ? formatTableTime(t.updated_at||t.created_at) : '—'}</td>
                    <td style={{padding:'8px 6px'}}>{t.type||t.kind||'—'}</td>
                    <td style={{padding:'8px 6px'}}><span className={`status-pill ${String(t.status)==='succeeded'? 'ok' : String(t.status)==='failed'? 'danger':''}`}>{labelValue(t.status)}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        {selected && (
          <div className="server-tasks-detail" style={{borderLeft:'1px solid var(--border)', paddingLeft:16}}>
            <h3>任务 #{selected.id}</h3>
            <dl className="server-detail-grid" style={{gridTemplateColumns:'1fr'}}>
              <div className="server-about-item"><span className="server-about-label">类型</span><span className="server-about-value">{selected.type||'—'}</span></div>
              <div className="server-about-item"><span className="server-about-label">状态</span><span className="server-about-value">{labelValue(selected.status)}</span></div>
              <div className="server-about-item"><span className="server-about-label">创建时间</span><span className="server-about-value">{selected.created_at ? formatTableTime(selected.created_at):'—'}</span></div>
              <div className="server-about-item"><span className="server-about-label">更新时间</span><span className="server-about-value">{selected.updated_at ? formatTableTime(selected.updated_at):'—'}</span></div>
              {selected.error && <div className="server-about-item"><span className="server-about-label">错误</span><span className="server-about-value">{String(selected.error)}</span></div>}
            </dl>
            <details className="task-details" open style={{marginTop:12}}>
              <summary>原始结果</summary>
              <pre style={{whiteSpace:'pre-wrap', wordBreak:'break-all', background:'var(--surface-2)', padding:10, borderRadius:8, maxHeight:360, overflow:'auto'}}>{(() => { try{ return JSON.stringify(JSON.parse(selected.result_json||'{}'), null, 2)}catch{return selected.result_json||'—'}})()}</pre>
            </details>
            <button type="button" className="ghost" style={{marginTop:10}} onClick={()=>setSelected(null)}>关闭详情</button>
          </div>
        )}
      </div>
      <footer className="dialog-actions"><button onClick={onClose}>关闭</button></footer>
    </MotionDialogPanel>
  )
}
