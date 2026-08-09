import type { ExternalOutbound, Inbound, ProxyPath, ProxyPathStep, Server } from './types'

export type GraphRelationTarget =
  | { kind: 'server' | 'entry' | 'external' | 'path'; id: number }
  | { kind: 'step'; id: number; pathIDs?: readonly number[] }

export type RelatedProxyPath = {
  path: ProxyPath
  rootServerID: number
  roles: string[]
}

export type ProxyGraphRelationData = {
  servers?: Server[]
  inbounds?: Inbound[]
  external_outbounds?: ExternalOutbound[]
  proxy_paths?: ProxyPath[]
  proxy_path_steps?: ProxyPathStep[]
}

function canonicalJSON(raw?: string) {
  const normalize = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(normalize)
    if (value && typeof value === 'object') {
      return Object.fromEntries(Object.keys(value).sort().map(key => [key, normalize((value as Record<string, unknown>)[key])]))
    }
    return value
  }
  try {
    return JSON.stringify(normalize(JSON.parse(raw || '{}')))
  } catch {
    return (raw || '').trim()
  }
}

function stepSignature(step: ProxyPathStep) {
  return JSON.stringify([
    step.position,
    step.node_type,
    step.transport_mode || 'singbox',
    step.processing_role === true,
    step.server_id || 0,
    step.inbound_id || 0,
    step.external_outbound_id || 0,
    canonicalJSON(step.config_json),
  ])
}

function stepPrefixSignatures(paths: ProxyPath[], stepsByPath: Map<number, ProxyPathStep[]>) {
  const signatures = new Map<number, string>()
  paths.forEach(path => {
    let prefix = `entry:${path.inbound_id}`
    ;(stepsByPath.get(path.id) || []).forEach(step => {
      prefix += `\u001f${stepSignature(step)}`
      signatures.set(step.id, prefix)
    })
  })
  return signatures
}

function addRole(roles: Map<number, Set<string>>, pathID: number, role: string) {
  const current = roles.get(pathID) || new Set<string>()
  current.add(role)
  roles.set(pathID, current)
}

export function relatedProxyPaths(data: ProxyGraphRelationData, target: GraphRelationTarget): RelatedProxyPath[] {
  const paths = (data.proxy_paths || []).slice()
  const steps = data.proxy_path_steps || []
  const inboundByID = new Map((data.inbounds || []).map(inbound => [inbound.id, inbound]))
  const pathByID = new Map(paths.map(path => [path.id, path]))
  const stepsByPath = new Map<number, ProxyPathStep[]>()
  steps.forEach(step => stepsByPath.set(step.path_id, [...(stepsByPath.get(step.path_id) || []), step]))
  stepsByPath.forEach(pathSteps => pathSteps.sort((left, right) => left.position - right.position || left.id - right.id))

  const roles = new Map<number, Set<string>>()
  if (target.kind === 'path') {
    if (pathByID.has(target.id)) addRole(roles, target.id, '直接出口')
  } else if (target.kind === 'step') {
    const selected = steps.find(step => step.id === target.id)
    const prefixes = stepPrefixSignatures(paths, stepsByPath)
    const selectedPrefix = selected ? prefixes.get(selected.id) : undefined
    const equivalentStepIDs = new Set<number>()
    if (selectedPrefix) {
      prefixes.forEach((prefix, stepID) => {
        if (prefix === selectedPrefix) equivalentStepIDs.add(stepID)
      })
    }
    steps.forEach(step => {
      if (!equivalentStepIDs.has(step.id)) return
      addRole(roles, step.path_id, `第 ${step.position} 跳`)
    })
    ;(target.pathIDs || []).forEach(pathID => {
      if (pathByID.has(pathID)) addRole(roles, pathID, pathByID.get(pathID)?.kind === 'direct' ? '共享前缀 / 直接出口' : '共享链路节点')
    })
    paths.forEach(path => {
      if (path.branch_source_step_id && equivalentStepIDs.has(path.branch_source_step_id)) addRole(roles, path.id, '共享前缀 / 直接出口')
    })
  } else {
    paths.forEach(path => {
      const root = inboundByID.get(path.inbound_id)
      const pathSteps = stepsByPath.get(path.id) || []
      if (target.kind === 'server') {
        if (root?.server_id === target.id) addRole(roles, path.id, '入口服务器')
        pathSteps.forEach(step => {
          const inbound = step.inbound_id ? inboundByID.get(step.inbound_id) : undefined
          if ((step.server_id || inbound?.server_id) !== target.id) return
          addRole(roles, path.id, step.processing_role ? `第 ${step.position} 跳 / 处理节点` : `第 ${step.position} 跳`)
        })
      }
      if (target.kind === 'entry') {
        if (path.inbound_id === target.id) addRole(roles, path.id, '入口节点')
        pathSteps.forEach(step => {
          if (step.inbound_id === target.id) addRole(roles, path.id, `第 ${step.position} 跳 / 已有入口`)
        })
      }
      if (target.kind === 'external') {
        pathSteps.forEach(step => {
          if (step.external_outbound_id === target.id) addRole(roles, path.id, `第 ${step.position} 跳 / 导入节点`)
        })
      }
    })
  }

  return Array.from(roles.entries())
    .flatMap(([pathID, pathRoles]) => {
      const path = pathByID.get(pathID)
      if (!path) return []
      return [{ path, rootServerID: inboundByID.get(path.inbound_id)?.server_id || 0, roles: Array.from(pathRoles) }]
    })
    .sort((left, right) => Number(left.path.enabled === false) - Number(right.path.enabled === false)
      || left.path.name.localeCompare(right.path.name, 'zh-CN')
      || left.path.id - right.path.id)
}
