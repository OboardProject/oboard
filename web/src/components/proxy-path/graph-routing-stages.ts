export type GraphRoutingPath = {
  id: number
  inbound_id: number
  kind?: string
  enabled?: boolean
  branch_source_step_id?: number
}

export type GraphRoutingStep = {
  id: number
  path_id: number
  position?: number
  node_type?: string
}

export type GraphRoutingRule = {
  id: number
  scope?: string
  proxy_path_id?: number
  stage_step_id?: number
  sort_position?: number
  match_source?: string
  match_json?: string
  action?: string
  name: string
  enabled?: boolean
}

export type GraphRoutingStage = {
  pathID: number
  stageStepID: number
  ruleIDs: number[]
  enabledRuleCount: number
  hasCatchAll?: boolean
}

export function graphRoutingStageKey(pathID: number, stageStepID: number) {
  return `${pathID}:${stageStepID || 0}`
}

export function graphRoutingStageNodeID(pathID: number, stageStepID: number) {
  return `routing-stage-${pathID}-${stageStepID || 'root'}`
}

export function graphRoutingStageSourceHandleID(pathID: number, stageStepID: number) {
  return `routing-stage-source-${pathID}-${stageStepID || 'root'}`
}

export function graphRoutingStageSource(handle?: string | null) {
  const match = /^routing-stage-source-(\d+)-(root|\d+)$/.exec(handle || '')
  if (!match) return null
  return { pathID: Number(match[1]), stageStepID: match[2] === 'root' ? 0 : Number(match[2]) }
}

export function graphRoutingRuleSourceHandleID(ruleID: number) {
  return `routing-rule-source-${ruleID}`
}

export function graphRoutingRuleSource(handle?: string | null) {
  const match = /^routing-rule-source-(\d+)$/.exec(handle || '')
  return match ? Number(match[1]) : 0
}

export function graphRoutingStageSiblingOffset(index: number, count: number, spacing = 260) {
  const siblingCount = Math.max(1, count)
  const siblingIndex = Math.max(0, Math.min(index, siblingCount - 1))
  return (siblingIndex - (siblingCount - 1) / 2) * spacing
}

export function graphPathHasImplicitDirectFallback(pathID: number, steps: GraphRoutingStep[], stages: GraphRoutingStage[]) {
  const ordered = steps
    .filter(step => step.path_id === pathID)
    .slice()
    .sort((left, right) => Number(left.position || 0) - Number(right.position || 0) || left.id - right.id)
  const terminal = ordered[ordered.length - 1]
  if (terminal && terminal.node_type !== 'server_inbound') return false
  const terminalStageStepID = terminal?.id || 0
  return stages.some(stage => stage.pathID === pathID && stage.stageStepID === terminalStageStepID)
}

export function graphPathHasTerminalCatchAll(pathID: number, stages: GraphRoutingStage[]) {
  return stages.some(stage => stage.pathID === pathID && stage.hasCatchAll)
}

export function graphDirectExitHiddenByRouting(
  path: GraphRoutingPath,
  paths: GraphRoutingPath[],
  steps: GraphRoutingStep[],
  stages: GraphRoutingStage[],
) {
  const pathSteps = steps.filter(step => step.path_id === path.id)
  if (!graphPathHasImplicitDirectFallback(path.id, pathSteps, stages) && !graphPathHasTerminalCatchAll(path.id, stages)) {
    return false
  }
  return !enabledRoutingPaths(paths).some(item => (
    item.id !== path.id
    && item.inbound_id === path.inbound_id
    && item.kind !== 'direct'
  ))
}

export type GraphRoutingHostSource = {
  inbound_id?: number
  step_id?: number
}

export type GraphRoutingHost =
  | { pathID: number; stageStepID: number }
  | { inboundID: number }

function enabledRoutingPaths(paths: GraphRoutingPath[]) {
  return paths.filter(path => path.enabled !== false)
}

function orderedPathSteps(steps: GraphRoutingStep[], pathID: number) {
  return steps
    .filter(step => step.path_id === pathID)
    .slice()
    .sort((left, right) => Number(left.position || 0) - Number(right.position || 0) || left.id - right.id)
}

function preferredChainForInbound(paths: GraphRoutingPath[], inboundID: number) {
  return enabledRoutingPaths(paths)
    .filter(path => path.inbound_id === inboundID && path.kind !== 'direct')
    .slice()
    .sort((left, right) => left.id - right.id)[0]
}

