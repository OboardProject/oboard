import type { Inbound, ProxyPath, ProxyPathStep } from './types'

export type ProxyPathMatrixCell =
  | { kind: 'entry'; entry: Inbound }
  | { kind: 'step'; path: ProxyPath; step: ProxyPathStep; terminal: boolean }
  | { kind: 'direct'; path: ProxyPath }

export type ProxyPathMatrixColumn = {
  id: string
  entry: Inbound
  path?: ProxyPath
  branch: boolean
  branchDepth: number
  cells: Map<number, ProxyPathMatrixCell>
}

export type ProxyPathMatrixGroup = {
  entry: Inbound
  columns: ProxyPathMatrixColumn[]
}

export type ProxyPathMatrix = {
  groups: ProxyPathMatrixGroup[]
  rows: number[]
  pathCount: number
}

type MatrixData = {
  inbounds?: Inbound[]
  proxy_paths?: ProxyPath[]
  proxy_path_steps?: ProxyPathStep[]
}

function orderedSteps(steps: ProxyPathStep[]) {
  return steps.slice().sort((left, right) => (left.position - right.position) || (left.id - right.id))
}

function stepSignature(step: ProxyPathStep) {
  return [
    step.position,
    step.node_type,
    step.transport_mode || 'singbox',
    step.processing_role === true,
    step.server_id || 0,
    step.inbound_id || 0,
    step.external_outbound_id || 0,
    step.config_json || '',
  ].join('\u0000')
}

function sharesPrefix(parent: ProxyPathStep[], branch: ProxyPathStep[], depth: number) {
  if (depth <= 0) return false
  const parentByPosition = new Map(parent.map(step => [step.position, stepSignature(step)]))
  const branchByPosition = new Map(branch.map(step => [step.position, stepSignature(step)]))
  for (let position = 1; position <= depth; position++) {
    if (!parentByPosition.has(position) || parentByPosition.get(position) !== branchByPosition.get(position)) return false
  }
  return true
}

function orderPaths(paths: ProxyPath[], stepsByPath: Map<number, ProxyPathStep[]>, stepByID: Map<number, ProxyPathStep>) {
  const roots = paths.filter(path => !path.branch_source_step_id).sort((left, right) => left.id - right.id)
  const branches = paths.filter(path => path.branch_source_step_id).sort((left, right) => {
    const leftDepth = stepByID.get(left.branch_source_step_id || 0)?.position || 0
    const rightDepth = stepByID.get(right.branch_source_step_id || 0)?.position || 0
    return (leftDepth - rightDepth) || (left.id - right.id)
  })
  const children = new Map<number, ProxyPath[]>()
  const unmatched: ProxyPath[] = []

  branches.forEach(branch => {
    const branchDepth = stepByID.get(branch.branch_source_step_id || 0)?.position || 0
    const branchSteps = stepsByPath.get(branch.id) || []
    const parent = roots.find(candidate => sharesPrefix(stepsByPath.get(candidate.id) || [], branchSteps, branchDepth))
    if (!parent) {
      unmatched.push(branch)
      return
    }
    children.set(parent.id, [...(children.get(parent.id) || []), branch])
  })

  return [
    ...roots.flatMap(path => [path, ...(children.get(path.id) || [])]),
    ...unmatched,
  ]
}

export function buildProxyPathMatrix(data: MatrixData, rootServerID: number): ProxyPathMatrix {
  const entries = (data.inbounds || [])
    .filter(entry => entry.server_id === rootServerID && entry.enabled !== false)
    .slice()
    .sort((left, right) => (left.port - right.port) || (left.id - right.id))
  const visibleEntryIDs = new Set(entries.map(entry => entry.id))
  const paths = (data.proxy_paths || []).filter(path => path.enabled !== false && visibleEntryIDs.has(path.inbound_id))
  const stepsByPath = new Map<number, ProxyPathStep[]>()
  ;(data.proxy_path_steps || []).forEach(step => {
    stepsByPath.set(step.path_id, [...(stepsByPath.get(step.path_id) || []), step])
  })
  stepsByPath.forEach((steps, pathID) => stepsByPath.set(pathID, orderedSteps(steps)))
  const stepByID = new Map((data.proxy_path_steps || []).map(step => [step.id, step]))
  let maximumDepth = 0
  let pathCount = 0

  const groups = entries.map(entry => {
    const entryPaths = orderPaths(paths.filter(path => path.inbound_id === entry.id), stepsByPath, stepByID)
    pathCount += entryPaths.length
    const columns: ProxyPathMatrixColumn[] = entryPaths.length ? entryPaths.map(path => {
      const steps = stepsByPath.get(path.id) || []
      const branchSource = stepByID.get(path.branch_source_step_id || 0)
      const branchDepth = branchSource?.path_id === path.id ? branchSource.position : 0
      const cells = new Map<number, ProxyPathMatrixCell>()

      if (!branchDepth) cells.set(0, { kind: 'entry', entry })
      steps.forEach((step, index) => {
        if (step.position <= branchDepth) return
        cells.set(step.position, { kind: 'step', path, step, terminal: path.kind !== 'direct' && index === steps.length - 1 })
      })
      if (path.kind === 'direct') {
        const lastStepDepth = steps.reduce((depth, step) => Math.max(depth, step.position), 0)
        cells.set(Math.max(branchDepth, lastStepDepth) + 1, { kind: 'direct', path })
      }
      cells.forEach((_cell, depth) => { maximumDepth = Math.max(maximumDepth, depth) })
      return {
        id: `path-${path.id}`,
        entry,
        path,
        branch: branchDepth > 0,
        branchDepth,
        cells,
      }
    }) : [{
      id: `entry-${entry.id}-empty`,
      entry,
      branch: false,
      branchDepth: 0,
      cells: new Map<number, ProxyPathMatrixCell>([[0, { kind: 'entry', entry }]]),
    }]
    return { entry, columns }
  })

  return {
    groups,
    rows: Array.from({ length: maximumDepth + 1 }, (_, depth) => depth),
    pathCount,
  }
}
