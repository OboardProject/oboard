import { describe, expect, it } from 'vitest'
import { deploymentStatusFromSummary, groupTasksForTimeline, latestDeploymentTasks, serverTaskStatusSummary, splitTaskAttempts, taskCategory, taskStatusSummary } from './task-groups'

const task = (id: number, status: string, extra = {}) => ({ id, status, server_id: 1, config_version: 10, type: 'apply_deployment', ...extra })
const status = (tasks: any[]) => deploymentStatusFromSummary(serverTaskStatusSummary(tasks))

describe('task execution outcomes', () => {
  it('uses the latest retry even when an old failure completes later', () => {
    const tasks = [task(2, 'succeeded'), task(1, 'failed', { updated_at: '2099-01-01' })]
    expect(status(tasks)).toBe('succeeded')
    expect(splitTaskAttempts(tasks).history.map(item => item.id)).toEqual([1])
  })
  it('does not hide a new failure or a running retry', () => {
    expect(status([task(1, 'succeeded'), task(2, 'failed')])).toBe('failed')
    expect(status([task(1, 'failed'), task(2, 'running')])).toBe('running')
    expect(splitTaskAttempts([task(1, 'running'), task(2, 'succeeded')]).current).toHaveLength(2)
  })
  it('preserves failures on another server or deployment step', () => {
    expect(status([task(1, 'failed', { server_id: 2 }), task(2, 'succeeded')])).toBe('partial_failed')
    expect(status([task(1, 'failed', { type: 'probe_inbounds' }), task(2, 'succeeded')])).toBe('failed')
  })
  it('selects each server latest version without erasing another server failure', () => {
    const current = latestDeploymentTasks([task(1, 'failed'), task(2, 'succeeded', { config_version: 11 }), task(3, 'failed', { server_id: 2 })])
    expect(current.map(item => item.id)).toEqual([3, 2])
    expect(status(current)).toBe('partial_failed')
    expect(status(latestDeploymentTasks([task(1, 'failed'), task(2, 'succeeded', { config_version: 11 })]))).toBe('succeeded')
  })
  it('keeps unrelated remote commands as separate outcomes within a compact batch', () => {
    const commands = [task(1, 'failed', { type: 'remote_exec', config_version: 0 }), task(2, 'succeeded', { type: 'remote_exec', config_version: 0 })]
    expect(splitTaskAttempts(commands).history).toHaveLength(0)
    expect(taskStatusSummary(commands).failed).toBe(1)
    expect(groupTasksForTimeline(commands, String)).toMatchObject([{ title: '远程命令', kind: 'batch' }])
    expect(taskCategory(commands[0])).toBe('remote')
    expect(taskCategory({ type: 'probe_inbounds' })).toBe('diagnostics')
    expect(taskCategory({ type: 'update_agent' })).toBe('maintenance')
  })
})
