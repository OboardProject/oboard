import { describe, expect, it } from 'vitest'

import { isIndeterminateMutationFailure, isUnknownMutationOutcome, markIndeterminateMutation } from './mutation-outcome'

function httpError(status: number, message = '服务暂时不可用') {
  const error = new Error(message) as Error & { status?: number }
  error.status = status
  return error
}

describe('indeterminate mutation failures', () => {
  it('treats a refusal as definite and a lost answer as unknown', () => {
    expect(isIndeterminateMutationFailure(httpError(409))).toBe(false)
    expect(isIndeterminateMutationFailure(httpError(404))).toBe(false)
    expect(isIndeterminateMutationFailure(httpError(500))).toBe(true)
    expect(isIndeterminateMutationFailure(httpError(502))).toBe(true)
    expect(isIndeterminateMutationFailure(httpError(408))).toBe(true)
    expect(isIndeterminateMutationFailure(new Error('Failed to fetch'))).toBe(true)
  })

  it('rewrites the message without losing the original reason', () => {
    const error = markIndeterminateMutation(httpError(502, 'SQLITE_BUSY: database is locked'))
    expect(error.message).toContain('提交结果未知')
    expect(error.message).toContain('请刷新确认是否已生效')
    // Callers that match on the underlying reason keep working.
    expect(error.message).toContain('database is locked')
    expect(isUnknownMutationOutcome(error)).toBe(true)
  })

  it('leaves a refusal alone', () => {
    const error = markIndeterminateMutation(httpError(409, '名称已存在'))
    expect(error.message).toBe('名称已存在')
    expect(isUnknownMutationOutcome(error)).toBe(false)
  })

  it('does not rewrite the same error twice', () => {
    const error = markIndeterminateMutation(httpError(500, '内部错误'))
    const again = markIndeterminateMutation(error)
    expect(again.message).toBe(error.message)
    expect((again.message.match(/提交结果未知/g) || []).length).toBe(1)
  })
})
