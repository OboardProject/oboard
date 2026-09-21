import { Monitor, Moon, Sparkles, Sun } from 'lucide-react'
import { type ThemeOrigin, type ThemePreference, resolveThemeOrigin } from '../../theme'

export function ThemeSelector({ value, onChange, variant }: {
  value: ThemePreference
  onChange: (value: ThemePreference, origin: ThemeOrigin) => void
  variant: 'sidebar' | 'hero' | 'login'
}) {
  const Icon = value === 'auto' ? Monitor : value === 'dark' ? Moon : value === 'glass' ? Sparkles : Sun
  const label = value === 'auto' ? '自动主题' : value === 'dark' ? '深色主题' : value === 'glass' ? '液态玻璃' : '浅色主题'
  const next = value === 'auto' ? 'dark' : value === 'dark' ? 'glass' : value === 'glass' ? 'light' : 'auto'
  const nextLabel = next === 'auto' ? '自动' : next === 'dark' ? '深色' : next === 'glass' ? '液态玻璃' : '浅色'
  const className = variant === 'sidebar' ? 'sidebar-footer-btn' : variant === 'hero' ? 'login-ghost-link' : 'login-theme-inline'

  return <button
    type="button"
    className={className}
    aria-label={`当前${label}${value === 'auto' ? '，跟随系统' : ''}；点击切换为${nextLabel}主题`}
    title={`${label}${value === 'auto' ? '（跟随系统）' : ''} · 切换为${nextLabel}`}
    onClick={event => onChange(next, resolveThemeOrigin(event))}
  >
    <Icon size={variant === 'sidebar' ? 16 : 14} aria-hidden="true" />
    <span>{label}</span>
  </button>
}
