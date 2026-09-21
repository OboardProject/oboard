import { LayoutDashboard, Sparkles } from 'lucide-react'
import { SettingsGroup } from './SettingsLayout'
import { resolveThemeOrigin, type ThemeOrigin, type ThemePreference } from '../../theme'

export interface AppearanceSettingsPanelProps {
  theme: ThemePreference
  onThemeChange?: (theme: ThemePreference, origin: ThemeOrigin) => void
  notify?: (message: string, tone?: 'success' | 'error' | 'warning' | 'info') => void
}

export function AppearanceSettingsPanel({ theme, onThemeChange, notify }: AppearanceSettingsPanelProps) {
  const isGlass = theme === 'glass'

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
    </section>
  )
}
