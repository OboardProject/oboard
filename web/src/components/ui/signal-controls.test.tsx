// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import postcss, { type Rule } from 'postcss'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Button } from './button'
import { Input } from './input'
import { Badge } from './badge'
import { Card, CardTitle } from './card'
import { Switch } from './switch'
import { Tabs, TabsList, TabsTrigger, TabsContent } from './tabs'

const source = readFileSync(path.resolve(__dirname, '../../style.css'), 'utf8')
const stylesheet = postcss.parse(source)
function ruleFor(selector: string) {
  const rules: Rule[] = []
  stylesheet.walkRules(rule => { if (rule.selectors.includes(selector)) rules.push(rule) })
  return rules
}
function declarations(rule: Rule) {
  const values: Record<string, string> = {}
  rule.walkDecls(decl => { values[decl.prop] = decl.value })
  return values
}
type RGB = [number, number, number]
function rgb(value: string): RGB {
  return [1, 3, 5].map(start => parseInt(value.slice(start, start + 2), 16)) as RGB
}
function contrast(first: RGB, second: RGB) {
  const luminance = (color: RGB) => color.map(value => {
    const channel = value / 255
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
  }).reduce((sum, channel, index) => sum + channel * [0.2126, 0.7152, 0.0722][index], 0)
  const [light, dark] = [luminance(first), luminance(second)].sort((a, b) => b - a)
  return (light + 0.05) / (dark + 0.05)
}

