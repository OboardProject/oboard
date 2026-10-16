// @vitest-environment jsdom
import React, { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ProbeDDNS, ProbeDDNSForm } from "./ProbeDDNS"
import type { Server } from "../proxy-path/types"

let host: HTMLDivElement
let root: Root
beforeEach(() => { (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true; host = document.createElement("div"); document.body.appendChild(host); root = createRoot(host) })
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false })
const record = (id: number, server_id: number, name: string) => ({ id, server_id, revision: 1, name, domain: `${name}.example.com`, record_types: "a", proxy_enabled: false, interval_seconds: 300, enabled: true, exclude_private: false, status: "ok" })
const servers = [{ id: 1, name: "Tokyo", agent_id: "a", status: "online" }, { id: 2, name: "Osaka" }, { id: 3, name: "Seoul" }] as Server[]
const groupNames = () => [...document.body.querySelectorAll(".probe-ddns-group")].map(item => item.getAttribute("aria-label"))

it("groups DDNS records under their server and hides servers without records by default", async () => {
  const requestV2 = vi.fn(async () => ({ records: [record(1, 1, "a"), record(2, 3, "b"), record(3, 1, "c"), record(4, 9, "d")] }))
  await act(async () => root.render(<ProbeDDNS client={{ request: vi.fn(), requestV2 }} servers={servers} credentials={[]} canManage />))
  expect(groupNames()).toEqual(["Tokyo", "Seoul", "服务器 #9"])
  const tokyo = document.body.querySelector('.probe-ddns-group[aria-label="Tokyo"]')!
  expect([...tokyo.querySelectorAll(".probe-ddns-row strong")].map(item => item.textContent)).toEqual(["a", "c"])
  await act(async () => document.body.querySelector<HTMLButtonElement>('[aria-label="服务器范围"]')!.click())
  await act(async () => [...document.body.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(item => item.textContent === "全部服务器")!.click())
  expect(groupNames()).toEqual(["Tokyo", "Osaka", "Seoul", "服务器 #9"])
  const toggle = tokyo.querySelector<HTMLButtonElement>(".probe-ddns-group-toggle")!
  await act(async () => toggle.click())
  expect(toggle.getAttribute("aria-expanded")).toBe("false")
  expect(tokyo.querySelector(".probe-ddns-rows")).toBeNull()
})

it("shows every resolved address on its own line", async () => {
  const requestV2 = vi.fn(async () => ({ records: [{ ...record(1, 1, "a"), current_address: "A 13.231.255.51，AAAA 2406:da14::1" }] }))
  await act(async () => root.render(<ProbeDDNS client={{ request: vi.fn(), requestV2 }} servers={servers} credentials={[]} canManage />))
  expect([...document.body.querySelectorAll(".probe-ddns-addresses > span")].map(item => item.textContent)).toEqual(["A13.231.255.51", "AAAA2406:da14::1"])
})

it("picks the DDNS server by region and lists verified DNS accounts", async () => {
  const regional = [{ id: 1, name: "Tokyo", status: "online", region_mode: "manual", region_code: "JP" }, { id: 2, name: "Osaka", region_mode: "manual", region_code: "JP" }, { id: 3, name: "Seoul", region_mode: "manual", region_code: "KR" }] as Server[]
  const credentials = [{ id: 7, name: "cf-main", provider: "cloudflare", enabled: true, verified_at: "2026-01-01T00:00:00Z" }, { id: 8, name: "unverified", provider: "cloudflare", enabled: true }]
  await act(async () => root.render(<ProbeDDNSForm record={{ id: 0, server_id: 0, revision: 0, name: "", domain: "", record_types: "a", proxy_enabled: false, interval_seconds: 300, enabled: true, exclude_private: false }} servers={regional} credentials={credentials} client={{ request: vi.fn() }} busy={false} canManage onCancel={vi.fn()} onSubmit={vi.fn()} />))
  const optionNames = () => [...document.body.querySelectorAll('[role="option"] .entry-server-name')].map(item => item.textContent)
  await act(async () => document.body.querySelector<HTMLButtonElement>('[aria-label="DDNS 服务器"]')!.click())
  expect(optionNames()).toEqual(["Tokyo", "Osaka", "Seoul"])
  await act(async () => document.body.querySelector<HTMLButtonElement>('[aria-label="按地区筛选DDNS 服务器"] button[title^="韩国"]')!.click())
  expect(optionNames()).toEqual(["Seoul"])
  await act(async () => document.body.querySelector<HTMLButtonElement>('[role="option"]')!.click())
  expect(document.body.querySelector('[aria-label="DDNS 服务器"] .entry-server-name')?.textContent).toBe("Seoul")
  await act(async () => document.body.querySelector<HTMLButtonElement>('[aria-label="域名服务账号"]')!.click())
  expect([...document.body.querySelectorAll('[role="option"]')].map(item => item.textContent)).toEqual(["选择账号", "cf-main"])
})
