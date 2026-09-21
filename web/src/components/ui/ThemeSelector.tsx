import { LayoutDashboard, Sparkles } from 'lucide-react'
import { type ThemeOrigin, type ThemePreference, resolveThemeOrigin } from '../../theme'

export function ThemeSelector({ value, onChange, variant }: {
  value: ThemePreference
  onChange: (value: ThemePreference, origin: ThemeOrigin) => void
  variant: 'sidebar' | 'hero' | 'login'
}) {
  const isGlass = value === 'glass'
  const Icon = isGlass ? Sparkles : LayoutDashboard
  const label = isGlass ? '通透主题' : '标准主题'
  const next: ThemePreference = isGlass ? 'dark' : 'glass'
  const nextLabel = isGlass ? '标准' : '通透'
  const className = variant === 'sidebar' ? 'sidebar-footer-btn' : variant === 'hero' ? 'login-ghost-link' : 'login-theme-inline'

  return <button
    type="button"
    className={className}
    aria-label={`当前${label}；点击切换为${nextLabel}主题`}
    title={`${label} · 切换为${nextLabel}主题`}
    onClick={event => onChange(next, resolveThemeOrigin(event))}
  >
    <Icon size={variant === 'sidebar' ? 16 : 14} aria-hidden="true" />
    <span>{label}</span>
  </button>
}
