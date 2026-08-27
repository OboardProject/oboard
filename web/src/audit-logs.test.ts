import { describe, expect, it } from 'vitest'

import {
  describeAuditLog,
  formatAuditTimeRange,
  formatDurationSeconds,
  groupConsecutiveAuditLogs,
  mixCopy,
  type AuditLogRow,
} from './audit-logs'

const users = [{ id: 7, username: 'StarLight' }]

function oauthRefresh(id: number, at: string, extra: Partial<AuditLogRow> = {}): AuditLogRow {
  return {
    id,
    actor_id: 7,
    action: 'oauth_token_refreshed',
    target: 'oauth_grant',
    detail: JSON.stringify({
      client_id: 'oboard-web',
      flow: 'refresh_token',
      offline_access: true,
      resource: 'https://panel.example.com/api/v1/mcp',
      scope: 'mcp:read mcp:write',
      target_id: `grant-${id}`,
    }),
    ip: '172.68.119.180',
    created_at: at,
    ...extra,
  }
}

describe('audit log copy', () => {
  it('inserts a space between Chinese and Latin fragments', () => {
    expect(mixCopy('用户 StarLightoauth_token_refreshed了')).toBe('用户 StarLightoauth_token_refreshed 了')
    expect(mixCopy('系统node_incident_resolved了49')).toBe('系统 node_incident_resolved 了 49')
  })

  it('turns OAuth refresh JSON into a short Chinese sentence', () => {
    const item = describeAuditLog(oauthRefresh(11, '2026-08-27T17:03:00Z'), { users })
    expect(item.actionLabel).toBe('刷新令牌')
    expect(item.title).toBe('用户 StarLight 刷新了面板访问令牌')
    expect(item.targetLabel).toBe('面板')
    expect(item.detail).toBe('')
    expect(item.title).not.toContain('{')
    expect(item.targetLabel).not.toContain('client_id')
  })

  it('keeps Latin application names spaced in Chinese sentences', () => {
    const item = describeAuditLog({
      ...oauthRefresh(12, '2026-08-27T17:03:00Z'),
      detail: JSON.stringify({ client_id: 'cursor-mcp', flow: 'refresh_token' }),
    }, { users })
    expect(item.title).toBe('用户 StarLight 刷新了应用 cursor-mcp 的访问令牌')
    expect(item.targetLabel).toBe('应用 cursor-mcp')
  })

  it('summarizes recovered node incidents without raw id pairs', () => {
    const item = describeAuditLog({
      id: 4,
      action: 'node_incident_resolved',
      target: 'node_incident',
      detail: '49:1011',
      ip: 'controller',
      created_at: '2026-08-27T16:55:00Z',
    })
    expect(item.actionLabel).toBe('节点恢复')
    expect(item.title).toBe('系统恢复了节点故障')
    expect(item.actor).toBe('系统')
    expect(item.ip).toBe('系统内部')
    expect(item.targetLabel).toBe('故障 #49')
    expect(item.detail).toBe('中断 16 分 51 秒')
  })

  it('keeps existing login copy', () => {
    const item = describeAuditLog({
      id: 8,
      actor_id: 7,
      action: 'login',
      target: 'user',
      detail: 'StarLight',
      ip: '203.0.113.10',
      created_at: '2026-08-27T10:00:00Z',
    }, { users })
    expect(item.title).toBe('用户 StarLight 登录成功')
  })
})

describe('audit log grouping', () => {
  it('merges consecutive same-kind operations and splits on a different action', () => {
    const rows = [
      oauthRefresh(1, '2026-08-27T17:03:00Z'),
      oauthRefresh(2, '2026-08-27T16:48:00Z'),
      oauthRefresh(3, '2026-08-27T16:33:00Z'),
      oauthRefresh(4, '2026-08-27T16:18:00Z'),
      oauthRefresh(5, '2026-08-27T16:03:00Z'),
      { id: 6, action: 'node_incident_resolved', target: 'node_incident', detail: '49:1011', ip: 'controller', created_at: '2026-08-27T16:00:00Z' },
      oauthRefresh(7, '2026-08-27T15:48:00Z'),
      oauthRefresh(8, '2026-08-27T15:33:00Z'),
    ].map(log => describeAuditLog(log, { users }))

    const groups = groupConsecutiveAuditLogs(rows)
    expect(groups.map(group => [group.actionLabel, group.logs.length])).toEqual([
      ['刷新令牌', 5],
      ['节点恢复', 1],
      ['刷新令牌', 2],
    ])
    expect(groups[0].title).toBe('用户 StarLight 刷新了面板访问令牌')
    expect(groups[0].detail).toBe('')
  })

  it('formats a same-day time range', () => {
    expect(formatAuditTimeRange(
      '2026-08-27T16:03:00Z',
      '2026-08-27T17:03:00Z',
      value => value === '2026-08-27T16:03:00Z' ? '2026-08-27 16:03' : '2026-08-27 17:03',
    )).toBe('2026-08-27 16:03 – 17:03')
  })

  it('formats outage duration in Chinese units', () => {
    expect(formatDurationSeconds(1011)).toBe('16 分 51 秒')
    expect(formatDurationSeconds(60)).toBe('1 分钟')
  })
})
