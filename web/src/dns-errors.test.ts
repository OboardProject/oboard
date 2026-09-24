import { describe, expect, it } from 'vitest'
import { localizeDNSError } from './dns-errors'

describe('DNS error localization', () => {
  it('names the group and the resolver position', () => {
    expect(localizeDNSError('bootstrap resolvers: candidate[0]: dns server must be a public address')).toBe('基础解析：第 1 个解析服务：解析服务地址必须是公网地址')
    expect(localizeDNSError('encrypted resolvers: candidate[2]: encrypted list only supports doh, dot, or doq')).toBe('加密解析：第 3 个解析服务：加密解析只支持 DoH、DoT 或 DoQ')
    expect(localizeDNSError('encrypted resolvers: choose either a shared dns list or custom resolvers, not both')).toBe('加密解析：同一组只能选择共享列表或自定义解析服务，不能同时使用')
  })

  it('localizes nested host errors and leaves unrelated text alone', () => {
    expect(localizeDNSError('bootstrap resolvers: candidate[1]: dns server: host contains unsafe characters')).toBe('基础解析：第 2 个解析服务：解析服务地址包含不允许的字符')
    expect(localizeDNSError('bootstrap_list_id or bootstrap_candidates are required')).toBe('请选择基础解析列表或填写自定义基础解析服务')
    expect(localizeDNSError('something else failed')).toBe('')
  })
})
