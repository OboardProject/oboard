import { useEffect, useRef, useState } from 'react'
import { usePresence, useReducedMotion } from 'motion/react'

export function MatrixLogoLoader({ loading }: { loading: boolean }) {
  const [isPresent, safeToRemove] = usePresence()
  const reducedMotion = useReducedMotion()
  const cornerRef = useRef<HTMLSpanElement>(null)
  const [phase, setPhase] = useState<'loading' | 'settled' | 'finished' | 'fading'>('loading')

  useEffect(() => {
    if (isPresent) {
      setPhase('loading')
      return
    }
    if (reducedMotion) {
      safeToRemove?.()
      return
    }
    // A hidden tab or disabled animation must not hold the loading screen open.
    const next = phase === 'loading' ? 'settled' : phase === 'settled' ? 'finished' : 'fading'
    const delay = phase === 'loading' ? 900 : phase === 'settled' ? 200 : phase === 'finished' ? 500 : 280
    const corner = cornerRef.current
    const settle = () => setPhase('settled')
    if (phase === 'loading') corner?.addEventListener('animationiteration', settle, { once: true })
    const timer = window.setTimeout(() => {
      if (phase === 'fading') safeToRemove?.()
      else setPhase(next)
    }, delay)
    return () => {
      window.clearTimeout(timer)
      corner?.removeEventListener('animationiteration', settle)
    }
  }, [isPresent, phase, reducedMotion, safeToRemove])

  return (
    <div className={`portal-loader is-${phase}`} role="status" aria-live="polite" aria-busy={isPresent}>
      <div className="portal-loader-mark" aria-hidden="true">
        <span className="portal-loader-square sq-tl" ref={cornerRef} />
        <span className="portal-loader-square sq-tr" />
        <span className="portal-loader-square sq-br" />
        <span className="portal-loader-square sq-bl" />
        <span className="portal-loader-square sq-c" />
      </div>
      <div className="portal-loader-copy">
        <h2>OBoard 控制台</h2>
        <span>{!isPresent ? '加载完成' : loading ? '正在加载当前页面...' : '正在准备控制台...'}</span>
      </div>
    </div>
  )
}
