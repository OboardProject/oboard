import { describe, expect, it } from 'vitest'
import { automationLandingTab, isAutomationNavTab, navTabVisible } from './automation-nav'

describe('automation navigation', () => {
  it('treats plugin pages as the automation entry', () => {
    expect(isAutomationNavTab('plugins')).toBe(true)
    expect(isAutomationNavTab('plugin-triggers')).toBe(true)
    expect(isAutomationNavTab('plugin-runs')).toBe(true)
    expect(isAutomationNavTab('automation')).toBe(true)
    expect(isAutomationNavTab('settings')).toBe(false)
  })

  it('shows automation to operators who can open plugins', () => {
    const canOpen = (page: string) => page === 'plugins'
    expect(navTabVisible('automation', canOpen)).toBe(true)
    expect(navTabVisible('settings', canOpen)).toBe(false)
    expect(automationLandingTab()).toBe('plugins')
  })
})
