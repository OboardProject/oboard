export type PageDataResponse<T> = {
  data: T
  epoch: number
}

export class PageDataRequestCoordinator<T> {
  private epochs = new Map<string, number>()
  private requests = new Map<string, Promise<PageDataResponse<T>>>()

  reset() {
    Array.from(this.requests.keys()).forEach(page => {
      this.epochs.set(page, (this.epochs.get(page) || 0) + 1)
    })
    this.requests.clear()
  }

  invalidate(page: string) {
    this.epochs.set(page, (this.epochs.get(page) || 0) + 1)
    this.requests.delete(page)
  }

  invalidateActive() {
    Array.from(this.requests.keys()).forEach(page => this.invalidate(page))
  }

  pending(page: string) {
    return this.requests.get(page)
  }

  isCurrent(page: string, response: PageDataResponse<T>) {
    return response.epoch === (this.epochs.get(page) || 0)
  }

  request(page: string, load: () => Promise<T>, forceFresh = false) {
    if (forceFresh) this.invalidate(page)
    const pending = this.requests.get(page)
    if (pending) return pending

    const epoch = this.epochs.get(page) || 0
    const request = load().then(data => ({ data, epoch }))
    this.requests.set(page, request)
    const clear = () => {
      if (this.requests.get(page) === request) this.requests.delete(page)
    }
    void request.then(clear, clear)
    return request
  }
}
