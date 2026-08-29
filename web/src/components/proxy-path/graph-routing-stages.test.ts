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
  graphPathHasTerminalCatchAll,
  graphDirectExitHiddenByRouting,
  routingHostForGraphSource,
  routingHostForServerConnect,
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

  it('omits the default direct exit after an earlier catch-all routing rule', () => {
    const stages = buildGraphRoutingStages(
      [{ id: 41, inbound_id: 7, enabled: true }],
      [{ id: 501, path_id: 41, position: 1, node_type: 'server_inbound' }],
      [{ id: 101, scope: 'path_stage', proxy_path_id: 41, name: 'all-via-eth0', match_source: 'inline', match_json: '{}', action: 'interface', enabled: true }],
    )

    expect(graphPathHasTerminalCatchAll(41, stages)).toBe(true)
  })

  it('keeps the default direct exit when the catch-all rule is disabled or remote', () => {
    const stages = buildGraphRoutingStages(
      [{ id: 41, inbound_id: 7, enabled: true }],
      [],
      [
        { id: 101, scope: 'path_stage', proxy_path_id: 41, name: 'disabled', match_source: 'inline', match_json: '{}', action: 'interface', enabled: false },
        { id: 102, scope: 'path_stage', proxy_path_id: 41, name: 'remote', match_source: 'rule_set', match_json: '{}', action: 'interface', enabled: true },
      ],
    )

    expect(graphPathHasTerminalCatchAll(41, stages)).toBe(false)
  })

  it('does not treat a sibling direct exit as the implicit fallback of another path\'s split', () => {
    const stages = buildGraphRoutingStages(
      [
        { id: 41, inbound_id: 7, kind: 'chain', enabled: true },
        { id: 42, inbound_id: 7, kind: 'direct', enabled: true, branch_source_step_id: 501 },
      ],
      [
        { id: 501, path_id: 41, position: 1, node_type: 'server_inbound' },
        { id: 601, path_id: 42, position: 1, node_type: 'server_inbound' },
      ],
      [{ id: 101, scope: 'path_stage', proxy_path_id: 41, stage_step_id: 501, name: 'cn', enabled: true }],
    )

    expect(graphPathHasImplicitDirectFallback(42, [
      { id: 601, path_id: 42, position: 1, node_type: 'server_inbound' },
    ], stages)).toBe(false)
    expect(graphPathHasImplicitDirectFallback(41, [
      { id: 501, path_id: 41, position: 1, node_type: 'server_inbound' },
    ], stages)).toBe(true)
  })

  it('keeps an explicit direct exit visible when a sibling chain already exists at the fork', () => {
    const paths = [
      { id: 41, inbound_id: 7, kind: 'chain', enabled: true },
      { id: 42, inbound_id: 7, kind: 'direct', enabled: true, branch_source_step_id: 501 },
    ]
    const steps = [
      { id: 501, path_id: 41, position: 1, node_type: 'server_inbound' },
      { id: 601, path_id: 42, position: 1, node_type: 'server_inbound' },
    ]
    const stages = buildGraphRoutingStages(paths, steps, [
      { id: 101, scope: 'path_stage', proxy_path_id: 42, stage_step_id: 601, name: 'cn', enabled: true },
    ])

    expect(graphDirectExitHiddenByRouting(paths[1], paths, steps, stages)).toBe(false)
    expect(graphDirectExitHiddenByRouting(
      { id: 42, inbound_id: 7, kind: 'direct', enabled: true },
      [{ id: 42, inbound_id: 7, kind: 'direct', enabled: true }],
      [],
      [{ pathID: 42, stageStepID: 0, ruleIDs: [101], enabledRuleCount: 1 }],
    )).toBe(true)
  })
})

describe('routing host for a new graph split', () => {
  it('hosts an inbound split as its own block instead of reusing an existing branch', () => {
    expect(routingHostForGraphSource(
      [
        { id: 42, inbound_id: 7, kind: 'direct', enabled: true },
        { id: 41, inbound_id: 7, kind: 'chain', enabled: true },
      ],
      [{ id: 501, path_id: 41, position: 1, node_type: 'server_inbound' }],
      { inbound_id: 7 },
    )).toEqual({ inboundID: 7 })
  })

  it('retargets a direct-exit prefix step to the parent chain at the same fork', () => {
    expect(routingHostForGraphSource(
      [
        { id: 41, inbound_id: 7, kind: 'chain', enabled: true },
        { id: 42, inbound_id: 7, kind: 'direct', enabled: true, branch_source_step_id: 501 },
      ],
      [
        { id: 501, path_id: 41, position: 1, node_type: 'server_inbound' },
        { id: 601, path_id: 42, position: 1, node_type: 'server_inbound' },
      ],
      { step_id: 601 },
    )).toEqual({ pathID: 41, stageStepID: 501 })
  })

  it('keeps a chain step as the host instead of stealing a sibling direct exit', () => {
    expect(routingHostForGraphSource(
      [
        { id: 41, inbound_id: 7, kind: 'chain', enabled: true },
        { id: 42, inbound_id: 7, kind: 'direct', enabled: true, branch_source_step_id: 501 },
      ],
      [
        { id: 501, path_id: 41, position: 1, node_type: 'server_inbound' },
        { id: 601, path_id: 42, position: 1, node_type: 'server_inbound' },
      ],
      { step_id: 501 },
    )).toEqual({ pathID: 41, stageStepID: 501 })
  })

  it('keeps a lone inbound as the host when no path exists yet', () => {
    expect(routingHostForGraphSource(
      [{ id: 42, inbound_id: 7, kind: 'direct', enabled: true }],
      [],
      { inbound_id: 7 },
    )).toEqual({ inboundID: 7 })
  })

  it('attaches a root server card to that server inbound without picking a branch', () => {
    expect(routingHostForServerConnect(
      [
        { id: 41, inbound_id: 7, kind: 'chain', enabled: true },
        { id: 42, inbound_id: 8, kind: 'chain', enabled: true },
      ],
      [
        { id: 501, path_id: 41, position: 1, node_type: 'server_inbound' },
        { id: 601, path_id: 42, position: 1, node_type: 'server_inbound' },
      ],
      [
        { id: 7, server_id: 1, enabled: true },
        { id: 8, server_id: 2, enabled: true },
      ],
      { serverID: 1 },
    )).toEqual({ inboundID: 7 })
  })

  it('keeps a path-instance server on its hop', () => {
    expect(routingHostForServerConnect(
      [{ id: 41, inbound_id: 7, kind: 'chain', enabled: true }],
      [{ id: 501, path_id: 41, position: 1, node_type: 'server_inbound' }],
      [{ id: 7, server_id: 1, enabled: true }],
      { serverID: 2, stepID: 501 },
    )).toEqual({ pathID: 41, stageStepID: 501 })
  })
})
