export type DNSBulkAction = 'save' | 'test' | 'test_and_apply'

export type DNSBulkPatch = {
  encryptedListID?: number
  bootstrapListID?: number
  strategy?: string
  hourlyTest?: boolean
}

export type DNSBulkPolicy = {
  server_id: number
  encrypted_list_id: number
  bootstrap_list_id: number
  strategy: string
  auto_test: 'never' | 'first_apply' | 'periodic'
  test_interval_seconds: number
}

export type DNSPolicyUpdatePayload = {
  encrypted_list_id: number
  bootstrap_list_id: number
  strategy: string
  auto_test: 'never' | 'first_apply' | 'periodic'
  test_interval_seconds: number
}

export type DNSBulkResult = {
  serverID: number
  operationID?: string
  status: 'succeeded' | 'failed' | 'skipped'
  message: string
}

type DNSBulkRequest = (path: string, init?: RequestInit) => Promise<any>
type DNSBulkCheckAvailability = (serverID: number) => string

const dnsPolicyRetryDelayMS = 250

export function hasDNSBulkPatch(patch: DNSBulkPatch) {
  return patch.encryptedListID !== undefined
    || patch.bootstrapListID !== undefined
    || patch.strategy !== undefined
    || patch.hourlyTest !== undefined
}

export function mergeDNSBulkPolicy(policy: DNSBulkPolicy, patch: DNSBulkPatch): DNSPolicyUpdatePayload {
  return {
    encrypted_list_id: patch.encryptedListID ?? policy.encrypted_list_id,
    bootstrap_list_id: patch.bootstrapListID ?? policy.bootstrap_list_id,
    strategy: patch.strategy ?? policy.strategy,
    auto_test: patch.hourlyTest === undefined ? policy.auto_test : patch.hourlyTest ? 'periodic' : 'first_apply',
    test_interval_seconds: patch.hourlyTest === undefined ? policy.test_interval_seconds : 3600,
  }
}

function isTransportError(error: unknown) {
  return error instanceof TypeError
}

function errorText(error: unknown, stage: 'save' | 'test') {
  if (isTransportError(error)) {
    return stage === 'save'
      ? '无法连接控制器，请检查网络后重试'
      : '与控制器的连接中断，请先查看测试记录'
  }
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return '未知错误'
}

function immediateTaskFailure(task: any) {
  if (task?.status !== 'failed') return null
  if (typeof task?.result_json === 'string') {
    try {
      const result = JSON.parse(task.result_json)
      const message = typeof result?.error === 'string' && result.error
        ? result.error
        : typeof result?.message === 'string' && result.message
          ? result.message
          : '暂时无法测试解析服务'
      return { message, unavailable: result?.offline === true }
    } catch {
      // Fall through to the bounded generic message.
    }
  }
  return { message: '暂时无法测试解析服务', unavailable: false }
}

async function runForPolicy(
  policy: DNSBulkPolicy,
  patch: DNSBulkPatch,
  action: DNSBulkAction,
  request: DNSBulkRequest,
  checkAvailability: DNSBulkCheckAvailability,
): Promise<DNSBulkResult> {
  const payload = mergeDNSBulkPolicy(policy, patch)
  const policyChanged = hasDNSBulkPatch(patch) && (
    payload.encrypted_list_id !== policy.encrypted_list_id
    || payload.bootstrap_list_id !== policy.bootstrap_list_id
    || payload.strategy !== policy.strategy
    || payload.auto_test !== policy.auto_test
    || payload.test_interval_seconds !== policy.test_interval_seconds
  )
  if (policyChanged) {
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        await request(`/servers/${policy.server_id}/dns-policy`, {
          method: 'PUT',
          body: JSON.stringify(payload),
        })
        break
      } catch (error) {
        if (attempt === 0 && isTransportError(error)) {
          await new Promise(resolve => setTimeout(resolve, dnsPolicyRetryDelayMS))
          continue
        }
        return { serverID: policy.server_id, status: 'failed', message: `保存失败：${errorText(error, 'save')}` }
      }
    }
  }

  if (action === 'save') return { serverID: policy.server_id, status: 'succeeded', message: '' }

  const unavailableReason = checkAvailability(policy.server_id)
  if (unavailableReason) return { serverID: policy.server_id, status: 'skipped', message: unavailableReason }

  try {
    const response = await request(`/servers/${policy.server_id}/dns-test`, {
      method: 'POST',
      body: JSON.stringify({ action }),
    })
    const failure = immediateTaskFailure(response?.task)
    if (failure?.unavailable) {
      return { serverID: policy.server_id, status: 'skipped', message: `DNS 策略已保存，测试已跳过：${failure.message}` }
    }
    if (failure) return { serverID: policy.server_id, status: 'failed', message: `测试失败：${failure.message}` }
    return { serverID: policy.server_id, status: 'succeeded', message: '' }
  } catch (error) {
    return { serverID: policy.server_id, status: 'failed', message: `测试状态未知：${errorText(error, 'test')}` }
  }
}

export async function runDNSBulkAction(
  policies: readonly DNSBulkPolicy[],
  patch: DNSBulkPatch,
  action: DNSBulkAction,
  request: DNSBulkRequest,
  checkAvailability: DNSBulkCheckAvailability = () => '',
) {
  if (action === 'test' && !hasDNSBulkPatch(patch) && policies.length > 1) {
    if (policies.length > 1000) return policies.map((policy): DNSBulkResult => ({ serverID: policy.server_id, status: 'failed', message: '未发送：一次最多测试 1000 台服务器，请缩小选择范围' }))
    try {
      const response = await request('/dns-test-batch', { method: 'POST', body: JSON.stringify({ server_ids: policies.map(policy => policy.server_id) }) })
      const operation = response?.operation as { id?: string; targets?: { target_type: string; target_id: string; state: string }[] } | undefined
      return policies.map((policy): DNSBulkResult => {
        const target = operation?.targets?.find(target => target.target_type === 'server' && target.target_id === String(policy.server_id))
        const accepted = target && ['pending', 'running', 'succeeded'].includes(target.state)
        return { serverID: policy.server_id, operationID: operation?.id, status: accepted ? 'succeeded' : 'failed', message: accepted ? '' : '测试未能入队，请查看任务记录' }
      })
    } catch (error) {
      return policies.map((policy): DNSBulkResult => ({ serverID: policy.server_id, status: 'failed', message: `测试状态未知：${errorText(error, 'test')}` }))
    }
  }
  const results: DNSBulkResult[] = []
  for (const policy of policies) {
    results.push(await runForPolicy(policy, patch, action, request, checkAvailability))
  }
  return results
}

export function failedDNSBulkServerIDs(results: readonly DNSBulkResult[]) {
  return results.filter(result => result.status === 'failed').map(result => result.serverID)
}
