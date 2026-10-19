export type BBRQdisc = 'fq' | 'cake' | 'fq_pie' | 'off'

export const bbrQdiscOptions: { value: BBRQdisc; label: string }[] = [
  { value: 'fq', label: 'BBR + FQ' },
  { value: 'cake', label: 'BBR + CAKE' },
  { value: 'fq_pie', label: 'BBR + FQ-PIE' },
  { value: 'off', label: '不修改' },
]

export const bbrQdiscHints: Record<BBRQdisc, string> = {
  fq: '通用推荐，需要 Linux 4.9 及以上。',
  cake: '需要 Linux 4.19 及以上，可额外限制出站带宽来减少排队延迟；内核不支持时自动改用 FQ。',
  fq_pie: '需要 Linux 5.6 及以上，在 FQ 的基础上主动控制队列延迟；内核不支持时自动改用 FQ。',
  off: '安装时不修改系统的拥塞控制和队列算法。',
}

export const cakeBandwidthMaxMbit = 100000

export const cakeBandwidthHint = '可选，仅作用于出站。填服务器实际上行带宽的 90%–95% 可降低排队延迟；填得过低会限速，留空表示不限速。'

export function parseBBRQdisc(value: unknown): BBRQdisc {
  const qdisc = String(value || 'fq')
  return bbrQdiscOptions.some(option => option.value === qdisc) ? qdisc as BBRQdisc : 'fq'
}

export function bbrQdiscLabel(value: unknown, cakeMbit?: unknown): string {
  const qdisc = parseBBRQdisc(value)
  const label = bbrQdiscOptions.find(option => option.value === qdisc)!.label
  const mbit = Number(cakeMbit)
  return qdisc === 'cake' && Number.isInteger(mbit) && mbit > 0 ? `${label} · ${mbit} Mbit/s` : label
}
