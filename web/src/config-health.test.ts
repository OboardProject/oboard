import { describe, expect, it } from 'vitest'
import {
  blockingDeploymentNotice,
  cleanupActionsFor,
  cleanupOutcomeMessage,
  cleanupOutcomeTone,
  configHealthFindingKey,
  configHealthHeadline,
  configHealthSummaryOf,
  groupConfigHealthFindings,
  hasDestructiveSelection,
  selectableFindings,
  shouldShowConfigHealthCard,
  type ConfigHealthFinding,
} from './config-health'

function finding(overrides: Partial<ConfigHealthFinding> = {}): ConfigHealthFinding {
  return {
    code: 'inbound.config.invalid',
    severity: 'blocking',
    scope: 'inbound',
    resource_id: 10,
    resource_name: 'vless-a',
    server_id: 1,
    title: '入口配置不符合当前协议模型',
    remedy: { kind: 'normalize', fields: ['multiplex.protocol'] },
    ...overrides,
  }
}

describe('config health summary', () => {
  it('treats a missing payload as clean', () => {
    expect(configHealthSummaryOf(undefined).total).toBe(0)
    expect(configHealthSummaryOf({}).total).toBe(0)
    expect(shouldShowConfigHealthCard(configHealthSummaryOf({}))).toBe(false)
  })

  it('hides the card when nothing needs attention', () => {
    // An always-visible "all good" badge trains operators to ignore the exact
    // spot where the real warning appears.
    expect(shouldShowConfigHealthCard({ blocking: 0, warning: 0, notice: 0, total: 0 })).toBe(false)
    expect(shouldShowConfigHealthCard({ blocking: 0, warning: 0, notice: 2, total: 2 })).toBe(true)
  })

  it('reads the dashboard projection', () => {
    const summary = configHealthSummaryOf({ config_health: { summary: { blocking: 2, warning: 1, notice: 3, total: 6 } } })
    expect(summary).toEqual({ blocking: 2, warning: 1, notice: 3, total: 6, truncated: false })
  })

  it('names only the severities that are present', () => {
    expect(configHealthHeadline({ blocking: 2, warning: 0, notice: 1, total: 3 })).toBe('2 项会阻断下发 · 1 项待规范化')
    expect(configHealthHeadline({ blocking: 0, warning: 1, notice: 0, total: 1 })).toBe('1 项行为异常')
  })
})

describe('grouping and selection', () => {
  it('groups by scope in a stable order', () => {
    const groups = groupConfigHealthFindings([
      finding({ scope: 'routing_rule', resource_id: 900 }),
      finding({ scope: 'inbound', resource_id: 10 }),
      finding({ scope: 'inbound', resource_id: 11 }),
    ])
    expect(groups.map(group => group.scope)).toEqual(['inbound', 'routing_rule'])
    expect(groups[0].findings).toHaveLength(2)
    expect(groups[0].label).toBe('入口')
  })

  it('excludes manual-only findings from selection', () => {
    const items = [finding(), finding({ resource_id: 11, remedy: { kind: 'none' } })]
    expect(selectableFindings(items).map(item => item.resource_id)).toEqual([10])
    // A manual finding that was somehow ticked still produces no action.
    const selected = new Set(items.map(configHealthFindingKey))
    expect(cleanupActionsFor(items, selected)).toEqual([
      { code: 'inbound.config.invalid', scope: 'inbound', resource_id: 10 },
    ])
  })

  it('detects a destructive selection so the dialog can require a second confirm', () => {
    const items = [
      finding(),
      finding({ scope: 'proxy_path', resource_id: 100, remedy: { kind: 'delete', destructive: true } }),
    ]
    expect(hasDestructiveSelection(items, new Set([configHealthFindingKey(items[0])]))).toBe(false)
    expect(hasDestructiveSelection(items, new Set([configHealthFindingKey(items[1])]))).toBe(true)
  })

  it('keys findings uniquely per resource and code', () => {
    const a = finding()
    const b = finding({ code: 'inbound.node_preset.unusable' })
    expect(configHealthFindingKey(a)).not.toBe(configHealthFindingKey(b))
  })
})

describe('cleanup outcome copy', () => {
  it('labels a dry run as a preview', () => {
    const response = { revision: 5, dry_run: true, applied: 2, skipped: 0, failed: 0, requires_deployment: false, results: [{ code: 'c', scope: 'inbound', resource_id: 1, status: 'applied' as const }, { code: 'd', scope: 'inbound', resource_id: 2, status: 'applied' as const }] }
    expect(cleanupOutcomeMessage(response)).toBe('预览完成，共 2 项')
  })

  it('tells the operator that a repair still needs a deployment', () => {
    const response = { revision: 5, dry_run: false, applied: 3, skipped: 0, failed: 0, requires_deployment: true, results: [] }
    expect(cleanupOutcomeMessage(response)).toBe('已处理 3 项，需要重新下发才会生效')
    expect(cleanupOutcomeTone(response)).toBe('success')
  })

  it('reports partial outcomes rather than a single verdict', () => {
    const response = { revision: 5, dry_run: false, applied: 1, skipped: 1, failed: 1, requires_deployment: true, results: [] }
    expect(cleanupOutcomeMessage(response)).toContain('跳过 1 项')
    expect(cleanupOutcomeMessage(response)).toContain('失败 1 项')
    expect(cleanupOutcomeTone(response)).toBe('danger')
  })
})

describe('deployment warning', () => {
  it('stays silent when nothing would block a push', () => {
    expect(blockingDeploymentNotice(null)).toBe('')
    expect(blockingDeploymentNotice({ summary: { blocking: 0, warning: 1, notice: 0, total: 1 }, findings: [finding({ severity: 'warning' })] })).toBe('')
  })

  it('warns about blocking findings without implying the push is refused', () => {
    const notice = blockingDeploymentNotice({
      summary: { blocking: 2, warning: 0, notice: 0, total: 2 },
      findings: [finding({ server_name: 'hk-1' }), finding({ resource_id: 20, server_name: 'jp-1' })],
    })
    expect(notice).toContain('2 项配置会导致下发失败')
    expect(notice).toContain('涉及 2 台服务器')
    expect(notice).toContain('配置体检')
  })
})
