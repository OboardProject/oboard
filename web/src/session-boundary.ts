import { useCallback, useMemo, useRef, useState } from 'react'

export function useSessionBoundary() {
  const boundary = useRef(new SessionBoundary())
  const [generation, setGeneration] = useState(0)
  const advance = useCallback(() => {
    boundary.current.advance()
    setGeneration(value => value + 1)
  }, [])
  const isCurrentSession = useMemo(() => boundary.current.capture(), [generation])
  return { advance, isCurrentSession }
}

export class SessionBoundary {
  private generation = 0

  advance(): void {
    this.generation++
  }

  capture(): () => boolean {
    const generation = this.generation
    return () => generation === this.generation
  }
}
