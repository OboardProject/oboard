import { useState } from 'react'
import { Check, LayoutDashboard, RotateCcw, Sparkles } from 'lucide-react'
import { SettingsGroup } from './SettingsLayout'
import {
  ACCENT_COLOR_PRESETS,
  DEFAULT_ACCENT_COLOR,
  applyAccentColorToDocument,
  getAccentColor,
  resolveThemeOrigin,
  saveAccentColor,
  type ThemeOrigin,
  type ThemePreference,
} from '../../theme'

export interface AppearanceSettingsPanelProps {
  theme: ThemePreference
  accentColor?: string
  onThemeChange?: (theme: ThemePreference, origin: ThemeOrigin) => void
  onAccentColorChange?: (color: string) => void
  notify?: (message: string, tone?: 'success' | 'error' | 'warning' | 'info') => void
}

export function AppearanceSettingsPanel({
  theme,
  accentColor: controlledAccent,
  onThemeChange,
  onAccentColorChange,
  notify,
}: AppearanceSettingsPanelProps) {
  const isGlass = theme === 'glass'
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
      <SettingsGroup title="界面主题" description="面板提供标准与通透两款全局外观主题。切换后立即对全局所有页面与组件生效。">
        <div className="theme-picker-grid" role="radiogroup" aria-label="主题风格选择">
          <button
            type="button"
            className={`theme-card-option${!isGlass ? ' active' : ''}`}
            role="radio"
            aria-checked={!isGlass}
            aria-label="标准主题"
            onClick={e => {
              onThemeChange?.('dark', resolveThemeOrigin(e))
              notify?.('已应用标准主题', 'success')
            }}
          >
            <div className="theme-preview-box theme-preview-standard" aria-hidden="true">
              <div className="preview-mock-window">
                <div className="preview-mock-bar">
                  <span className="preview-dot dot-red" />
                  <span className="preview-dot dot-amber" />
                  <span className="preview-dot dot-green" />
                </div>
                <div className="preview-mock-body">
                  <div className="preview-mock-sidebar" />
                  <div className="preview-mock-content">
                    <span className="preview-mock-pill" />
                    <span className="preview-mock-pill pill-sm" />
                  </div>
                </div>
              </div>
            </div>
            <div className="theme-card-header">
              <div className="theme-card-title-wrap">
                <LayoutDashboard size={18} className="theme-card-icon" />
                <strong>标准主题</strong>
              </div>
              {!isGlass ? <span className="status-pill ok">当前生效中</span> : <span className="status-pill">点击应用</span>}
            </div>
            <p className="theme-card-desc">经典实色面板质感，高对比度边框与深邃沉稳背景，利落清晰，适合专注运维管控。</p>
          </button>

          <button
            type="button"
            className={`theme-card-option${isGlass ? ' active' : ''}`}
            role="radio"
            aria-checked={isGlass}
            aria-label="通透主题"
            onClick={e => {
              onThemeChange?.('glass', resolveThemeOrigin(e))
              notify?.('已应用通透主题', 'success')
            }}
          >
            <div className="theme-preview-box theme-preview-glass" aria-hidden="true">
              <div className="preview-mock-window">
                <div className="preview-mock-bar">
                  <span className="preview-dot dot-red" />
                  <span className="preview-dot dot-amber" />
                  <span className="preview-dot dot-green" />
                </div>
                <div className="preview-mock-body">
                  <div className="preview-mock-sidebar glass-element" />
                  <div className="preview-mock-content">
                    <span className="preview-mock-pill glass-pill" />
                    <span className="preview-mock-pill pill-sm glass-pill" />
                  </div>
                </div>
              </div>
            </div>
            <div className="theme-card-header">
              <div className="theme-card-title-wrap">
                <Sparkles size={18} className="theme-card-icon text-cyan" />
                <strong>通透主题</strong>
              </div>
              {isGlass ? <span className="status-pill ok">当前生效中</span> : <span className="status-pill">点击应用</span>}
            </div>
            <p className="theme-card-desc">液态玻璃流体质感，全景光学折射、多重背景虚化、菲涅尔高光与极光氛围，极致沉浸。</p>
          </button>
        </div>
      </SettingsGroup>

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
