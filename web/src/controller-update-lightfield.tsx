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

type Beam = { angle: number; r: number; length: number; width: number; speed: number; alpha: number; tint: number }

const MIN_SPAWN = 1.4
const MAX_SPAWN = 150
const MIN_SPEED = 38
const MAX_SPEED = 980
const MAX_BEAMS = 300

function spawnBeam(reach: number, orbRadius: number, random: () => number): Beam {
  const roll = random()
  const heavy = roll > 0.93
  const medium = !heavy && roll > 0.74
  const width = heavy ? 5 + random() * 7 : medium ? 2 + random() * 1.8 : 0.6 + random() * 1.1
  const length = heavy ? 70 + random() * 150 : 26 + random() * (medium ? 130 : 110)
  return {
    angle: random() * Math.PI * 2,
    r: Math.max(orbRadius * 1.6, reach * (0.72 + random() * 0.4)) + length,
    length,
    width,
    speed: heavy ? 0.55 + random() * 0.35 : 0.8 + random() * 0.55,
    alpha: heavy ? 0.78 + random() * 0.2 : 0.45 + random() * 0.5,
    tint: random(),
  }
}

type Palette = { core: string; mid: string; edge: string; glow: string }
const ORB_CALM: Palette = { core: '#ffd477', mid: '#ff9b24', edge: '#ed610d', glow: '255, 126, 24' }
const ORB_FAILED: Palette = { core: '#ffd0a3', mid: '#ef6135', edge: '#9e291c', glow: '242, 80, 36' }

type Engine = { setMode: (mode: LightfieldMode) => void; setLifted: (lifted: boolean) => void; destroy: () => void }

// Orb anchor and size mirror the CSS that places the status dock under it
// (--lf-anchor, --lf-orb): keep both sides in sync.
const ANCHOR_REST = 0.42
const ANCHOR_LIFTED = 0.2
const ORB_LIFTED_SCALE = 0.62

export function lightfieldOrbRadius(width: number, height: number): number {
  return Math.max(44, Math.min(108, Math.min(width, height) * 0.11))
}

