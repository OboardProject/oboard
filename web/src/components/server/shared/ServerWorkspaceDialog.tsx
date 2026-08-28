import React from 'react'
import { X } from 'lucide-react'
import { MotionDialogPanel } from '../../ui/motion'
import type { Server } from '../../proxy-path/types'

export type WorkspaceTab = { id: string; label: string; disabled?: boolean; hint?: string }

type Props = {
  server: Server
  title: string
  tabs: WorkspaceTab[]
  activeTab: string
  onTabChange: (id: string) => void
  onClose: () => void
  children: React.ReactNode
  footer?: React.ReactNode
  headerExtra?: React.ReactNode
}

function serverStatusLabel(server: Server) {
  const isOnline = String(server.status || '').toLowerCase() === 'online'
  const enrolled = Boolean(String(server.agent_id || '').trim())
  if (!enrolled) return { label: '未接入', tone: 'unenrolled' }
  return isOnline ? { label: '在线', tone: 'online' } : { label: '离线', tone: 'offline' }
}

function RegionFlagInline({ code, size = 20 }: { code?: string; size?: number }) {
  const value = String(code || '').trim().toUpperCase()
  // fallback to emoji
  if (!value || value.length !== 2) return <span style={{ width: size, height: size, display: 'inline-flex', alignItems: 'center', justifyContent: 'center', fontSize: size * 0.7 }}>🌐</span>
  const flag = String.fromCodePoint(...Array.from(value).map(c => 127397 + c.charCodeAt(0)))
  return <span style={{ fontSize: size * 0.85, lineHeight: 1 }} aria-hidden="true">{flag}</span>
}

function serverRegionCodeLocal(server?: Pick<Server, 'region_mode' | 'region_code' | 'detected_region_code'>) {
  if (!server) return ''
  const raw = server.region_mode === 'manual' ? server.region_code : server.detected_region_code
  const v = String(raw || '').trim().toUpperCase()
  return /^[A-Z]{2}$/.test(v) ? v : ''
}

export function ServerWorkspaceDialog({ server, title, tabs, activeTab, onTabChange, onClose, children, footer, headerExtra }: Props) {
  const status = serverStatusLabel(server)
  const region = serverRegionCodeLocal(server)
  return (
    <MotionDialogPanel onCancel={onClose} className="server-workspace-dialog">
      <header className="server-workspace-header">
        <div className="server-workspace-title">
          <RegionFlagInline code={region} size={22} />
          <div className="server-workspace-title-text">
            <h2>{server.name || `服务器 #${server.id}`} <span className="server-workspace-title-id">#{server.id}</span></h2>
            <span className={`server-workspace-status ${status.tone}`}><i className="server-workspace-status-dot" />{status.label}</span>
          </div>
          <span className="server-workspace-subtitle">{title}</span>
        </div>
        <div className="server-workspace-header-actions">
          {headerExtra}
          <button type="button" className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><X size={16} /></button>
        </div>
      </header>
      <div className="server-workspace-tabs" role="tablist" aria-label={`${title} 分类`}>
        {tabs.map(tab => (
          <button
            key={tab.id}
            type="button"
            role="tab"
            aria-selected={activeTab === tab.id}
            className={activeTab === tab.id ? 'active' : tab.disabled ? 'disabled' : ''}
            disabled={Boolean(tab.disabled)}
            title={tab.hint || tab.label}
            onClick={() => !tab.disabled && onTabChange(tab.id)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div className="server-workspace-body">
        {children}
      </div>
      {footer && <footer className="server-workspace-footer">{footer}</footer>}
    </MotionDialogPanel>
  )
}
