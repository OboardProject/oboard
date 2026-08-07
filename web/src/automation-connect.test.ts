import { describe, expect, it } from 'vitest'
import {
  automationConnectArtifacts,
  automationMCPURL,
  codexOAuthRisk2Scopes,
  codexOAuthScopes,
  normalizeAutomationControllerURL,
  type AutomationConnectClient,
} from './automation-connect'

describe('automation MCP connection artifacts', () => {
  it('normalizes the public controller URL and preserves its base path', () => {
    expect(normalizeAutomationControllerURL(' https://panel.example.com/oboard/// ')).toBe('https://panel.example.com/oboard')
    expect(automationMCPURL('https://panel.example.com/oboard/')).toBe('https://panel.example.com/oboard/mcp')
    expect(normalizeAutomationControllerURL('https://user:secret@example.com')).toBe('')
    expect(normalizeAutomationControllerURL('https://panel.example.com/?token=secret')).toBe('')
  })

  it.each<AutomationConnectClient>(['codex', 'claude', 'generic'])('generates a complete %s OAuth guide without a secret', client => {
    const artifacts = automationConnectArtifacts(client, 'https://panel.example.com/base')
    expect(artifacts.config).toContain('https://panel.example.com/base/mcp')
    expect(artifacts.prompt).toContain('Streamable HTTP')
    expect(artifacts.prompt).toContain('保留其他服务器配置')
    expect(artifacts.prompt).not.toContain('obk_super-secret-token')
    expect(artifacts.prompt).toContain('OAuth 2.1')
    expect(`${artifacts.command}\n${artifacts.config}\n${artifacts.prompt}`).not.toContain('bearer_token')
  })

  it('uses the current Codex and Claude Code remote MCP configuration forms', () => {
    const codex = automationConnectArtifacts('codex', 'https://panel.example.com')
    expect(codex.command).toContain('codex mcp login oboard')
    expect(codex.config).toContain('auth = "oauth"')
    expect(codex.config).toContain('oauth_resource = "https://panel.example.com/mcp"')
    expect(codex.config).toContain('default_tools_approval_mode = "writes"')
    for (const scope of codexOAuthScopes) expect(codex.config).toContain(`"${scope}"`)
    for (const scope of codexOAuthRisk2Scopes) expect(codex.config).toContain(`"${scope}"`)
    const claude = automationConnectArtifacts('claude', 'https://panel.example.com')
    expect(claude.command).toContain('claude mcp add --transport http --scope user')
    expect(claude.config).not.toContain('Authorization')
  })

  it('adds risk 2 write scopes to the request only when enabled', () => {
    const enabled = automationConnectArtifacts('codex', 'https://panel.example.com', { risk2: true })
    for (const scope of codexOAuthRisk2Scopes) {
      expect(enabled.config).toContain(`"${scope}"`)
      expect(enabled.prompt).toContain(`- ${scope}`)
    }
    expect(enabled.prompt).toContain('已勾选')
    const disabled = automationConnectArtifacts('codex', 'https://panel.example.com', { risk2: false })
    for (const scope of codexOAuthRisk2Scopes) expect(disabled.config).not.toContain(`"${scope}"`)
    for (const scope of codexOAuthScopes) expect(disabled.config).toContain(`"${scope}"`)
    expect(disabled.prompt).toContain('未勾选')
    expect(disabled.prompt).not.toContain('- servers:onboard')
  })

  it('uses RFC well-known locations when the controller has a base path', () => {
    const generic = automationConnectArtifacts('generic', 'https://panel.example.com/qzq')
    expect(generic.config).toContain('https://panel.example.com/.well-known/oauth-protected-resource/qzq/mcp')
    expect(generic.config).toContain('https://panel.example.com/.well-known/oauth-authorization-server/qzq')
    expect(generic.config).not.toContain('/qzq/.well-known/')
  })

  it('never emits a static MCP token configuration path', () => {
    const guide = automationConnectArtifacts('codex', 'https://panel.example.com')
    expect(JSON.stringify(guide)).not.toContain('OBOARD_MCP_TOKEN')
    expect(JSON.stringify(guide)).not.toContain('bearer_token_env_var')
  })
})
