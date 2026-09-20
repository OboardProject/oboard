import { expect, it } from 'vitest'
import { configurationSyncFailureIssues, configurationSyncPresentation, type ConfigurationSyncProblem } from './configuration-sync'

it('uses structured codes and IDs independently of message, retaining multiple issues', () => {
 const problem: ConfigurationSyncProblem = { code: 'duplicate_direct_paths', retry_policy: 'after_change', resources: [{ type: 'inbound', id: '42' }], message: 'old text' }
 const first = configurationSyncFailureIssues([{ server_id: 1, state: 'failed', problems: [problem] }])[0]
 const second = configurationSyncFailureIssues([{ server_id: 1, state: 'failed', problems: [{ ...problem, message: 'SQLITE_BUSY 入口 999' }] }])[0]
 expect(second).toMatchObject({ key: first.key, kind: first.kind, inboundID: 42, resolution: first.resolution, targetTab: 'proxy-paths' })
 expect(configurationSyncFailureIssues([{ server_id: 1, state: 'failed', problems: [problem, { code: 'future', message: '入口 999 存在相同位置的直接出口分支' }] }])).toHaveLength(2)
})

it('safely handles unknown codes, partial fields, unsafe IDs and waiting', () => {
 const problems: ConfigurationSyncProblem[] = [{ code: 'future', message: 'database is locked', resources: [{ type: 'url', id: 'https://attacker.invalid' }] }, { code: 'duplicate_direct_paths', resources: [{ type: 'inbound', id: 'javascript:alert(1)' }] }]
 const issues = configurationSyncFailureIssues([{ server_id: 1, state: 'failed', problems }])
 expect(issues.every(issue => issue.kind === 'config' && issue.targetTab === 'tasks' && issue.inboundID === undefined)).toBe(true)
 expect(configurationSyncPresentation([{ server_id: 1, state: 'pending', problems }])).toMatchObject({ tone: 'info', retryServerIDs: [] })
 expect(configurationSyncFailureIssues([{ server_id: 1, state: 'failed', error: 'database is locked' }])[0].kind).toBe('busy')
})
