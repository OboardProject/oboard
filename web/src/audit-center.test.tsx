// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { AuditCenter, auditScoreText } from './audit-center'

vi.mock('./components/ui/motion', () => ({ MotionDialogPanel: ({ children }: any) => <div role="dialog">{children}</div> }))
let root: Root
let container: HTMLDivElement
const pending: Array<{ path: string; signal: AbortSignal; resolve: (data: any) => void; reject: (error: Error) => void }> = []
const client = { request: vi.fn((path: string, init?: RequestInit) => path === '/audit/status' ? Promise.resolve({ status: 'pending', pending_event_count: 0, pending_inbox_reports: null, pending_evaluations: 0, pending_notifications: 0, last_snapshot_time: null, degradation: ['snapshots_missing'], collection_mode: 'light', audit_enabled: true }) : new Promise((resolve, reject) => pending.push({ path, signal: init!.signal as AbortSignal, resolve, reject }))) }
async function click(label: string) {
  const button = [...container.querySelectorAll('button')].find(item => item.textContent === label || item.getAttribute('aria-label') === label)
  expect(button).toBeTruthy()
  await act(async () => button!.click())
}
beforeEach(async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  container = document.createElement('div')
  root = createRoot(container)
  pending.length = 0
  client.request.mockClear()
  await act(async () => root.render(<AuditCenter client={client} isAdmin enabled settings={<p>设置内容</p>} renderLogs={() => <p>管理操作日志</p>} />))
})
afterEach(() => { act(() => root.unmount()); vi.unstubAllGlobals() })

