export type PageDataResponse<T> = {
  data: T
  epoch: number
}

export type RequestPriority = 'foreground' | 'prefetch' | 'background'
export type PageDataRole = 'admin' | 'operator' | 'viewer' | 'none'

const IDLE_PREFETCH_PAGES: Record<PageDataRole, string[]> = {
  none: ['nodes', 'account'],
  viewer: ['nodes', 'notifications', 'account'],
  operator: ['servers', 'proxy-paths', 'tasks'],
  admin: ['servers', 'proxy-paths', 'users', 'tasks'],
}

export function idlePrefetchPages(role: PageDataRole, activePage: string) {
  return IDLE_PREFETCH_PAGES[role].filter(page => page !== activePage)
}

type PendingRequest<T> = {
  promise: Promise<PageDataResponse<T>>
  controller: AbortController
  priority: RequestPriority
}

export type PageDataRequestOptions = {
  forceFresh?: boolean
  priority?: RequestPriority
}

export function shouldRevalidatePageData(fetchedAt: number | undefined, dirty: boolean, ttlMS: number, now = Date.now()) {
  return dirty || !fetchedAt || now - fetchedAt >= ttlMS
}

export class PageDataRequestCoordinator<T> {
  private epochs = new Map<string, number>()
  private requests = new Map<string, PendingRequest<T>>()

  reset() {
    this.requests.forEach(pending => pending.controller.abort())
    this.requests.forEach((_, page) => {
      this.epochs.set(page, (this.epochs.get(page) || 0) + 1)
    })
    this.requests.clear()
  }

  invalidate(page: string) {
    this.epochs.set(page, (this.epochs.get(page) || 0) + 1)
    this.abort(page)
    this.requests.delete(page)
  }

  invalidateActive() {
    Array.from(this.requests.keys()).forEach(page => this.invalidate(page))
  }

  pending(page: string) {
    return this.requests.get(page)?.promise
  }

  isCurrent(page: string, response: PageDataResponse<T>) {
    return response.epoch === (this.epochs.get(page) || 0)
  }

  priority(page: string): RequestPriority | undefined {
    return this.requests.get(page)?.priority
  }

  request(page: string, load: (signal: AbortSignal) => Promise<T>, options?: boolean | PageDataRequestOptions) {
    const forceFresh = typeof options === 'boolean' ? options : Boolean(options?.forceFresh)
    const priority = typeof options === 'boolean' ? undefined : options?.priority
    if (forceFresh) this.invalidate(page)
    const existing = this.requests.get(page)
    if (existing) {
      // Reuse the in-flight request; a foreground navigation promotes it so
      // the caller waits on the same HTTP request instead of duplicating it.
      if (priority === 'foreground' || existing.priority === 'foreground') {
        existing.priority = 'foreground'
      }
      return existing.promise
    }

    const epoch = this.epochs.get(page) || 0
    const controller = new AbortController()
    const request = load(controller.signal).then(data => ({ data, epoch }))
    this.requests.set(page, { promise: request, controller, priority: priority || 'background' })
    const clear = () => {
      if (this.requests.get(page)?.promise === request) this.requests.delete(page)
    }
    void request.then(clear, clear)
    return request
  }

  // cancel aborts a page request without invalidating its epoch, for
  // low-priority preloads the user no longer needs.
  cancel(page: string) {
    this.abort(page)
    this.requests.delete(page)
  }

  // cancelPrefetch aborts a non-foreground request only; a page the user is
  // actively waiting on is never torn down by the prefetch scheduler.
  cancelPrefetch(page: string) {
    const pending = this.requests.get(page)
    if (!pending || pending.priority === 'foreground') return
    pending.controller.abort()
    this.requests.delete(page)
  }

  // cancelPrefetches aborts warm-up requests that would compete with a
  // foreground navigation. The target page can be retained so its in-flight
  // request is promoted and reused instead of restarted.
  cancelPrefetches(exceptPage?: string) {
    Array.from(this.requests.entries()).forEach(([page, pending]) => {
      if (pending.priority !== 'prefetch' || page === exceptPage) return
      pending.controller.abort()
      this.requests.delete(page)
    })
  }

  private abort(page: string) {
    this.requests.get(page)?.controller.abort()
  }
}
