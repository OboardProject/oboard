// @vitest-environment jsdom
import React, { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { VPNGatePage } from "./VPNGatePage"

let host: HTMLDivElement
let root: Root
beforeEach(() => { (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true; host = document.createElement("div"); document.body.appendChild(host); root = createRoot(host) })
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false })

const button = (name: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find(item => item.textContent?.includes(name))!
const pool = { id: 3, name: "日本 NTT", countries: ["JP"], operator_asns: ["AS4713"], network: "any", select_mode: "latency", max_rtt_ms: 0, min_quality_score: 0, hot_standby: 1, pool_size: 4, switch_policy: "on_failure", refresh_minutes: 30, enabled: true, in_use: true }
const live = { pool_id: 3, server_id: 1, revision: 2, members: [{ node_id: 9, member_id: "n9", ip: "219.100.37.9", port: 443, network: "tcp", country_code: "JP", status: "succeeded", egress_ip: "153.1.1.1", asn: "AS4713", rtt_ms: 120, quality: 80, eligible: true, success_rate: 0.9, selected: true }], candidates: [
  { node_id: 9, member_id: "n9", ip: "219.100.37.9", port: 443, network: "tcp", country_code: "JP", status: "succeeded", egress_ip: "153.1.1.1", asn: "AS4713", rtt_ms: 120, quality: 80, eligible: true, success_rate: 0.9, selected: true },
  { node_id: 10, member_id: "n10", ip: "219.100.37.10", port: 443, network: "tcp", country_code: "JP", status: "succeeded", egress_ip: "126.2.2.2", asn: "AS17676", rtt_ms: 60, quality: 70, eligible: false, reason: "运营商不符", success_rate: 0.6, selected: false },
], status: { revision: 2, active_member: "n9", healthy: 1, members: [{ member_id: "n9", state: "up", active: true, failures: 0 }] }, history: [{ at: "2026-10-15T00:00:00Z", reason: "更新候选节点", to: "n9" }] }

it("shows the live exit, why a faster node was rejected, and requests a reselect", async () => {
  const requestV2 = vi.fn(async (_path: string, init?: RequestInit) => init?.method === "POST" ? { applied: true } : { pools: [{ ...pool, servers: [live] }], catalog: { total: 61, entries: 100, skipped: 2 }, providers: { ipinfo: true } })
  await act(async () => root.render(<VPNGatePage client={{ request: vi.fn(), requestV2 }} servers={[{ id: 1, name: "东京出口" }]} canManage />))
  expect(document.body.textContent).toContain("153.1.1.1")
  expect(document.body.textContent).toContain("东京出口")
  expect(button("删除").disabled).toBe(true)
  await act(async () => button("节点详情").click())
  expect(document.body.textContent).toContain("运营商不符")
  expect(document.body.textContent).toContain("当前出口")
  await act(async () => button("重新挑选").click())
  const call = requestV2.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({ action: "reselect", input: { id: 3, refresh_catalog: true } })
})

it("warns that an operator filter cannot work without IPinfo and marks an empty pool as refusing", async () => {
  const empty = { ...live, members: [], status: { revision: 2, healthy: 0, members: [] }, last_error: "没有符合条件的可用节点，当前拒绝出站" }
  const requestV2 = vi.fn(async () => ({ pools: [{ ...pool, servers: [empty] }], catalog: { total: 0, entries: 0, skipped: 0 }, providers: {} }))
  await act(async () => root.render(<VPNGatePage client={{ request: vi.fn(), requestV2 }} servers={[{ id: 1, name: "东京出口" }]} canManage={false} />))
  expect(document.body.textContent).toContain("IPinfo 未填写 Key")
  expect(document.body.textContent).toContain("暂停出站")
  expect(document.body.textContent).toContain("1 台暂停出站")
  expect(button("新建出口池").disabled).toBe(true)
})

it("saves edits without overview metadata and keeps the draft open on failure", async () => {
  let fail = true
  const notify = vi.fn()
  const requestV2 = vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      if (fail) throw new Error("保存失败，请重试")
      return { applied: true }
    }
    return { pools: [{ ...pool, created_at: "2026-10-15T00:00:00Z", updated_at: "2026-10-16T00:00:00Z", servers: [live] }], catalog: {}, providers: { ipinfo: true } }
  })
  await act(async () => root.render(<VPNGatePage client={{ request: vi.fn(), requestV2 }} servers={[]} canManage notify={notify} />))
  await act(async () => button("编辑").click())
  const name = document.body.querySelector<HTMLInputElement>('[aria-label="名称"]')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "日本 NTT 新名称")
    name.dispatchEvent(new Event("input", { bubbles: true }))
  })
  const save = () => document.getElementById("vpngate-pool-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }))
  await act(async () => { save() })
  expect(notify).toHaveBeenCalledWith("保存失败，请重试", "error")
  expect(document.body.querySelector<HTMLInputElement>('[aria-label="名称"]')?.value).toBe("日本 NTT 新名称")
  expect(document.body.textContent).not.toContain("保存失败，请重试")
  const call = requestV2.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(JSON.parse(String(call[1]?.body))).toMatchObject({ action: "save", input: { id: 3, name: "日本 NTT 新名称" } })
  expect(Object.keys(JSON.parse(String(call[1]?.body)).input).sort()).toEqual(["id", "name", "countries", "operator_asns", "network", "select_mode", "max_rtt_ms", "min_quality_score", "hot_standby", "pool_size", "switch_policy", "refresh_minutes", "enabled"].sort())
  fail = false
  await act(async () => { save() })
  expect(notify).toHaveBeenCalledWith("已保存，将按新条件重新挑选节点", "success")
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 300)) })
  expect(document.getElementById("vpngate-pool-form")).toBeNull()
})

it("does not repeat a polling failure until the overview recovers", async () => {
  vi.useFakeTimers()
  try {
    let fail = true
    const notify = vi.fn()
    const requestV2 = vi.fn(async () => {
      if (fail) throw new Error("目录加载失败")
      return { pools: [], catalog: {}, providers: {} }
    })
    await act(async () => root.render(<VPNGatePage client={{ request: vi.fn(), requestV2 }} servers={[]} canManage notify={notify} />))
    await act(async () => { await vi.advanceTimersByTimeAsync(30000) })
    expect(notify).toHaveBeenCalledTimes(1)
    expect(notify).toHaveBeenCalledWith("目录加载失败", "error")
    fail = false
    await act(async () => { await vi.advanceTimersByTimeAsync(15000) })
    fail = true
    await act(async () => { await vi.advanceTimersByTimeAsync(15000) })
    expect(notify).toHaveBeenCalledTimes(2)
  } finally { vi.useRealTimers() }
})
