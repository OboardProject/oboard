import { markIndeterminateMutation } from './mutation-outcome'

export class SupersededAuthRequestError extends Error {
  constructor() {
    super('superseded authentication request')
    this.name = 'SupersededAuthRequestError'
  }
}

export type MutationResponseObserver = (path: string, data: any, method: string) => void

export function createAPIClientFactory(appPath: (path: string) => string, apiRequestError: (data: any, response: Response) => Error) {
  return function api(token: string, onUnauthorized?: (failedToken: string) => boolean, onMutationResponse?: MutationResponseObserver, isCurrentSession: () => boolean = () => true) {
    const assertCurrentSession = () => { if (!isCurrentSession()) throw new SupersededAuthRequestError() }
    const observe: MutationResponseObserver = (path, data, method) => { if (isCurrentSession()) onMutationResponse?.(path, data, method) }
    async function sessionResult<T>(result: Promise<T>): Promise<T> {
      try {
        const value = await result
        assertCurrentSession()
        return value
      } catch (error) {
        assertCurrentSession()
        throw error
      }
    }
    const authHeaders: Record<string, string> = token && token !== 'cookie' ? { authorization: `Bearer ${token}` } : {}
    const csrfHeaders = (): Record<string, string> => {
      const csrf = token === 'cookie' ? sessionStorage.getItem('oboard.csrf') || '' : ''
      return csrf ? { 'x-oboard-csrf': csrf } : {}
    }
    async function request<T = any>(path: string, init: RequestInit = {}): Promise<T> {
      assertCurrentSession()
      const method = String(init.method || 'GET').toUpperCase()
      const mutation = method !== 'GET' && method !== 'HEAD'
      if (mutation) observe(path, { mutation_pending: true }, method)
      let res: Response
      try {
        res = await fetch(appPath('/api/v1/ui' + path), {
          ...init,
          credentials: 'same-origin',
          headers: {
            'content-type': 'application/json',
            ...authHeaders,
            ...csrfHeaders(),
            ...(init.headers || {})
          }
        })
      } catch (error) {
        assertCurrentSession()
        if (mutation) observe(path, { mutation_pending: false }, method)
        // The request never reached an answer. For a write that is not a
        // failure, so it is reported as an outcome to be confirmed rather than
        // one to be repeated.
        throw mutation ? markIndeterminateMutation(error) : error
      }
      const data = await res.json().catch(() => ({}))
      assertCurrentSession()
      if (!res.ok) {
        if (mutation) observe(path, { mutation_pending: false }, method)
        if (res.status === 401 && token && onUnauthorized) {
          if (!onUnauthorized(token)) throw new SupersededAuthRequestError()
          throw apiRequestError({ error: '登录已过期，请重新登录' }, res)
        }
        const failure = apiRequestError(data, res)
        throw mutation ? markIndeterminateMutation(failure) : failure
      }
      if (mutation) observe(path, { ...data, mutation_pending: false }, method)
      return data
    }
    async function requestV2<T = any>(path: string, init: RequestInit = {}): Promise<T> {
      assertCurrentSession()
      const method = String(init.method || 'GET').toUpperCase()
      const mutation = method !== 'GET' && method !== 'HEAD'
      if (mutation) observe(path, { mutation_pending: true }, method)
      let res: Response
      try {
        res = await fetch(appPath('/api/v1' + path), {
          ...init,
          credentials: 'same-origin',
          headers: {
            'content-type': 'application/json',
            ...authHeaders,
            ...csrfHeaders(),
            ...(init.headers || {})
          }
        })
      } catch (error) {
        assertCurrentSession()
        if (mutation) observe(path, { mutation_pending: false }, method)
        throw mutation ? markIndeterminateMutation(error) : error
      }
      const payload = await res.json().catch(() => ({})) as any
      assertCurrentSession()
      if (!res.ok) {
        if (mutation) observe(path, { mutation_pending: false }, method)
        if (res.status === 401 && token && onUnauthorized) {
          if (!onUnauthorized(token)) throw new SupersededAuthRequestError()
          throw new Error('登录已过期，请重新登录')
        }
        const v2Error = payload?.error && typeof payload.error === 'object' ? payload.error : null
        const failure = apiRequestError({ error: v2Error?.message || payload?.error, message: payload?.message }, res)
        throw mutation ? markIndeterminateMutation(failure) : failure
      }
      if (mutation) observe(path, { ...(payload.data || {}), mutation_pending: false }, method)
      return payload.data as T
    }
    async function download(path: string): Promise<{ blob: Blob; filename: string }> {
      assertCurrentSession()
      const res = await sessionResult(fetch(appPath('/api/v1/ui' + path), { credentials: 'same-origin', headers: authHeaders }))
      assertCurrentSession()
      if (!res.ok) {
        const data = await sessionResult(res.json().catch(() => ({})))
        throw apiRequestError(data, res)
      }
      const disposition = res.headers.get('content-disposition') || ''
      const filename = disposition.match(/filename="?([^";]+)"?/i)?.[1] || 'oboard-logs.zip'
      const blob = await sessionResult(res.blob())
      assertCurrentSession()
      return { blob, filename }
    }
    async function upload<T = any>(path: string, body: FormData): Promise<T> {
      assertCurrentSession()
      const res = await sessionResult(fetch(appPath('/api/v1/ui' + path), { method: 'POST', body, credentials: 'same-origin', headers: { ...authHeaders, ...csrfHeaders() } }))
      const data = await res.json().catch(() => ({}))
      assertCurrentSession()
      if (!res.ok) throw apiRequestError(data, res)
      return data
    }
    return { request, requestV2, download, upload }
  }

}
