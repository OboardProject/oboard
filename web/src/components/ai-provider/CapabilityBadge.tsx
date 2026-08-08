import type { AIProviderCapability } from './types'

export function capabilityGradeLabel(grade?: string): string {
  return ({ A: 'A 级', B: 'B 级', C: 'C 级', unusable: '不可用' } as Record<string, string>)[grade || ''] || '未测试'
}

export function CapabilityBadge({ capability }: { capability?: AIProviderCapability }) {
  const grade = capability?.audit_grade
  return <span className={`ai-provider-grade grade-${String(grade || 'none').toLowerCase()}`} title={capability?.notes?.join('；') || capability?.note || '尚未测试'}>{capabilityGradeLabel(grade)}</span>
}
