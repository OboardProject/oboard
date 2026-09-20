import React, { useEffect, useRef, useState } from 'react'
import { MotionDialogPanel } from '../ui/motion'
import type { Server } from '../proxy-path/types'

function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return String(v); const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }
function labelValue(v:any){ const m:Record<string,string>={ pending:'等待中', running:'执行中', succeeded:'成功', failed:'失败', rollback_failed:'回滚失败'}; return m[String(v)]||String(v) }

type ServerTasksDialogProps = { server: Server; client:any; onClose:()=>void }

export function ServerTasksDialog(props: ServerTasksDialogProps) {
  return <ServerTasksSession key={props.server.id} {...props} />
}

function ServerTasksSession({ server, client, onClose }: ServerTasksDialogProps) {
  const [filter, setFilter]=useState<'all'|'running'|'failed'|'succeeded'>('all')
  const [tasks, setTasks]=useState<any[]>([])
  const [loading, setLoading]=useState(true)
  const [error, setError]=useState('')
  const [selectedID, setSelectedID]=useState<number | null>(null)
  const selected = tasks.find(task => task.id === selectedID)
  const requestRef = useRef<AbortController | null>(null)

  const load=async()=>{
    requestRef.current?.abort()
    const controller = new AbortController()
    requestRef.current = controller
    setLoading(true); setError('')
    try{
      const res = await client.request(`/servers/${server.id}/tasks?limit=100`, { signal: controller.signal })
      if (!controller.signal.aborted) setTasks(Array.isArray(res.tasks)? res.tasks: [])
    } catch(e:unknown){
      if (!controller.signal.aborted) setError(e instanceof Error ? e.message : '任务记录暂时无法刷新')
    } finally{ if (!controller.signal.aborted) setLoading(false) }
  }
  useEffect(()=>{ void load(); return () => requestRef.current?.abort() }, [server.id, client])

  const filtered = tasks.filter(t=>{
    if(filter==='all') return true
    if(filter==='running') return ['pending','running'].includes(String(t.status))
    return filter === 'failed' ? ['failed', 'rollback_failed'].includes(String(t.status)) : String(t.status)===filter
  })

  return (
    <MotionDialogPanel onCancel={onClose} className="server-tasks-dialog server-workspace-dialog" placement="right" drawerSize="wide" surfaceMotion="workspace" ariaLabel={`任务记录 · ${server.name || `服务器 #${server.id}`}`}>
      <header className="dialog-head">
        <div><h2>任务记录 · {server.name || `服务器 #${server.id}`}</h2><p className="muted">最近 {tasks.length} 条执行记录</p></div>
        <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭">×</button>
      </header>
      <div className="server-workspace-tabs" role="tablist">
        {(['all','running','failed','succeeded'] as const).map(f=> (
          <button key={f} type="button" role="tab" aria-selected={filter===f} className={filter===f? 'active':''} onClick={()=>setFilter(f)}>{f==='all'? '全部' : f==='running'? '运行中' : f==='failed'? '失败':'成功'}</button>
        ))}
        <button type="button" className="ghost" disabled={loading} aria-busy={loading} onClick={()=>void load()} style={{marginLeft:'auto'}}>{loading ? '刷新中…' : '刷新'}</button>
      </div>
      <div className={`dialog-body server-tasks-body${selected ? ' has-selection' : ''}`}>
        <div className="server-tasks-list">
          {error && <div className="access-note warning" role="status"><span>{tasks.length ? '刷新失败，以下保留上次加载的记录。' : '任务记录加载失败，请重试。'}</span><details><summary>错误详情</summary>{error}</details></div>}
          {loading && !tasks.length ? <p className="muted" role="status">正在加载…</p> : !filtered.length ? !error && <p className="muted">暂无任务</p> : (
            <table className="server-tasks-table" style={{width:'100%', borderCollapse:'collapse'}}>
              <thead><tr><th style={{textAlign:'left', padding:'8px 6px'}}>更新时间</th><th style={{textAlign:'left', padding:'8px 6px'}}>类型</th><th style={{textAlign:'left', padding:'8px 6px'}}>状态</th></tr></thead>
              <tbody>
                {filtered.map((t:any)=> (
                  <tr key={t.id} onClick={()=> setSelectedID(t.id)} style={{cursor:'pointer', background: selected?.id===t.id? 'var(--surface-2)':'transparent'}}>
                    <td style={{padding:'8px 6px', fontVariantNumeric:'tabular-nums'}}>{t.updated_at||t.created_at ? formatTableTime(t.updated_at||t.created_at) : '—'}</td>
                    <td style={{padding:'8px 6px'}}><button type="button" className="ghost" aria-label={`查看任务 #${t.id}`} aria-pressed={selectedID === t.id} onClick={()=>setSelectedID(t.id)}>{t.type||t.kind||'—'}</button></td>
                    <td style={{padding:'8px 6px'}}><span className={`status-pill ${String(t.status)==='succeeded'? 'ok' : ['failed','rollback_failed'].includes(String(t.status))? 'danger':''}`}>{labelValue(t.status)}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        {selected && (
          <div className="server-tasks-detail">
            <h3>任务 #{selected.id}</h3>
            <dl className="server-detail-grid" style={{gridTemplateColumns:'1fr'}}>
              <div className="server-about-item"><span className="server-about-label">类型</span><span className="server-about-value">{selected.type||'—'}</span></div>
              <div className="server-about-item"><span className="server-about-label">状态</span><span className="server-about-value">{labelValue(selected.status)}</span></div>
              <div className="server-about-item"><span className="server-about-label">创建时间</span><span className="server-about-value">{selected.created_at ? formatTableTime(selected.created_at):'—'}</span></div>
              <div className="server-about-item"><span className="server-about-label">更新时间</span><span className="server-about-value">{selected.updated_at ? formatTableTime(selected.updated_at):'—'}</span></div>
              {selected.error && <div className="server-about-item"><span className="server-about-label">错误</span><span className="server-about-value">{String(selected.error)}</span></div>}
            </dl>
            <details className="task-details" style={{marginTop:12}}>
              <summary>诊断详情</summary>
              <pre style={{whiteSpace:'pre-wrap', wordBreak:'break-all', background:'var(--surface-2)', padding:10, borderRadius:'var(--radius-sm)', maxHeight:360, overflow:'auto'}}>{(() => { try{ return JSON.stringify(JSON.parse(selected.result_json||'{}'), null, 2)}catch{return selected.result_json||'—'}})()}</pre>
            </details>
            <button type="button" className="ghost" style={{marginTop:10}} onClick={()=>setSelectedID(null)}>关闭详情</button>
          </div>
        )}
      </div>
      <footer className="dialog-actions"><button onClick={onClose}>关闭</button></footer>
    </MotionDialogPanel>
  )
}
