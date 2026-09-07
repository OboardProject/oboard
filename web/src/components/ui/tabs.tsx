import * as React from "react"

interface TabsContextType {
  value: string
  onValueChange?: (value: string) => void
}

const TabsContext = React.createContext<TabsContextType | null>(null)

export interface TabsProps extends React.HTMLAttributes<HTMLDivElement> {
  value: string
  onValueChange?: (value: string) => void
}

export function Tabs({ value, onValueChange, className = "", children, ...props }: TabsProps) {
  return (
    <TabsContext.Provider value={{ value, onValueChange }}>
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

export function TabsTrigger({ value, className = "", children, ...props }: TabsTriggerProps) {
  const context = React.useContext(TabsContext)
  if (!context) throw new Error("TabsTrigger must be used within Tabs")

  const isActive = context.value === value

  return (
    <button
      type="button"
      role="tab"
      aria-selected={isActive}
      className={`ui-tabs-trigger${isActive ? ' active' : ''}${className ? ` ${className}` : ''}`}
      onClick={() => context.onValueChange?.(value)}
      {...props}
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
      className={`ui-tabs-panel ${className}`.trim()}
      {...props}
    >
      {children}
    </div>
  )
}
