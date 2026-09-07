import { describe, expect, it } from 'vitest'
import { automationLandingTab, isAutomationNavTab, navTabVisible } from './automation-nav'

describe('automation navigation', () => {
  it('treats script pages as the automation entry', () => {
    expect(isAutomationNavTab('scripts')).toBe(true)
    expect(isAutomationNavTab('script-triggers')).toBe(true)
    expect(isAutomationNavTab('script-runs')).toBe(true)
    expect(isAutomationNavTab('automation')).toBe(true)
    expect(isAutomationNavTab('settings')).toBe(false)
  })

  it('shows automation to operators who can open scripts', () => {
    const canOpen = (page: string) => page === 'scripts'
    expect(navTabVisible('automation', canOpen)).toBe(true)
    expect(navTabVisible('settings', canOpen)).toBe(false)
    expect(automationLandingTab()).toBe('scripts')
  })
})
