import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const main = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')
const style = readFileSync(new URL('./style.css', import.meta.url), 'utf8')
const syncStatus = readFileSync(new URL('./configuration-sync-ui.tsx', import.meta.url), 'utf8')
const realtimePages = readFileSync(new URL('./realtime-pages.ts', import.meta.url), 'utf8')
const assignDialog = readFileSync(new URL('./components/node-assignment/AssignPlanUsersDialog.tsx', import.meta.url), 'utf8')
const userPlanDialog = readFileSync(new URL('./pages/UserPlanDialog.tsx', import.meta.url), 'utf8')

describe('configuration save UI contract', () => {
  it('removes the global manual deployment control and endpoint from the normal UI', () => {
    expect(main).not.toContain('topbar-apply')
    expect(main).not.toContain("client.request('/deployments/apply'")
    expect(main).not.toContain('确认下发配置')
    expect(style).not.toContain('.topbar-apply')
  })

  it('shows save/sync state and exposes retry only through failed sync rows', () => {
    expect(main).toContain('<ConfigurationSyncStatus')
    expect(syncStatus).toContain('configurationSyncPresentation(rows, saving, retrying)')
    expect(syncStatus).toContain('onClick={onRetry}')
    expect(main).toContain("client.request('/configuration-sync/retry'")
    expect(main).toContain("configurationSync.filter(item => item.state === 'failed')")
  })

  it('does not ask operators whether normal node assignments should deploy', () => {
    expect(assignDialog).not.toContain('立即下发部署任务')
    expect(assignDialog).not.toContain('deploy,')
    expect(userPlanDialog).not.toContain('deploy: true')
    expect(assignDialog).toContain('保存分配')
    expect(userPlanDialog).toContain('保存分配')
  })

  it('maps cross-session configuration events to every visible configuration page', () => {
    expect(syncStatus).toContain('aria-live="polite"')
    expect(realtimePages).toContain("configuration: ['dashboard', 'servers', 'proxy-paths', 'users', 'plans', 'nodes', 'dns', 'tasks']")
    expect(main).toContain('realtimeInvalidatedPages(event, tab, Object.keys(pageCacheRef.current))')
    expect(main).toContain('scheduleRealtimePageRefresh(tab)')
  })

  it('patches topology mutations without a duplicate mutation-time page-data request', () => {
    expect(main).toContain('const reconcileTopology = () => undefined')
    expect(main).toContain('mergeTopologyMutation(current, result)')
  })
})
