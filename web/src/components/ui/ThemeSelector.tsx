import { Monitor, Moon, Sun } from 'lucide-react'
import { type ThemeOrigin, type ThemePreference, resolveThemeOrigin } from '../../theme'

const themeOptions = [
  { value: 'light', label: '浅色模式', Icon: Sun },
  { value: 'auto', label: '自动模式，跟随系统', Icon: Monitor },
  { value: 'dark', label: '暗黑模式', Icon: Moon },
] as const

export function ThemeSelector({ value, onChange, variant }: {
  value: ThemePreference
  onChange: (value: ThemePreference, origin: ThemeOrigin) => void
  variant: 'sidebar' | 'hero' | 'login'
}) {
  return <div className={`theme-selector theme-selector--${variant}`} role="group" aria-label="主题模式" data-value={value}>
    {themeOptions.map(({ value: option, label, Icon }) => (
      <button
        key={option}
        type="button"
        className="theme-selector-option"
        aria-label={label}
        title={label}
        aria-pressed={value === option}
        onClick={event => {
          if (value !== option) onChange(option, resolveThemeOrigin(event))
        }}
      >
        <Icon size={17} aria-hidden="true" />
      </button>
    ))}
  </div>
}
