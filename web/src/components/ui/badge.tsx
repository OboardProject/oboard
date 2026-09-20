import * as React from "react"

export interface BadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: "default" | "secondary" | "destructive" | "outline" | "success" | "warning" | "soft"
}

export function Badge({ className = "", variant = "soft", ...props }: BadgeProps) {
  return (
    <div data-slot="badge" data-variant={variant} className={`ui-badge ${className}`.trim()} {...props} />
  )
}
