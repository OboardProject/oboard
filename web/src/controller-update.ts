export const CONTROLLER_UPDATE_PENDING_MESSAGE = '正在等待更新完成'
export const CONTROLLER_UPDATE_FORCE_FINISH_PHRASE = '强制结束更新任务'

export function isControllerUpdateForceFinishConfirmation(value: string | null | undefined): boolean {
  return String(value || '').trim() === CONTROLLER_UPDATE_FORCE_FINISH_PHRASE
}

const CONTROLLER_UPDATE_IN_PROGRESS_STATUSES = [
  'checking',
  'downloading',
  'preflight',
  'backing_up',
  'ready',
  'installing',
  'restarting',
  'verifying',
  'cancelling',
]
const EXPECTED_DISCONNECT_MARKERS = ['failed to fetch', 'networkerror', 'load failed', 'bad gateway', 'service unavailable', 'gateway timeout']

export function isControllerUpdateInProgressStatus(status: string | undefined | null): boolean {
  return status != null && CONTROLLER_UPDATE_IN_PROGRESS_STATUSES.includes(status)
}

export function controllerUpdateDisplayPhase(status: { status?: string; operation?: { active?: boolean; phase?: string } } | null | undefined): string {
  if (status?.operation?.active && status.operation.phase) return status.operation.phase
  return status?.status || ''
}

const FLOW_BASE: Record<string, number> = {
  starting: 6,
  checking: 10,
  downloading: 22,
  preflight: 32,
  backing_up: 34,
  ready: 74,
  installing: 78,
  restarting: 88,
  verifying: 96,
  cancelling: 40,
}

export function controllerUpdateFlowPercent(phase: string, backupPercent?: number, downloadPercent?: number): number {
  if (phase === 'complete' || phase === 'installed') return 100
  if (phase === 'downloading' && downloadPercent !== undefined) return 10 + Math.max(0, Math.min(100, downloadPercent)) * 0.2
  if (phase === 'backing_up') {
    const backup = Math.max(0, Math.min(100, backupPercent || 0))
    return Math.max(34, Math.min(72, 34 + backup * 0.38))
  }
  const base = FLOW_BASE[phase]
  if (typeof base === 'number') return base
  return 8
}

export function monotonicPercent(previous: number, next: number): number {
  const value = Math.max(0, Math.min(100, Math.round(next)))
  if (value < previous) return previous
  return value
}

export function isControllerUpdateFailedStatus(status: string | undefined | null, lastError?: string | null): boolean {
  return status === 'failed' || status === 'unavailable' || (status === 'cancelled' && Boolean(lastError))
}

const EXPECTED_DISCONNECT_STATUSES = [502, 503, 504]

export function createControllerUpdateRequestGuard() {
  let latestRequest = 0
  return {
    beginRequest() {
      latestRequest += 1
      return latestRequest
    },
    invalidate() {
      latestRequest += 1
    },
    isLatest(request: number) {
      return request === latestRequest
    },
  }
}

export function shouldDeferControllerUpdateTerminalStatus(status: string, installRequestPending: boolean, cancelExpected: boolean): boolean {
  if (!installRequestPending || cancelExpected) return false
  return status === 'cancelled' || status === 'idle' || status === 'pinned' || status === 'available'
}

export function isExpectedControllerUpdateDisconnect(error: unknown): boolean {
  if (error instanceof TypeError) return true
  const status = Number((error as any)?.status)
  if (EXPECTED_DISCONNECT_STATUSES.includes(status)) return true
  const message = String((error as any)?.message || error || '').trim().toLowerCase()
  return EXPECTED_DISCONNECT_MARKERS.some(value => message.includes(value))
}

export type ControllerUpdatePendingToast = { message: string; kind: 'info' }

// Only the "installing" and "restarting" phases actually replace or relaunch the
// Controller process (see internal/controller/controller_update_orchestrator.go).
// Every earlier phase (checking/downloading/preflight/backing_up/ready) keeps the
// same Controller process serving requests, so a network failure there is a real,
// unrelated error and must not be swallowed as an expected update disconnect.
const CONTROLLER_UPDATE_RESTART_PHASES = ['installing', 'restarting']

export function isControllerUpdateRestartPhase(phase: string | undefined | null): boolean {
  return phase != null && CONTROLLER_UPDATE_RESTART_PHASES.includes(phase)
}

export function controllerUpdatePendingToast(updateInProgress: boolean, error: unknown, phase?: string | null): ControllerUpdatePendingToast | null {
  if (!updateInProgress) return null
  if (!isControllerUpdateRestartPhase(phase)) return null
  if (!isExpectedControllerUpdateDisconnect(error)) return null
  return { message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' }
}

export type ControllerUpdateStatusLineInput = {
  phase: string
  connectionInterrupted?: boolean
  downloadPercent?: number
  backupPercent?: number
  targetVersion?: string
  failure?: string
}

// One short line under the update animation. Details and logs live behind it.
export function controllerUpdateStatusLine(input: ControllerUpdateStatusLineInput): string {
  const { phase, connectionInterrupted, downloadPercent, backupPercent, targetVersion, failure } = input
  if (connectionInterrupted && ['installing', 'restarting'].includes(phase)) return '主控正在重启，等待重新连接'
  switch (phase) {
    case 'starting': return '正在启动更新'
    case 'checking': return '正在检查更新'
    case 'downloading': return downloadPercent === undefined ? '正在下载更新' : `正在下载更新 · ${downloadPercent.toFixed(1)}%`
    case 'preflight': return '正在准备安装'
    case 'backing_up': return `正在备份数据库 · ${Math.round(backupPercent || 0)}%`
    case 'ready': return '即将安装新版本'
    case 'installing': return '正在安装更新'
    case 'restarting': return '主控正在重启'
    case 'verifying': return '正在验证新版本'
    case 'cancelling': return '正在停止更新'
    case 'complete': return targetVersion ? `更新完成 · ${targetVersion}` : '更新完成'
    case 'cancelled': return '更新已安全中断，当前版本未改动'
    case 'stopped': return '本次更新已停止'
    case 'force_finished': return '已停止追踪本次更新'
    case 'failed': {
      const reason = String(failure || '').split('\n')[0].trim()
      return reason ? `更新失败：${reason}` : '更新失败'
    }
    default: return '正在准备更新'
  }
}

export type ControllerUpdateAnimationMode = 'idle' | 'running' | 'success' | 'failed'

export function controllerUpdateAnimationMode(phase: string): ControllerUpdateAnimationMode {
  if (phase === 'confirm') return 'idle'
  if (phase === 'complete') return 'success'
  if (phase === 'failed' || phase === 'stopped' || phase === 'force_finished') return 'failed'
  if (phase === 'cancelled') return 'idle'
  return 'running'
}
