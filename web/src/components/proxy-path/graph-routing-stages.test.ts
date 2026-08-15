import { describe, expect, it } from 'vitest'
import {
  buildGraphRoutingStages,
  graphRoutingStageNodeID,
  graphRoutingRuleSource,
  graphRoutingRuleSourceHandleID,
  graphRoutingStageSiblingOffset,
  graphRoutingStageSource,
  graphRoutingStageSourceHandleID,
  graphPathHasImplicitDirectFallback,
} from './graph-routing-stages'

it('encodes and parses rule-specific source handles separately from fallback handles', () => {
  expect(graphRoutingRuleSourceHandleID(91)).toBe('routing-rule-source-91')
  expect(graphRoutingRuleSource('routing-rule-source-91')).toBe(91)
  expect(graphRoutingRuleSource('routing-stage-source-4-root')).toBe(0)
})

describe('graph routing stages', () => {
  it('keeps a saved root routing block addressable after its direct fallback is created', () => {
    const stages = buildGraphRoutingStages(
      [{ id: 41, inbound_id: 7, enabled: true }],
      [],
      [{ id: 101, scope: 'path_stage', proxy_path_id: 41, name: 'cn', enabled: true }],
    )

    expect(stages).toEqual([{
      pathID: 41,
      stageStepID: 0,
      ruleIDs: [101],
      enabledRuleCount: 1,
    }])
    expect(graphRoutingStageNodeID(41, 0)).toBe('routing-stage-41-root')
    expect(graphRoutingStageSource(graphRoutingStageSourceHandleID(41, 0))).toEqual({ pathID: 41, stageStepID: 0 })
  })

  it('does not merge rules from sibling paths that share the same server stages', () => {
    const stages = buildGraphRoutingStages(
      [
        { id: 41, inbound_id: 7, enabled: true },
        { id: 42, inbound_id: 7, enabled: true },
      ],
      [
        { id: 501, path_id: 41 },
        { id: 502, path_id: 42 },
      ],
      [
        { id: 101, scope: 'path_stage', proxy_path_id: 41, stage_step_id: 501, name: 'path-a', enabled: true },
        { id: 102, scope: 'path_stage', proxy_path_id: 42, stage_step_id: 502, name: 'path-b', enabled: false },
        { id: 103, scope: 'server', proxy_path_id: 41, stage_step_id: 501, name: 'global', enabled: true },
      ],
    )

    expect(stages).toEqual([
      { pathID: 41, stageStepID: 501, ruleIDs: [101], enabledRuleCount: 1 },
      { pathID: 42, stageStepID: 502, ruleIDs: [102], enabledRuleCount: 0 },
    ])
    expect(graphRoutingStageNodeID(41, 501)).not.toBe(graphRoutingStageNodeID(42, 502))
  })

  it('fans sibling routing blocks around their shared source without overlap', () => {
    expect([
      graphRoutingStageSiblingOffset(0, 2),
      graphRoutingStageSiblingOffset(1, 2),
    ]).toEqual([-130, 130])
    expect([
      graphRoutingStageSiblingOffset(0, 3),
      graphRoutingStageSiblingOffset(1, 3),
      graphRoutingStageSiblingOffset(2, 3),
    ]).toEqual([-260, 0, 260])
  })

  it('treats a terminal routing block as the direct fallback without another exit node', () => {
    const rootStages = [{ pathID: 41, stageStepID: 0, ruleIDs: [101], enabledRuleCount: 1 }]
    expect(graphPathHasImplicitDirectFallback(41, [], rootStages)).toBe(true)
    expect(graphPathHasImplicitDirectFallback(41, [
      { id: 500, path_id: 41, position: 1, node_type: 'imported' },
    ], rootStages)).toBe(false)

    const terminalStages = [{ pathID: 42, stageStepID: 501, ruleIDs: [102], enabledRuleCount: 1 }]
    expect(graphPathHasImplicitDirectFallback(42, [
      { id: 501, path_id: 42, position: 1, node_type: 'server_inbound' },
    ], terminalStages)).toBe(true)
    expect(graphPathHasImplicitDirectFallback(42, [
      { id: 501, path_id: 42, position: 1, node_type: 'server_inbound' },
      { id: 502, path_id: 42, position: 2, node_type: 'imported' },
    ], terminalStages)).toBe(false)
  })
})
