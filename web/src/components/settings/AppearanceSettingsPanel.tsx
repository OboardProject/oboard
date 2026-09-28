import { useState } from 'react'
import { Check, RotateCcw } from 'lucide-react'
import { SettingsGroup } from './SettingsLayout'
import {
  ACCENT_COLOR_PRESETS,
  DEFAULT_ACCENT_COLORS,
  accentContrastColor,
  applyAccentColorToDocument,
  getAccentColor,
  saveAccentColor,
  type ThemeName,
} from '../../theme'

export interface AppearanceSettingsPanelProps {
  accentColors?: Partial<Record<ThemeName, string>>
  onAccentColorChange?: (theme: ThemeName, color: string) => void
  notify?: (message: string, tone?: 'success' | 'error' | 'warning' | 'info') => void
}

export function AppearanceSettingsPanel({
  accentColors: controlledAccents,
  onAccentColorChange,
  notify,
}: AppearanceSettingsPanelProps) {
  const [internalAccents, setInternalAccents] = useState<Record<ThemeName, string>>(() => ({
    light: getAccentColor('light'),
    dark: getAccentColor('dark'),
  }))

  const handleAccentChange = (theme: ThemeName, color: string) => {
    setInternalAccents(previous => ({ ...previous, [theme]: color }))
    saveAccentColor(theme, color)
    if (document.documentElement.dataset.theme === theme) applyAccentColorToDocument(color)
    onAccentColorChange?.(theme, color)
    notify?.(`已应用${theme === 'light' ? '浅色' : '深色'}模式强调色`, 'success')
  }

  return (
    <section id="settings-panel-appearance" className="settings-card">
      <SettingsGroup title="强调色" description="分别设置浅色与深色模式的主按钮、交互高亮和拓扑主链路颜色。">
        {(['light', 'dark'] as const).map(theme => {
          const label = theme === 'light' ? '浅色模式' : '深色模式'
          const currentAccent = controlledAccents?.[theme] ?? internalAccents[theme]
          return <div className="accent-theme-group" key={theme}>
            <h3>{label}</h3>
            <div className="accent-color-picker-wrap">
              <div className="accent-color-presets" role="group" aria-label={`${label}强调色预设`}>
                {ACCENT_COLOR_PRESETS[theme].map(preset => {
                  const isSelected = currentAccent.toLowerCase() === preset.color.toLowerCase()
                  return <button
                    key={preset.color}
                    type="button"
                    className={`accent-color-circle${isSelected ? ' active' : ''}`}
                    aria-pressed={isSelected}
                    aria-label={`${label}：${preset.name}`}
                    title={`${preset.name} (${preset.color})`}
                    style={{ backgroundColor: preset.color, color: accentContrastColor(preset.color) }}
                    onClick={() => handleAccentChange(theme, preset.color)}
                  >
                    {isSelected && <Check size={16} className="accent-check-icon" aria-hidden="true" />}
                  </button>
                })}
              </div>
              <div className="accent-custom-row">
                <label className="accent-custom-label">
                  <span className="accent-custom-preview" style={{ backgroundColor: currentAccent }}>
                    <input
                      type="color"
                      value={currentAccent}
                      onChange={e => handleAccentChange(theme, e.target.value)}
                      className="accent-color-native-input"
                      aria-label={`${label}自定义强调色拾色器`}
                    />
                  </span>
                  <span>自定义颜色</span>
                  <span className="accent-custom-hex">{currentAccent.toUpperCase()}</span>
                </label>
                {currentAccent.toLowerCase() !== DEFAULT_ACCENT_COLORS[theme].toLowerCase() && <button
                  type="button"
                  className="ghost ui-btn-ghost accent-reset-btn"
                  onClick={() => handleAccentChange(theme, DEFAULT_ACCENT_COLORS[theme])}
                  title={`恢复${label}默认蓝色`}
                >
                  <RotateCcw size={13} />
                  <span>恢复默认蓝色</span>
                </button>}
              </div>
            </div>
          </div>
        })}
      </SettingsGroup>
    </section>
  )
}
