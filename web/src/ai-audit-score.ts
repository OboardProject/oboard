export type AuditHealthScoreTone = 'good' | 'warning' | 'danger'

export function normalizeAuditHealthScore(score: number): number {
  if (!Number.isFinite(score)) return 0
  return Math.min(100, Math.max(0, Math.round(score)))
}

export function auditHealthScoreTone(score: number): AuditHealthScoreTone {
  const normalized = normalizeAuditHealthScore(score)
  if (normalized >= 80) return 'good'
  if (normalized >= 60) return 'warning'
  return 'danger'
}
