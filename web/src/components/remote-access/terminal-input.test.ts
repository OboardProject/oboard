import { describe, expect, it } from 'vitest'
import {
  composedDuplicateWindowMS,
  createCompositionGuard,
  exceedsLongPressMove,
  noteCompositionEnd,
  noteCompositionStart,
  shouldDropComposedDuplicate,
} from './terminal-input'

describe('composition guard', () => {
  it('sends a committed IME candidate once when the same text arrives twice', () => {
    // iOS Chinese keyboard: type "ping", tap the candidate. compositionend and the follow-up
    // insertText input event both deliver "ping" to xterm.
    const guard = createCompositionGuard()
    noteCompositionStart(guard)
    noteCompositionEnd(guard, 'ping', 1_000)

    expect(shouldDropComposedDuplicate(guard, 'ping', 1_001)).toBe(false)
    expect(shouldDropComposedDuplicate(guard, 'ping', 1_002)).toBe(true)
  })

  it('only ever swallows one repeat per composition', () => {
    const guard = createCompositionGuard()
    noteCompositionStart(guard)
    noteCompositionEnd(guard, 'ls', 1_000)

    expect(shouldDropComposedDuplicate(guard, 'ls', 1_001)).toBe(false)
    expect(shouldDropComposedDuplicate(guard, 'ls', 1_002)).toBe(true)
    expect(shouldDropComposedDuplicate(guard, 'ls', 1_003)).toBe(false)
  })

  it('keeps a repeat the operator typed after the dedupe window', () => {
    const guard = createCompositionGuard()
    noteCompositionStart(guard)
    noteCompositionEnd(guard, 'ping', 1_000)

    expect(shouldDropComposedDuplicate(guard, 'ping', 1_001)).toBe(false)
    expect(shouldDropComposedDuplicate(guard, 'ping', 1_001 + composedDuplicateWindowMS + 1)).toBe(false)
  })

  it('never touches input that differs from the committed candidate', () => {
    const guard = createCompositionGuard()
    noteCompositionStart(guard)
    noteCompositionEnd(guard, 'ping', 1_000)

    expect(shouldDropComposedDuplicate(guard, 'ping', 1_001)).toBe(false)
    expect(shouldDropComposedDuplicate(guard, 'pong', 1_002)).toBe(false)
    expect(shouldDropComposedDuplicate(guard, 'p', 1_003)).toBe(false)
  })

  it('resets when a new composition starts so the next candidate is not swallowed', () => {
    const guard = createCompositionGuard()
    noteCompositionStart(guard)
    noteCompositionEnd(guard, 'ping', 1_000)
    expect(shouldDropComposedDuplicate(guard, 'ping', 1_001)).toBe(false)

    noteCompositionStart(guard)
    noteCompositionEnd(guard, 'ping', 1_010)
    expect(shouldDropComposedDuplicate(guard, 'ping', 1_011)).toBe(false)
    expect(shouldDropComposedDuplicate(guard, 'ping', 1_012)).toBe(true)
  })

  it('leaves plain typing alone when no composition happened', () => {
    const guard = createCompositionGuard()
    expect(shouldDropComposedDuplicate(guard, 'a', 1_000)).toBe(false)
    expect(shouldDropComposedDuplicate(guard, 'a', 1_001)).toBe(false)
  })
})

describe('long press tracking', () => {
  it('treats a drift as a scroll rather than a long press', () => {
    expect(exceedsLongPressMove({ x: 100, y: 100 }, { x: 104, y: 103 })).toBe(false)
    expect(exceedsLongPressMove({ x: 100, y: 100 }, { x: 100, y: 140 })).toBe(true)
    expect(exceedsLongPressMove({ x: 100, y: 100 }, { x: 60, y: 100 })).toBe(true)
  })
})
