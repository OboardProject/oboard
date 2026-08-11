import * as React from "react"

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "default" | "destructive" | "outline" | "secondary" | "ghost" | "link"
  size?: "default" | "sm" | "lg" | "icon"
  busy?: boolean
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className = "", variant = "default", size = "default", busy = false, disabled, children, ...props }, ref) => {
    const baseStyles = "relative isolate inline-flex shrink-0 transform-gpu cursor-pointer items-center justify-center font-medium whitespace-nowrap outline-offset-1 transition duration-250 ease-[cubic-bezier(0.175,0.885,0.32,1.5)] select-none active:scale-[0.97] active:translate-y-px disabled:opacity-50 disabled:pointer-events-none ui-btn"
    const variants = {
      default: "ui-btn-primary bg-primary text-primary-foreground hover:bg-primary/90 shadow-sm",
      destructive: "ui-btn-danger bg-danger text-white hover:bg-danger/90 shadow-sm",
      outline: "ui-btn-outline border border-border bg-background/80 hover:bg-background-muted text-foreground-strong backdrop-blur-sm",
      secondary: "ui-btn-secondary bg-secondary text-secondary-foreground hover:bg-secondary/80",
      ghost: "ui-btn-ghost ghost hover:bg-background-muted text-foreground-strong",
      link: "ui-btn-link text-primary underline-offset-4 hover:underline",
    }
    const sizes = {
      default: "ui-btn-md h-9 px-4 py-2 text-sm rounded-lg",
      sm: "ui-btn-sm h-8 px-3 text-xs rounded-md",
      lg: "ui-btn-lg h-11 px-6 text-base rounded-xl",
      icon: "ui-btn-icon size-9 p-0 rounded-lg",
    }

    return (
      <button
        className={`${baseStyles} ${variants[variant]} ${sizes[size]} ${className}`.trim()}
        ref={ref}
        disabled={disabled || busy}
        aria-busy={busy || undefined}
        {...props}
      >
        {children}
      </button>
    )
  }
)
Button.displayName = "Button"
