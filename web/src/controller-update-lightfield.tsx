import React, { useEffect, useLayoutEffect, useRef } from 'react'

export type LightfieldMode = 'idle' | 'running' | 'success' | 'failed'

// A channel eases from its current value toward a target over a fixed duration.
// Density (how many beams) and speed (how fast they travel) are separate
// channels so a run can start with a few slow beams that multiply and speed up,
// and end by first slowing down and only then thinning out.
type Channel = { from: number; to: number; start: number; delay: number; duration: number; ease: (t: number) => number }

const easeInCubic = (t: number) => t * t * t
const easeInOutSine = (t: number) => -(Math.cos(Math.PI * t) - 1) / 2
const easeOutCubic = (t: number) => 1 - Math.pow(1 - t, 3)
const easeInQuad = (t: number) => t * t

export type LightfieldRamp = { density: Omit<Channel, 'from' | 'start'>; speed: Omit<Channel, 'from' | 'start'> }

// Starting is deliberately slower than stopping: the flow builds up over
// several seconds, while a finished run settles in about three.
export function lightfieldRamp(mode: LightfieldMode): LightfieldRamp {
  if (mode === 'running') {
    return {
      density: { to: 1, delay: 0, duration: 5200, ease: easeInOutSine },
      speed: { to: 1, delay: 400, duration: 6200, ease: easeInCubic },
    }
  }
  if (mode === 'failed') {
    return {
      density: { to: 0, delay: 250, duration: 1500, ease: easeOutCubic },
      speed: { to: 0.04, delay: 0, duration: 900, ease: easeOutCubic },
    }
  }
  return {
    density: { to: 0, delay: 900, duration: 2300, ease: easeInQuad },
    speed: { to: 0.05, delay: 0, duration: 1900, ease: easeOutCubic },
  }
}

function channelValue(channel: Channel, now: number): number {
  const t = (now - channel.start - channel.delay) / channel.duration
  if (t <= 0) return channel.from
  if (t >= 1) return channel.to
  return channel.from + (channel.to - channel.from) * channel.ease(t)
}

type Beam = { angle: number; r: number; length: number; width: number; speed: number; alpha: number }

const MIN_SPAWN = 1.4
const MAX_SPAWN = 150
const MIN_SPEED = 38
const MAX_SPEED = 980
const MAX_BEAMS = 300

function spawnBeam(reach: number, random: () => number): Beam {
  const roll = random()
  const heavy = roll > 0.93
  const medium = !heavy && roll > 0.74
  const width = heavy ? 2 + random() * 2.5 : medium ? 1 + random() * 1.2 : 0.45 + random() * 0.8
  const length = heavy ? 90 + random() * 170 : 40 + random() * (medium ? 160 : 120)
  return {
    angle: random() * Math.PI * 2,
    r: 8 + random() * reach * 0.14,
    length,
    width,
    speed: heavy ? 0.55 + random() * 0.35 : 0.8 + random() * 0.55,
    alpha: heavy ? 0.7 + random() * 0.2 : 0.32 + random() * 0.48,
  }
}

type Engine = { setMode: (mode: LightfieldMode) => void; destroy: () => void }

