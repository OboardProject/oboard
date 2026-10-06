import React from 'react'
import type { Server } from '../../proxy-path/types'

function formatBytes(v:number){
  if(!v) return '0 B'
  const units=['B','KB','MB','GB','TB']; let n=Number(v); let i=0; while(n>=1024 && i<units.length-1){n/=1024;i++}
  return `${n>=10||i===0 ? n.toFixed(0):n.toFixed(1)} ${units[i]}`
}
function formatTableTime(v:string){ const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}` }

export function SystemOverviewTab({ server, connectivity }: { server: Server; connectivity?: any }) {
  const isOnline = String(server.status||'').toLowerCase()==='online'
  return (
    <div className="server-system-overview">
      <section className="server-detail-section">
        <h3>系统</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><dt className="server-about-label">系统</dt><dd className="server-about-value">{server.distro_name || server.os || '—'} {server.distro_version||''}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">Kernel</dt><dd className="server-about-value">{(server as any).kernel_version || '—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">架构</dt><dd className="server-about-value">{server.arch||'—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">运行时间</dt><dd className="server-about-value">{(server as any).uptime ? `${Math.floor(Number((server as any).uptime)/86400)} 天` : '—'}</dd></div>
        </dl>
      </section>
      <section className="server-detail-section">
        <h3>资源</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><dt className="server-about-label">CPU</dt><dd className="server-about-value">{Number.isFinite(server.cpu_usage_percent) ? `${Number(server.cpu_usage_percent).toFixed(1)}%` : '—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">内存</dt><dd className="server-about-value">{server.memory_total_bytes ? `${formatBytes(server.memory_used_bytes||0)} / ${formatBytes(server.memory_total_bytes)}` : '—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">磁盘</dt><dd className="server-about-value">{server.disk_total_bytes ? `${formatBytes(server.disk_bytes||0)} / ${formatBytes(server.disk_total_bytes)}` : formatBytes(server.disk_bytes||0) || '—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">连接数</dt><dd className="server-about-value">TCP {server.tcp_connection_count||0} · UDP {server.udp_connection_count||0}</dd></div>
        </dl>
      </section>
      <section className="server-detail-section">
        <h3>OBoard 服务</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><dt className="server-about-label">oboard-agent</dt><dd className={`server-about-value ${isOnline?'ok':'fail'}`}>● {isOnline?'running':'offline'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">oboard-sb</dt><dd className={`server-about-value ${isOnline?'ok':'fail'}`}>● {isOnline?'running':'offline'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">Agent 版本</dt><dd className="server-about-value">{server.agent_version||'—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">Kernel 版本</dt><dd className="server-about-value">{server.sing_box_version||'—'}</dd></div>
          <div className="server-about-item"><dt className="server-about-label">最后心跳</dt><dd className="server-about-value">{server.telemetry_updated_at ? formatTableTime(server.telemetry_updated_at) : '—'}</dd></div>
        </dl>
      </section>
    </div>
  )
}
