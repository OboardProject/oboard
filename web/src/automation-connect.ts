export type AutomationConnectClient = 'codex' | 'claude' | 'generic'

export type AutomationConnectArtifacts = {
  command: string
  config: string
  prompt: string
}

export const codexOAuthScopes = [
  'inventory:read',
  'servers:read',
  'servers:plan',
  'topology:read',
  'topology:plan',
  'topology:write',
  'deployments:plan',
  'deployments:validate',
  'deployments:apply',
  'offline_access',
]

export const codexOAuthRisk2Scopes = [
  'servers:onboard',
  'subscriptions:resume',
  'subscriptions:manage',
]

export type AutomationConnectPermissions = {
  risk2?: boolean
}

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
  return base ? `${base}/mcp` : ''
}

export function automationConnectArtifacts(client: AutomationConnectClient, controllerURL: string, permissions: AutomationConnectPermissions = {}): AutomationConnectArtifacts {
  const base = normalizeAutomationControllerURL(controllerURL)
  if (!base) return { command: '', config: '', prompt: '' }
  const includeRisk2 = permissions.risk2 !== false
  const scopes = includeRisk2 ? [...codexOAuthScopes, ...codexOAuthRisk2Scopes] : codexOAuthScopes
  const mcpURL = `${base}/mcp`
  const command = clientCommand(client, mcpURL)
  const config = clientConfig(client, mcpURL, base, scopes)
  const completion = client === 'codex'
    ? '完成配置后运行 `codex mcp login oboard`，等待我完成浏览器授权。'
    : client === 'claude'
      ? '完成配置后让我在 Claude Code 的 `/mcp` 面板中完成登录。'
      : '使用 MCP OAuth discovery 完成授权，不要自行构造或保存用户密码。'
  const target = client === 'codex' ? 'Codex' : client === 'claude' ? 'Claude Code' : '当前 MCP 客户端'
  const prompt = `请为当前用户配置 OBoard MCP，目标客户端是 ${target}。

连接参数：
- 传输：Streamable HTTP
- MCP 地址：${mcpURL}
- 服务名称：oboard
- 认证：使用服务端 OAuth 2.1 discovery；需要我在浏览器中登录并确认 OBoard 展示的权限与资源范围

权限选择（风险 2 级）：
${includeRisk2 ? '- 已勾选：接入服务器（servers:onboard）、恢复订阅访问（subscriptions:resume）、订阅自定义路径（subscriptions:manage）' : '- 未勾选：不申请接入服务器、恢复订阅、订阅自定义路径等风险 2 级写权限'}

请在 OAuth 授权页核对「申请的权限」清单与上述选择一致后再允许授权。需要申请的范围：
${scopes.map(scope => `- ${scope}`).join('\n')}

请先检查现有的用户级 MCP 配置，仅幂等新增或更新 oboard，保留其他服务器配置，不要修改当前项目或仓库文件。

建议命令：
${command || '当前客户端没有统一命令，请按下面配置写入其用户级 MCP 设置。'}

配置参考：
${config}

${completion}
完成后只验证连接状态、工具列表和资源列表；不要调用任何会创建 Changeset 或执行管理变更的工具。最后报告修改了哪个用户级配置位置以及验证结果。`
  return { command, config, prompt }
}

function clientCommand(client: AutomationConnectClient, mcpURL: string) {
  if (client === 'codex') {
    return `codex mcp add oboard --url ${posixQuote(mcpURL)}\ncodex mcp login oboard`
  }
  if (client === 'claude') {
    return `claude mcp add --transport http --scope user oboard ${posixQuote(mcpURL)}\n# 随后在 Claude Code 中打开 /mcp 完成登录`
  }
  return ''
}

function clientConfig(client: AutomationConnectClient, mcpURL: string, baseURL: string, scopes: string[]) {
  if (client === 'codex') {
    const scopeLines = scopes.map(scope => `  ${JSON.stringify(scope)},`).join('\n')
    return `[mcp_servers.oboard]\nurl = ${JSON.stringify(mcpURL)}\nauth = "oauth"\noauth_resource = ${JSON.stringify(mcpURL)}\nrequired = true\ntool_timeout_sec = 120\ndefault_tools_approval_mode = "writes"\nscopes = [\n${scopeLines}\n]`
  }
  const server: Record<string, unknown> = { type: 'http', url: mcpURL }
  if (client === 'claude') return JSON.stringify({ mcpServers: { oboard: server } }, null, 2)
  return JSON.stringify({
    mcp: { name: 'oboard', transport: 'streamable-http', ...server },
    oauth: {
      protected_resource_metadata: automationWellKnownURL(baseURL, 'oauth-protected-resource', '/mcp'),
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

function posixQuote(value: string) {
  return `'${value.replace(/'/g, `'\\''`)}'`
}
