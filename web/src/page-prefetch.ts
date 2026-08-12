export type PrefetchPriority = 'high' | 'normal' | 'idle'

export type PrefetchJob = {
  page: string
  priority: PrefetchPriority
  run: () => Promise<void>
}

const PRIORITY_RANK: Record<PrefetchPriority, number> = { high: 0, normal: 1, idle: 2 }

export type PrefetchGuard = (page: string) => boolean

type NetworkInfo = { saveData?: boolean; effectiveType?: string }

// PagePrefetchScheduler warms page-data with bounded concurrency and three
// priority levels. HIGH jobs (explicit navigation intent) jump the queue,
// NORMAL follows, and IDLE pages are warmed last. Foreground navigation
// pauses new NORMAL/IDLE starts so a heavy background queue never delays the
// page the operator just clicked; HIGH intent still enqueues.
export class PagePrefetchScheduler {
  private queue: PrefetchJob[] = []
  private active = new Set<string>()
  private running = 0
  private maxConcurrency: number
  private enabled = true
  private paused = false
  private guard: PrefetchGuard

  constructor(guard: PrefetchGuard, options?: { maxConcurrency?: number }) {
    this.guard = guard
    const connection = (navigator as Navigator & { connection?: NetworkInfo }).connection
    if (connection?.saveData || connection?.effectiveType === 'slow-2g' || connection?.effectiveType === '2g') {
      this.enabled = false
      this.maxConcurrency = 0
    } else if (connection?.effectiveType === '3g') {
      this.maxConcurrency = 1
    } else {
      this.maxConcurrency = Math.max(1, options?.maxConcurrency || 2)
    }
  }

  get isEnabled() {
    return this.enabled
  }

  get size() {
    return this.queue.length
  }

  // enqueue adds or upgrades a page. Re-enqueuing with a higher priority
  // promotes the pending job without duplicating it. While a foreground
  // navigation is paused, NORMAL/IDLE jobs are accepted but not started.
  enqueue(page: string, priority: PrefetchPriority, run: () => Promise<void>) {
    if (!this.enabled) return
    const existing = this.queue.find(job => job.page === page)
    if (existing) {
      if (PRIORITY_RANK[priority] < PRIORITY_RANK[existing.priority]) {
        existing.priority = priority
        this.sortQueue()
      }
      return
    }
    if (this.active.has(page)) return
    this.queue.push({ page, priority, run })
    this.sortQueue()
  }

  // promote moves a queued page to the front for an explicit navigation.
  promote(page: string) {
    const existing = this.queue.find(job => job.page === page)
    if (!existing) return
    existing.priority = 'high'
    this.sortQueue()
  }

  // remove drops a queued page (e.g. it just became fresh or dirty).
  remove(page: string) {
    this.queue = this.queue.filter(job => job.page !== page)
  }

  // pauseIdle blocks new NORMAL/IDLE starts while a foreground navigation is
  // in flight; already running requests finish.
  pauseIdle() {
    this.paused = true
  }

  resumeIdle() {
    this.paused = false
    this.pump()
  }

  clear() {
    this.queue = []
    this.running = 0
    this.active.clear()
  }

  private sortQueue() {
    this.queue.sort((left, right) => PRIORITY_RANK[left.priority] - PRIORITY_RANK[right.priority])
  }

  // pump keeps at most maxConcurrency requests active. Guarded pages (active
  // tab, fresh cache, dirty) are skipped at dequeue time.
  pump() {
    if (!this.enabled) return
    while (this.running < this.maxConcurrency && this.queue.length > 0) {
      const index = this.queue.findIndex(job => job.priority === 'high' || !this.paused)
      if (index < 0) return
      const job = this.queue.splice(index, 1)[0]
      if (this.guard(job.page)) continue
      this.active.add(job.page)
      this.running++
      void job.run().finally(() => {
        this.active.delete(job.page)
        this.running--
        this.pump()
      })
    }
  }
}
