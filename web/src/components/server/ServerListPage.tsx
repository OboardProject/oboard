import { useRef, useState, type ReactNode } from 'react'
import { Select } from '../ui/select'

const pageSize = 24

export function ServerListPage<T>({ items, view, renderItem }: {
  items: T[]
  view: 'grid' | 'list'
  renderItem: (item: T, index: number) => ReactNode
}) {
  const [page, setPage] = useState(0)
  const topRef = useRef<HTMLDivElement>(null)
  const pageCount = Math.max(1, Math.ceil(items.length / pageSize))
  const currentPage = Math.min(page, pageCount - 1)
  if (page !== currentPage) setPage(currentPage)
  const offset = currentPage * pageSize

  const changePage = (next: number, position: string) => {
    setPage(next)
    if (position === '底部') topRef.current?.querySelector<HTMLButtonElement>('[aria-label="服务器页码顶部"]')?.focus({ preventScroll: true })
    topRef.current?.scrollIntoView({ block: 'start' })
  }
  const navigation = (position: string) => pageCount > 1 && <nav className="server-list-pagination" aria-label={`服务器分页${position}`}>
    <span className="muted" role={position === '顶部' ? 'status' : undefined}>{offset + 1}–{Math.min(offset + pageSize, items.length)} / {items.length} 台</span>
    <div className="server-list-pagination-controls">
      <button type="button" className="ghost" disabled={currentPage === 0} onClick={() => changePage(currentPage - 1, position)}>上一页</button>
      <Select aria-label={`服务器页码${position}`} value={String(currentPage)} onChange={event => changePage(Number(event.target.value), position)}>
        {Array.from({ length: pageCount }, (_, index) => <option key={index} value={index}>第 {index + 1} / {pageCount} 页</option>)}
      </Select>
      <button type="button" className="ghost" disabled={currentPage === pageCount - 1} onClick={() => changePage(currentPage + 1, position)}>下一页</button>
    </div>
  </nav>

  return <div ref={topRef} className="server-list-page">
    {navigation('顶部')}
    <div className={view === 'grid' ? 'server-grid' : 'server-list'}>
      {items.slice(offset, offset + pageSize).map((item, index) => renderItem(item, offset + index))}
    </div>
    {navigation('底部')}
  </div>
}
