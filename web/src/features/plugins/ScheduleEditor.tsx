import React, { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Button } from '../../components/ui/button'
import { Input } from '../../components/ui/input'
import { Select } from '../../components/ui/select'
import { Switch } from '../../components/ui/switch'
import { eventLabels, formatTime } from './domain'
import type { Manifest, Schedule } from './types'

type Draft = { kind: Schedule['kind']; interval: string; cron: string; timezone: string; event: string }

const localTimezone = () => {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC' } catch { return 'UTC' }
}

export function describeSchedule(schedule: Schedule) {
  if (schedule.kind === 'interval') return `每 ${schedule.interval}`
  if (schedule.kind === 'cron') return `Cron ${schedule.cron}（${schedule.timezone || 'UTC'}）`
  return eventLabels[schedule.event || ''] || schedule.event || '事件'
}

export function ScheduleEditor({ manifest, schedules, canEdit, busy, onCreate, onToggle, onDelete }: {
  manifest?: Manifest
  schedules: Schedule[]
  canEdit: boolean
  busy?: boolean
  onCreate: (input: Partial<Schedule>) => void
  onToggle: (schedule: Schedule, enabled: boolean) => void
  onDelete: (schedule: Schedule) => void
}) {
  const events = manifest?.triggers?.events || []
  const scheduleAllowed = Boolean(manifest?.triggers?.schedule)
  const kinds: Schedule['kind'][] = [...(scheduleAllowed ? ['interval', 'cron'] as const : []), ...(events.length ? ['event'] as const : [])]
  const [draft, setDraft] = useState<Draft>({ kind: kinds[0] || 'interval', interval: '15m', cron: '0 * * * *', timezone: localTimezone(), event: events[0] || '' })
  const [open, setOpen] = useState(false)

  const submit = () => {
    if (draft.kind === 'interval') onCreate({ kind: 'interval', interval: draft.interval.trim(), enabled: true })
    else if (draft.kind === 'cron') onCreate({ kind: 'cron', cron: draft.cron.trim(), timezone: draft.timezone.trim(), enabled: true })
    else onCreate({ kind: 'event', event: draft.event, enabled: true })
    setOpen(false)
  }

  return <div className="plugin-schedules">
    {!kinds.length && <p className="text-sm text-muted-foreground">此插件只能手动运行。</p>}
    {schedules.length > 0 && <ul className="plugin-record-list">
      {schedules.map(schedule => <li key={schedule.id} className="plugin-record-row">
        <div className="min-w-0">
          <strong>{describeSchedule(schedule)}</strong>
          <p className="plugin-env-help">
            {schedule.kind !== 'event' && <>下次 {formatTime(schedule.next_due_at)} · </>}
            上次触发 {formatTime(schedule.last_fired_at)}
            {schedule.last_skip_reason && <> · 上次跳过：{schedule.last_skip_reason}</>}
          </p>
        </div>
        {canEdit && <div className="plugin-row-actions">
          <Switch checked={schedule.enabled} disabled={busy} ariaLabel={`${schedule.enabled ? '停用' : '启用'}计划`} onChange={enabled => onToggle(schedule, enabled)} />
          <Button type="button" size="icon" variant="ghost" aria-label="删除计划" disabled={busy} onClick={() => onDelete(schedule)}><Trash2 size={15} /></Button>
        </div>}
      </li>)}
    </ul>}
    {canEdit && kinds.length > 0 && (open ? <div className="plugin-schedule-form">
      <Select aria-label="触发方式" value={draft.kind} onChange={event => setDraft({ ...draft, kind: event.target.value as Schedule['kind'] })}>
        {kinds.map(kind => <option key={kind} value={kind}>{kind === 'interval' ? '固定间隔' : kind === 'cron' ? 'Cron 表达式' : '服务器事件'}</option>)}
      </Select>
      {draft.kind === 'interval' && <label className="plugin-inline-field">间隔<Input value={draft.interval} placeholder="15m" onChange={event => setDraft({ ...draft, interval: event.target.value })} /></label>}
      {draft.kind === 'cron' && <>
        <label className="plugin-inline-field">表达式<Input value={draft.cron} placeholder="0 * * * *" onChange={event => setDraft({ ...draft, cron: event.target.value })} /></label>
        <label className="plugin-inline-field">时区<Input value={draft.timezone} onChange={event => setDraft({ ...draft, timezone: event.target.value })} /></label>
      </>}
      {draft.kind === 'event' && <Select aria-label="事件" value={draft.event} onChange={event => setDraft({ ...draft, event: event.target.value })}>
        {events.map(item => <option key={item} value={item}>{eventLabels[item] || item}</option>)}
      </Select>}
      <p className="plugin-env-help">最短间隔 1 分钟。错过的触发会合并为一次，不会补跑积压。</p>
      <div className="plugin-actions">
        <Button type="button" variant="ghost" onClick={() => setOpen(false)}>取消</Button>
        <Button type="button" busy={busy} onClick={submit}>添加计划</Button>
      </div>
    </div> : <Button type="button" size="sm" variant="outline" onClick={() => setOpen(true)}><Plus size={14} />添加计划</Button>)}
  </div>
}
