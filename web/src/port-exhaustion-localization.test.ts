// @vitest-environment node

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const main = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'main.tsx'), 'utf8')

function localizeManagedPublicPortExhaustion(raw: string) {
  const match = /^server (.+?) has no available port in the managed public range (\d+)-(\d+) for shared (.+?) chain service$/i.exec(raw)
  if (!match) return null
  const [, server, start, end, protocol] = match
  const protocolLabel = (() => {
    switch (String(protocol || '').trim().toLowerCase()) {
      case 'shadowsocks': return 'SS'
      case 'hy2': return 'HY2'
      case 'anytls': return 'AnyTLS'
      case 'mieru': return 'Mieru'
      case 'vless': return 'VLESS'
      case 'socks': return 'SOCKS'
      case 'snell': return 'Snell'
      case 'ssh': return 'SSH'
      default: return String(protocol || '').trim() || '协议'
    }
  })()
  return [
    '公网端口不足',
    `服务器「${server}」的公网端口范围 ${start}–${end} 已满，无法再分配共享 ${protocolLabel} 链式服务。`,
    '请到该服务器设置中扩大公网端口范围后重试。',
  ].join('\n')
}

describe('managed public port exhaustion localization', () => {
  it('keeps the localization helper wired into localizeErrorMessage', () => {
    expect(main).toContain('localizeManagedPublicPortExhaustion(raw)')
    expect(main).toContain('ErrorMessageCopy')
    expect(main).toContain('related-path-error-copy')
  })

  it('renders a Chinese title, range detail, and recovery hint', () => {
    const localized = localizeManagedPublicPortExhaustion(
      'server GLBB has no available port in the managed public range 55000-55001 for shared shadowsocks chain service',
    )
    expect(localized).toBe([
      '公网端口不足',
      '服务器「GLBB」的公网端口范围 55000–55001 已满，无法再分配共享 SS 链式服务。',
      '请到该服务器设置中扩大公网端口范围后重试。',
    ].join('\n'))
  })
})
