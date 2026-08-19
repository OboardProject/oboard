import { Check, Info, RefreshCw } from 'lucide-react'

import { configurationSyncPresentation, type ConfigurationSyncRow } from './configuration-sync'

type ConfigurationSyncStatusProps = {
  rows: ConfigurationSyncRow[]
  saving?: boolean
  retrying?: boolean
  canOperate?: boolean
  onRetry?: () => void
}

export function ConfigurationSyncStatus({ rows, saving = false, retrying = false, canOperate = true, onRetry }: ConfigurationSyncStatusProps) {
  if (!canOperate) return null
  const presentation = configurationSyncPresentation(rows, saving, retrying)
  const failed = rows.filter(item => item.state === 'failed')
  if (failed.length > 0) {
    return (
      <button
        type="button"
        className="deploy-status-pill dismissable danger"
        onClick={onRetry}
        disabled={retrying || !onRetry}
        title={failed.map(item => item.error).filter(Boolean).join('；') || '点击重试同步'}
        aria-label={presentation.label}
      >
        <RefreshCw size={15} className={retrying ? 'spin' : ''} />
        <span>{presentation.label}</span>
      </button>
    )
  }
  return (
    <div className={`deploy-status-pill ${saving || presentation.busy ? 'info' : presentation.tone === 'ok' ? 'ok' : 'warn'}`} aria-live="polite">
      {saving || presentation.busy ? <RefreshCw size={15} className="spin" /> : presentation.tone === 'ok' ? <Check size={16} /> : <Info size={16} />}
      <span>{presentation.label}</span>
    </div>
  )
}
