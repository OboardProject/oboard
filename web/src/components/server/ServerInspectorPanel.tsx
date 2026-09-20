import * as React from 'react'
import { InspectorPanel } from '../ui/inspector-panel'
import { RegionFlag } from '../ui/RegionFlag'
import { formatBytes } from '../../shared/presentation'
import type { Server } from '../proxy-path/types'

export type Role = 'admin' | 'operator' | 'viewer' | 'none'
import {
  Settings,
  Activity,
  Terminal,
  Network,
  Cpu,
  HardDrive,
  Clock,
  ArrowUp,
  ArrowDown,
  ChevronRight,
  ExternalLink,
} from 'lucide-react'

export interface ServerInspectorPanelProps {
  server: Server | null
  role?: Role
  onClose: () => void
  onAction: (type: string, server: Server) => void
}

function formatByteRate(bytesPerSec = 0) {
  if (bytesPerSec <= 0) return '0 B/s'
  if (bytesPerSec < 1024) return `${bytesPerSec} B/s`
  if (bytesPerSec < 1024 * 1024) return `${(bytesPerSec / 1024).toFixed(1)} KB/s`
  if (bytesPerSec < 1024 * 1024 * 1024) return `${(bytesPerSec / (1024 * 1024)).toFixed(1)} MB/s`
  return `${(bytesPerSec / (1024 * 1024 * 1024)).toFixed(2)} GB/s`
}

function serverRegionCode(server?: Pick<Server, 'region_mode' | 'region_code' | 'detected_region_code'> | null) {
  if (!server) return ''
  const raw = server.region_mode === 'manual' ? server.region_code : server.detected_region_code
  const v = String(raw || '').trim().toUpperCase()
  return /^[A-Z]{2}$/.test(v) ? v : ''
}

