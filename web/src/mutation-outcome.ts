// Whether a failed mutation is known not to have happened.
//
// A 4xx is that answer: the request was refused before it changed anything. A
// 5xx, a 408, or no response at all is not - the write may have committed and
// the answer got lost on the way back. Telling the operator such a request
// "failed" invites them to repeat an operation that may already have landed,
// which for a token rotation or a queued task is not a harmless retry.
export function isIndeterminateMutationFailure(error: unknown): boolean {
  const status = Number((error as { status?: unknown } | null)?.status || 0)
  if (status === 0) return true
  return status >= 500 || status === 408
}

export const unknownMutationPrefix = '提交结果未知'

// markIndeterminateMutation rewrites the message an indeterminate failure
// carries, keeping the original reason inside it so message matching elsewhere
// still works, and flags the error so a caller that knows better can react.
export function markIndeterminateMutation<T>(error: T): T {
  if (!isIndeterminateMutationFailure(error)) return error
  const target = error as T & { message?: string; unknownOutcome?: boolean }
  if (target.unknownOutcome) return error
  target.unknownOutcome = true
  const reason = String(target.message || '').trim()
  target.message = reason
    ? `${unknownMutationPrefix}：${reason}。请刷新确认是否已生效`
    : `${unknownMutationPrefix}，请刷新确认是否已生效`
  return error
}

export function isUnknownMutationOutcome(error: unknown): boolean {
  return Boolean((error as { unknownOutcome?: boolean } | null)?.unknownOutcome)
}
