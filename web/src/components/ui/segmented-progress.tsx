export type ProgressSegment = { key: string; label: string; value: number; tone: 'success' | 'info' | 'warning' | 'danger' | 'muted' }

export function SegmentedProgress({ label, segments, done, caption }: { label: string; segments: ProgressSegment[]; done: number; caption?: React.ReactNode }) {
  const total = segments.reduce((sum, item) => sum + Math.max(0, item.value || 0), 0)
  const percent = total > 0 ? Math.round((done / total) * 100) : 0
  return <div className="segmented-progress">
    <div className="segmented-progress-head">
      <span>{caption}</span>
      <span><strong>{done}</strong> / {total}（{percent}%）</span>
    </div>
    <div className="segmented-progress-bar" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={total} aria-valuenow={done}>
      {total > 0 && segments.filter(item => item.value > 0).map(item => <span key={item.key} className={`is-${item.tone}`} style={{ flexGrow: item.value }} title={`${item.label} ${item.value}`} />)}
    </div>
    <div className="segmented-progress-legend">
      {segments.map(item => <span key={item.key} className={`is-${item.tone}`}><i />{item.label}<strong>{item.value}</strong></span>)}
    </div>
  </div>
}
