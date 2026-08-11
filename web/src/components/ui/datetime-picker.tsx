import * as React from 'react'
import { Calendar as CalendarIcon, Clock, ChevronLeft, ChevronRight, X, Check } from 'lucide-react'
import { motion, AnimatePresence } from 'framer-motion'

export interface DateTimePickerProps {
  value?: string // Formats: "YYYY-MM-DDTHH:mm" or "YYYY-MM-DD HH:mm:ss"
  onChange?: (value: string) => void
  placeholder?: string
  disabled?: boolean
  required?: boolean
  className?: string
  style?: React.CSSProperties
  'aria-label'?: string
  title?: string
}

function parseDateTimeString(val?: string): { year: number; month: number; day: number; hour: number; minute: number } | null {
  if (!val) return null
  const cleaned = val.replace(' ', 'T')
  const [datePart, timePart] = cleaned.split('T')
  if (!datePart) return null
  const [y, m, d] = datePart.split('-').map(Number)
  if (!y || !m || !d) return null
  let h = 0
  let min = 0
  if (timePart) {
    const [hStr, minStr] = timePart.split(':').map(Number)
    if (!isNaN(hStr)) h = hStr
    if (!isNaN(minStr)) min = minStr
  }
  return { year: y, month: m, day: d, hour: h, minute: min }
}

function formatToInputValue(year: number, month: number, day: number, hour: number, minute: number): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${year}-${pad(month)}-${pad(day)}T${pad(hour)}:${pad(minute)}`
}

function formatDisplayString(year: number, month: number, day: number, hour: number, minute: number): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${year}-${pad(month)}-${pad(day)} ${pad(hour)}:${pad(minute)}`
}

const WEEKDAYS = ['日', '一', '二', '三', '四', '五', '六']

