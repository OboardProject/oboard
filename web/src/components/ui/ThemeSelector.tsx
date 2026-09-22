import { Monitor, Moon, Sun } from 'lucide-react'
import { type ThemeOrigin, type ThemePreference, resolveThemeOrigin, getThemeMode, isGlassTheme, withThemeStyle } from '../../theme'

export function ThemeSelector({ value, onChange, variant }: {
  value: ThemePreference
  onChange: (value: ThemePreference, origin: ThemeOrigin) => void
  variant: 'sidebar' | 'hero' | 'login'
}) {
  const mode = getThemeMode(value)
  const isAuto = mode === 'auto'
  const isLight = mode === 'light'
  const isDark = !isAuto && !isLight

  const Icon = isAuto ? Monitor : isLight ? Sun : Moon
  const label = isAuto ? '自动模式' : isLight ? '浅色模式' : '暗黑模式'
  const next: ThemePreference = isAuto ? 'dark' : isDark ? 'light' : 'auto'
  const nextLabel = next === 'auto' ? '自动模式' : next === 'dark' ? '暗黑模式' : '浅色模式'
  const className = variant === 'sidebar' ? 'sidebar-footer-btn' : variant === 'hero' ? 'login-ghost-link' : 'login-theme-inline'

  return <button
    type="button"
    className={className}
    aria-label={`当前${label}${isAuto ? '（跟随系统）' : ''}；点击切换为${nextLabel}`}
    title={`${label}${isAuto ? '（跟随系统）' : ''} · 点击切换为${nextLabel}`}
    onClick={event => onChange(withThemeStyle(next, isGlassTheme(value)), resolveThemeOrigin(event))}
  >
    <Icon size={variant === 'sidebar' ? 16 : 14} aria-hidden="true" />
    <span>{label}</span>
  </button>
}

