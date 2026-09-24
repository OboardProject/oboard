import { useState } from 'react'
import { Check, RotateCcw } from 'lucide-react'
import { SettingsGroup } from './SettingsLayout'
import {
  ACCENT_COLOR_PRESETS,
  DEFAULT_ACCENT_COLOR,
  applyAccentColorToDocument,
  getAccentColor,
  saveAccentColor,
} from '../../theme'

export interface AppearanceSettingsPanelProps {
  accentColor?: string
  onAccentColorChange?: (color: string) => void
  notify?: (message: string, tone?: 'success' | 'error' | 'warning' | 'info') => void
}

export function AppearanceSettingsPanel({
  accentColor: controlledAccent,
  onAccentColorChange,
  notify,
}: AppearanceSettingsPanelProps) {
  const [internalAccent, setInternalAccent] = useState<string>(() => controlledAccent ?? getAccentColor())
  const currentAccent = controlledAccent ?? internalAccent

  const handleAccentChange = (color: string) => {
    setInternalAccent(color)
    saveAccentColor(color)
    applyAccentColorToDocument(color)
    onAccentColorChange?.(color)
    notify?.('已应用强调色', 'success')
  }

  return (
    <section id="settings-panel-appearance" className="settings-card">
      <SettingsGroup title="强调色" description="自定义全局主按钮、激活指示器与交互高亮的主题颜色。">
        <div className="accent-color-picker-wrap">
          <div className="accent-color-presets" role="radiogroup" aria-label="强调色预设">
            {ACCENT_COLOR_PRESETS.map(preset => {
              const isSelected = currentAccent.toLowerCase() === preset.color.toLowerCase()
              return (
                <button
                  key={preset.color}
                  type="button"
                  className={`accent-color-circle${isSelected ? ' active' : ''}`}
                  role="radio"
                  aria-checked={isSelected}
                  aria-label={preset.name}
                  title={`${preset.name} (${preset.color})`}
                  style={{ backgroundColor: preset.color }}
                  onClick={() => handleAccentChange(preset.color)}
                >
                  {isSelected && <Check size={14} className="accent-check-icon" />}
                </button>
              )
            })}
          </div>
          <div className="accent-custom-row">
            <label className="accent-custom-label">
              <span className="accent-custom-preview" style={{ backgroundColor: currentAccent }}>
                <input
                  type="color"
                  value={currentAccent}
                  onChange={e => handleAccentChange(e.target.value)}
                  className="accent-color-native-input"
                  aria-label="自定义强调色拾色器"
                />
              </span>
              <span>自定义颜色</span>
              <span className="accent-custom-hex">{currentAccent.toUpperCase()}</span>
            </label>
            {currentAccent.toLowerCase() !== DEFAULT_ACCENT_COLOR.toLowerCase() && (
              <button
                type="button"
                className="ghost ui-btn-ghost accent-reset-btn"
                onClick={() => handleAccentChange(DEFAULT_ACCENT_COLOR)}
                title="恢复默认蓝色"
              >
                <RotateCcw size={13} />
                <span>恢复默认蓝色</span>
              </button>
            )}
          </div>
        </div>
      </SettingsGroup>
    </section>
  )
}
