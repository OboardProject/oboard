import { describe, expect, it } from 'vitest'
import { createEditSession } from './edit-session'

const create = () => createEditSession<{ name: string }>({ clone: value => ({ ...value }), equal: (a, b) => a.name === b.name })

describe('edit session submission snapshots', () => {
  it('confirms only the submitted draft and preserves later input', () => {
    const editor = create()
    editor.begin('server:1', { name: 'original' })
    editor.change({ name: 'A' })
    const submission = editor.capture()!
    editor.change({ name: 'B' })
    expect(submission.draft.name).toBe('A')
    expect(editor.settle(submission, 'applied')).toEqual({ current: true, canClose: false })
    expect(editor.getSnapshot()).toMatchObject({ draft: { name: 'B' }, dirty: true, submitting: false })
  })

  it('does not close or modify B when A finishes', () => {
    const editor = create()
    editor.begin('server:A', { name: 'A' })
    const submission = editor.capture()!
    editor.begin('server:B', { name: 'B' })
    expect(editor.settle(submission, 'applied')).toEqual({ current: false, canClose: false })
    expect(editor.getSnapshot()).toMatchObject({ key: 'server:B', draft: { name: 'B' }, dirty: false })
  })

  it('does not close a reopened session for the same object', () => {
    const editor = create()
    editor.begin('server:A', { name: 'A' })
    const submission = editor.capture()!
    editor.close()
    editor.begin('server:A', { name: 'new view' })
    expect(editor.capture()).toBeNull()
    expect(editor.settle(submission, 'applied').canClose).toBe(false)
    expect(editor.getSnapshot()).toMatchObject({ draft: { name: 'new view' }, submitting: false })
  })

  it('retains draft on refusal, blocks double submit and closes only a clean confirmed snapshot', () => {
    const editor = create()
    editor.begin('server:1', { name: 'original' })
    editor.change({ name: 'changed' })
    const rejected = editor.capture()!
    expect(editor.capture()).toBeNull()
    editor.settle(rejected, 'rejected')
    expect(editor.getSnapshot()).toMatchObject({ dirty: true, draft: { name: 'changed' } })
    const confirmed = editor.capture()!
    expect(editor.settle(confirmed, 'applied', { name: 'normalized' }).canClose).toBe(true)
    expect(editor.getSnapshot()).toMatchObject({ dirty: false, draft: { name: 'normalized' } })
  })

  it('keeps uncertainty when the view closes and reopens, without persisting sensitive drafts', () => {
    const editor = create()
    editor.begin('server:1', { name: 'draft' })
    const submission = editor.capture()!
    editor.close()
    expect(editor.getSnapshot().draft).toBeNull()
    editor.settle(submission, 'unknown')
    editor.begin('server:1', { name: 'observed' })
    expect(editor.getSnapshot().unknown).toBe(true)
    expect(editor.capture()).toBeNull()
    editor.reset()
    editor.begin('server:1', { name: 'different account' })
    expect(editor.getSnapshot().unknown).toBe(false)
  })

  it('ignores repeated completions and an old completion after reset', () => {
    const editor = create()
    editor.begin('server:1', { name: 'A' })
    const submission = editor.capture()!
    editor.reset()
    editor.begin('server:1', { name: 'B' })
    expect(editor.settle(submission, 'unknown')).toEqual({ current: false, canClose: false })
    expect(editor.getSnapshot()).toMatchObject({ unknown: false, draft: { name: 'B' } })
  })
})
