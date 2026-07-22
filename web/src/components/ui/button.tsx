import * as React from "react"

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "default" | "destructive" | "outline" | "secondary" | "ghost" | "link"
  size?: "default" | "sm" | "lg" | "icon"
  busy?: boolean
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className = "", variant = "default", size = "default", busy = false, disabled, children, ...props }, ref) => {
    const baseStyles = "ui-btn"
    const variants = {
      default: "ui-btn-primary",
      destructive: "ui-btn-danger",
      outline: "ui-btn-outline",
      secondary: "ui-btn-secondary",
      ghost: "ui-btn-ghost ghost",
      link: "ui-btn-link",
    }
    const sizes = {
      default: "ui-btn-md",
      sm: "ui-btn-sm",
      lg: "ui-btn-lg",
      icon: "ui-btn-icon",
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
