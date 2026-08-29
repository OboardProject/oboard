import { describe, expect, it } from 'vitest'
import { configurationSyncAgentReachable, configurationSyncBusyRows, configurationSyncBusyStateLabel, configurationSyncFailureIssues, configurationSyncPresentation, mergeConfigurationMutationResponse, mergeConfigurationSyncResponse, MutationActivityTracker } from './configuration-sync'

describe('configuration sync feedback', () => {
  it('shows local saving feedback synchronously before a response exists', () => {
    expect(configurationSyncPresentation([], true, false)).toMatchObject({ tone: 'info', label: '正在保存...', busy: true })
  })

  it('uses only failed servers for the explicit retry action', () => {
    const status = configurationSyncPresentation([
      { server_id: 1, state: 'synced' },
      { server_id: 2, state: 'failed', error: 'prepare failed' },
      { server_id: 3, state: 'failed', error: 'agent failed' },
    ])
    expect(status).toMatchObject({ tone: 'danger', label: '配置同步被阻塞 · 2 个问题', retryServerIDs: [2, 3], busy: false })
  })

  it('deduplicates repeated server failures and explains the direct-branch conflict', () => {
    const rows = Array.from({ length: 8 }, (_, index) => ({
      server_id: index + 1,
      state: 'failed',
      error: '入口 15 的直接出口分支「东京直出」与「备用直出」位于同一位置；请删除或停用其中一条后再同步',
      task_id: 100 + index,
    }))
    const issues = configurationSyncFailureIssues(rows)
    expect(issues).toHaveLength(1)
    expect(issues[0]).toMatchObject({
      title: '入口 15 存在重复的直接出口分支',
      serverIDs: [1, 2, 3, 4, 5, 6, 7, 8],
      targetTab: 'proxy-paths',
      targetLabel: '打开代理拓扑',
      inboundID: 15,
      conflictingPathNames: ['东京直出', '备用直出'],
    })
    expect(issues[0].resolution).toContain('删除或停用同一位置的重复直出分支')
    expect(configurationSyncPresentation(rows).label).toBe('配置同步被阻塞 · 1 个问题')
    expect(configurationSyncFailureIssues([{ server_id: 1, state: 'failed', error: '入口 15 已存在相同位置的直接出口分支' }])[0].inboundID).toBe(15)
  })

  it('lists busy sync rows and labels their in-flight state', () => {
    const rows = [
      { server_id: 1, state: 'running' },
      { server_id: 2, state: 'queued' },
      { server_id: 3, state: 'synced' },
      { server_id: 4, state: 'failed' },
      { server_id: 5, state: 'preparing' },
      { server_id: 6, state: 'pending' },
    ]
    expect(configurationSyncBusyRows(rows).map(item => item.server_id)).toEqual([1, 2, 5, 6])
    expect(configurationSyncBusyStateLabel('running')).toBe('下发中')
    expect(configurationSyncBusyStateLabel('queued')).toBe('排队中')
    expect(configurationSyncBusyStateLabel('preparing')).toBe('准备中')
    expect(configurationSyncBusyStateLabel('pending')).toBe('等待中')
  })

  it('ignores offline and unenrolled servers in the sync wait', () => {
    const rows = [
      { server_id: 1, state: 'queued' as const, agent_reachable: false },
      { server_id: 2, state: 'pending' as const },
      { server_id: 3, state: 'running' as const },
      { server_id: 4, state: 'synced' as const },
    ]
    const servers = [
      { id: 2, name: '未接入节点', status: 'unknown', agent_id: '' },
      { id: 3, name: '在线入口', status: 'online', agent_id: 'agent-3' },
      { id: 4, name: '已同步', status: 'online', agent_id: 'agent-4' },
    ]
    expect(configurationSyncAgentReachable(rows[0], servers)).toBe(false)
    expect(configurationSyncBusyRows(rows, servers).map(item => item.server_id)).toEqual([3])
    expect(configurationSyncPresentation(rows, false, false, servers)).toMatchObject({ tone: 'info', label: '正在同步 1 台服务器', busy: true })
    expect(configurationSyncPresentation([
      { server_id: 1, state: 'queued', agent_reachable: false },
      { server_id: 2, state: 'pending' },
      { server_id: 4, state: 'synced' },
    ], false, false, servers)).toMatchObject({ tone: 'ok', label: '配置已同步', busy: false })
    expect(configurationSyncPresentation([
      { server_id: 1, state: 'queued', agent_reachable: false },
      { server_id: 2, state: 'pending' },
    ], false, false, servers)).toMatchObject({ tone: 'warn', label: '配置已保存', busy: false })
  })

  it('merges desired revision and sync rows without discarding page entities', () => {
    const current = { servers: [{ id: 1, name: 'edge' }], desired_revision: 4, configuration_sync: [] }
    const next = mergeConfigurationSyncResponse(current, { desired_revision: 5, configuration_sync: [{ server_id: 1, state: 'pending' }] })
    expect(next.servers).toBe(current.servers)
    expect(next.desired_revision).toBe(5)
    expect(next.configuration_sync).toEqual([{ server_id: 1, state: 'pending' }])
  })

  it('does not mutate cached data when a failed response has no sync metadata', () => {
    const current = { servers: [{ id: 1 }], configuration_sync: [{ server_id: 1, state: 'synced' }] }
    expect(mergeConfigurationSyncResponse(current, { error: 'validation failed' })).toBe(current)
  })

  it('patches the returned entity and deletes only the confirmed row', () => {
    const current = { inbounds: [{ id: 1, name: 'old' }, { id: 2, name: 'keep' }] }
    const updated = mergeConfigurationMutationResponse(current, { inbound: { id: 1, name: 'new' } }, '/inbounds/1')
    expect(updated.inbounds).toEqual([{ id: 1, name: 'new' }, { id: 2, name: 'keep' }])
    const deleted = mergeConfigurationMutationResponse(updated, { deleted: true }, '/inbounds/1')
    expect(deleted.inbounds).toEqual([{ id: 2, name: 'keep' }])
    expect(current.inbounds).toEqual([{ id: 1, name: 'old' }, { id: 2, name: 'keep' }])
  })

  it('keeps saving active until all concurrent mutations settle and rolls back on extra completions', () => {
    const tracker = new MutationActivityTracker()
    expect(tracker.update(true)).toBe(true)
    expect(tracker.update(true)).toBe(true)
    expect(tracker.count).toBe(2)
    expect(tracker.update(false)).toBe(true)
    expect(tracker.update(false)).toBe(false)
    expect(tracker.update(false)).toBe(false)
    expect(tracker.count).toBe(0)
  })
})
