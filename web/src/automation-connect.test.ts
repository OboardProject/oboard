import { describe, expect, it } from 'vitest'
import {
  automationConnectArtifacts,
  automationMCPURL,
  normalizeAutomationControllerURL,
  oboardOAuthScopes,
  type AutomationConnectClient,
} from './automation-connect'

describe('automation MCP connection artifacts', () => {
  it('normalizes the public controller URL and preserves its base path', () => {
    expect(normalizeAutomationControllerURL(' https://panel.example.com/oboard/// ')).toBe('https://panel.example.com/oboard')
    expect(automationMCPURL('https://panel.example.com/oboard/')).toBe('https://panel.example.com/oboard/api/v1/mcp')
    expect(normalizeAutomationControllerURL('https://user:secret@example.com')).toBe('')
    expect(normalizeAutomationControllerURL('https://panel.example.com/?token=secret')).toBe('')
  })

  it('generates a complete generic OAuth guide without a secret', () => {
    const artifacts = automationConnectArtifacts('https://panel.example.com/base')
    expect(artifacts.config).toContain('https://panel.example.com/base/api/v1/mcp')
    expect(artifacts.prompt).toContain('Streamable HTTP')
    expect(artifacts.prompt).toContain('保留其他服务器配置')
    expect(artifacts.prompt).not.toContain('obk_super-secret-token')
    expect(artifacts.prompt).toContain('OAuth 2.1')
    expect(`${artifacts.command}\n${artifacts.config}\n${artifacts.prompt}`).not.toContain('bearer_token')
    expect(artifacts.prompt).toContain('Hermes')
  })

  it('produces a Hermes-compatible generic configuration without client-specific commands', () => {
    const artifacts = automationConnectArtifacts('https://panel.example.com')
    expect(artifacts.command).toBe('')
    expect(artifacts.config).toContain('"url": "https://panel.example.com/api/v1/mcp"')
    expect(artifacts.config).toContain('"transport": "streamable-http"')
    expect(artifacts.config).toContain('oauth')
    expect(artifacts.config).toContain('protected_resource_metadata')
    expect(artifacts.prompt).toContain('Hermes')
    expect(artifacts.prompt).not.toContain('Codex')
    expect(artifacts.prompt).not.toContain('Claude Code')
    expect(JSON.stringify(artifacts)).not.toContain('codex mcp')
    expect(JSON.stringify(artifacts)).not.toContain('claude mcp')
  })

  it('uses only the current coarse OAuth scopes, not legacy fine-grained scopes', () => {
    const artifacts = automationConnectArtifacts('https://panel.example.com')
    for (const scope of oboardOAuthScopes) {
      expect(artifacts.prompt).toContain(`- ${scope}`)
    }
    for (const legacy of ['inventory:read', 'servers:onboard', 'topology:write', 'deployments:apply']) {
      expect(artifacts.config).not.toContain(legacy)
      expect(artifacts.prompt).not.toContain(legacy)
    }
    expect(artifacts.prompt).toContain('跟随当前 OBoard 用户角色')
    expect(artifacts.prompt).not.toContain('风险 2 级写权限')
  })

  it('uses RFC well-known locations when the controller has a base path', () => {
    const generic = automationConnectArtifacts('https://panel.example.com/qzq')
    expect(generic.config).toContain('https://panel.example.com/.well-known/oauth-protected-resource/qzq/api/v1/mcp')
    expect(generic.config).toContain('https://panel.example.com/.well-known/oauth-authorization-server/qzq')
    expect(generic.config).not.toContain('/qzq/.well-known/')
  })

  it('keeps backward-compatible two-arg call for generic', () => {
    const viaSingle = automationConnectArtifacts('https://panel.example.com/base')
    const viaGeneric = automationConnectArtifacts('generic' as AutomationConnectClient, 'https://panel.example.com/base')
    expect(viaGeneric.config).toBe(viaSingle.config)
    expect(viaGeneric.prompt).toBe(viaSingle.prompt)
  })

  it('never emits a static MCP token configuration path', () => {
    const guide = automationConnectArtifacts('https://panel.example.com')
    expect(JSON.stringify(guide)).not.toContain('OBOARD_MCP_TOKEN')
    expect(JSON.stringify(guide)).not.toContain('bearer_token_env_var')
  })
})
