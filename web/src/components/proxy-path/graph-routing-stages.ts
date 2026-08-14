export type GraphRoutingPath = {
  id: number
  inbound_id: number
  enabled?: boolean
}

export type GraphRoutingStep = {
  id: number
  path_id: number
  position?: number
}

export type GraphRoutingRule = {
  id: number
  scope?: string
  proxy_path_id?: number
  stage_step_id?: number
  sort_position?: number
  name: string
  enabled?: boolean
}

export type GraphRoutingStage = {
  pathID: number
  stageStepID: number
  ruleIDs: number[]
  enabledRuleCount: number
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

export function graphRoutingStageSiblingOffset(index: number, count: number, spacing = 260) {
  const siblingCount = Math.max(1, count)
  const siblingIndex = Math.max(0, Math.min(index, siblingCount - 1))
  return (siblingIndex - (siblingCount - 1) / 2) * spacing
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
      }
    })
}
