import React, { useState } from 'react'
import type { Server } from '../../proxy-path/types'

function formatBytes(v:number){
  if(!v) return '0 B'
  const units=['B','KB','MB','GB','TB']; let n=Number(v); let i=0; while(n>=1024 && i<units.length-1){n/=1024;i++}
  return `${n>=10||i===0 ? n.toFixed(0):n.toFixed(1)} ${units[i]}`
}
function formatTableTime(v:string){
  const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function NetworkTrafficTab({ server, onResetTraffic, disabled, disabledReason }: { server: Server; onResetTraffic: ()=>void; disabled?: boolean; disabledReason?: string }) {
  const upload = Number(server.traffic_upload_bytes||0)
  const download = Number(server.traffic_download_bytes||0)
  const total = upload+download
  const limit = Number(server.traffic_limit_bytes||0)
  const pct = limit>0 ? Math.min(100, total/limit*100) : 0
  const tone = pct>=90 ? 'danger' : pct>=75 ? 'warning' : ''
  const [confirm, setConfirm]=useState(false)
  return (
    <div className="server-traffic-tab">
      <section className="server-detail-section">
        <h3>累计流量</h3>
        <div className="server-traffic-cards">
          <div className="server-traffic-card down">
            <span className="server-traffic-label">↓ 下载</span>
            <strong className="server-traffic-value">{formatBytes(download)}</strong>
          </div>
          <div className="server-traffic-card up">
            <span className="server-traffic-label">↑ 上传</span>
            <strong className="server-traffic-value">{formatBytes(upload)}</strong>
          </div>
          <div className="server-traffic-card total">
            <span className="server-traffic-label">总计</span>
            <strong className="server-traffic-value">{formatBytes(total)}{limit>0 ? <small> / {formatBytes(limit)} · {pct.toFixed(pct>=10?0:1)}%</small> : null}</strong>
          </div>
        </div>
        {limit>0 && <div className="server-traffic-quota-track" role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}><div className={`server-traffic-quota-fill ${tone}`} style={{ width:`${pct}%` }}/></div>}
        <div className="server-traffic-meta">
          <span>统计周期：{(server as any).traffic_reset_mode==='month_day' ? `每月 ${(server as any).traffic_reset_day||1} 日` : '自然月'}</span>
          {server.traffic_period_start && server.traffic_period_end ? <span>周期范围：{formatTableTime(server.traffic_period_start)} 至 {formatTableTime(server.traffic_period_end)}</span> : null}
        </div>
      </section>

      <section className="server-detail-section">
        <h3>即时操作</h3>
        <div className="server-operation-card">
          <div>
            <strong>清零已用流量</strong>
            <small className="muted">仅清零当前周期的面板统计，不影响限额与重置日</small>
          </div>
          {!confirm ? (
            <button type="button" className="ghost danger-text" disabled={disabled} title={disabled? disabledReason: '清零已用流量'} onClick={()=>setConfirm(true)}>清零已用流量</button>
          ) : (
            <div className="server-confirm-row">
              <span className="muted">确定清零服务器「{server.name || `#${server.id}`}」的已使用流量统计吗？</span>
              <div style={{display:'flex', gap:8}}>
                <button type="button" className="ghost" onClick={()=>setConfirm(false)}>取消</button>
                <button type="button" className="danger-button" onClick={()=>{setConfirm(false); onResetTraffic()}}>确认清零</button>
              </div>
            </div>
          )}
          {disabled && <small className="muted" style={{marginTop:6}}>{disabledReason}</small>}
        </div>
      </section>
    </div>
  )
}
