// @vitest-environment jsdom
import React, { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ProbeDDNS } from "./ProbeDDNS"
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
  expect([...tokyo.querySelectorAll("tbody strong")].map(item => item.textContent)).toEqual(["a", "c"])
  await act(async () => document.body.querySelector<HTMLButtonElement>('[aria-label="服务器范围"]')!.click())
  await act(async () => [...document.body.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(item => item.textContent === "全部服务器")!.click())
  expect(groupNames()).toEqual(["Tokyo", "Osaka", "Seoul", "服务器 #9"])
  const toggle = tokyo.querySelector<HTMLButtonElement>(".probe-ddns-group-toggle")!
  await act(async () => toggle.click())
  expect(toggle.getAttribute("aria-expanded")).toBe("false")
  expect(tokyo.querySelector("table")).toBeNull()
})
