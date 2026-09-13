import * as React from 'react'

export type SurfaceMotion = 'form' | 'compact' | 'workspace' | 'none'

const durations: Record<Exclude<SurfaceMotion, 'none'>, number> = {
  compact: 180,
  form: 220,
  workspace: 260,
}

// Observe intrinsic border-box sizes, never the animated visual rectangle.
// The individual scale property leaves Motion's entrance transform untouched.
export function useSurfaceResize(
  ref: React.RefObject<HTMLElement | null>,
  enabled: boolean,
  motion: SurfaceMotion,
) {
  React.useLayoutEffect(() => {
    const panel = ref.current
    if (!panel || !enabled || motion === 'none' || typeof ResizeObserver === 'undefined' || !panel.animate) return
    let previous: { width: number; height: number } | undefined
    let animation: Animation | undefined
    const observer = new ResizeObserver(entries => {
      const entry = entries[0]
      const box = entry?.borderBoxSize?.[0]
      if (!box) return
      const next = { width: box.inlineSize, height: box.blockSize }
      const before = previous
      previous = next
      if (!before || next.width <= 0 || next.height <= 0 ||
        (Math.abs(before.width - next.width) < 1 && Math.abs(before.height - next.height) < 1)) return
      const currentScale = animation?.playState === 'running'
        ? getComputedStyle(panel).scale.split(' ').map(Number)
        : [1, 1]
      const x = Number.isFinite(currentScale[0]) ? currentScale[0] : 1
      const y = Number.isFinite(currentScale[1]) ? currentScale[1] : x
      animation?.cancel()
      panel.style.willChange = 'scale'
      animation = panel.animate([
        { scale: `${before.width * x / next.width} ${before.height * y / next.height}` },
        { scale: '1 1' },
      ], { duration: durations[motion], easing: 'cubic-bezier(0.22, 1, 0.36, 1)' })
      animation.onfinish = () => { panel.style.willChange = '' }
    })
    observer.observe(panel, { box: 'border-box' })
    return () => {
      observer.disconnect()
      animation?.cancel()
      panel.style.willChange = ''
    }
  }, [ref, enabled, motion])
}
