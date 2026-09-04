import { describe, expect, it } from 'vitest'

import { localizeManagedPublicPortExhaustion } from './error-localization'

describe('localizeManagedPublicPortExhaustion', () => {
  it('renders a Chinese title, range detail, and recovery hint', () => {
    expect(localizeManagedPublicPortExhaustion(
      'server GLBB has no available port in the managed public range 55000-55001 for shared shadowsocks chain service',
    )).toBe([
      '公网端口不足',
      '服务器「GLBB」的公网端口范围 55000–55001 已满，无法再分配共享 SS 链式服务。',
      '请到该服务器设置中扩大公网端口范围后重试。',
    ].join('\n'))
  })

  it('returns null for unrelated errors', () => {
    expect(localizeManagedPublicPortExhaustion('invalid credentials')).toBeNull()
  })
})