function createEngine(canvas: HTMLCanvasElement, reduceMotion: boolean, initial: LightfieldMode): Engine | null {
  let ctx: CanvasRenderingContext2D | null = null
  try { ctx = canvas.getContext('2d') } catch { ctx = null }
  if (!ctx) return null
  const g = ctx
  const random = Math.random
  let width = 0
  let height = 0
  let dpr = 1
  let frame = 0
  let last = performance.now()
  let spawnCarry = 0
  let mode: LightfieldMode = initial
  const beams: Beam[] = []
  const start = performance.now()
  const idleRamp = lightfieldRamp('success')
  let density: Channel = { from: 0, start, ...idleRamp.density, to: 0 }
  let speed: Channel = { from: 0.05, start, ...idleRamp.speed, to: 0.05 }

  let clientWidth = -1
  let clientHeight = -1
  const resize = () => {
    clientWidth = canvas.clientWidth
    clientHeight = canvas.clientHeight
    const rect = canvas.getBoundingClientRect()
    dpr = Math.min(2, window.devicePixelRatio || 1)
    width = Math.max(1, rect.width)
    height = Math.max(1, rect.height)
    canvas.width = Math.round(width * dpr)
    canvas.height = Math.round(height * dpr)
    if (reduceMotion) draw(performance.now(), 0)
  }

  const setMode = (next: LightfieldMode) => {
    if (next === mode && frame) return
    const now = performance.now()
    mode = next
    const ramp = lightfieldRamp(next)
    density = { from: channelValue(density, now), start: now, ...ramp.density }
    speed = { from: channelValue(speed, now), start: now, ...ramp.speed }
    if (reduceMotion) draw(now, 0)
  }

  const drawBeams = (cx: number, cy: number, reach: number, stretch: number) => {
    for (const beam of beams) {
      const length = beam.length * stretch
      const inner = Math.max(0, beam.r - length)
      const outer = beam.r
      if (inner > reach + 40) continue
      const cos = Math.cos(beam.angle)
      const sin = Math.sin(beam.angle)
      const perspective = Math.min(1, outer / reach)
      const wOuter = beam.width * (0.35 + perspective * 1.4)
      const wInner = Math.max(0.2, beam.width * 0.25 * perspective)
      const fadeCenter = Math.min(1, Math.max(0, inner / (reach * 0.18)))
      const fadeEdge = Math.max(0.35, Math.min(1, (reach - outer) / (reach * 0.14)))
      const alpha = beam.alpha * fadeCenter * fadeEdge * 0.75
      if (alpha <= 0.01) continue
      const px = -sin
      const py = cos
      const ix = cx + cos * inner
      const iy = cy + sin * inner
      const ox = cx + cos * outer
      const oy = cy + sin * outer
      const gradient = g.createLinearGradient(ix, iy, ox, oy)
      gradient.addColorStop(0, 'rgba(235, 241, 248, 0)')
      gradient.addColorStop(0.55, `rgba(235, 241, 248, ${alpha * 0.5})`)
      gradient.addColorStop(1, `rgba(255, 255, 255, ${alpha})`)
      g.fillStyle = gradient
      g.beginPath()
      g.moveTo(ox + px * wOuter / 2, oy + py * wOuter / 2)
      g.lineTo(ix + px * wInner / 2, iy + py * wInner / 2)
      g.lineTo(ix - px * wInner / 2, iy - py * wInner / 2)
      g.lineTo(ox - px * wOuter / 2, oy - py * wOuter / 2)
      g.closePath()
      g.fill()
    }
  }

  const draw = (now: number, dt: number) => {
    const d = channelValue(density, now)
    const s = channelValue(speed, now)
    const cx = width / 2
    const cy = height / 2
    const reach = Math.hypot(Math.max(cx, width - cx), Math.max(cy, height - cy))

    if (reduceMotion) {
      beams.length = 0
      if (mode === 'running') {
        const seeded = mulberry(7)
        for (let i = 0; i < 42; i += 1) {
          const beam = spawnBeam(reach, seeded)
          beam.r = reach * (0.18 + seeded() * 0.82)
          beams.push(beam)
        }
      }
    } else {
      // A full-window field needs more beams than a small stage to look as dense.
      const areaScale = Math.max(1, Math.min(2.4, reach / 360))
      spawnCarry += dt * (d > 0.002 ? MIN_SPAWN + (MAX_SPAWN - MIN_SPAWN) * d * areaScale : 0)
      while (spawnCarry >= 1 && beams.length < MAX_BEAMS * areaScale) {
        spawnCarry -= 1
        beams.push(spawnBeam(reach, random))
      }
      if (spawnCarry > 1) spawnCarry = 1
      const velocity = MIN_SPEED + (MAX_SPEED - MIN_SPEED) * s
      for (let i = beams.length - 1; i >= 0; i -= 1) {
        const beam = beams[i]
        beam.r += velocity * beam.speed * dt * (0.55 + 0.45 * Math.min(1, beam.r / reach))
        if (beam.r - beam.length > reach + 40) {
          beams.splice(i, 1)
        } else if (s < 0.1 && d < 0.05) {
          beam.alpha *= Math.max(0, 1 - dt * 2.4)
          if (beam.alpha < 0.02) beams.splice(i, 1)
        }
      }
    }

    g.setTransform(dpr, 0, 0, dpr, 0, 0)
    g.clearRect(0, 0, width, height)
    drawBeams(cx, cy, reach, reduceMotion ? 1 : 0.6 + 0.8 * s)
  }

  const loop = (now: number) => {
    if (canvas.clientWidth !== clientWidth || canvas.clientHeight !== clientHeight) resize()
    const dt = Math.min(0.05, Math.max(0, (now - last) / 1000))
    last = now
    draw(now, dt)
    frame = window.requestAnimationFrame(loop)
  }

  const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(resize) : null
  observer?.observe(canvas)
  resize()
  setMode(initial)
  if (reduceMotion) draw(performance.now(), 0)
  else frame = window.requestAnimationFrame(loop)

  return {
    setMode,
    destroy() {
      observer?.disconnect()
      if (frame) window.cancelAnimationFrame(frame)
      frame = 0
    },
  }
}

function mulberry(seed: number) {
  let a = seed
  return () => {
    a |= 0
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export function ControllerUpdateLightfield({ mode, reduceMotion, children }: { mode: LightfieldMode; reduceMotion?: boolean; children?: React.ReactNode }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const engineRef = useRef<Engine | null>(null)
  const modeRef = useRef(mode)
  modeRef.current = mode

  useLayoutEffect(() => {
    const existing = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
    const meta = existing ?? document.createElement('meta')
    const previous = meta.getAttribute('content')
    meta.name = 'theme-color'
    meta.content = '#050505'
    if (!existing) document.head.appendChild(meta)
    return () => {
      if (!existing) meta.remove()
      else if (previous === null) meta.removeAttribute('content')
      else meta.content = previous
    }
  }, [])

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const engine = createEngine(canvas, Boolean(reduceMotion), modeRef.current)
    engineRef.current = engine
    return () => {
      engine?.destroy()
      engineRef.current = null
    }
  }, [reduceMotion])

  useEffect(() => { engineRef.current?.setMode(mode) }, [mode])

  return <div className={`controller-update-lightfield ${mode}`}>
    <canvas ref={canvasRef} aria-hidden="true" />
    {children}
  </div>
}
