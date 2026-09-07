// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const menu = readFileSync(path.resolve(__dirname, 'components/server/ServerActionMenu.tsx'), 'utf8')

describe('Server related key-management jumps', () => {
  it('exposes jumps to the selected server DNS panel and domain accounts', () => {
    expect(source).toMatch(/function openServerPanel\(/)
    expect(source).toMatch(/function ServerRelatedJumps\(/)
    expect(source).toMatch(/openServerPanel\(id, 'network', 'dns'\)/)
    expect(source).toMatch(/goTab\('dns-records'\)/)
    expect(source).toMatch(/<ServerRelatedJumps serverID=\{Number\(draft\.server_id/)
    expect(source).toMatch(/<ServerRelatedJumps serverID=\{Number\(draft\.issuance_server_id/)
    expect(menu).toMatch(/label: '服务器 DNS', type: 'dns'/)
    expect(menu).toMatch(/label: '域名账号', type: 'dns-records'/)
  })
})
