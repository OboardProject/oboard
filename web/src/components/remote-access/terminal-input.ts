// Mobile IME guard.
//
// On iOS (and some Android IMEs) a Chinese keyboard commits a candidate through two
// independent paths: the `compositionend` handler reads the textarea and emits the committed
// text, and an `input` event with `inputType="insertText"` carrying the same text also reaches
// xterm because no `keydown` precedes a soft-keyboard commit. Typing `ping` and tapping the
// candidate then writes `pingping` into the shell.
//
// The guard lets the first delivery through and drops one identical repeat that arrives right
// after the same composition. A new composition, different text, or a late repeat is never
// touched, so desktop and single-delivery IMEs keep their current behaviour.

export const composedDuplicateWindowMS = 250

export type CompositionGuard = {
  composing: boolean
  data: string
  endedAt: number
  delivered: boolean
}

export function createCompositionGuard(): CompositionGuard {
  return { composing: false, data: '', endedAt: 0, delivered: false }
}

export function noteCompositionStart(guard: CompositionGuard) {
  guard.composing = true
  guard.data = ''
  guard.delivered = false
  guard.endedAt = 0
}

export function noteCompositionEnd(guard: CompositionGuard, data: string, now: number) {
  guard.composing = false
  guard.data = data || ''
  guard.delivered = false
  guard.endedAt = now
}

// Returns true when this chunk is the duplicate half of a just-committed composition.
export function shouldDropComposedDuplicate(guard: CompositionGuard, data: string, now: number) {
  if (!guard.data || data !== guard.data) return false
  if (now - guard.endedAt > composedDuplicateWindowMS) return false
  if (!guard.delivered) {
    guard.delivered = true
    return false
  }
  // Only ever swallow one repeat per composition.
  guard.data = ''
  return true
}

// A long press on a phone has no terminal context menu, so the pane opens its own. A press
// that drifts is a scroll or a selection drag and must not become a menu.
export const longPressDelayMS = 500
export const longPressMoveTolerancePX = 12

export function exceedsLongPressMove(start: { x: number; y: number }, current: { x: number; y: number }) {
  return Math.abs(current.x - start.x) > longPressMoveTolerancePX
    || Math.abs(current.y - start.y) > longPressMoveTolerancePX
}
