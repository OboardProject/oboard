// @vitest-environment jsdom
import React, { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ProbeMonitors } from "./ProbeMonitors"
import type { Server } from "../proxy-path/types"

let host: HTMLDivElement
let root: Root
beforeEach(() => { (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true; host = document.createElement("div"); document.body.appendChild(host); root = createRoot(host) })
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false })
const task = { id: 7, server_id: 1, revision: 2, name: "Route A", kind: "route", target: "1.1.1.1", mode: "icmp", port: 0, interval_seconds: 900, enabled: true, status: "unknown", error: "ASN 查询不完整" }
const servers = [{ id: 1, name: "A" }] as Server[]
const button = (name: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find(item => item.textContent === name)!
it("queues a scoped run and preserves old route history when latest result is unknown", async () => {
  const requestV2 = vi.fn(async (path: string, init?: RequestInit) => init?.method === "POST" ? { applied: true } : path.includes("?id=") ? { versions: [{ id: 3, checked_at: "2026-10-14T00:00:00Z", last_seen_at: "2026-10-14T01:00:00Z", asn_path: ["AS13335", "AS15169"], trace: { hops: [{ hop: 1, addresses: ["1.1.1.1"], probes: [{ rtt_ms: 3, timeout: false }] }] } }] } : { monitors: [task], providers: { ipinfo: true } })
  await act(async () => root.render(<ProbeMonitors client={{ request: vi.fn(), requestV2 }} servers={servers} kind="route" canManage />))
  await act(async () => button("立即检测").click())
  const call = requestV2.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({ action: "run", input: { id: 7, server_id: 1, revision: 2 } })
  await act(async () => button("结果").click())
  expect(document.body.textContent).toContain("AS13335 → AS15169")
  expect(document.body.textContent).toContain("ASN 查询不完整")
  expect(document.body.textContent).toContain("1.1.1.1")
})
it("keeps missing quality data unknown and shows independent provider scores", async () => {
  const quality = { ...task, kind: "ip_quality", result: { ip: "1.1.1.1", providers: [{ provider: "proxycheck", status: "ok", proxy: false, hosting: true, risk: 33, confidence: 100 }, { provider: "abuseipdb", status: "not_configured", error: "未配置 API Key" }] } }
  const requestV2 = vi.fn(async () => ({ monitors: [quality], providers: {} }))
  await act(async () => root.render(<ProbeMonitors client={{ request: vi.fn(), requestV2 }} servers={servers} kind="ip_quality" canManage={false} />))
  expect(button("立即检测").disabled).toBe(true)
  await act(async () => button("结果").click())
  const metric = (label: string) => [...document.body.querySelectorAll(".probe-quality-metrics div")].find(item => item.querySelector("dt")?.textContent === label)?.querySelector("dd")?.textContent
  expect(metric("VPN")).toBe("未知")
  expect(metric("机房")).toBe("是")
  expect(metric("风险分")).toBe("33 / 100")
  expect(document.body.textContent).toContain("未配置 API Key")
})
