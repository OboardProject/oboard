export type AutomationConnectClient = 'codex' | 'claude' | 'generic'
export type AutomationConnectAuth = 'oauth' | 'token'

export type AutomationConnectArtifacts = {
  command: string
  config: string
  prompt: string
}

const tokenEnvironmentName = 'OBOARD_MCP_TOKEN'

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

export function automationConnectArtifacts(client: AutomationConnectClient, auth: AutomationConnectAuth, controllerURL: string): AutomationConnectArtifacts {
  const base = normalizeAutomationControllerURL(controllerURL)
  if (!base) return { command: '', config: '', prompt: '' }
  const mcpURL = `${base}/mcp`
  const oauth = auth === 'oauth'
  const command = clientCommand(client, auth, mcpURL)
  const config = clientConfig(client, auth, mcpURL, base)
  const authentication = oauth
    ? '使用服务端 OAuth 2.1 discovery 和动态客户端注册；需要我在浏览器中登录并确认 OBoard 展示的权限。'
    : `使用 Bearer Token，但配置只能引用环境变量 ${tokenEnvironmentName}；不要读取、打印、记录或要求我在聊天中提供变量值。`
  const completion = client === 'codex'
    ? oauth ? '完成配置后运行 `codex mcp login oboard`，等待我完成浏览器授权。' : '确认配置使用 `bearer_token_env_var`，不要把 Token 写进 TOML。'
    : client === 'claude'
      ? oauth ? '完成配置后让我在 Claude Code 的 `/mcp` 面板中完成登录。' : '在配置中保留 `${OBOARD_MCP_TOKEN}` 环境变量引用，不要展开为明文。'
      : oauth ? '使用 MCP OAuth discovery 完成授权，不要自行构造或保存用户密码。' : `把 ${tokenEnvironmentName} 作为 Authorization Bearer 值的运行时来源。`
  const target = client === 'codex' ? 'Codex' : client === 'claude' ? 'Claude Code' : '当前 MCP 客户端'
  const prompt = `请为当前用户配置 OBoard MCP，目标客户端是 ${target}。

连接参数：
- 传输：Streamable HTTP
- MCP 地址：${mcpURL}
- 服务名称：oboard
- 认证：${authentication}

请先检查现有的用户级 MCP 配置，仅幂等新增或更新 oboard，保留其他服务器配置，不要修改当前项目或仓库文件。

建议命令：
${command || '当前客户端没有统一命令，请按下面配置写入其用户级 MCP 设置。'}

配置参考：
${config}

${completion}
完成后只验证连接状态、工具列表和资源列表；不要调用任何会创建 Changeset 或执行管理变更的工具。最后报告修改了哪个用户级配置位置以及验证结果。`
  return { command, config, prompt }
}

export function serviceTokenEnvironmentCommands(token: string) {
  const value = String(token || '')
  return {
    posix: `export ${tokenEnvironmentName}=${posixQuote(value)}`,
    powershell: `$env:${tokenEnvironmentName} = '${value.replace(/'/g, "''")}'`,
  }
}

function clientCommand(client: AutomationConnectClient, auth: AutomationConnectAuth, mcpURL: string) {
  if (client === 'codex') {
    const tokenFlag = auth === 'token' ? ` --bearer-token-env-var ${tokenEnvironmentName}` : ''
    return `codex mcp add oboard --url ${posixQuote(mcpURL)}${tokenFlag}${auth === 'oauth' ? '\ncodex mcp login oboard' : ''}`
  }
  if (client === 'claude') {
    if (auth === 'oauth') return `claude mcp add --transport http --scope user oboard ${posixQuote(mcpURL)}\n# 随后在 Claude Code 中打开 /mcp 完成登录`
    const json = JSON.stringify({ type: 'http', url: mcpURL, headers: { Authorization: `Bearer \${${tokenEnvironmentName}}` } })
    return `claude mcp add-json --scope user oboard ${posixQuote(json)}`
  }
  return ''
}

function clientConfig(client: AutomationConnectClient, auth: AutomationConnectAuth, mcpURL: string, baseURL: string) {
  if (client === 'codex') {
    return auth === 'oauth'
      ? `[mcp_servers.oboard]\nurl = ${JSON.stringify(mcpURL)}`
      : `[mcp_servers.oboard]\nurl = ${JSON.stringify(mcpURL)}\nbearer_token_env_var = ${JSON.stringify(tokenEnvironmentName)}`
  }
  const server: Record<string, unknown> = { type: 'http', url: mcpURL }
  if (auth === 'token') server.headers = { Authorization: `Bearer \${${tokenEnvironmentName}}` }
  if (client === 'claude') return JSON.stringify({ mcpServers: { oboard: server } }, null, 2)
  return JSON.stringify({
    mcp: { name: 'oboard', transport: 'streamable-http', ...server },
    oauth: auth === 'oauth' ? {
      protected_resource_metadata: automationWellKnownURL(baseURL, 'oauth-protected-resource', '/mcp'),
      authorization_server_metadata: automationWellKnownURL(baseURL, 'oauth-authorization-server'),
      pkce: 'S256',
    } : undefined,
    api: { base_url: `${baseURL}/api/v2`, openapi: `${baseURL}/api/v2/openapi.json` },
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
