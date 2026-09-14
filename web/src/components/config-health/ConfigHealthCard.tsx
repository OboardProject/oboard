import { configHealthHeadline, shouldShowConfigHealthCard, type ConfigHealthSummary } from '../../config-health'

export interface ConfigHealthCardProps {
  summary: ConfigHealthSummary
  onOpen: () => void
}

// ConfigHealthCard renders nothing at all when the configuration is clean.
// A permanent "all good" badge would sit in the same spot as the real warning
// and train operators to skip over it.
export function ConfigHealthCard({ summary, onOpen }: ConfigHealthCardProps) {
  if (!shouldShowConfigHealthCard(summary)) return null
  const tone = summary.blocking > 0 ? 'danger' : summary.warning > 0 ? 'warning' : 'neutral'
  return (
    <section className={`config-health-card tone-${tone}`} aria-label="配置体检">
      <div className="config-health-card-copy">
        <div className="config-health-card-head">
          <strong>配置存在 {summary.total} 项问题</strong>
          <span className={`config-health-severity severity-${summary.blocking > 0 ? 'blocking' : summary.warning > 0 ? 'warning' : 'notice'}`}>
            {summary.blocking > 0 ? '需要处理' : summary.warning > 0 ? '建议处理' : '待规范化'}
          </span>
        </div>
        <p>{configHealthHeadline(summary)}</p>
        <small>
          {summary.blocking > 0
            ? '这些配置会让相关服务器的下发失败，普通表单无法保存修正。'
            : '这些配置可以正常下发，但与拓扑显示的行为不一致。'}
        </small>
      </div>
      <button type="button" onClick={onOpen}>查看并清理 →</button>
    </section>
  )
}
