import * as React from "react"
import { createPopoverPortal } from "./modal-layer"

interface DropdownContextType {
  anchorRef: React.RefObject<HTMLDivElement | null>
  contentRef: React.RefObject<HTMLDivElement | null>
  isOpen: boolean
  setIsOpen: (open: boolean) => void
}

const DropdownContext = React.createContext<DropdownContextType | null>(null)

export function Dropdown({ children }: { children: React.ReactNode }) {
  const [isOpen, setIsOpen] = React.useState(false)
  const ref = React.useRef<HTMLDivElement>(null)
  const contentRef = React.useRef<HTMLDivElement>(null)

  React.useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (ref.current && !ref.current.contains(event.target as Node) && !contentRef.current?.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }
    if (!isOpen) return
    const handleEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setIsOpen(false)
      ref.current?.querySelector<HTMLButtonElement>('button')?.focus()
    }
    document.addEventListener("mousedown", handleClickOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener("mousedown", handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [isOpen])

  return (
    <DropdownContext.Provider value={{ isOpen, setIsOpen, anchorRef: ref, contentRef }}>
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
    "aria-haspopup": "menu",
    "aria-expanded": context.isOpen,
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

  const [position, setPosition] = React.useState<React.CSSProperties>({ visibility: 'hidden' })
  const { isOpen, anchorRef, contentRef } = context
  React.useLayoutEffect(() => {
    if (!isOpen) return
    const place = () => {
      const anchor = anchorRef.current?.getBoundingClientRect()
      const menu = contentRef.current
      if (!anchor || !menu) return
      const width = menu.offsetWidth
      const height = menu.offsetHeight
      setPosition({
        position: 'fixed',
        left: Math.max(8, Math.min(align === 'left' ? anchor.left : anchor.right - width, window.innerWidth - width - 8)),
        top: anchor.bottom + height + 6 <= window.innerHeight - 8 ? anchor.bottom + 6 : Math.max(8, anchor.top - height - 6),
        maxHeight: 'calc(100dvh - 16px)',
        maxWidth: 'calc(100vw - 16px)',
        overflowY: 'auto',
      })
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [isOpen, align, anchorRef, contentRef])
  if (!isOpen) return null

  return createPopoverPortal(
    <div
      ref={contentRef}
      data-popover="true"
      role="menu"
      style={position}
      className={`fixed w-56 origin-top-right rounded-xl border border-border bg-popover p-1.5 text-foreground shadow-lg backdrop-blur-md ring-1 ring-black/5 dark:ring-white/10 focus:outline-none dropdown-menu-content ${className}`}
      {...props}
    >
      {children}
    </div>,
    document.body,
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
      role="menuitem"
      onClick={() => {
        if (!disabled && onClick) {
          onClick()
          context.setIsOpen(false)
        }
      }}
      disabled={disabled}
      className={`dropdown-item flex w-full items-center justify-start rounded-lg px-2.5 py-1.5 text-left text-sm font-medium hover:bg-accent hover:text-accent-foreground transition-all duration-150 disabled:pointer-events-none disabled:opacity-50 min-h-0 border-none shadow-none text-foreground bg-transparent ${className}`}
      {...props}
    >
      {children}
    </button>
  )
}