it('loads event summaries by default, never the legacy risk overview', () => {
  expect(pending.map(item => item.path)).toEqual(['/audit/events?limit=50&offset=0&status=pending'])
  expect(container.textContent).toContain('仅告警')
  expect(container.textContent).not.toContain('设备克隆')
  expect(client.request.mock.calls.some(([path]) => path === '/audit/status')).toBe(true)
  expect(container.textContent).toContain('待接收应用：不可用')
})
it('settings cancels reads and does not request risk overview or accounts', async () => {
  await click('审计配置')
  expect(pending[0].signal.aborted).toBe(true)
  expect(pending).toHaveLength(1)
  await act(async () => pending[0].resolve({ items: [{ username: 'obsolete' }] }))
  expect(container.textContent).not.toContain('obsolete')
  await click('关闭审计配置')
  expect(pending).toHaveLength(2)
})
it('logs and account navigation fetch only their own paginated endpoints', async () => {
  await click('操作日志')
  expect(pending[0].signal.aborted).toBe(true)
  expect(pending[1].path).toBe('/audit-logs?limit=50&offset=0')
  await click('用户活动')
  expect(pending[1].signal.aborted).toBe(true)
  expect(pending[2].path).toBe('/audit/accounts?limit=50&offset=0')
  await act(async () => pending[2].resolve({ items: [{ account_id: 9, username: 'alice', snapshot: null }], next_offset: null }))
  expect(container.textContent).toContain('待评估')
  expect(container.textContent).not.toContain('0 分')
  await click('查看依据')
  expect(container.textContent).toContain('缺少数据不代表没有风险')
  expect(pending).toHaveLength(3)
})
it('uses the server pagination cursor and preserves snapshots without rescoring', async () => {
  await act(async () => pending[0].resolve({ items: [{ id: 1, user_id: 7, risk_type: 'activity', status: 'pending', score: 73, snapshot: { account_id: 7, status: 'evaluated', activity: { lower: 73, upper: 90, status: 'range', level: 'uncertain' }, exposure: null, resource: null, as_of: '2026-01-01T01:00:00Z' } }], next_offset: 50 }))
  expect(container.textContent).toContain('73 分（事件保存分数）')
  await click('查看依据')
  expect(pending[1].path).toBe('/audit/events?event_id=1')
  expect(container.textContent).toContain('正在加载详情')
  await act(async () => pending[1].resolve({ items: [{ id: 1, snapshot: { status: 'evaluated', activity: { lower: 73, upper: 90, status: 'range' } } }] }))
  expect(container.textContent).toContain('73～90 分（可能范围）')
  expect(pending).toHaveLength(3)
  expect(pending[2].path).toBe('/audit/executions?event_id=1&limit=20&offset=0')
  expect(container.textContent).toContain('正在读取处理记录')
  expect(container.textContent).not.toContain('暂无人工处理记录')
  await click('关闭详情')
  expect(pending[2].signal.aborted).toBe(true)
  await act(async () => pending[2].resolve({ items: [], next_offset: null }))
  await click('下一页')
  expect(pending[3].path).toBe('/audit/events?limit=50&offset=50&status=pending')
  await act(async () => pending[3].resolve({ items: [], next_offset: null }))
  const next = [...container.querySelectorAll('button')].find(item => item.textContent === '下一页')
  expect(next?.disabled).toBe(true)
})
it('loads event evidence only on demand and ignores cancelled detail responses', async () => {
  await act(async () => pending[0].resolve({ items: [{ id: 9, evaluation_status: 'stale', snapshot: null }] }))
  expect(container.textContent).toContain('数据过期')
  expect(pending).toHaveLength(1)
  await click('查看依据')
  expect(pending[1].path).toBe('/audit/events?event_id=9')
  await click('关闭详情')
  expect(pending[1].signal.aborted).toBe(true)
  await click('查看依据')
  await act(async () => pending[2].resolve({ items: [{ id: 9, snapshot: { status: 'stale', as_of: 'new-evidence' } }] }))
  await act(async () => pending[1].resolve({ items: [{ id: 9, snapshot: { status: 'evaluated', as_of: 'obsolete-evidence' } }] }))
  expect(container.textContent).toContain('new-evidence')
  expect(container.textContent).not.toContain('obsolete-evidence')
})
it('settings are unavailable to a nonadministrator', async () => {
  await act(async () => root.render(<AuditCenter client={client} isAdmin={false} enabled settings={<p>设置内容</p>} renderLogs={() => null} />))
  expect([...container.querySelectorAll('button')].some(item => item.textContent === '审计配置')).toBe(false)
})
it('shows an existing event with no snapshot as pending evaluation', async () => {
  await act(async () => pending[0].resolve({ items: [{ id: 9, snapshot: null }] }))
  await click('查看依据')
  await act(async () => pending[1].resolve({ items: [{ id: 9, snapshot: null }] }))
  expect(container.textContent).toContain('缺少数据不代表没有风险')
  expect(container.textContent).not.toContain('事件不存在或无权查看')
})
it('opens raw diagnostic evidence only when requested and cancels detail reads for settings', async () => {
  await act(async () => pending[0].resolve({ items: [{ id: 9 }] }))
  await click('查看依据')
  await act(async () => pending[1].resolve({ items: [{ id: 9, snapshot: { status: 'not_evaluable', quality: null } }] }))
  expect(container.textContent).toContain('质量明细不可用')
  expect(pending.some(item => item.path.startsWith('/audit/evidence'))).toBe(false)
  await click('查看诊断证据')
  expect(pending[3].path).toBe('/audit/evidence?event_id=9&limit=20&offset=0')
  await click('审计配置')
  expect(pending[2].signal.aborted).toBe(true)
  expect(pending[3].signal.aborted).toBe(true)
  expect(container.textContent).not.toContain('事实与判断边界')
  await act(async () => pending[3].resolve({ items: [{ event_time: 'obsolete', account_id: 9 }], next_offset: null }))
  expect(container.textContent).not.toContain('obsolete')
})
it('hiding the page cancels obsolete work and visibility reconciles', async () => {
  await act(async () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(pending[0].signal.aborted).toBe(true)
  await act(async () => pending[0].resolve({ items: [{ username: 'stale' }] }))
  expect(container.textContent).not.toContain('stale')
  await act(async () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(pending).toHaveLength(2)
})
it('renders errors explicitly instead of empty healthy state', async () => {
  await act(async () => pending[0].reject(new Error('403')))
  expect(container.querySelector('[role="alert"]')?.textContent).toContain('加载失败')
  expect(container.textContent).not.toContain('暂无待处理事件')
})
it('keeps score ranges and unknown values distinct without recalculation', () => {
  expect(auditScoreText(null)).toBe('不可评估')
  expect(auditScoreText({ lower: 0, upper: 100, status: 'not_evaluable', level: 'unknown' })).toBe('不可评估')
  expect(auditScoreText({ lower: 47, upper: 100, status: 'range', level: 'medium' })).toBe('47～100 分（可能范围）')
  expect(auditScoreText({ lower: 0, upper: 0, status: 'complete', level: 'low' })).toBe('0 分')
})
