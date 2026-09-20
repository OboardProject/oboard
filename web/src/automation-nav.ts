export function isAutomationNavTab(tab: string) {
  return tab === 'automation' || tab === 'plugins' || tab === 'plugin-triggers' || tab === 'plugin-runs'
}

export function navTabVisible(tab: string, canOpen: (page: string) => boolean) {
  if (tab === 'automation') return canOpen('automation') || canOpen('plugins')
  return canOpen(tab)
}

export function automationLandingTab() {
  return 'plugins'
}
