import React, { useState, useEffect } from 'react'
import { Select } from '../../ui/select'
import { FormField } from '../../ui/form-field'
import type { Server } from '../../proxy-path/types'

function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }

export function NetworkMTUTab({ server, client, onSaved, disabled, disabledReason }: { server: Server; client:any; onSaved?:()=>void; disabled?:boolean; disabledReason?:string }) {
  const [value, setValue]=useState(()=>{
    // server mtu fields
    return {
      mtu_mode: (server as any).mtu_mode || 'detect',
      mtu_value: (server as any).mtu_value || 0,
      mtu_probe_host: (server as any).mtu_probe_host || '1.1.1.1',
      mtu_probe_port: (server as any).mtu_probe_port || 443,
      mtu_overhead_bytes: (server as any).mtu_overhead_bytes || 0,
    }
  })
  const [saving, setSaving]=useState(false)
  const [detecting, setDetecting]=useState(false)
  const [detectResult, setDetectResult]=useState<any>(null)
  const [history, setHistory]=useState<any[]>([])
  const update=(patch:any)=> setValue((o:any)=>({...o,...patch}))

  useEffect(()=>{
    // fetch mtu detections lazy
    let active=true
    client.request(`/mtu-detections?server_id=${server.id}&limit=20`).then((r:any)=>{
      if(!active) return
      const list = Array.isArray(r.mtu_detections) ? r.mtu_detections : Array.isArray(r.detections) ? r.detections : []
      setHistory(list)
    }).catch(()=>{})
    return ()=>{ active=false }
  }, [client, server.id])

  const save=async()=>{
    if(saving|| disabled) return
    setSaving(true)
    try{
      const payload={
        mtu_mode: value.mtu_mode,
        mtu_value: Math.max(0, Number(value.mtu_value)||0),
        mtu_probe_host: value.mtu_probe_host||'1.1.1.1',
        mtu_probe_port: Math.max(1, Math.min(65535, Number(value.mtu_probe_port)||443)),
        mtu_overhead_bytes: Math.max(0, Number(value.mtu_overhead_bytes)||0),
      }
      await client.request(`/servers/${server.id}`, { method:'PATCH', body: JSON.stringify({ ...server, ...payload }) })
      onSaved?.()
    } finally{ setSaving(false) }
  }

  const detectNow=async()=>{
    if(detecting|| disabled) return
    setDetecting(true)
    setDetectResult(null)
    try{
      const created = await client.request(`/servers/${server.id}/mtu-detect`, { method:'POST', body: JSON.stringify({}) })
      const taskID = created.task?.id
      if(!taskID){
        // direct result?
        setDetectResult(created)
        return
      }
      for(let i=0;i<30;i++){
        await new Promise(r=> setTimeout(r,1000))
        const res = await client.request(`/servers/${server.id}/tasks?limit=30`)
        const next = (res.tasks||[]).find((x:any)=> Number(x.id)===Number(taskID))
        if(next && ['succeeded','failed','rollback_failed'].includes(String(next.status))){
          const parsed = (()=>{ try{ return JSON.parse(next.result_json||'{}')}catch{return {}}})()
          setDetectResult({ status: next.status, ...parsed })
          if(next.status==='succeeded'){
            const list = await client.request(`/mtu-detections?server_id=${server.id}&limit=20`).catch(()=>null)
            if(list){
              const l = Array.isArray(list.mtu_detections) ? list.mtu_detections : []
              setHistory(l)
            }
          }
          break
        }
      }
    } catch(e:any){
      setDetectResult({ error: e?.message||String(e) })
    } finally{ setDetecting(false) }
  }

  const mtuModes = ['disabled','detect','apply']
  function labelValue(v:any){ const m:Record<string,string>={ disabled:'禁用', detect:'检测', apply:'自动应用'}; return m[String(v)]||String(v) }

  return (
    <div className="server-mtu-tab">
      <section className="server-detail-section">
        <h3>当前状态</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><span className="server-about-label">MTU 模式</span><span className="server-about-value">{labelValue((server as any).mtu_mode||'detect')}</span></div>
          <div className="server-about-item"><span className="server-about-label">当前 MTU</span><span className="server-about-value">{(server as any).mtu_value || '自动'}</span></div>
          <div className="server-about-item"><span className="server-about-label">最近 Path MTU</span><span className="server-about-value">{history[0]?.mtu || history[0]?.result?.mtu || '—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">推荐 MTU</span><span className="server-about-value">{history[0]?.recommended_mtu || '—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">最后检测</span><span className="server-about-value">{history[0]?.created_at ? formatTableTime(history[0].created_at) : '—'}</span></div>
        </dl>
      </section>

      <section className="server-detail-section">
        <h3>配置</h3>
        <div className="form labeled-form">
          <FormField label="模式">
            <Select value={value.mtu_mode} onChange={e=> update({ mtu_mode: e.target.value })} disabled={disabled}>
              {mtuModes.map(x=> <option key={x} value={x}>{labelValue(x)}</option>)}
            </Select>
          </FormField>
          <FormField label="指定 MTU" hint="0 表示自动">
            <input type="number" placeholder="0" value={value.mtu_value===0? '' : value.mtu_value} onChange={e=> update({ mtu_value: e.target.value===''? '' : Number(e.target.value)})} disabled={disabled} />
          </FormField>
          <FormField label="探测目标主机" hint="默认 1.1.1.1">
            <input value={value.mtu_probe_host} onChange={e=> update({ mtu_probe_host: e.target.value })} placeholder="1.1.1.1" disabled={disabled} />
          </FormField>
          <FormField label="探测目标端口" hint="默认 443">
            <input type="number" placeholder="443" value={value.mtu_probe_port===''? '' : value.mtu_probe_port} onChange={e=> update({ mtu_probe_port: e.target.value===''? '' : Number(e.target.value)})} disabled={disabled} />
          </FormField>
          <FormField label="额外开销字节" hint="不确定时保持 0">
            <input type="number" placeholder="0" value={value.mtu_overhead_bytes===''? '' : value.mtu_overhead_bytes} onChange={e=> update({ mtu_overhead_bytes: e.target.value===''? '' : Number(e.target.value)})} disabled={disabled} />
          </FormField>
          {disabled && <small className="muted">{disabledReason}</small>}
        </div>
        <div className="server-workspace-actions">
          <button type="button" className="ghost" disabled={saving|| disabled} onClick={()=>void save()}>{saving? '保存中...':'保存设置'}</button>
          <button type="button" disabled={detecting|| disabled} onClick={()=>void detectNow()}>{detecting? '检测中...':'立即检测 MTU'}</button>
        </div>
      </section>

      {(detectResult || history.length>0) && (
        <section className="server-detail-section">
          <h3>检测结果</h3>
          {detectResult && (
            <div className="access-note" style={{marginBottom:12}}>
              <strong>最近检测结果：{detectResult.status||'—'}</strong>
              <span>{detectResult.error|| detectResult.mtu || JSON.stringify(detectResult).slice(0,200)}</span>
            </div>
          )}
          {history.length>0 ? (
            <div className="server-mtu-history">
              {history.slice(0,8).map((h:any, idx:number)=> (
                <div key={h.id||idx} className="server-mtu-history-row">
                  <span>{formatTableTime(h.created_at||h.time||'')}</span>
                  <span>MTU {h.mtu||h.result?.mtu||'—'}</span>
                  <span className={`status-pill ${h.status==='succeeded' ? 'ok' : h.status==='failed' ? 'danger':'warning'}`}>{h.status||'—'}</span>
                </div>
              ))}
            </div>
          ) : <small className="muted">暂无历史记录</small>}
        </section>
      )}
    </div>
  )
}
