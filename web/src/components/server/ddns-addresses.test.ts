import { describe, expect, it } from 'vitest'
import { ddnsAddressChoices, ddnsPrefixFromAddress } from './ddns-addresses'

const interfaces = [
  { name: 'lo', up: true, running: true, loopback: true, addresses: ['127.0.0.1/8'] },
  { name: 'eth0', up: true, running: true, loopback: false, addresses: ['203.0.113.8/24', '192.168.1.8/24', '100.64.0.8/32', '2001:db8::8/64', 'fe80::1/64'] },
  { name: 'eth1', up: false, running: false, loopback: false, addresses: ['198.51.100.9/32'] },
]

describe('ddnsAddressChoices', () => {
  it('hides loopback, down interfaces, and private addresses when asked', () => {
    const choices = ddnsAddressChoices(interfaces, '', true)
    expect(choices.map(choice => choice.address)).toEqual(['203.0.113.8/24', '2001:db8::8/64'])
  })

  it('keeps a chosen private address only when the filter is off', () => {
    const choices = ddnsAddressChoices(interfaces, 'ipv4', false)
    expect(choices.map(choice => choice.address)).toEqual(['203.0.113.8/24', '192.168.1.8/24', '100.64.0.8/32'])
  })

  it('filters by family', () => {
    expect(ddnsAddressChoices(interfaces, 'ipv6', true).map(choice => choice.address)).toEqual(['2001:db8::8/64'])
  })
})

it('generates network prefixes using the reported mask for both address families', () => {
  expect(ddnsPrefixFromAddress('203.0.113.8/24')).toBe('203.0.113.0/24')
  expect(ddnsPrefixFromAddress('203.0.113.138/25')).toBe('203.0.113.128/25')
  expect(ddnsPrefixFromAddress('2001:db8:1234:abcd::8/48')).toBe('2001:db8:1234::/48')
  expect(ddnsPrefixFromAddress('2001:db8:1234:abcd::8/57')).toBe('2001:db8:1234:ab80::/57')
  expect(ddnsPrefixFromAddress('2001:db8::8/128')).toBe('2001:db8::8/128')
  expect(ddnsPrefixFromAddress('203.0.113.8')).toBe('203.0.113.8/32')
  expect(ddnsPrefixFromAddress('2001:db8::8/0')).toBe('::/0')
  for (const invalid of ['203.0.113.8/33', '2001:db8::8/129', '203.0.113.8/', '203.0.113.8/24/1']) expect(ddnsPrefixFromAddress(invalid)).toBeNull()
})
