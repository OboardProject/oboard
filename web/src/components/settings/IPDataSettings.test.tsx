// @vitest-environment jsdom
import React, { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { IPDataSettings } from "./IPDataSettings"

let host: HTMLDivElement
let root: Root
beforeEach(() => { (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true; host = document.createElement("div"); document.body.appendChild(host); root = createRoot(host) })
afterEach(() => { act(() => root.unmount()); host.remove(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false })

const providers = [
  { name: "ipinfo", label: "IPinfo", roles: ["attribution"], key_required: false, key_label: "Access Token", keyless: "未填写时使用免 Key 接口", signup_url: "https://ipinfo.io/signup", configured: true, used_today: 3, daily_budget: 100000 },
  { name: "abuseipdb", label: "AbuseIPDB", roles: ["quality"], key_required: true, key_label: "API Key", signup_url: "https://www.abuseipdb.com/register", configured: false, used_today: 0, daily_budget: 900 },
  { name: "nexttrace", label: "NextTrace", roles: ["route"], key_required: false, key_label: "API Token", keyless: "免费接口", signup_url: "https://www.nxtrace.org/", configured: false, used_today: 0, daily_budget: 0 },
]

it("groups sources by role and saves a key without echoing it", async () => {
  const request = vi.fn(async (_path: string, init?: RequestInit) => ({ providers: init?.method === "POST" ? providers.map(p => p.name === "abuseipdb" ? { ...p, configured: true } : p) : providers, offline: { available: true } }))
  await act(async () => root.render(<IPDataSettings client={{ request }} notify={vi.fn()} />))
  const text = document.body.textContent || ""
  expect(text).toContain("IP 归属信息")
  expect(text).toContain("IP 质量信息")
  expect(text).toContain("路由数据")
  expect(text).toContain("已配置 Key")
  expect(text).toContain("离线库（ip2region）")
  const open = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === "填写 Key" && b.closest(".settings-row")?.textContent?.includes("AbuseIPDB"))!
  await act(async () => open.click())
  const input = document.body.querySelector<HTMLInputElement>(".ip-data-key-dialog input[type=password]")!
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!
    setter.call(input, "abuse-key")
    input.dispatchEvent(new Event("input", { bubbles: true }))
  })
  await act(async () => document.body.querySelector<HTMLFormElement>(".ip-data-key-dialog form")!.requestSubmit())
  const post = request.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(post[0]).toBe("/ip-data/keys")
  expect(JSON.parse(String(post[1]?.body))).toEqual({ keys: { abuseipdb: "abuse-key" }, clear: [] })
  expect(input.value).toBe("")
})
