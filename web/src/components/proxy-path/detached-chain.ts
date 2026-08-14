import type { ProxyPathStep } from './types'
import type { GraphPosition } from './layout'

export type DetachedChainStep = {
  step: ProxyPathStep
  position: GraphPosition
}

export type CanvasDetachedChain = {
  instance_id: string
  root_server_id: number
  source_path_id: number
  source_path_name: string
  steps: DetachedChainStep[]
}

export type DisconnectPathCandidate = {
  pathID: number
  step: ProxyPathStep
}

export function disconnectPathCandidates(
  edgeStepID: number,
  pathIDs: readonly number[],
  steps: readonly ProxyPathStep[],
): DisconnectPathCandidate[] {
  const edgeStep = steps.find(step => step.id === edgeStepID)
  if (!edgeStep) return []
  const candidates = Array.from(new Set([edgeStep.path_id, ...pathIDs]))
    .map(pathID => ({
      pathID,
      step: steps.find(step => step.path_id === pathID && step.position === edgeStep.position),
    }))
    .filter((candidate): candidate is DisconnectPathCandidate => Boolean(candidate.step))
  return candidates.sort((left, right) => left.pathID - right.pathID)
}

export function detachedPathSuffix(
  pathID: number,
  fromStepID: number,
  steps: readonly ProxyPathStep[],
): ProxyPathStep[] {
  const fromStep = steps.find(step => step.id === fromStepID && step.path_id === pathID)
  if (!fromStep) return []
  return steps
    .filter(step => step.path_id === pathID && step.position >= fromStep.position)
    .slice()
    .sort((left, right) => left.position - right.position || left.id - right.id)
}

export function detachedStepCreateRequest(step: ProxyPathStep, pathID: number, position: number) {
  return {
    path_id: pathID,
    position,
    node_type: step.node_type,
    transport_mode: step.transport_mode || 'singbox',
    processing_role: Boolean(step.processing_role),
    server_id: step.server_id,
    inbound_id: step.inbound_id,
    external_outbound_id: step.external_outbound_id,
    config_json: step.config_json || '{}',
  }
}
