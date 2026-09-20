import * as React from 'react'

export interface SignalRingProps extends React.SVGProps<SVGSVGElement> {
  size?: number
  className?: string
  ariaLabel?: string
}

/**
 * SignalRing: A calm, precision brand glyph derived from letter 'O'.
 * A thin-line broken circular ring with a small terracotta accent arc near the break.
 * Static, non-rotating, non-pulsing.
 */
export function SignalRing({
  size = 18,
  className = '',
  ariaLabel = 'Oboard Signal Ring',
  ...props
}: SignalRingProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      className={`signal-ring ${className}`}
      role="img"
      aria-label={ariaLabel}
      {...props}
    >
      {/* Neutral base path: circle with a 45-degree gap at top right */}
      <path
        d="M 12 3 A 9 9 0 1 0 20.5 8.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        className="signal-ring-base"
        style={{ opacity: 0.65 }}
      />
      {/* Precision terracotta accent: short accent segment terminating right at the break */}
      <path
        d="M 15.5 3.6 A 9 9 0 0 1 20.5 8.5"
        stroke="var(--color-accent, #c4552d)"
        strokeWidth="1.75"
        strokeLinecap="round"
        className="signal-ring-accent"
      />
    </svg>
  )
}
