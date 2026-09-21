import { useRef, useState, type ReactNode } from 'react'
import { Select } from '../ui/select'

export const SERVER_PAGE_SIZE = 24

export function ServerListPage<T>({ items, view, renderItem, page: controlledPage, onPageChange, hideTopPagination }: {
  items: T[]
  view: 'grid' | 'list'
  renderItem: (item: T, index: number) => ReactNode
  page?: number
  onPageChange?: (page: number) => void
  hideTopPagination?: boolean
}) {
  const [internalPage, setInternalPage] = useState(0)
  const page = controlledPage !== undefined ? controlledPage : internalPage
  const topRef = useRef<HTMLDivElement>(null)
  const pageSize = SERVER_PAGE_SIZE
  const pageCount = Math.max(1, Math.ceil(items.length / pageSize))
  const currentPage = Math.min(page, pageCount - 1)
  if (page !== currentPage && controlledPage === undefined) setInternalPage(currentPage)
  const offset = currentPage * pageSize

  const changePage = (next: number, position: string) => {
    if (controlledPage === undefined) {
      setInternalPage(next)
    }
    onPageChange?.(next)
    if (position === '底部') {
      const topSelect = topRef.current?.querySelector<HTMLButtonElement>('[aria-label="服务器页码顶部"]') || topRef.current?.ownerDocument?.querySelector<HTMLButtonElement>('[aria-label="服务器页码顶部"]')
      topSelect?.focus({ preventScroll: true })
    }
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
    {!hideTopPagination && navigation('顶部')}
    <div className={view === 'grid' ? 'server-grid' : 'server-list'}>
      {items.slice(offset, offset + pageSize).map((item, index) => renderItem(item, offset + index))}
    </div>
    {navigation('底部')}
  </div>
}
