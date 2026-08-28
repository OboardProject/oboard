import React, { useState } from 'react'
import { Copy, Check, Info, Globe, HardDrive, Cpu } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import type { Server } from '../proxy-path/types'

function RegionFlag({ code, size = 22 }: { code?: string; size?: number }) {
  const v = String(code || '').trim().toUpperCase()
  if (!/^[A-Z]{2}$/.test(v)) return <span style={{ fontSize: size * 0.7 }}>🌐</span>
  const flag = String.fromCodePoint(...Array.from(v).map(c => 127397 + c.charCodeAt(0)))
  return <span style={{ fontSize: size * 0.85 }}>{flag}</span>
}
function serverRegionCode(s?: Pick<Server, 'region_mode' | 'region_code' | 'detected_region_code'>) {
  if (!s) return ''
  const raw = s.region_mode === 'manual' ? s.region_code : s.detected_region_code
  const v = String(raw || '').trim().toUpperCase()
  return /^[A-Z]{2}$/.test(v) ? v : ''
}
function formatTableTime(v: string) {
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function formatBytes(v: number) {
  if (!v) return '0 B'
  const units = ['B','KB','MB','GB','TB']
  let n = Number(v); let i=0
  while (n>=1024 && i<units.length-1){n/=1024;i++}
  return `${n>=10||i===0 ? n.toFixed(0): n.toFixed(1)} ${units[i]}`
}
async function copyText(value: string) {
  const text = String(value||'')
  if (!text) return false
  try { if (navigator.clipboard?.writeText && window.isSecureContext){ await navigator.clipboard.writeText(text); return true } } catch {}
  const ta = document.createElement('textarea'); ta.value=text; ta.style.position='fixed'; ta.style.left='-9999px'; document.body.appendChild(ta); ta.select(); try{return document.execCommand('copy')}catch{return false}finally{document.body.removeChild(ta)}
}
function compactBuildLabel(value?: string) {
  const b = String(value||'').trim(); if(/^\d{14}$/.test(b)) return `${b.slice(4,8)}.${b.slice(8,12)}`; if(b.length>10) return b.slice(-10); return b||'—'
}
function serverOSLabel(s: Server) { return [s.distro_name || s.os, s.distro_version].filter(Boolean).join(' ') || '未知系统' }
function serverDefaultEntryAddress(server?: Server) {
  if (!server) return ''
  const mode = server.entry_ip_mode || 'auto'
  if (mode==='ipv4') return server.public_ipv4||''
  if (mode==='ipv6') return server.public_ipv6 || server.interface_ipv6 || ''
  if (mode==='custom') return server.entry_address||''
  return server.public_ipv4 || server.public_ipv6 || server.interface_ipv6 || ''
}

function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  if (!value || value==='—') return null
  return (
    <button type="button" className="ghost icon-button" style={{ width:22, height:22, minHeight:22, minWidth:22 }} onClick={async()=>{ const ok=await copyText(value); if(ok){setCopied(true); setTimeout(()=>setCopied(false),1200)}}} aria-label="复制" title="复制">
      {copied ? <Check size={12}/> : <Copy size={12}/>}
    </button>
  )
}
function DetailItem({ label, value, copyValue }: { label: string; value: React.ReactNode; copyValue?: string }) {
  return (
    <div className="server-about-item">
      <span className="server-about-label">{label}</span>
      <span className="server-about-value">{value} {copyValue ? <CopyButton value={copyValue}/> : null}</span>
    </div>
  )
}

