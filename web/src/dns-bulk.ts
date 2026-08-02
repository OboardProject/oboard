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
  ok: boolean
  error: string
}

type DNSBulkRequest = (path: string, init?: RequestInit) => Promise<any>

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

function errorText(error: unknown) {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return '未知错误'
}

function immediateTaskFailure(task: any) {
  if (task?.status !== 'failed') return ''
  if (typeof task?.result_json === 'string') {
    try {
      const result = JSON.parse(task.result_json)
      if (typeof result?.error === 'string' && result.error) return result.error
      if (typeof result?.message === 'string' && result.message) return result.message
    } catch {
      // Fall through to the bounded generic message.
    }
  }
  return '暂时无法检查解析服务'
}

async function runForPolicy(
  policy: DNSBulkPolicy,
  patch: DNSBulkPatch,
  action: DNSBulkAction,
  request: DNSBulkRequest,
): Promise<DNSBulkResult> {
  try {
    await request(`/servers/${policy.server_id}/dns-policy`, {
      method: 'PUT',
      body: JSON.stringify(mergeDNSBulkPolicy(policy, patch)),
    })
  } catch (error) {
    return { serverID: policy.server_id, ok: false, error: `保存失败：${errorText(error)}` }
  }

  if (action === 'save') return { serverID: policy.server_id, ok: true, error: '' }

  try {
    const response = await request(`/servers/${policy.server_id}/dns-test`, {
      method: 'POST',
      body: JSON.stringify({ action }),
    })
    const failure = immediateTaskFailure(response?.task)
    if (failure) return { serverID: policy.server_id, ok: false, error: `检查失败：${failure}` }
    return { serverID: policy.server_id, ok: true, error: '' }
  } catch (error) {
    return { serverID: policy.server_id, ok: false, error: `检查失败：${errorText(error)}` }
  }
}

export async function runDNSBulkAction(
  policies: readonly DNSBulkPolicy[],
  patch: DNSBulkPatch,
  action: DNSBulkAction,
  request: DNSBulkRequest,
  concurrency = 4,
) {
  const results = new Array<DNSBulkResult>(policies.length)
  let nextIndex = 0
  const workerCount = Math.min(Math.max(1, Math.floor(concurrency)), policies.length)

  await Promise.all(Array.from({ length: workerCount }, async () => {
    while (nextIndex < policies.length) {
      const index = nextIndex++
      results[index] = await runForPolicy(policies[index], patch, action, request)
    }
  }))

  return results
}

export function failedDNSBulkServerIDs(results: readonly DNSBulkResult[]) {
  return results.filter(result => !result.ok).map(result => result.serverID)
}
