import { describe, expect, it } from 'vitest'
import { subscriptionBaseURL, subscriptionRelayCommand, subscriptionRelayDomain, subscriptionRelayPublicURL, subscriptionRelayStatus } from './subscription-relay'

describe('subscription relay commands', () => {
  it('keeps enrollment credentials out of the URL', () => {
    const command = subscriptionRelayCommand({
      controllerURL: 'https://panel.example/hidden',
      version: '1.2.3',
      action: 'install',
      enrollmentToken: 'secret-token',
    })
    expect(command).toContain("OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN='secret-token'")
    expect(command).toContain("'https://panel.example/hidden/install/subscription-relay.sh'")
    expect(command).not.toContain('?')
  })

  it('generates update and uninstall actions without enrollment material', () => {
    expect(subscriptionRelayCommand({ controllerURL: 'https://panel.example', version: 'dev', action: 'update' })).toContain("OBOARD_ACTION='update'")
    expect(subscriptionRelayCommand({ controllerURL: 'https://panel.example', version: 'dev', action: 'uninstall' })).toContain("OBOARD_ACTION='uninstall'")
  })

  it('uses the mutable development release for commit-qualified versions', () => {
    const command = subscriptionRelayCommand({
      controllerURL: 'https://panel.example/qzq',
      version: 'dev-5e1ebffaa8b1',
      action: 'install',
      enrollmentToken: 'secret-token',
    })
    expect(command).toContain("VERSION='dev'")
    expect(command).not.toContain('vdev-5e1ebffaa8b1')
  })

  it('maps operational states to visible labels', () => {
    expect(subscriptionRelayStatus('online')).toEqual({ label: '在线', tone: 'ok' })
    expect(subscriptionRelayStatus('failed').label).toBe('更新失败')
  })

  it('builds the public URL from a domain and the current base path', () => {
    expect(subscriptionRelayDomain('https://sub.example.com/qzq')).toBe('sub.example.com')
    expect(subscriptionRelayPublicURL('Sub.Example.com', '/qzq')).toBe('https://sub.example.com/qzq')
    expect(subscriptionRelayPublicURL('sub.example.com', '')).toBe('https://sub.example.com')
  })

  it('rejects protocol, port, IP and path values in the domain field', () => {
    expect(subscriptionRelayPublicURL('https://sub.example.com', '/qzq')).toBe('')
    expect(subscriptionRelayPublicURL('sub.example.com:8443', '/qzq')).toBe('')
    expect(subscriptionRelayPublicURL('203.0.113.1', '/qzq')).toBe('')
    expect(subscriptionRelayPublicURL('sub.example.com/path', '/qzq')).toBe('')
  })

  it('uses the active relay as the subscription base URL', () => {
    expect(subscriptionBaseURL('https://relay.example/qzq/', 'https://controller.example/qzq')).toBe('https://relay.example/qzq')
    expect(subscriptionBaseURL('', 'https://controller.example/qzq/')).toBe('https://controller.example/qzq')
  })
})