export function ServerInspectorPanel({
  server,
  role = 'viewer',
  onClose,
  onAction,
}: ServerInspectorPanelProps) {
  if (!server) return null

  const isOnline = String(server.status || '').toLowerCase() === 'online'
  const enrolled = Boolean(String(server.agent_id || '').trim())
  const cpuPercent = Number.isFinite(server.cpu_usage_percent) ? Math.round(Number(server.cpu_usage_percent)) : 0
  const memTotal = Number(server.memory_total_bytes || 0)
  const memUsed = Number(server.memory_used_bytes || 0)
  const memPercent = memTotal > 0 ? Math.min(100, Math.round((memUsed / memTotal) * 100)) : 0

  const diskTotal = Number(server.disk_total_bytes || 0)
  const diskUsed = Number(server.disk_bytes || 0)
  const diskPercent = diskTotal > 0 ? Math.min(100, Math.round((diskUsed / diskTotal) * 100)) : 0

  const uploadSpeed = formatByteRate(server.network_upload_bps || 0)
  const downloadSpeed = formatByteRate(server.network_download_bps || 0)

  const region = serverRegionCode(server)

  const statusBadge = (
    <span
      className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-xs font-medium ${
        !enrolled
          ? 'bg-neutral-100 text-neutral-600 dark:bg-neutral-800 dark:text-neutral-400'
          : isOnline
          ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400'
          : 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-400'
      }`}
    >
      <span
        className={`w-1.5 h-1.5 rounded-full ${
          !enrolled ? 'bg-neutral-400' : isOnline ? 'bg-emerald-500' : 'bg-red-500'
        }`}
      />
      {!enrolled ? '未接入' : isOnline ? '在线' : '离线'}
    </span>
  )

  return (
    <InspectorPanel
      isOpen={Boolean(server)}
      onClose={onClose}
      title={
        <div className="flex items-center gap-2 min-w-0">
          <RegionFlag code={region} size={20} />
          <span className="truncate font-semibold text-sm">
            {server.name || `服务器 #${server.id}`}
          </span>
          <span className="text-xs text-muted font-normal">#{server.id}</span>
        </div>
      }
      badge={statusBadge}
      ariaLabel={`${server.name || '服务器'} 概览面板`}
      width={420}
    >
      {/* Action Shortcut Bar */}
      <div className="grid grid-cols-4 gap-2 pb-2">
        <button
          type="button"
          onClick={() => onAction('basic-settings', server)}
          className="flex flex-col items-center justify-center p-2 rounded-lg border border-border hover:bg-neutral-50 dark:hover:bg-neutral-800/50 transition-colors text-xs text-foreground group"
          title="基础设置"
        >
          <Settings size={16} className="mb-1 text-muted group-hover:text-foreground transition-colors" />
          <span>设置</span>
        </button>

        <button
          type="button"
          onClick={() => onAction('network', server)}
          className="flex flex-col items-center justify-center p-2 rounded-lg border border-border hover:bg-neutral-50 dark:hover:bg-neutral-800/50 transition-colors text-xs text-foreground group"
          title="网络工具与 DNS"
        >
          <Network size={16} className="mb-1 text-muted group-hover:text-foreground transition-colors" />
          <span>网络</span>
        </button>

        <button
          type="button"
          onClick={() => onAction('system', server)}
          className="flex flex-col items-center justify-center p-2 rounded-lg border border-border hover:bg-neutral-50 dark:hover:bg-neutral-800/50 transition-colors text-xs text-foreground group"
          title="Agent 维护与配置"
        >
          <Activity size={16} className="mb-1 text-muted group-hover:text-foreground transition-colors" />
          <span>维护</span>
        </button>

        <button
          type="button"
          onClick={() => onAction('terminal', server)}
          disabled={!isOnline}
          className="flex flex-col items-center justify-center p-2 rounded-lg border border-border hover:bg-neutral-50 dark:hover:bg-neutral-800/50 transition-colors text-xs text-foreground group disabled:opacity-40 disabled:cursor-not-allowed"
          title={isOnline ? '连接 Web 终端' : '服务器离线'}
        >
          <Terminal size={16} className="mb-1 text-muted group-hover:text-foreground transition-colors" />
          <span>终端</span>
        </button>
      </div>

      {/* Real-time Hardware Telemetry Gauges */}
      <div className="space-y-3 p-3.5 rounded-lg border border-border bg-neutral-50/50 dark:bg-neutral-900/30">
        <div className="text-xs font-semibold text-muted uppercase tracking-wider">实时资源指标</div>

        {/* CPU */}
        <div>
          <div className="flex justify-between text-xs mb-1">
            <span className="flex items-center gap-1.5 text-secondary">
              <Cpu size={13} className="text-muted" /> CPU 使用率
            </span>
            <span className="font-mono font-medium text-foreground">{cpuPercent}%</span>
          </div>
          <div className="h-1.5 w-full bg-neutral-200 dark:bg-neutral-800 rounded-full overflow-hidden">
            <div
              className={`h-full rounded-full transition-all duration-300 ${
                cpuPercent > 85 ? 'bg-red-500' : cpuPercent > 65 ? 'bg-amber-500' : 'bg-neutral-700 dark:bg-neutral-300'
              }`}
              style={{ width: `${cpuPercent}%` }}
            />
          </div>
        </div>

        {/* Memory */}
        <div>
          <div className="flex justify-between text-xs mb-1">
            <span className="flex items-center gap-1.5 text-secondary">
              <Activity size={13} className="text-muted" /> 内存使用
            </span>
            <span className="font-mono font-medium text-foreground">
              {memTotal > 0 ? `${formatBytes(memUsed)} / ${formatBytes(memTotal)} (${memPercent}%)` : '—'}
            </span>
          </div>
          <div className="h-1.5 w-full bg-neutral-200 dark:bg-neutral-800 rounded-full overflow-hidden">
            <div
              className={`h-full rounded-full transition-all duration-300 ${
                memPercent > 90 ? 'bg-red-500' : memPercent > 75 ? 'bg-amber-500' : 'bg-neutral-700 dark:bg-neutral-300'
              }`}
              style={{ width: `${memPercent}%` }}
            />
          </div>
        </div>

        {/* Disk */}
        {diskTotal > 0 && (
          <div>
            <div className="flex justify-between text-xs mb-1">
              <span className="flex items-center gap-1.5 text-secondary">
                <HardDrive size={13} className="text-muted" /> 磁盘使用
              </span>
              <span className="font-mono font-medium text-foreground">
                {formatBytes(diskUsed)} / {formatBytes(diskTotal)} ({diskPercent}%)
              </span>
            </div>
            <div className="h-1.5 w-full bg-neutral-200 dark:bg-neutral-800 rounded-full overflow-hidden">
              <div
                className={`h-full rounded-full transition-all duration-300 ${
                  diskPercent > 90 ? 'bg-red-500' : diskPercent > 75 ? 'bg-amber-500' : 'bg-neutral-700 dark:bg-neutral-300'
                }`}
                style={{ width: `${diskPercent}%` }}
              />
            </div>
          </div>
        )}

        {/* Network Speeds */}
        <div className="pt-2 border-t border-border/60 grid grid-cols-2 gap-3 text-xs">
          <div>
            <span className="text-muted flex items-center gap-1 mb-0.5">
              <ArrowUp size={12} className="text-emerald-600" /> 上传速率
            </span>
            <span className="font-mono font-medium text-foreground text-sm">{uploadSpeed}</span>
          </div>
          <div>
            <span className="text-muted flex items-center gap-1 mb-0.5">
              <ArrowDown size={12} className="text-blue-600" /> 下载速率
            </span>
            <span className="font-mono font-medium text-foreground text-sm">{downloadSpeed}</span>
          </div>
        </div>
      </div>

      {/* Network & Inbound Information */}
      <div className="space-y-2.5">
        <div className="text-xs font-semibold text-muted uppercase tracking-wider">网络与接入</div>
        <dl className="grid grid-cols-1 gap-2 text-xs">
          <div className="flex items-center justify-between py-1 border-b border-border/50">
            <dt className="text-secondary">公网 IPv4</dt>
            <dd className="font-mono text-foreground select-all">{server.public_ipv4 || '—'}</dd>
          </div>
          {server.public_ipv6 && (
            <div className="flex items-center justify-between py-1 border-b border-border/50">
              <dt className="text-secondary">公网 IPv6</dt>
              <dd className="font-mono text-foreground truncate max-w-[200px] select-all" title={server.public_ipv6}>
                {server.public_ipv6}
              </dd>
            </div>
          )}
          <div className="flex items-center justify-between py-1 border-b border-border/50">
            <dt className="text-secondary">活跃连接数</dt>
            <dd className="font-mono text-foreground">
              TCP {server.tcp_connection_count ?? '—'} / UDP {server.udp_connection_count ?? '—'}
            </dd>
          </div>
          <div className="flex items-center justify-between py-1 border-b border-border/50">
            <dt className="text-secondary">Agent 版本</dt>
            <dd className="font-mono text-foreground">{server.agent_version || '—'}</dd>
          </div>
          <div className="flex items-center justify-between py-1 border-b border-border/50">
            <dt className="text-secondary">sing-box 核心</dt>
            <dd className="font-mono text-foreground">{server.sing_box_version || '—'}</dd>
          </div>
          <div className="flex items-center justify-between py-1 border-b border-border/50">
            <dt className="text-secondary">系统平台</dt>
            <dd className="text-foreground truncate max-w-[200px]" title={`${server.distro_name || server.os || ''} (${server.arch || ''})`}>
              {server.distro_name || server.os || '—'} {server.arch ? `· ${server.arch}` : ''}
            </dd>
          </div>
        </dl>
      </div>

      {/* View full details button */}
      <div className="pt-2">
        <button
          type="button"
          onClick={() => onAction('about', server)}
          className="w-full flex items-center justify-center gap-1.5 py-2 px-3 text-xs font-medium border border-border rounded-lg text-secondary hover:text-foreground hover:bg-neutral-50 dark:hover:bg-neutral-800 transition-colors"
        >
          <span>查看全部系统参数与技术指标</span>
          <ChevronRight size={14} />
        </button>
      </div>
    </InspectorPanel>
  )
}
