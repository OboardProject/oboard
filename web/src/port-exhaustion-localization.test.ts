import { describe, expect, it } from 'vitest'

import { localizeManagedPublicPortExhaustion, localizeRelayUpdateFailure } from './error-localization'

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

describe('localizeRelayUpdateFailure', () => {
  it('explains bare exit status failures', () => {
    expect(localizeRelayUpdateFailure('relay update failed: exit status 2')).toBe([
      '订阅中继更新失败',
      '安装脚本异常退出（exit status 2）。',
      '请到中继主机查看 updater 日志后重试，或先开启主控直连订阅恢复访问。',
    ].join('\n'))
  })

  it('keeps installer detail text', () => {
    expect(localizeRelayUpdateFailure('relay update failed: exit status 1: 无法从主控下载 oboard-subscription-relay-linux-amd64.tar.gz.')).toContain('无法从主控下载')
  })
})
