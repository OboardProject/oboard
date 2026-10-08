import React from 'react'
import { Button } from '../../components/ui/button'
import { Badge } from '../../components/ui/badge'
import { formatTime } from './domain'
import type { InstancePage, ViewNode, ViewTone } from './types'

const bindingLabels: Record<string, string> = {
  server_id: '服务器', name: '名称', status: '状态', online: '在线', region_code: '地区',
  public_ipv4: 'IPv4', public_ipv6: 'IPv6', enrolled: '已接入', agent_version: 'Agent',
  last_seen_at: '最近在线', agent_connected: 'Agent 连接', stale: '数据过期', config_sync: '配置同步',
  connectivity_status: '连通性', core_version: '内核', cpu_usage_percent: 'CPU',
  memory_used_bytes: '已用内存', memory_total_bytes: '内存总量', disk_used_bytes: '已用磁盘',
  disk_total_bytes: '磁盘总量', tcp_connections: 'TCP 连接', udp_connections: 'UDP 连接',
  network_upload_bps: '上行', network_download_bps: '下行', observed_at: '采样时间', exists: '有采样',
}

function toneVariant(tone?: ViewTone): 'success' | 'warning' | 'destructive' | 'secondary' {
  if (tone === 'success') return 'success'
  if (tone === 'warning') return 'warning'
  if (tone === 'danger') return 'destructive'
  return 'secondary'
}

function displayValue(value: unknown) {
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : ''
  if (typeof value === 'string') return value
  return ''
}

function Block({ node, canExecute, busy, onAction }: { node: ViewNode; canExecute: boolean; busy: boolean; onAction: (action: string) => void }) {
  switch (node.type) {
    case 'stack':
      return <div className="plugin-view-stack">{node.children.map((child, index) => <Block key={index} node={child} canExecute={canExecute} busy={busy} onAction={onAction} />)}</div>
    case 'heading':
      return <h4 className="plugin-subhead">{node.text}</h4>
    case 'text':
      return <p className="plugin-view-text">{node.text}</p>
    case 'empty':
      return <p className="plugin-env-help">{node.text}</p>
    case 'metric':
      return <div className="plugin-metric" data-tone={node.tone || 'neutral'}><span>{node.label}</span><strong>{node.value}</strong></div>
    case 'badge':
      return <Badge variant={toneVariant(node.tone)}>{node.text}</Badge>
    case 'button':
      return <Button size="sm" variant="outline" disabled={!canExecute || busy} onClick={() => onAction(node.action)}>{node.label}</Button>
    case 'table':
      return <div className="plugin-view-table-wrap"><table className="plugin-view-table">
        <thead><tr>{node.columns.map((column, index) => <th key={index} scope="col">{column.label}</th>)}</tr></thead>
        <tbody>{node.rows.map((row, index) => <tr key={index}>{row.map((cell, cellIndex) => <td key={cellIndex}>{cell}</td>)}</tr>)}</tbody>
      </table></div>
    case 'binding':
      if (node.error) return <p className="plugin-field-error" role="alert">{node.error.message}</p>
      if (!node.data) return <p className="plugin-env-help">没有数据</p>
      return <dl className="plugin-facts">{Object.entries(node.data).map(([key, value]) => {
        const text = displayValue(value)
        if (!text && typeof value !== 'boolean') return null
        return <div key={key}><dt>{bindingLabels[key] || key}</dt><dd>{text}</dd></div>
      })}</dl>
    default:
      return null
  }
}

export function PageView({ pages, pageID, onSelect, canExecute, busy, onRefresh, onAction }: {
  pages: InstancePage[]
  pageID: string
  onSelect: (id: string) => void
  canExecute: boolean
  busy: boolean
  onRefresh: (page: InstancePage) => void
  onAction: (page: InstancePage, action: string) => void
}) {
  const page = pages.find(item => item.id === pageID) || pages[0]
  if (!page) return <p className="text-sm text-muted-foreground">这个插件没有声明界面。</p>
  return <div className="plugin-view">
    {pages.length > 1 && <div className="plugin-page-switch" role="tablist" aria-label="插件界面">
      {pages.map(item => <button key={item.id} type="button" role="tab" aria-selected={item.id === page.id} className={item.id === page.id ? 'active' : ''} onClick={() => onSelect(item.id)}>{item.title}</button>)}
    </div>}
    <div className="plugin-view-bar">
      <p className="plugin-env-help">{page.document ? `更新于 ${formatTime(page.published_at)}` : '还没有界面数据'}</p>
      <Button size="sm" variant="outline" disabled={!canExecute || busy} onClick={() => onRefresh(page)}>刷新</Button>
    </div>
    {page.document ? <div className="plugin-view-body">
      {page.document.title && <h3 className="plugin-view-title">{page.document.title}</h3>}
      {page.document.body.map((node, index) => <Block key={index} node={node} canExecute={canExecute} busy={busy} onAction={action => onAction(page, action)} />)}
    </div> : <p className="text-sm text-muted-foreground">点击「刷新」后，插件会生成这一页。打开页面本身不会运行插件。</p>}
  </div>
}
