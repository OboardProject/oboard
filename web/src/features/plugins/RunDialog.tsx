import React, { useEffect, useRef, useState } from 'react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import * as api from './api'
import { describeError, errorMessage, formatTime, runStatusLabels, runTerminal, runTone, triggerLabels } from './domain'
import type { RequestFn, Run, RunLog } from './types'

export function RunDialog({ id, request, canCancel, onClose }: { id: number; request: RequestFn; canCancel: boolean; onClose: () => void }) {
  const [run, setRun] = useState<Run | null>(null)
  const [logs, setLogs] = useState<RunLog[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const lastSeq = useRef(0)

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    let cancelled = false
    lastSeq.current = 0
    const load = async () => {
      try {
        const [next, page] = await Promise.all([api.getRun(request, id), api.runLogs(request, id, lastSeq.current)])
        if (cancelled) return
        setRun(next.run)
        if (page.logs?.length) {
          lastSeq.current = page.logs[page.logs.length - 1].seq
          setLogs(current => [...current, ...page.logs].slice(-2000))
        }
        setError('')
        if (!runTerminal(next.run.status)) timer = setTimeout(load, 1500)
      } catch (e) {
        if (!cancelled) setError(errorMessage(e, '读取执行记录失败'))
      }
    }
    void load()
    return () => { cancelled = true; if (timer) clearTimeout(timer) }
  }, [id, request])

  const cancel = async () => {
    setBusy(true)
    try { setRun((await api.cancelRun(request, id)).run) } catch (e) { setError(errorMessage(e, '取消失败')) } finally { setBusy(false) }
  }

  return <Dialog isOpen onClose={onClose} title={`执行 #${id}`} size="lg" placement="right" drawerSize="wide"
    footer={run && !runTerminal(run.status) && canCancel ? <Button variant="outline" busy={busy} disabled={run.cancel_requested} onClick={() => void cancel()}>{run.cancel_requested ? '已请求取消' : '取消执行'}</Button> : undefined}>
    <div className="plugin-run-detail">
      {error && <p role="alert" className="plugin-field-error">{error}</p>}
      {!run ? <p className="text-sm text-muted-foreground" role="status">正在读取…</p> : <>
        <div className="plugin-run-summary">
          <Badge variant={runTone(run.status)}>{runStatusLabels[run.status] || run.status}</Badge>
          <span>{run.plugin_key} {run.plugin_version}</span>
          <span>{triggerLabels[run.trigger] || run.trigger}</span>
        </div>
        <dl className="plugin-facts">
          <div><dt>排队</dt><dd>{formatTime(run.queued_at)}</dd></div>
          <div><dt>开始</dt><dd>{formatTime(run.started_at)}</dd></div>
          <div><dt>结束</dt><dd>{formatTime(run.finished_at)}</dd></div>
          <div><dt>能力调用</dt><dd>{run.capability_call_count} 次 · HTTP {run.http_call_count} · 节点 {run.agent_operation_count}</dd></div>
        </dl>
        {run.error_code && <div className="plugin-callout danger"><p>{describeError(run.error_code, run.error_message)}</p></div>}
        {run.result !== undefined && run.result !== null && <section>
          <h4 className="plugin-subhead">返回值</h4>
          <pre className="plugin-pre">{JSON.stringify(run.result, null, 2)}</pre>
        </section>}
      </>}
      <section>
        <h4 className="plugin-subhead">日志</h4>
        {!logs.length ? <p className="text-sm text-muted-foreground">暂无日志。</p> : <ol className="plugin-log" aria-live="polite">
          {logs.map(line => <li key={line.seq} data-level={line.level}><span className="plugin-log-level">{line.level}</span><span className="plugin-log-text">{line.message}</span></li>)}
        </ol>}
      </section>
    </div>
  </Dialog>
}
