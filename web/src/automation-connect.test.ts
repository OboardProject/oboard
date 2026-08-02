import { describe, expect, it } from 'vitest'
import {
  automationConnectArtifacts,
  automationMCPURL,
  normalizeAutomationControllerURL,
  serviceTokenEnvironmentCommands,
  type AutomationConnectAuth,
  type AutomationConnectClient,
} from './automation-connect'

describe('automation MCP connection artifacts', () => {
  it('normalizes the public controller URL and preserves its base path', () => {
    expect(normalizeAutomationControllerURL(' https://panel.example.com/oboard/// ')).toBe('https://panel.example.com/oboard')
    expect(automationMCPURL('https://panel.example.com/oboard/')).toBe('https://panel.example.com/oboard/mcp')
    expect(normalizeAutomationControllerURL('https://user:secret@example.com')).toBe('')
    expect(normalizeAutomationControllerURL('https://panel.example.com/?token=secret')).toBe('')
  })

  it.each<[AutomationConnectClient, AutomationConnectAuth]>([
    ['codex', 'oauth'], ['codex', 'token'], ['claude', 'oauth'], ['claude', 'token'], ['generic', 'oauth'], ['generic', 'token'],
  ])('generates a complete %s %s guide without a secret', (client, auth) => {
    const artifacts = automationConnectArtifacts(client, auth, 'https://panel.example.com/base')
    expect(artifacts.config).toContain('https://panel.example.com/base/mcp')
    expect(artifacts.prompt).toContain('Streamable HTTP')
    expect(artifacts.prompt).toContain('保留其他服务器配置')
    expect(artifacts.prompt).not.toContain('obk_super-secret-token')
    if (auth === 'token') {
      expect(`${artifacts.command}\n${artifacts.config}\n${artifacts.prompt}`).toContain('OBOARD_MCP_TOKEN')
    } else {
      expect(artifacts.prompt).toContain('OAuth 2.1')
    }
  })

  it('uses the current Codex and Claude Code remote MCP configuration forms', () => {
    const codex = automationConnectArtifacts('codex', 'token', 'https://panel.example.com')
    expect(codex.command).toContain('--bearer-token-env-var OBOARD_MCP_TOKEN')
    expect(codex.config).toContain('bearer_token_env_var = "OBOARD_MCP_TOKEN"')
    const claude = automationConnectArtifacts('claude', 'token', 'https://panel.example.com')
    expect(claude.command).toContain('claude mcp add-json --scope user')
    expect(claude.config).toContain('Bearer ${OBOARD_MCP_TOKEN}')
  })

  it('uses RFC well-known locations when the controller has a base path', () => {
    const generic = automationConnectArtifacts('generic', 'oauth', 'https://panel.example.com/qzq')
    expect(generic.config).toContain('https://panel.example.com/.well-known/oauth-protected-resource/qzq/mcp')
    expect(generic.config).toContain('https://panel.example.com/.well-known/oauth-authorization-server/qzq')
    expect(generic.config).not.toContain('/qzq/.well-known/')
  })

  it('keeps the one-time token only in separate environment commands', () => {
    const token = "obk_super-secret-token'quoted"
    const commands = serviceTokenEnvironmentCommands(token)
    expect(commands.posix).toContain(token.replace("'", "'\\''"))
    expect(commands.powershell).toContain("obk_super-secret-token''quoted")
    const guide = automationConnectArtifacts('codex', 'token', 'https://panel.example.com')
    expect(JSON.stringify(guide)).not.toContain(token)
  })
})
