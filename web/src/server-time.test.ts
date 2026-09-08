import { describe, expect, it } from 'vitest'

import { getServerTimeIssue, hasServerTimeMeasurement } from './server-time'

describe('getServerTimeIssue', () => {
  it('reports config persistence failures without claiming a clock problem', () => {
    expect(getServerTimeIssue({
      time_check_status: 'config_error',
      time_check_error: 'rename config.json.tmp config.json: device or resource busy',
    })).toEqual({ kind: 'config_error', summary: '配置保存失败', tone: 'danger' })
  })
  it.each([
    ['normal', { time_check_status: 'ok' }],
    ['corrected', { time_check_status: 'corrected' }],
    ['pending', { time_check_status: 'pending' }],
    ['unknown', { time_check_status: 'unknown' }],
  ])('does not report %s time state', (_name, server) => {
    expect(getServerTimeIssue(server)).toBeNull()
  })

  it('reports an excessive clock skew', () => {
    expect(getServerTimeIssue({ time_check_status: 'skewed' })).toEqual({
      kind: 'skewed',
      summary: '时间偏差过大',
      tone: 'warning',
    })
  })

  it('reports an unavailable time check before secondary errors', () => {
    expect(getServerTimeIssue({
      time_check_status: 'unavailable',
      time_check_error: 'NTP 无响应',
      time_unsupported_paths: ['mieru'],
    })).toEqual({
      kind: 'unavailable',
      summary: '时间检测失败',
      tone: 'danger',
    })
  })

  it('reports a synchronization error on an otherwise successful result', () => {
    expect(getServerTimeIssue({ time_check_status: 'ok', time_check_error: '内核时间状态同步失败' })).toEqual({
      kind: 'error',
      summary: '时间同步异常',
      tone: 'danger',
    })
  })

  it('reports unsupported logical-time paths', () => {
    expect(getServerTimeIssue({ time_check_status: 'corrected', time_unsupported_paths: ['mieru', 'reality'] })).toEqual({
      kind: 'unsupported',
      summary: '部分路径时间受限',
      tone: 'warning',
    })
  })

  it('ignores empty error and path values', () => {
    expect(getServerTimeIssue({ time_check_status: 'ok', time_check_error: '  ', time_unsupported_paths: ['', ' '] })).toBeNull()
  })
})

describe('hasServerTimeMeasurement', () => {
  it.each(['config_error', 'unavailable', 'pending', 'unknown'])('does not show zero offsets for %s even with an attempt timestamp', status => {
    expect(hasServerTimeMeasurement({ time_check_status: status, time_checked_at: '2026-09-08T00:00:00Z' })).toBe(false)
  })

  it.each(['ok', 'skewed', 'corrected'])('shows offsets for a measured %s result', status => {
    expect(hasServerTimeMeasurement({ time_check_status: status, time_checked_at: '2026-09-08T00:00:00Z' })).toBe(true)
    expect(hasServerTimeMeasurement({ time_check_status: status })).toBe(false)
  })
})
