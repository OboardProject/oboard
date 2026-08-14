import { ExternalLink, GitCommitHorizontal, Scale } from 'lucide-react'
import logo from '../assets/logo.svg'

export type OBoardVersionInfo = {
  name?: string
  version?: string
  build?: string
  commit?: string
  built_at?: string
  dev?: boolean
  agent_expected_version?: string
  agent_expected_build?: string
  kernel_version?: string
  kernel_build?: string
  kernel?: string
}

const projectURL = 'https://github.com/OboardProject/oboard'

function present(value?: string) {
  const normalized = String(value || '').trim()
  return normalized && normalized !== 'unknown' ? normalized : '未提供'
}

function versionLabel(value?: string) {
  const normalized = String(value || '').trim()
  if (!normalized) return '未提供'
  return normalized.startsWith('v') ? normalized : `v${normalized}`
}

function buildLabel(version?: string, build?: string) {
  const versionText = versionLabel(version)
  const buildText = present(build)
  return buildText === '未提供' ? versionText : `${versionText} · ${buildText}`
}

function formatBuiltAt(value?: string) {
  const normalized = String(value || '').trim()
  if (!normalized || normalized === 'unknown') return '未提供'
  const date = new Date(normalized)
  if (Number.isNaN(date.getTime())) return normalized
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date)
}

export function AboutSettingsPanel({ version = {} }: { version?: OBoardVersionInfo }) {
  const commit = present(version.commit)
  const commitURL = commit === '未提供' ? '' : `${projectURL}/commit/${encodeURIComponent(commit)}`

  return (
    <section
      id="settings-panel-about"
      className="settings-card about-settings"
      role="tabpanel"
      aria-labelledby="settings-tab-about"
      tabIndex={0}
    >
      <header className="about-product">
        <img className="about-product-logo" src={logo} alt="" />
        <div className="about-product-copy">
          <div className="about-product-title">
            <h3>{version.name || 'OBoard'}</h3>
            <span className="about-version-badge">{versionLabel(version.version)}</span>
            {version.dev && <span className="about-dev-badge">开发构建</span>}
          </div>
          <p className="muted">开源的多服务器代理控制、部署与订阅管理项目。</p>
        </div>
      </header>

      <dl className="about-details">
        <div>
          <dt>Controller</dt>
          <dd className="tabular-nums">{buildLabel(version.version, version.build)}</dd>
        </div>
        <div>
          <dt>Agent</dt>
          <dd className="tabular-nums">{buildLabel(version.agent_expected_version, version.agent_expected_build)}</dd>
        </div>
        <div>
          <dt>内核</dt>
          <dd>
            <span className="tabular-nums">{buildLabel(version.kernel_version, version.kernel_build)}</span>
            {version.kernel && <small>{version.kernel}</small>}
          </dd>
        </div>
        <div>
          <dt>提交</dt>
          <dd className="tabular-nums">
            <GitCommitHorizontal size={16} aria-hidden="true" />
            {commitURL
              ? <a href={commitURL} target="_blank" rel="noreferrer">{commit}</a>
              : <span>{commit}</span>}
          </dd>
        </div>
        <div>
          <dt>构建时间</dt>
          <dd className="tabular-nums">{formatBuiltAt(version.built_at)}</dd>
        </div>
        <div>
          <dt>许可证</dt>
          <dd><Scale size={16} aria-hidden="true" />GPL-3.0</dd>
        </div>
      </dl>

      <div className="about-project-link">
        <div>
          <strong>项目地址</strong>
          <span>{projectURL}</span>
        </div>
        <a href={projectURL} target="_blank" rel="noreferrer">
          查看 GitHub
          <ExternalLink size={16} aria-hidden="true" />
        </a>
      </div>
    </section>
  )
}
