// The Controller's failure cache backs off for five seconds. Wait beyond it
// before one more chart read; never retry writes, authentication or browser aborts.
export const MONITOR_CHART_RETRY_DELAY_MS = 6000

export async function readMonitorChart<T>(signal: AbortSignal, read: () => Promise<T>, onRetry: () => void): Promise<T> {
  try {
    return await read()
  } catch (error) {
    if (signal.aborted) throw new DOMException('Cancelled', 'AbortError')
    if (!isUnavailable(error)) throw error
  }
  onRetry()
  await new Promise<void>((resolve, reject) => {
    if (signal.aborted) { reject(new DOMException('Cancelled', 'AbortError')); return }
    const abort = () => { clearTimeout(timer); signal.removeEventListener('abort', abort); reject(new DOMException('Cancelled', 'AbortError')) }
    const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve() }, MONITOR_CHART_RETRY_DELAY_MS)
    signal.addEventListener('abort', abort, { once: true })
  })
  if (signal.aborted) throw new DOMException('Cancelled', 'AbortError')
  try {
    return await read()
  } catch (error) {
    if (signal.aborted) throw new DOMException('Cancelled', 'AbortError')
    if (isUnavailable(error)) throw Object.assign(new Error('主控暂时繁忙，自动重试后仍未完成；请稍后重试或缩短时间范围'), { status: 503 })
    throw error
  }
}

function isUnavailable(error: unknown) {
  return !!error && typeof error === 'object' && 'status' in error && error.status === 503 && (!('code' in error) || error.code !== 'history_catching_up')
}
