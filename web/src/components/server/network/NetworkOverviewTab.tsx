import React from 'react'
import type { Server } from '../../proxy-path/types'

function formatTableTime(v: string) {
  const d=new Date(v); if(Number.isNaN(d.getTime())) return v; const pad=(n:number)=>String(n).padStart(2,'0'); return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function labelValue(v:any){ const map:Record<string,string>={ auto:'自动', ipv4_only:'仅 IPv4', ipv6_only:'仅 IPv6', dual_stack:'双栈', prefer_ipv4:'优先 IPv4', prefer_ipv6:'优先 IPv6', allow:'允许', block:'阻断', uot:'UoT', ipv4:'IPv4', ipv6:'IPv6', custom:'自定义', dual:'双栈'}; return map[String(v)] || String(v) }

export function NetworkOverviewTab({ server, connectivity }: { server: Server; connectivity?: any }) {
  const checks = [
    { label:'公网地址', value: server.public_ipv4 || server.public_ipv6 ? '正常':'待检测', tone: server.public_ipv4||server.public_ipv6 ? 'ok':'warn' },
    { label:'IPv4', value: server.public_ipv4 ? '正常':'未配置', tone: server.public_ipv4 ? 'ok':'warn' },
    { label:'IPv6', value: server.public_ipv6 || server.interface_ipv6 ? '正常':'未配置', tone: server.public_ipv6||server.interface_ipv6 ? 'ok':'warn' },
    { label:'DNS', value: connectivity ? '正常':'待检测', tone:'ok' },
    { label:'MTU', value: server.mtu_value ? String(server.mtu_value) : String(server.mtu_mode||'自动'), tone:'ok' },
    { label:'UDP', value: labelValue(server.udp_inbound_mode||'allow'), tone:'ok' },
  ]
  return (
    <div className="server-network-overview">
      <section className="server-detail-section">
        <h3>网络状态概览</h3>
        <dl className="server-detail-grid">
          <div className="server-about-item"><span className="server-about-label">公网 IPv4</span><span className="server-about-value">{server.public_ipv4 || '待检测'}</span></div>
          <div className="server-about-item"><span className="server-about-label">公网 IPv6</span><span className="server-about-value">{server.public_ipv6 || server.interface_ipv6 || '待检测'}</span></div>
          <div className="server-about-item"><span className="server-about-label">入口地址模式</span><span className="server-about-value">{labelValue(server.entry_ip_mode||'auto')}</span></div>
          <div className="server-about-item"><span className="server-about-label">当前入口地址</span><span className="server-about-value">{server.entry_address || server.public_ipv4 || server.public_ipv6 || '待检测'}</span></div>
          <div className="server-about-item"><span className="server-about-label">监听地址</span><span className="server-about-value">{server.listen_ip || '0.0.0.0'} · {labelValue(server.listen_mode||'auto')}</span></div>
          <div className="server-about-item"><span className="server-about-label">IP Stack</span><span className="server-about-value">{labelValue(server.ip_stack||'auto')}</span></div>
          <div className="server-about-item"><span className="server-about-label">UDP 入站模式</span><span className="server-about-value">{labelValue(server.udp_inbound_mode||'allow')}</span></div>
          <div className="server-about-item"><span className="server-about-label">端口范围</span><span className="server-about-value">{server.port_range_start && server.port_range_end ? `${server.port_range_start}-${server.port_range_end}` : '—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">内部回环端口</span><span className="server-about-value">{server.internal_port_range_start && server.internal_port_range_end ? `${server.internal_port_range_start}-${server.internal_port_range_end}` : '—'}</span></div>
          <div className="server-about-item"><span className="server-about-label">当前 MTU</span><span className="server-about-value">{server.mtu_value||'自动'} · {server.mtu_mode||'detect'}</span></div>
        </dl>
      </section>
      <section className="server-detail-section">
        <h3>网络健康状态</h3>
        <div className="server-health-grid">
          {checks.map(c=> (
            <div key={c.label} className={`server-health-card ${c.tone}`}>
              <span className="server-health-label">{c.label}</span>
              <span className="server-health-value">{c.value}</span>
            </div>
          ))}
        </div>
        {server.last_seen_at && <small className="muted">最近网络检查：{formatTableTime(server.last_seen_at)}</small>}
      </section>
    </div>
  )
}
