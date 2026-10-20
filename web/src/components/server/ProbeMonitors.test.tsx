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
it("shows attribution and quality separately, with votes and degraded sources", async () => {
  const unknown = { state: "unknown", positive: 0, negative: 0, flagged_by: [] }
  const result = {
    ip: "1.1.1.1",
    attribution: { status: "degraded", primary: "ipapi_is", asn: "AS13335", as_name: "Cloudflare", country_code: "US", field_sources: { asn: "ipapi_is", country: "ipapi_is" }, asn_agreement: { agree: 2, total: 2 }, country_agreement: { agree: 2, total: 3 }, conflicts: [{ provider: "ipwhois", field: "country", value: "AU" }], sources: [{ provider: "ipinfo", status: "invalid_key", error: "API Key 无效或无权限" }] },
    quality: { status: "partial", proxy: { state: "no", positive: 0, negative: 2, flagged_by: [] }, vpn: unknown, tor: unknown, hosting: { state: "yes", positive: 2, negative: 0, flagged_by: ["proxycheck", "ipapi_is"] }, abuser: { state: "conflict", positive: 1, negative: 1, flagged_by: ["ipapi_is"] }, risk: 33, risk_source: "proxycheck", sources: [{ provider: "abuseipdb", status: "not_configured", error: "未配置 API Key" }] },
  }
  const requestV2 = vi.fn(async () => ({ monitors: [{ ...task, kind: "ip_quality", result }], providers: { ipinfo: false } }))
  await act(async () => root.render(<ProbeMonitors client={{ request: vi.fn(), requestV2 }} servers={servers} kind="ip_quality" canManage={false} />))
  expect(button("立即检测").disabled).toBe(true)
  expect(document.body.textContent).toContain("IPinfo 未填写 Key")
  await act(async () => button("结果").click())
  const metric = (label: string) => [...document.body.querySelectorAll(".probe-quality-metrics div")].find(item => item.querySelector("dt")?.textContent === label)?.querySelector("dd")?.textContent
  expect(metric("ASN")).toBe("AS13335（ipapi.is）")
  expect(metric("国家校验")).toBe("2/3 来源一致")
  expect(metric("VPN")).toBe("未知")
  expect(metric("机房")).toBe("是 2/2")
  expect(metric("滥用")).toBe("来源不一致 1/2")
  expect(metric("风险分")).toBe("33 / 100（Proxycheck）")
  expect(document.body.textContent).toContain("IPinfo 暂不可用")
  expect(document.body.textContent).toContain("IPWhois 国家 AU")
  expect(document.body.textContent).toContain("未配置 API Key")
})
it("scores the latest IP quality result and flags the server without describing sources", async () => {
  const quality = { status: "partial", risk: 72, risk_source: "proxycheck", abuse_score: 0, abuse_source: "abuseipdb" }
  const monitor = { ...task, kind: "ip_quality", target: "", status: "partial", error: "部分数据源未配置或查询失败，已自动使用备用来源", result: { ip: "1.1.1.1", attribution: { status: "degraded" }, quality } }
  const requestV2 = vi.fn(async () => ({ monitors: [monitor], providers: {} }))
  await act(async () => root.render(<ProbeMonitors client={{ request: vi.fn(), requestV2 }} servers={[{ id: 1, name: "A", region_mode: "auto", region_code: "", detected_region_code: "JP" }] as Server[]} kind="ip_quality" canManage />))
  const row = document.body.querySelector("tbody tr")!
  expect(row.querySelector('[data-label="服务器"] img')?.getAttribute("alt")).toBe("日本")
  const cell = row.querySelector('[data-label="最近结果"]')!
  expect([...cell.querySelectorAll(".probe-task-badge")].map(item => [item.textContent, item.className])).toEqual([["风险 72", "probe-task-badge offline"], ["滥用 0", "probe-task-badge online"]])
  expect(cell.textContent).not.toContain("数据源")
  expect(cell.textContent).not.toContain("部分可用")
  await act(async () => button("结果").click())
  expect(document.body.textContent).not.toContain("部分数据源未配置")
})

it.each(["route", "ip_quality"] as const)("toasts a failed %s edit while preserving its draft", async kind => {
  const notify = vi.fn()
  const requestV2 = vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method === "POST") throw new Error("任务保存失败")
    return { monitors: [{ ...task, kind, interval_seconds: 3600 }], providers: {} }
  })
  await act(async () => root.render(<ProbeMonitors client={{ request: vi.fn(), requestV2 }} servers={servers} kind={kind} canManage notify={notify} />))
  await act(async () => button("编辑").click())
  await act(async () => document.body.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
  expect(notify).toHaveBeenCalledWith("任务保存失败", "error")
  expect(document.body.querySelector<HTMLInputElement>('[aria-label="任务名称"]')?.value).toBe(task.name)
  expect(document.body.textContent).not.toContain("任务保存失败")
})
