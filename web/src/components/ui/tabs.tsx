import * as React from "react"

interface TabsContextType {
  id: string
  value: string
  onValueChange?: (value: string) => void
}

const TabsContext = React.createContext<TabsContextType | null>(null)

export interface TabsProps extends React.HTMLAttributes<HTMLDivElement> {
  value: string
  onValueChange?: (value: string) => void
}

export function Tabs({ value, onValueChange, className = "", children, ...props }: TabsProps) {
  const id = React.useId()
  return (
    <TabsContext.Provider value={{ id, value, onValueChange }}>
      <div className={`w-full ${className}`} {...props}>
        {children}
      </div>
    </TabsContext.Provider>
  )
}

export interface TabsListProps extends React.HTMLAttributes<HTMLDivElement> {}

export function TabsList({ className = "", children, ...props }: TabsListProps) {
  return (
    <div
      className={`ui-tabs-list ${className}`.trim()}
      role="tablist"
      {...props}
    >
      {children}
    </div>
  )
}

export interface TabsTriggerProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  value: string
}

export function TabsTrigger({ value, className = "", children, onKeyDown, ...props }: TabsTriggerProps) {
  const context = React.useContext(TabsContext)
  if (!context) throw new Error("TabsTrigger must be used within Tabs")

  const isActive = context.value === value

  return (
    <button
      type="button"
      role="tab"
      aria-selected={isActive}
      id={`${context.id}-tab-${value}`}
      aria-controls={`${context.id}-panel-${value}`}
      tabIndex={isActive ? 0 : -1}
      className={`ui-tabs-trigger${isActive ? ' active' : ''}${className ? ` ${className}` : ''}`}
      onClick={() => context.onValueChange?.(value)}
      {...props}
      onKeyDown={event => {
        onKeyDown?.(event)
        if (event.defaultPrevented || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
        const list = event.currentTarget.closest('[role="tablist"]')
        const tabs = Array.from(list?.querySelectorAll<HTMLButtonElement>('[role="tab"]:not(:disabled)') || [])
        if (!tabs.length) return
        const index = tabs.indexOf(event.currentTarget)
        const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length
        event.preventDefault()
        tabs[next].focus()
        tabs[next].click()
      }}
    >
      {children}
    </button>
  )
}

export interface TabsContentProps extends React.HTMLAttributes<HTMLDivElement> {
  value: string
}

export function TabsContent({ value, className = "", children, ...props }: TabsContentProps) {
  const context = React.useContext(TabsContext)
  if (!context) throw new Error("TabsContent must be used within Tabs")

  const isActive = context.value === value

  if (!isActive) return null

  return (
    <div
      role="tabpanel"
      id={`${context.id}-panel-${value}`}
      aria-labelledby={`${context.id}-tab-${value}`}
      className={`ui-tabs-panel ${className}`.trim()}
      {...props}
    >
      {children}
    </div>
  )
}