/**
 * Chooses which path should host a newly connected routing block. Explicit
 * direct-exit branches stay siblings; attaching the split to them would hide
 * the exit card as an implicit fallback while the branch still exists.
 */
export function routingHostForGraphSource(
  paths: GraphRoutingPath[],
  steps: GraphRoutingStep[],
  source: GraphRoutingHostSource,
): GraphRoutingHost | null {
  const enabled = enabledRoutingPaths(paths)
  const pathByID = new Map(enabled.map(path => [path.id, path]))
  const stepByID = new Map(steps.map(step => [step.id, step]))

  if (source.step_id) {
    const step = stepByID.get(source.step_id)
    const path = step ? pathByID.get(step.path_id) : undefined
    if (!step || !path) return null
    if (path.kind !== 'direct') return { pathID: path.id, stageStepID: step.id }

    const parentStep = path.branch_source_step_id ? stepByID.get(path.branch_source_step_id) : undefined
    const parentPath = parentStep ? pathByID.get(parentStep.path_id) : undefined
    if (parentStep && parentPath && parentPath.kind !== 'direct') {
      const directSteps = orderedPathSteps(steps, path.id)
      const parentPrefix = orderedPathSteps(steps, parentPath.id)
        .filter(item => Number(item.position || 0) <= Number(parentStep.position || 0))
      const directIndex = directSteps.findIndex(item => item.id === step.id)
      const mapped = directIndex >= 0 ? parentPrefix[directIndex] : parentStep
      return { pathID: parentPath.id, stageStepID: mapped?.id || parentStep.id }
    }

    const sibling = preferredChainForInbound(enabled, path.inbound_id)
    if (sibling) return { pathID: sibling.id, stageStepID: 0 }
    return { pathID: path.id, stageStepID: step.id }
  }

  const inboundID = Number(source.inbound_id || 0)
  if (!inboundID) return null
  const chain = preferredChainForInbound(enabled, inboundID)
  if (chain) return { pathID: chain.id, stageStepID: 0 }
  const direct = enabled
    .filter(path => path.inbound_id === inboundID)
    .slice()
    .sort((left, right) => left.id - right.id)[0]
  if (direct) return { pathID: direct.id, stageStepID: 0 }
  return { inboundID }
}

function isEnabledInlineCatchAll(rule: GraphRoutingRule) {
  if (rule.enabled === false || rule.match_source !== 'inline') return false
  try {
    const match = JSON.parse(rule.match_json || '')
    return Boolean(match) && typeof match === 'object' && !Array.isArray(match) && Object.keys(match).length === 0
  } catch {
    return false
  }
}

export function buildGraphRoutingStages(
  paths: GraphRoutingPath[],
  steps: GraphRoutingStep[],
  rules: GraphRoutingRule[],
): GraphRoutingStage[] {
  const enabledPathIDs = new Set(paths.filter(path => path.enabled !== false).map(path => path.id))
  const stepByID = new Map(steps.map(step => [step.id, step]))
  const groups = new Map<string, { stage: GraphRoutingStage; rules: GraphRoutingRule[] }>()

  rules.forEach(rule => {
    const pathID = Number(rule.proxy_path_id || 0)
    const stageStepID = Number(rule.stage_step_id || 0)
    if (rule.scope !== 'path_stage' || !enabledPathIDs.has(pathID)) return
    if (stageStepID && stepByID.get(stageStepID)?.path_id !== pathID) return
    const key = graphRoutingStageKey(pathID, stageStepID)
    const group = groups.get(key) || {
      stage: { pathID, stageStepID, ruleIDs: [], enabledRuleCount: 0 },
      rules: [],
    }
    group.rules.push(rule)
    groups.set(key, group)
  })

  const stepOrder = (stepID: number) => stepID ? stepByID.get(stepID)?.position || Number.MAX_SAFE_INTEGER : 0
  return Array.from(groups.values())
    .sort((left, right) => left.stage.pathID - right.stage.pathID
      || stepOrder(left.stage.stageStepID) - stepOrder(right.stage.stageStepID)
      || left.stage.stageStepID - right.stage.stageStepID)
    .map(({ stage, rules: stageRules }) => {
      stageRules.sort((left, right) => Number(left.sort_position || 0) - Number(right.sort_position || 0) || left.id - right.id)
      return {
        ...stage,
        ruleIDs: stageRules.map(rule => rule.id),
        enabledRuleCount: stageRules.filter(rule => rule.enabled !== false).length,
        ...(stageRules.some(isEnabledInlineCatchAll) ? { hasCatchAll: true } : {}),
      }
    })
}
