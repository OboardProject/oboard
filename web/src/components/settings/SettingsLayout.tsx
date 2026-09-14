import { useRef, type ReactNode } from 'react'
import { ChevronDown } from 'lucide-react'
import { Switch } from '../ui/switch'
import { FieldHelp } from '../ui/form-field'


type SettingsGroupProps = {
  title: string
  description?: string
  collapsible?: boolean
  defaultOpen?: boolean
  actions?: ReactNode
  children: ReactNode
  className?: string
}

export function SettingsGroup({ title, description, actions, children, collapsible = false, defaultOpen = false, className = '' }: SettingsGroupProps) {
  const heading = <div className="settings-heading"><h3>{title}</h3>{description && <FieldHelp label={title} hint={description} placement="bottom" />}</div>
  if (collapsible) return (
    <section className={`settings-group${className ? ` ${className}` : ''}`}>
      <SettingsDisclosure title={title} description={description} summary={actions} defaultOpen={defaultOpen}>
        {children}
      </SettingsDisclosure>
    </section>
  )
  return (
    <section className={`settings-group${className ? ` ${className}` : ''}`}>
      <header className="settings-group-head">
        {heading}
        {actions && <div className="settings-group-actions">{actions}</div>}
      </header>
      <div className="settings-group-body">{children}</div>
    </section>
  )
}

type SettingsRowProps = {
  label: ReactNode
  description?: ReactNode
  children: ReactNode
  htmlFor?: string
  className?: string
}

export function SettingsRow({ label, description, children, htmlFor, className = '' }: SettingsRowProps) {
  return (
    <div className={`settings-row${className ? ` ${className}` : ''}`}>
      <div className="settings-row-copy settings-heading">
        {htmlFor ? <label htmlFor={htmlFor}><strong>{label}</strong></label> : <strong>{label}</strong>}
        {description && <FieldHelp label={typeof label === 'string' ? label : '设置项'} hint={description} placement="bottom" />}
      </div>
      <div className="settings-row-control">{children}</div>
    </div>
  )
}

type SettingsSwitchRowProps = {
  label: ReactNode
  description?: ReactNode
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
  ariaLabel: string
  describedBy?: string
}

export function SettingsSwitchRow({ label, description, checked, onChange, disabled, ariaLabel, describedBy }: SettingsSwitchRowProps) {
  return (
    <SettingsRow label={label} description={description}>
      <span className="settings-switch-state">{checked ? '已开启' : '已关闭'}</span>
      <Switch checked={checked} onChange={onChange} disabled={disabled} ariaLabel={ariaLabel} aria-describedby={describedBy} />
    </SettingsRow>
  )
}

type SettingsDisclosureProps = {
  title: string
  description?: string
  summary?: ReactNode
  children: ReactNode
  defaultOpen?: boolean
  className?: string
}

export function SettingsDisclosure({ title, description, summary, children, defaultOpen = false, className = '' }: SettingsDisclosureProps) {
  const detailsRef = useRef<HTMLDetailsElement>(null)
  const isAnimating = useRef(false)

  const handleSummaryClick = (e: React.MouseEvent<HTMLElement>) => {
    const details = detailsRef.current
    if (!details || typeof details.animate !== 'function' || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return

    e.preventDefault()
    if (isAnimating.current) return

    const summaryEl = details.querySelector('summary')
    const bodyEl = details.querySelector('.settings-disclosure-body') as HTMLElement | null
    const summaryHeight = summaryEl ? summaryEl.offsetHeight : 54

    if (details.open) {
      isAnimating.current = true
      const startHeight = details.offsetHeight
      const endHeight = summaryHeight

      const animation = details.animate(
        [
          { height: `${startHeight}px`, overflow: 'hidden' },
          { height: `${endHeight}px`, overflow: 'hidden' },
        ],
        { duration: 220, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' },
      )

      animation.onfinish = () => {
        details.open = false
        isAnimating.current = false
      }
    } else {
      details.open = true
      isAnimating.current = true
      const bodyHeight = bodyEl ? bodyEl.offsetHeight : 0
      const startHeight = summaryHeight
      const endHeight = summaryHeight + bodyHeight

      const animation = details.animate(
        [
          { height: `${startHeight}px`, overflow: 'hidden' },
          { height: `${endHeight}px`, overflow: 'hidden' },
        ],
        { duration: 250, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' },
      )

      animation.onfinish = () => {
        isAnimating.current = false
      }
    }
  }

  return (
    <details ref={detailsRef} className={`settings-disclosure${className ? ` ${className}` : ''}`} open={defaultOpen || undefined}>
      <summary onClick={handleSummaryClick}>
        <span className="settings-disclosure-copy">
          <strong>{title}</strong>
          {description && <FieldHelp label={title} hint={description} placement="bottom" />}
        </span>
        {summary && <span className="settings-disclosure-summary">{summary}</span>}
        <ChevronDown size={16} className="settings-disclosure-chevron" aria-hidden="true" />
      </summary>
      <div className="settings-disclosure-body">{children}</div>
    </details>
  )
}
