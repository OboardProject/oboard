import { describe, expect, it } from 'vitest'
import { proxyPathGeneratedReuseCountKey } from './reuse-target-options'

describe('proxyPathGeneratedReuseCountKey', () => {
  it('keeps generated SOCKS5 paths out of the default Shadowsocks bucket', () => {
    expect(proxyPathGeneratedReuseCountKey({ chain_protocol: 'socks' })).toBe('socks')
  })

  it('uses the default Shadowsocks profile only when the protocol is omitted', () => {
    expect(proxyPathGeneratedReuseCountKey({})).toBe('shadowsocks:2022-blake3-aes-128-gcm')
    expect(proxyPathGeneratedReuseCountKey({ chain_protocol: 'unknown' })).toBeNull()
  })
})
