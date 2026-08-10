import { describe, expect, it } from 'vitest'
import { subscriptionRelayCommand, subscriptionRelayStatus } from './subscription-relay'

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

  it('maps operational states to visible labels', () => {
    expect(subscriptionRelayStatus('online')).toEqual({ label: '在线', tone: 'ok' })
    expect(subscriptionRelayStatus('failed').label).toBe('更新失败')
  })
})