export function DateTimePicker({
  value = '',
  onChange,
  placeholder = '年 / 月 / 日  --:--',
  disabled = false,
  required = false,
  className = '',
  style,
  'aria-label': ariaLabel,
  title,
}: DateTimePickerProps) {
  const containerRef = React.useRef<HTMLDivElement>(null)
  const [isOpen, setIsOpen] = React.useState(false)

  const parsed = React.useMemo(() => parseDateTimeString(value), [value])

  const now = new Date()
  const [viewYear, setViewYear] = React.useState<number>(parsed?.year || now.getFullYear())
  const [viewMonth, setViewMonth] = React.useState<number>(parsed?.month || (now.getMonth() + 1))
  const [selectedHour, setSelectedHour] = React.useState<number>(parsed?.hour ?? 0)
  const [selectedMinute, setSelectedMinute] = React.useState<number>(parsed?.minute ?? 0)

  // Sync view when parsed value changes or popover opens
  React.useEffect(() => {
    if (parsed) {
      setViewYear(parsed.year)
      setViewMonth(parsed.month)
      setSelectedHour(parsed.hour)
      setSelectedMinute(parsed.minute)
    }
  }, [parsed])

  // Close when clicking outside
  React.useEffect(() => {
    if (!isOpen) return
    const handleClickOutside = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [isOpen])

  // Month navigation
  const prevMonth = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (viewMonth === 1) {
      setViewYear(y => y - 1)
      setViewMonth(12)
    } else {
      setViewMonth(m => m - 1)
    }
  }

  const nextMonth = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (viewMonth === 12) {
      setViewYear(y => y + 1)
      setViewMonth(1)
    } else {
      setViewMonth(m => m + 1)
    }
  }

  // Days matrix for current view
  const daysMatrix = React.useMemo(() => {
    const firstDay = new Date(viewYear, viewMonth - 1, 1).getDay()
    const daysInMonth = new Date(viewYear, viewMonth, 0).getDate()
    const daysInPrevMonth = new Date(viewYear, viewMonth - 1, 0).getDate()

    const items: Array<{ year: number; month: number; day: number; isCurrentMonth: boolean }> = []

    // Prev month padding
    for (let i = firstDay - 1; i >= 0; i--) {
      const prevM = viewMonth === 1 ? 12 : viewMonth - 1
      const prevY = viewMonth === 1 ? viewYear - 1 : viewYear
      items.push({ year: prevY, month: prevM, day: daysInPrevMonth - i, isCurrentMonth: false })
    }

    // Current month days
    for (let d = 1; d <= daysInMonth; d++) {
      items.push({ year: viewYear, month: viewMonth, day: d, isCurrentMonth: true })
    }

    // Next month padding to fill 42 cells
    const remaining = 42 - items.length
    for (let d = 1; d <= remaining; d++) {
      const nextM = viewMonth === 12 ? 1 : viewMonth + 1
      const nextY = viewMonth === 12 ? viewYear + 1 : viewYear
      items.push({ year: nextY, month: nextM, day: d, isCurrentMonth: false })
    }

    return items
  }, [viewYear, viewMonth])

  const handleSelectDay = (dayItem: { year: number; month: number; day: number }) => {
    const formatted = formatToInputValue(dayItem.year, dayItem.month, dayItem.day, selectedHour, selectedMinute)
    onChange?.(formatted)
  }

  const handleTimeChange = (h: number, min: number) => {
    setSelectedHour(h)
    setSelectedMinute(min)
    if (parsed) {
      const formatted = formatToInputValue(parsed.year, parsed.month, parsed.day, h, min)
      onChange?.(formatted)
    }
  }

  const handleQuickPreset = (offsetDays: number) => {
    const target = new Date()
    target.setDate(target.getDate() + offsetDays)
    const y = target.getFullYear()
    const m = target.getMonth() + 1
    const d = target.getDate()
    setViewYear(y)
    setViewMonth(m)
    const formatted = formatToInputValue(y, m, d, offsetDays > 0 ? 23 : selectedHour, offsetDays > 0 ? 59 : selectedMinute)
    onChange?.(formatted)
  }

  const handleClear = (e: React.MouseEvent) => {
    e.stopPropagation()
    onChange?.('')
  }

  const displayVal = parsed ? formatDisplayString(parsed.year, parsed.month, parsed.day, parsed.hour, parsed.minute) : ''

  const isToday = (d: { year: number; month: number; day: number }) => {
    return d.year === now.getFullYear() && d.month === (now.getMonth() + 1) && d.day === now.getDate()
  }

  const isSelected = (d: { year: number; month: number; day: number }) => {
    return parsed && d.year === parsed.year && d.month === parsed.month && d.day === parsed.day
  }

  return (
    <div
      ref={containerRef}
      className={`ui-datetime-picker ${isOpen ? 'is-open' : ''} ${disabled ? 'is-disabled' : ''} ${className}`}
      style={style}
    >
      <div
        className="ui-datetime-input-wrap"
        onClick={() => !disabled && setIsOpen(!isOpen)}
        title={title || ariaLabel}
      >
        <CalendarIcon className="ui-datetime-icon" size={15} />
        <span className={`ui-datetime-value ${!displayVal ? 'is-placeholder' : ''}`}>
          {displayVal || placeholder}
        </span>
        {displayVal && !disabled && (
          <button
            type="button"
            className="ui-datetime-clear icon-button ghost"
            onClick={handleClear}
            aria-label="清除时间"
            title="清除时间"
          >
            <X size={13} />
          </button>
        )}
      </div>

      <AnimatePresence>
        {isOpen && (
          <motion.div
            className="ui-datetime-popover"
            initial={{ opacity: 0, scale: 0.96, y: -4 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.96, y: -4 }}
            transition={{ duration: 0.18, ease: [0.16, 1, 0.3, 1] }}
          >
            {/* Header: Month & Year Controls */}
            <div className="ui-datetime-header">
              <button type="button" className="ghost icon-button" onClick={prevMonth} aria-label="上个月">
                <ChevronLeft size={16} />
              </button>
              <span className="ui-datetime-month-title">
                {viewYear}年 {String(viewMonth).padStart(2, '0')}月
              </span>
              <button type="button" className="ghost icon-button" onClick={nextMonth} aria-label="下个月">
                <ChevronRight size={16} />
              </button>
            </div>

            {/* Quick Presets */}
            <div className="ui-datetime-presets">
              <button type="button" className="sub-chip" onClick={() => handleQuickPreset(0)}>今天</button>
              <button type="button" className="sub-chip" onClick={() => handleQuickPreset(1)}>明天</button>
              <button type="button" className="sub-chip" onClick={() => handleQuickPreset(7)}>7天后</button>
              <button type="button" className="sub-chip" onClick={() => handleQuickPreset(30)}>30天后</button>
              <button type="button" className="sub-chip" onClick={() => handleQuickPreset(365)}>1年后</button>
            </div>

            {/* Weekdays Bar */}
            <div className="ui-datetime-weekdays">
              {WEEKDAYS.map(w => (
                <span key={w}>{w}</span>
              ))}
            </div>

            {/* Calendar Grid */}
            <div className="ui-datetime-days-grid">
              {daysMatrix.map((item, idx) => {
                const selected = isSelected(item)
                const today = isToday(item)
                return (
                  <button
                    key={`${item.year}-${item.month}-${item.day}-${idx}`}
                    type="button"
                    className={`ui-datetime-day-cell ${item.isCurrentMonth ? 'is-current-month' : 'is-other-month'} ${selected ? 'is-selected' : ''} ${today ? 'is-today' : ''}`}
                    onClick={() => handleSelectDay(item)}
                  >
                    {item.day}
                  </button>
                )
              })}
            </div>

            {/* Time Controls */}
            <div className="ui-datetime-time-section">
              <div className="ui-datetime-time-picker">
                <Clock size={14} className="muted" />
                <span>时间</span>
                <input
                  type="number"
                  min={0}
                  max={23}
                  value={String(selectedHour).padStart(2, '0')}
                  onChange={e => {
                    const h = Math.min(23, Math.max(0, Number(e.target.value) || 0))
                    handleTimeChange(h, selectedMinute)
                  }}
                  className="ui-datetime-time-input"
                  aria-label="小时"
                />
                <span>:</span>
                <input
                  type="number"
                  min={0}
                  max={59}
                  value={String(selectedMinute).padStart(2, '0')}
                  onChange={e => {
                    const min = Math.min(59, Math.max(0, Number(e.target.value) || 0))
                    handleTimeChange(selectedHour, min)
                  }}
                  className="ui-datetime-time-input"
                  aria-label="分钟"
                />
              </div>
              <div className="ui-datetime-time-presets">
                <button type="button" className="sub-chip" onClick={() => handleTimeChange(0, 0)}>00:00</button>
                <button type="button" className="sub-chip" onClick={() => handleTimeChange(12, 0)}>12:00</button>
                <button type="button" className="sub-chip" onClick={() => handleTimeChange(23, 59)}>23:59</button>
              </div>
            </div>

            {/* Footer */}
            <div className="ui-datetime-footer">
              {value && (
                <button type="button" className="ghost danger-text" onClick={handleClear}>
                  清除
                </button>
              )}
              <button
                type="button"
                className="ui-datetime-confirm-btn"
                onClick={() => setIsOpen(false)}
              >
                <Check size={14} /> 确认
              </button>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}
