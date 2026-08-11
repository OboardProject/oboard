import * as React from "react"

export interface BadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: "default" | "secondary" | "destructive" | "outline" | "success" | "warning" | "soft"
}

export function Badge({ className = "", variant = "soft", ...props }: BadgeProps) {
  const baseStyles = "relative isolate inline-flex shrink-0 items-center justify-center rounded-full whitespace-nowrap outline-offset-1 select-none font-medium px-3 py-0.5 text-xs transition duration-250 ease-[cubic-bezier(0.175,0.885,0.32,1.5)] active:scale-[0.97] active:translate-y-px"

  const variants = {
    default: "bg-primary text-primary-foreground border border-transparent shadow-xs",
    secondary: "bg-secondary text-secondary-foreground border border-transparent",
    soft: "bg-background-muted/80 text-foreground-strong border border-border/60 backdrop-blur-md",
    destructive: "bg-danger-soft text-danger border border-danger/20",
    success: "bg-success-soft text-success border border-success/20",
    warning: "bg-warning-soft text-warning border border-warning/20",
    outline: "text-foreground-strong border border-border bg-background/50 backdrop-blur-sm",
  }

  return (
    <div data-slot="badge" data-variant={variant} className={`${baseStyles} ${variants[variant]} ${className}`} {...props} />
  )
}
