import { describe, expect, it } from 'vitest'
import { SERVER_GRAPH_SOURCE_HANDLE, graphConnectionSourceIntent, graphServerEntrySourceOptions, inboundIDFromServerHandle, isGenericServerSourceHandle, serverEntryHandleID, serverEntryTargetHandleID } from './graph-sources'

describe('server graph sources', () => {
  it('reserves the shared source handle for server cards', () => {
    expect(SERVER_GRAPH_SOURCE_HANDLE).toBe('server-source')
    expect(isGenericServerSourceHandle(SERVER_GRAPH_SOURCE_HANDLE)).toBe(true)
    expect(isGenericServerSourceHandle('source-bottom')).toBe(false)
    expect(isGenericServerSourceHandle('server-entry-12')).toBe(false)
  })

  it('keeps each inbound on its own server handle', () => {
    expect(serverEntryHandleID(12)).toBe('server-entry-12')
    expect(serverEntryTargetHandleID(12)).toBe('server-entry-target-12')
    expect(inboundIDFromServerHandle('server-entry-12')).toBe(12)
    expect(inboundIDFromServerHandle(SERVER_GRAPH_SOURCE_HANDLE)).toBe(0)
  })

  it('offers only local inbounds when a server connection needs a choice', () => {
    const options = graphServerEntrySourceOptions([
      { id: 11, label: 'VLESS:443', title: '主入口' },
      { id: 12, label: 'SSH:22', title: 'SSH 入口' },
    ])

    expect(options).toEqual([
      { key: 'inbound:11', label: '主入口', detail: 'VLESS:443', source: { inbound_id: 11 } },
      { key: 'inbound:12', label: 'SSH 入口', detail: 'SSH:22', source: { inbound_id: 12 } },
    ])
    expect(graphConnectionSourceIntent('server', 5, options)).toEqual({ kind: 'choose-entry', options })
  })

  it('branches directly from the displayed shared path step instead of asking which downstream path to use', () => {
    const downstreamOptions = [
      { key: 'step:31', label: 'G → SGL', detail: '继续 · 路径 7', source: { step_id: 31 } },
      { key: 'step:32', label: 'G → Softbank', detail: '继续 · 路径 8', source: { step_id: 32 } },
    ]

    expect(graphConnectionSourceIntent('proxy-path-step', 20, downstreamOptions)).toEqual({
      kind: 'resolved',
      sources: [{ step_id: 20 }],
    })
  })
})