export function ServerAboutDialog({ server, onClose }: { server: Server; onClose: () => void }) {
  const isOnline = String(server.status||'').toLowerCase()==='online'
  const region = serverRegionCode(server)
  return (
    <MotionDialogPanel onCancel={onClose} className="server-detail-dialog server-about-dialog">
      <header className="dialog-head server-detail-head">
        <div className="server-detail-title">
          <RegionFlag code={region} size={28} />
          <div>
            <h2>关于 · {server.name || `服务器 #${server.id}`}</h2>
            <p className="muted">#{server.id} · {isOnline ? '在线' : '离线'} · 创建于 {server.created_at ? formatTableTime(server.created_at) : '—'}</p>
          </div>
        </div>
        <div className="server-detail-head-actions">
          <span className={`server-detail-status ${isOnline ? 'online' : 'offline'}`}><i />{isOnline ? '在线' : '离线'}</span>
          <button className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭">×</button>
        </div>
      </header>
      <div className="dialog-body server-detail-body">
        <section className="server-detail-section">
          <div className="server-detail-section-head"><Info size={15} /><h3>身份信息</h3></div>
          <div className="server-about-grid">
            <DetailItem label="服务器名称" value={server.name || `server-${server.id}`} copyValue={String(server.name||'')} />
            <DetailItem label="Server ID" value={String(server.id)} copyValue={String(server.id)} />
            <DetailItem label="地区" value={region ? `${region} ${serverRegionLabel(region)}` : '地区待检测'} />
            <DetailItem label="状态" value={isOnline ? '在线' : '离线'} />
            <DetailItem label="创建时间" value={server.created_at ? formatTableTime(server.created_at) : '—'} />
            <DetailItem label="到期时间" value={server.expires_at ? formatTableTime(server.expires_at) : '未设置'} />
          </div>
        </section>

        <section className="server-detail-section">
          <div className="server-detail-section-head"><Globe size={15} /><h3>网络身份</h3></div>
          <div className="server-about-grid">
            <DetailItem label="IPv4" value={server.public_ipv4 || '待检测'} copyValue={server.public_ipv4} />
            <DetailItem label="IPv6" value={server.public_ipv6 || server.interface_ipv6 || '待检测'} copyValue={server.public_ipv6 || server.interface_ipv6} />
            <DetailItem label="当前入口地址" value={serverDefaultEntryAddress(server) || '待检测'} copyValue={serverDefaultEntryAddress(server)} />
            <DetailItem label="最近在线时间" value={server.last_seen_at ? formatTableTime(server.last_seen_at) : '—'} />
          </div>
        </section>

        <section className="server-detail-section">
          <div className="server-detail-section-head"><HardDrive size={15} /><h3>系统信息</h3></div>
          <div className="server-about-grid">
            <DetailItem label="操作系统" value={serverOSLabel(server)} />
            <DetailItem label="发行版" value={[server.distro_id, server.distro_version].filter(Boolean).join(' ') || '—'} />
            <DetailItem label="架构" value={server.arch || '未知架构'} />
            <DetailItem label="CPU" value={server.cpu || '—'} />
            <DetailItem label="CPU 核心数" value={server.cpu_cores ? String(server.cpu_cores) : '—'} />
            <DetailItem label="内存" value={server.memory_total_bytes ? `${formatBytes(server.memory_used_bytes||0)} / ${formatBytes(server.memory_total_bytes)}` : '—'} />
            <DetailItem label="磁盘" value={server.disk_total_bytes ? `${formatBytes(server.disk_bytes||0)} / ${formatBytes(server.disk_total_bytes)}` : formatBytes(server.disk_bytes||0) || '—'} />
          </div>
        </section>

        <section className="server-detail-section">
          <div className="server-detail-section-head"><Cpu size={15} /><h3>OBoard 运行环境</h3></div>
          <div className="server-about-grid">
            <DetailItem label="Agent 版本" value={server.agent_version || '—'} copyValue={server.agent_version} />
            <DetailItem label="Agent Build" value={server.agent_build ? compactBuildLabel(server.agent_build) : '—'} copyValue={server.agent_build} />
            <DetailItem label="oboard-sb 版本" value={server.sing_box_version || '—'} copyValue={server.sing_box_version} />
            <DetailItem label="最后心跳" value={server.telemetry_updated_at ? formatTableTime(server.telemetry_updated_at) : '—'} />
            <DetailItem label="最后部署版本" value={(server as any).last_deployment_version ? String((server as any).last_deployment_version) : '—'} />
          </div>
        </section>
      </div>
      <footer className="dialog-actions"><button type="button" onClick={onClose}>关闭</button></footer>
    </MotionDialogPanel>
  )
}

function serverRegionLabel(code?: string) {
  const v = String(code||'').trim().toUpperCase()
  if (!v) return '地区待检测'
  const overrides: Record<string,string> = { CN:'中国', HK:'香港', MO:'澳门', TW:'台湾' }
  if (overrides[v]) return overrides[v]
  try {
    const dn = new (Intl as any).DisplayNames(['zh-CN'], { type:'region' })
    const l = dn?.of(v)
    return l && l!==v ? l : v
  } catch { return v }
}