function createEngine(canvas: HTMLCanvasElement, reduceMotion: boolean, initial: LightfieldMode, initialLifted: boolean): Engine | null {
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
  let absorb = 0
  let flash = 0
  let failedMix = initial === 'failed' ? 1 : 0
  let mode: LightfieldMode = initial
  let lift = initialLifted ? 1 : 0
  let liftTarget = lift
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
    const previous = mode
    mode = next
    const ramp = lightfieldRamp(next)
    density = { from: channelValue(density, now), start: now, ...ramp.density }
    speed = { from: channelValue(speed, now), start: now, ...ramp.speed }
    if (next === 'success' && previous === 'running') flash = 1
    if (reduceMotion) draw(now, 0)
  }

  const setLifted = (lifted: boolean) => {
    liftTarget = lifted ? 1 : 0
    if (reduceMotion) {
      lift = liftTarget
      draw(performance.now(), 0)
    }
  }

  const drawBeams = (cx: number, cy: number, orbRadius: number, reach: number, stretch: number) => {
    g.globalCompositeOperation = 'lighter'
    for (const beam of beams) {
      const length = beam.length * stretch
      const inner = beam.r - length
      const outer = beam.r
      if (inner > reach + 40) continue
      const cos = Math.cos(beam.angle)
      const sin = Math.sin(beam.angle)
      // Beams read as perspective streaks: wide at the rim, thin at the core.
      const wOuter = beam.width * (0.3 + 0.7 * Math.min(1, outer / reach))
      const wInner = Math.max(0.25, beam.width * 0.12 * Math.min(1, inner / reach + 0.2))
      const fadeIn = Math.min(1, Math.max(0, (reach + length - inner) / (length * 0.9)))
      const fadeCore = Math.min(1, Math.max(0, (inner - orbRadius) / (orbRadius * 0.9)))
      const alpha = beam.alpha * fadeIn * fadeCore * 0.58
      if (alpha <= 0.01) continue
      const px = -sin
      const py = cos
      const ix = cx + cos * inner
      const iy = cy + sin * inner
      const ox = cx + cos * outer
      const oy = cy + sin * outer
      const gradient = g.createLinearGradient(ox, oy, ix, iy)
      const hue = beam.tint > 0.7 ? '255, 158, 58' : '255, 216, 148'
      gradient.addColorStop(0, `rgba(${hue}, 0)`)
      gradient.addColorStop(0.35, `rgba(${hue}, ${alpha * 0.8})`)
      gradient.addColorStop(1, `rgba(255, 237, 188, ${alpha})`)
      g.fillStyle = gradient
      g.beginPath()
      g.moveTo(ox + px * wOuter / 2, oy + py * wOuter / 2)
      g.lineTo(ix + px * wInner / 2, iy + py * wInner / 2)
      g.lineTo(ix - px * wInner / 2, iy - py * wInner / 2)
      g.lineTo(ox - px * wOuter / 2, oy - py * wOuter / 2)
      g.closePath()
      g.fill()
    }
    g.globalCompositeOperation = 'source-over'
  }

  const drawOrb = (cx: number, cy: number, radius: number, time: number, energy: number) => {
    time = reduceMotion ? 0 : time
    const breathe = Math.sin(time / 2400) * 0.012
    const r = radius * (1 + breathe + absorb * 0.018 + flash * 0.025)
    const mix = failedMix
    const glowColor = mix > 0.5 ? ORB_FAILED.glow : ORB_CALM.glow
    const haloRadius = r * (2.35 + energy * 0.15)
    const halo = g.createRadialGradient(cx, cy, r * 0.7, cx, cy, haloRadius)
    halo.addColorStop(0, `rgba(${glowColor}, ${0.14 + energy * 0.035 + flash * 0.04})`)
    halo.addColorStop(0.24, `rgba(${glowColor}, 0.065)`)
    halo.addColorStop(0.58, `rgba(${glowColor}, 0.018)`)
    halo.addColorStop(1, `rgba(${glowColor}, 0)`)
    g.fillStyle = halo
    g.beginPath()
    g.arc(cx, cy, haloRadius, 0, Math.PI * 2)
    g.fill()

    g.save()
    g.beginPath()
    g.arc(cx, cy, r, 0, Math.PI * 2)
    g.clip()
    for (const [palette, weight] of [[ORB_CALM, 1], [ORB_FAILED, mix]] as const) {
      if (weight <= 0.01) continue
      const body = g.createRadialGradient(cx, cy, 0, cx, cy, r)
      body.addColorStop(0, palette.core)
      body.addColorStop(0.6, palette.mid)
      body.addColorStop(0.91, palette.edge)
      body.addColorStop(0.97, `rgba(${palette.glow}, 0.7)`)
      body.addColorStop(1, `rgba(${palette.glow}, 0)`)
      g.globalAlpha = weight
      g.fillStyle = body
      g.fillRect(cx - r, cy - r, r * 2, r * 2)
    }
    g.globalAlpha = 1
    const flow = time / 6500
    // Overlapping convection cells keep the surface fluid without latitude-like stripes.
    for (let i = 0; i < 28; i += 1) {
      const phase = i * 2.39996 + flow * (i % 2 ? 0.24 : -0.18)
      const distance = r * (0.12 + (i % 9) * 0.075)
      const hx = cx + Math.cos(phase) * distance
      const hy = cy + Math.sin(phase + Math.sin(flow * 0.7 + i) * 0.16) * distance
      const size = r * (0.16 + (i % 4) * 0.045)
      const pulse = 0.5 + Math.sin(flow * 1.3 + i * 1.7) * 0.5
      const color = i % 3 === 0 ? '184, 49, 3' : '255, 232, 145'
      const cell = g.createRadialGradient(hx, hy, 0, hx, hy, size)
      cell.addColorStop(0, `rgba(${color}, ${0.12 + pulse * 0.18 + energy * 0.035})`)
      cell.addColorStop(0.45, `rgba(${color}, ${0.06 + pulse * 0.07})`)
      cell.addColorStop(1, `rgba(${color}, 0)`)
      g.fillStyle = cell
      g.fillRect(hx - size, hy - size, size * 2, size * 2)
    }

    g.restore()
  }

  const draw = (now: number, dt: number) => {
    const d = channelValue(density, now)
    const s = channelValue(speed, now)
    const cx = width / 2
    if (!reduceMotion) lift += (liftTarget - lift) * Math.min(1, dt * 7)
    const cy = height * (ANCHOR_REST + (ANCHOR_LIFTED - ANCHOR_REST) * lift)
    const orbRadius = lightfieldOrbRadius(width, height) * (1 + (ORB_LIFTED_SCALE - 1) * lift)
    const reach = Math.hypot(Math.max(cx, width - cx), Math.max(cy, height - cy))

    if (reduceMotion) {
      beams.length = 0
      if (mode === 'running') {
        const seeded = mulberry(7)
        for (let i = 0; i < 42; i += 1) {
          const beam = spawnBeam(reach, orbRadius, seeded)
          beam.r = orbRadius * 1.3 + beam.length + seeded() * reach * 0.7
          beams.push(beam)
        }
      }
    } else {
      // A full-window field needs more beams than a small stage to look as dense.
      const areaScale = Math.max(1, Math.min(2.4, reach / 360))
      spawnCarry += dt * (d > 0.002 ? MIN_SPAWN + (MAX_SPAWN - MIN_SPAWN) * d * areaScale : 0)
      while (spawnCarry >= 1 && beams.length < MAX_BEAMS * areaScale) {
        spawnCarry -= 1
        beams.push(spawnBeam(reach, orbRadius, random))
      }
      if (spawnCarry > 1) spawnCarry = 1
      const velocity = MIN_SPEED + (MAX_SPEED - MIN_SPEED) * s
      for (let i = beams.length - 1; i >= 0; i -= 1) {
        const beam = beams[i]
        beam.r -= velocity * beam.speed * dt * (0.55 + 0.45 * Math.min(1, beam.r / reach))
        if (beam.r - beam.length * 0.15 <= orbRadius) {
          absorb = Math.min(1, absorb + beam.width * 0.012)
          beams.splice(i, 1)
        } else if (s < 0.1 && d < 0.05) {
          beam.alpha *= Math.max(0, 1 - dt * 2.4)
          if (beam.alpha < 0.02) beams.splice(i, 1)
        }
      }
      absorb = Math.max(0, absorb - dt * 1.4)
      flash = Math.max(0, flash - dt * 0.9)
      const failedTarget = mode === 'failed' ? 1 : 0
      failedMix += (failedTarget - failedMix) * Math.min(1, dt * 3)
    }
    if (reduceMotion) failedMix = mode === 'failed' ? 1 : 0

    g.setTransform(dpr, 0, 0, dpr, 0, 0)
    g.clearRect(0, 0, width, height)
    drawBeams(cx, cy, orbRadius, reach, reduceMotion ? 1 : 0.6 + 0.8 * s)
    drawOrb(cx, cy, orbRadius, now - start, reduceMotion ? (mode === 'running' ? 0.6 : 0) : Math.max(d, s) * 0.8)
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
    setLifted,
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

export function ControllerUpdateLightfield({ mode, reduceMotion, lifted = false, children }: { mode: LightfieldMode; reduceMotion?: boolean; lifted?: boolean; children?: React.ReactNode }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const engineRef = useRef<Engine | null>(null)
  const modeRef = useRef(mode)
  modeRef.current = mode
  const liftedRef = useRef(lifted)
  liftedRef.current = lifted

  useLayoutEffect(() => {
    const existing = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
    const meta = existing ?? document.createElement('meta')
    const previous = meta.getAttribute('content')
    meta.name = 'theme-color'
    meta.content = '#0c0806'
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
    const engine = createEngine(canvas, Boolean(reduceMotion), modeRef.current, liftedRef.current)
    engineRef.current = engine
    return () => {
      engine?.destroy()
      engineRef.current = null
    }
  }, [reduceMotion])

  useEffect(() => { engineRef.current?.setMode(mode) }, [mode])
  useEffect(() => { engineRef.current?.setLifted(lifted) }, [lifted])

  return <div className={`controller-update-lightfield ${mode}${lifted ? ' lifted' : ''}`}>
    <canvas ref={canvasRef} aria-hidden="true" />
    {children}
  </div>
}