describe('Signal design contract', () => {
  it('uses Chinese system type, Outfit numeric stack and one shared palette per theme', () => {
    expect(source).toMatch(/@import\s+url\(['"]?https:\/\/fonts\.googleapis\.com/)
    const root = declarations(ruleFor(':root')[0])
    expect(root['--font-sans']).toContain('PingFang SC')
    expect(root['--font-sans']).toContain('Microsoft YaHei')
    expect(root['--font-display']).toBe('var(--font-sans)')
    expect(root['--font-numeric']).toContain('Outfit')
    expect(root['--font-numeric']).toContain('DIN Alternate')
    expect(root['--font-numeric']).toContain('Bahnschrift')
    expect(root['font-variant-numeric']).toBe('tabular-nums')
    expect(ruleFor('#oboard-theme-old-layer[data-theme="light"]')).toHaveLength(1)
    expect(ruleFor('#oboard-theme-old-layer[data-theme="dark"]')).toHaveLength(1)
  })

  it.each(['light', 'dark'])('keeps %s text, placeholders, states and focus legible', theme => {
    const palette = declarations(ruleFor(`#oboard-theme-old-layer[data-theme="${theme}"]`)[0])
    const color = (token: string): RGB => {
      const value = palette[token]
      return value.startsWith('var(') ? color(value.slice(4, -1)) : rgb(value)
    }
    for (const surface of ['--bg-page', '--bg-card', '--bg-input', '--bg-control', '--surface-3']) {
      for (const text of ['--text-primary', '--text-secondary', '--text-muted', '--color-primary']) {
        expect(contrast(color(text), color(surface)), `${theme}: ${text} on ${surface}`).toBeGreaterThanOrEqual(4.5)
      }
      expect(contrast(color('--border-focus'), color(surface))).toBeGreaterThanOrEqual(3)
      for (const state of ['primary', 'success', 'warning', 'danger']) {
        const backgroundToken = state === 'primary' ? '--color-primary-light' : `--color-${state}-bg`
        const alpha = Number(palette[backgroundToken].match(/,\s*([\d.]+)\)$/)![1])
        const foreground = color(`--color-${state}`)
        const background = color(surface).map((channel, index) => foreground[index] * alpha + channel * (1 - alpha)) as RGB
        expect(contrast(foreground, background), `${theme}: ${state} badge on ${surface}`).toBeGreaterThanOrEqual(4.5)
      }
    }
    expect(contrast(color('--primary-contrast'), color('--color-primary'))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(color('--primary-contrast'), color('--color-primary-hover'))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(color('--danger-contrast'), color('--danger-fill'))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(color('--border-control'), color('--bg-input'))).toBeGreaterThanOrEqual(3)
  })

  it('keeps spring press feedback, visible focus and reduced-motion overrides authoritative', () => {
    expect(declarations(ruleFor('.ui-switch:active:not(.is-disabled)')[0]).transform).toBe('scale(0.97) translateY(1px)')
    expect(declarations(ruleFor(':focus-visible')[0]).outline).toBe('2px solid var(--border-focus)')
    const reduced = ruleFor('.ui-switch:active').find(rule => rule.parent?.type === 'atrule' && rule.parent.params === '(prefers-reduced-motion: reduce)')!
    expect(declarations(reduced).transform).toBe('none')
    expect(declarations(ruleFor('.ui-tabs-list')[0])['max-width']).toBe('100%')
    expect(declarations(ruleFor('.ui-btn-ghost')[0]).background).toBe('transparent')
    expect(ruleFor('button.ui-btn')).toHaveLength(0)
    expect(ruleFor('.ui-input').filter(rule => rule.parent?.type === 'root')).toHaveLength(0)
  })
})

let container: HTMLDivElement
let root: Root
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

it('preserves button busy, disabled, event and ref semantics across variants', async () => {
  const onClick = vi.fn()
  const ref = React.createRef<HTMLButtonElement>()
  await act(async () => root.render(<Button busy variant="destructive" ref={ref} onClick={onClick}>删除</Button>))
  expect(ref.current?.disabled).toBe(true)
  expect(ref.current?.getAttribute('aria-busy')).toBe('true')
  ref.current!.click()
  expect(onClick).not.toHaveBeenCalled()
  await act(async () => root.render(<Button variant="ghost" ref={ref} onClick={onClick}>取消</Button>))
  expect(ref.current?.disabled).toBe(false)
  expect(ref.current?.hasAttribute('aria-busy')).toBe(false)
  await act(async () => ref.current!.click())
  expect(onClick).toHaveBeenCalledOnce()
  expect(ref.current?.classList.contains('ui-btn-ghost')).toBe(true)
  expect(ref.current?.classList.contains('ghost')).toBe(false)
  await act(async () => root.render(<Button disabled ref={ref} onClick={onClick}>保存</Button>))
  ref.current!.click()
  expect(onClick).toHaveBeenCalledOnce()
})

it('preserves form attributes and static status/card semantics', async () => {
  const ref = React.createRef<HTMLInputElement>()
  const headingRef = React.createRef<HTMLHeadingElement>()
  await act(async () => root.render(<Card><CardTitle ref={headingRef}>节点</CardTitle><Input ref={ref} aria-label="名称" aria-invalid="true" required defaultValue="上海" /><Badge variant="success">在线</Badge></Card>))
  expect(ref.current?.value).toBe('上海')
  expect(ref.current?.required).toBe(true)
  expect(ref.current?.getAttribute('aria-invalid')).toBe('true')
  expect(headingRef.current?.tagName).toBe('H3')
  const badge = container.querySelector('[data-slot="badge"]')!
  expect(badge.getAttribute('data-variant')).toBe('success')
  expect(badge.hasAttribute('tabindex')).toBe(false)
  expect(badge.className).not.toMatch(/active:|backdrop/)
})

it('keeps switch changes local and ignores disabled presses', async () => {
  const changed = vi.fn()
  const outer = vi.fn()
  await act(async () => root.render(<div onClick={outer}><Switch ariaLabel="启用" onChange={changed} /></div>))
  const input = container.querySelector('input')!
  await act(async () => input.click())
  expect(input.checked).toBe(true)
  expect(input.getAttribute('aria-checked')).toBe('true')
  expect(changed).toHaveBeenCalledWith(true)
  expect(outer).not.toHaveBeenCalled()
  await act(async () => root.render(<div onClick={outer}><Switch ariaLabel="启用" checked disabled onChange={changed} /></div>))
  input.click()
  expect(changed).toHaveBeenCalledOnce()
  expect(input.closest('label')?.classList.contains('is-disabled')).toBe(true)
})

it('retains tab selection, panel visibility and disabled behavior', async () => {
  function Harness() {
    const [value, setValue] = React.useState('one')
    return <Tabs value={value} onValueChange={setValue}><TabsList><TabsTrigger value="one">概览</TabsTrigger><TabsTrigger value="two">记录</TabsTrigger><TabsTrigger value="three" disabled>禁用</TabsTrigger></TabsList><TabsContent value="one">节点概览</TabsContent><TabsContent value="two">运行记录</TabsContent></Tabs>
  }
  await act(async () => root.render(<Harness />))
  const tabs = container.querySelectorAll<HTMLButtonElement>('[role="tab"]')
  await act(async () => tabs[1].click())
  expect(tabs[0].getAttribute('aria-selected')).toBe('false')
  expect(tabs[1].getAttribute('aria-selected')).toBe('true')
  expect(container.querySelector('[role="tabpanel"]')?.textContent).toBe('运行记录')
  tabs[2].click()
  expect(tabs[1].getAttribute('aria-selected')).toBe('true')
})
