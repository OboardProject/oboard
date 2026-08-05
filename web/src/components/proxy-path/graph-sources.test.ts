import { describe, expect, it } from 'vitest'
import { SERVER_GRAPH_SOURCE_HANDLE, graphServerSourceOptions, inboundIDFromServerHandle, serverEntryHandleID, serverEntryTargetHandleID } from './graph-sources'

describe('server graph sources', () => {
  it('keeps the batch source handle stable', () => {
    expect(SERVER_GRAPH_SOURCE_HANDLE).toBe('server-source')
  })

  it('keeps each inbound on its own server handle', () => {
    expect(serverEntryHandleID(12)).toBe('server-entry-12')
    expect(serverEntryTargetHandleID(12)).toBe('server-entry-target-12')
    expect(inboundIDFromServerHandle('server-entry-12')).toBe(12)
    expect(inboundIDFromServerHandle(SERVER_GRAPH_SOURCE_HANDLE)).toBe(0)
  })

  it('offers both local inbounds and paths that can continue from the server', () => {
    expect(graphServerSourceOptions(
      [
        { id: 11, label: 'VLESS:443', title: '主入口' },
        { id: 12, label: 'SSH:22', title: 'SSH 入口' },
      ],
      [
        { step_id: 31, label: '继续 · 路径 7', title: '香港中转 / 第 2 跳后继续连接' },
      ],
    )).toEqual([
      { key: 'inbound:11', label: '主入口', detail: 'VLESS:443', source: { inbound_id: 11 } },
      { key: 'inbound:12', label: 'SSH 入口', detail: 'SSH:22', source: { inbound_id: 12 } },
      { key: 'step:31', label: '香港中转', detail: '继续 · 路径 7', source: { step_id: 31 } },
    ])
  })
})
