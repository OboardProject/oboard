import * as React from "react"

interface DropdownContextType {
  isOpen: boolean
  setIsOpen: (open: boolean) => void
}

const DropdownContext = React.createContext<DropdownContextType | null>(null)

export function Dropdown({ children }: { children: React.ReactNode }) {
  const [isOpen, setIsOpen] = React.useState(false)
  const ref = React.useRef<HTMLDivElement>(null)

  React.useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (ref.current && !ref.current.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }
    document.addEventListener("mousedown", handleClickOutside)
    return () => document.removeEventListener("mousedown", handleClickOutside)
  }, [])

  return (
    <DropdownContext.Provider value={{ isOpen, setIsOpen }}>
      <div ref={ref} className="relative inline-block text-left">
        {children}
      </div>
    </DropdownContext.Provider>
  )
}

export function DropdownTrigger({ children }: { children: React.ReactNode }) {
  const context = React.useContext(DropdownContext)
  if (!context) throw new Error("DropdownTrigger must be used within Dropdown")

  return React.cloneElement(children as React.ReactElement<any>, {
    onClick: (e: React.MouseEvent) => {
      e.preventDefault()
      context.setIsOpen(!context.isOpen)
    }
  })
}

export interface DropdownContentProps extends React.HTMLAttributes<HTMLDivElement> {
  align?: "left" | "right"
}

export function DropdownContent({ children, align = "right", className = "", ...props }: DropdownContentProps) {
  const context = React.useContext(DropdownContext)
  if (!context) throw new Error("DropdownContent must be used within Dropdown")

  if (!context.isOpen) return null

  const alignClass = align === "left" ? "left-0" : "right-0"

  return (
    <div
      className={`absolute ${alignClass} z-50 mt-2 w-56 origin-top-right rounded-xl border border-border bg-popover p-1.5 text-foreground shadow-lg backdrop-blur-md ring-1 ring-black/5 dark:ring-white/10 focus:outline-none dropdown-menu-content ${className}`}
      {...props}
    >
      {children}
    </div>
  )
}

export interface DropdownItemProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  children: React.ReactNode
  onClick?: () => void
  className?: string
  disabled?: boolean
}

export function DropdownItem({
  children,
  onClick,
  className = "",
  disabled = false,
  ...props
}: DropdownItemProps) {
  const context = React.useContext(DropdownContext)
  if (!context) throw new Error("DropdownItem must be used within Dropdown")

  return (
    <button
      type="button"
      onClick={() => {
        if (!disabled && onClick) {
          onClick()
          context.setIsOpen(false)
        }
      }}
      disabled={disabled}
      className={`flex w-full items-center rounded-lg px-2.5 py-2 text-left text-sm font-medium hover:bg-accent hover:text-accent-foreground transition-all duration-150 disabled:pointer-events-none disabled:opacity-50 min-h-0 border-none shadow-none text-foreground bg-transparent ${className}`}
      {...props}
    >
      {children}
    </button>
  )
}
