import { useRef, type ReactNode } from 'react'
import { ChevronDown } from 'lucide-react'
import { Switch } from '../ui/switch'


type SettingsGroupProps = {
  title: string
  description?: string
  actions?: ReactNode
  children: ReactNode
  className?: string
}

export function SettingsGroup({ title, description, actions, children, className = '' }: SettingsGroupProps) {
  return (
    <section className={`settings-group${className ? ` ${className}` : ''}`}>
      <header className="settings-group-head">
        <div>
          <h3>{title}</h3>
          {description && <p>{description}</p>}
        </div>
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
  const copy = (
    <>
      <strong>{label}</strong>
      {description && <span>{description}</span>}
    </>
  )
  return (
    <div className={`settings-row${className ? ` ${className}` : ''}`}>
      {htmlFor
        ? <label className="settings-row-copy" htmlFor={htmlFor}>{copy}</label>
        : <div className="settings-row-copy">{copy}</div>}
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
    if (!details || typeof details.animate !== 'function') return

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
          {description && <small>{description}</small>}
        </span>
        {summary && <span className="settings-disclosure-summary">{summary}</span>}
        <ChevronDown size={16} className="settings-disclosure-chevron" aria-hidden="true" />
      </summary>
      <div className="settings-disclosure-body">{children}</div>
    </details>
  )
}
