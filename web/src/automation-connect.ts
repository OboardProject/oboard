export type AutomationConnectClient = 'generic'

export type AutomationConnectArtifacts = {
  command: string
  config: string
  prompt: string
}

export const oboardOAuthScopes = [
  'oboard:read',
  'oboard:operate',
  'offline_access',
]

export function normalizeAutomationControllerURL(raw: string) {
  const value = String(raw || '').trim().replace(/\/+$/, '')
  if (!value) return ''
  try {
    const parsed = new URL(value)
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || parsed.search || parsed.hash) return ''
    return parsed.toString().replace(/\/+$/, '')
  } catch {
    return ''
  }
}

export function automationMCPURL(controllerURL: string) {
  const base = normalizeAutomationControllerURL(controllerURL)
  return base ? `${base}/api/v1/mcp` : ''
}

export function automationConnectArtifacts(controllerURL: string): AutomationConnectArtifacts
export function automationConnectArtifacts(client: AutomationConnectClient, controllerURL: string): AutomationConnectArtifacts
export function automationConnectArtifacts(arg1: string, arg2?: string): AutomationConnectArtifacts {
  const controllerURL = arg2 !== undefined ? arg2 : arg1
  const base = normalizeAutomationControllerURL(controllerURL)
  if (!base) return { command: '', config: '', prompt: '' }
  const scopes = oboardOAuthScopes
  const mcpURL = `${base}/api/v1/mcp`
  const config = genericClientConfig(mcpURL, base)
  const completion = '使用 MCP OAuth discovery 完成授权，不要自行构造或保存用户密码。'
  const prompt = `请为当前用户配置 OBoard MCP，目标客户端是 Hermes 等通用 MCP 客户端。

连接参数：
- 传输：Streamable HTTP
- MCP 地址：${mcpURL}
- 服务名称：oboard
- 认证：使用服务端 OAuth 2.1 discovery；需要我在浏览器中登录并确认 OBoard 展示的权限与资源范围

权限说明：
- MCP 能力跟随当前 OBoard 用户角色；授权后由服务端按角色、资源边界与审批策略实时检查。
- OAuth scope 只控制访问级别和是否离线刷新，不再细分业务权限。

请在 OAuth 授权页核对「继承当前账号权限」与能力预览后再允许授权。需要申请的范围：
${scopes.map(scope => `- ${scope}`).join('\n')}

请先检查现有的用户级 MCP 配置，仅幂等新增或更新 oboard，保留其他服务器配置，不要修改当前项目或仓库文件。

配置参考：
${config}

${completion}
完成后只验证连接状态、工具列表和资源列表；不要调用任何会创建 Changeset 或执行管理变更的工具。最后报告修改了哪个用户级配置位置以及验证结果。`
  return { command: '', config, prompt }
}

function genericClientConfig(mcpURL: string, baseURL: string) {
  const server: Record<string, unknown> = { type: 'http', url: mcpURL }
  return JSON.stringify({
    mcp: { name: 'oboard', transport: 'streamable-http', ...server },
    oauth: {
      protected_resource_metadata: automationWellKnownURL(baseURL, 'oauth-protected-resource', '/api/v1/mcp'),
      authorization_server_metadata: automationWellKnownURL(baseURL, 'oauth-authorization-server'),
      pkce: 'S256',
      resource: mcpURL,
    },
  }, null, 2)
}

function automationWellKnownURL(baseURL: string, metadata: string, suffix = '') {
  const parsed = new URL(baseURL)
  const basePath = parsed.pathname.replace(/\/+$/, '')
  parsed.pathname = `/.well-known/${metadata}${basePath === '/' ? '' : basePath}${suffix}`
  return parsed.toString()
}
