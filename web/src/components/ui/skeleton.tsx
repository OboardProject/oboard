import * as React from "react"

export function Skeleton({ className = "", ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={`skeleton-shimmer ${className}`.trim()}
      aria-hidden="true"
      {...props}
    />
  )
}

export function TableSkeleton() {
  return (
    <div className="skeleton-table" aria-busy="true" aria-live="polite">
      <div className="skeleton-table-head">
        <Skeleton className="skeleton-line w-1/4" />
        <Skeleton className="skeleton-line w-1/4" />
        <Skeleton className="skeleton-line w-1/4" />
        <Skeleton className="skeleton-line w-1/4" />
      </div>
      <div className="skeleton-table-body">
        {[1, 2, 3, 4, 5].map((i) => (
          <div key={i} className="skeleton-table-row">
            <Skeleton className="skeleton-line w-1/4" />
            <Skeleton className="skeleton-line w-1/4" />
            <Skeleton className="skeleton-line w-1/4" />
            <Skeleton className="skeleton-line w-1/4" />
          </div>
        ))}
      </div>
    </div>
  )
}

export function CardSkeleton() {
  return (
    <div className="skeleton-card-grid" aria-busy="true" aria-live="polite">
      {[1, 2, 3].map((i) => (
        <div key={i} className="skeleton-card">
          <div className="skeleton-card-head">
            <Skeleton className="skeleton-line w-1/3" />
            <Skeleton className="skeleton-pill" />
          </div>
          <Skeleton className="skeleton-block" />
          <div className="skeleton-card-foot">
            <Skeleton className="skeleton-line w-2/3" />
            <Skeleton className="skeleton-line w-1/4" />
          </div>
        </div>
      ))}
    </div>
  )
}

export function DashboardSkeleton() {
  return (
    <div className="skeleton-dashboard" aria-busy="true" aria-live="polite">
      <Skeleton className="skeleton-line w-2/3 skeleton-title" />
      <div className="skeleton-stat-grid">
        {[1, 2, 3, 4].map((i) => (
          <div key={i} className="skeleton-card skeleton-stat">
            <Skeleton className="skeleton-line w-1/3" />
            <Skeleton className="skeleton-line w-2/3 skeleton-metric" />
            <Skeleton className="skeleton-line w-1/2" />
          </div>
        ))}
      </div>
      <TableSkeleton />
    </div>
  )
}
