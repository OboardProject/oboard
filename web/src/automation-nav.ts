export function isAutomationNavTab(tab: string) {
  return tab === 'automation' || tab === 'scripts' || tab === 'script-triggers' || tab === 'script-runs'
}

export function navTabVisible(tab: string, canOpen: (page: string) => boolean) {
  if (tab === 'automation') return canOpen('automation') || canOpen('scripts')
  return canOpen(tab)
}

export function automationLandingTab() {
  return 'scripts'
}
