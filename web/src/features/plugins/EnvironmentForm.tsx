import React, { useEffect, useId, useState } from 'react'
import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { Button } from '../../components/ui/button'
import { Input } from '../../components/ui/input'
import { Select } from '../../components/ui/select'
import { Switch } from '../../components/ui/switch'
import { SearchableMultiSelect } from '../../components/ui/SearchableMultiSelect'
import { envTypeLabels, fieldActive } from './domain'
import type { CustomVar, EnvField, EnvType, ServerOption } from './types'

export interface SecretSlot { configured: boolean; updated_at?: string }

interface FieldInputProps {
  field: EnvField
  value: unknown
  onChange: (value: unknown) => void
  servers: ServerOption[]
  secret?: SecretSlot
  onSetSecret?: (name: string) => void
  invalid?: boolean
  describedBy?: string
  id: string
  disabled?: boolean
}

function serverChoices(field: EnvField, servers: ServerOption[]) {
  const filter = new Set(field.filter || [])
  return servers.filter(server =>
    (!filter.has('enrolled') || server.enrolled) &&
    (!filter.has('online') || server.online) &&
    (!filter.has('ipv4') || server.ipv4) &&
    (!filter.has('ipv6') || server.ipv6))
}

function serverLabel(server: ServerOption) {
  return `${server.name}${server.online ? '' : '（离线）'}`
}

function JSONInput({ value, onChange, id, invalid, describedBy, disabled }: { value: unknown; onChange: (value: unknown) => void; id: string; invalid?: boolean; describedBy?: string; disabled?: boolean }) {
  const [text, setText] = useState(() => (value === undefined ? '' : JSON.stringify(value, null, 2)))
  const [parseError, setParseError] = useState('')
  useEffect(() => {
    try {
      if (text.trim() && JSON.stringify(JSON.parse(text)) === JSON.stringify(value)) return
    } catch { /* keep the operator's text while it is being edited */ return }
    setText(value === undefined ? '' : JSON.stringify(value, null, 2))
  }, [value])
  return <>
    <textarea
      id={id}
      className="plugin-code-input min-h-28"
      value={text}
      disabled={disabled}
      aria-invalid={invalid || Boolean(parseError) || undefined}
      aria-describedby={describedBy}
      spellCheck={false}
      onChange={event => {
        const next = event.target.value
        setText(next)
        if (!next.trim()) { setParseError(''); onChange(undefined); return }
        try { onChange(JSON.parse(next)); setParseError('') } catch { setParseError('JSON 格式不正确') }
      }}
    />
    {parseError && <p className="plugin-field-error">{parseError}</p>}
  </>
}

export function FieldInput({ field, value, onChange, servers, secret, onSetSecret, invalid, describedBy, id, disabled }: FieldInputProps) {
  const common = { id, disabled, 'aria-invalid': invalid || undefined, 'aria-describedby': describedBy }
  switch (field.type) {
    case 'text':
      return <textarea {...common} className="plugin-code-input min-h-24" placeholder={field.placeholder} value={typeof value === 'string' ? value : ''} onChange={event => onChange(event.target.value)} />
    case 'json':
      return <JSONInput id={id} value={value} onChange={onChange} invalid={invalid} describedBy={describedBy} disabled={disabled} />
    case 'integer':
    case 'number':
      return <Input {...common} type="number" inputMode={field.type === 'integer' ? 'numeric' : 'decimal'} step={field.type === 'integer' ? 1 : 'any'} min={field.min} max={field.max} placeholder={field.placeholder}
        value={typeof value === 'number' ? String(value) : ''}
        onChange={event => onChange(event.target.value === '' ? undefined : Number(event.target.value))} />
    case 'boolean':
      return <Switch id={id} disabled={disabled} checked={Boolean(value)} onChange={onChange} ariaLabel={field.label} />
    case 'select':
      return <Select {...common} aria-label={field.label} value={typeof value === 'string' ? value : ''} placeholder="请选择" onChange={event => onChange(event.target.value || undefined)}>
        <option value="">请选择</option>
        {(field.options || []).map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
      </Select>
    case 'multi_select':
      return <SearchableMultiSelect ariaLabel={field.label} placeholder="选择一项或多项" value={Array.isArray(value) ? value.map(String) : []} onChange={next => onChange(next)}
        options={(field.options || []).map(option => ({ value: option.value, label: option.label }))} />
    case 'server':
      return <Select {...common} aria-label={field.label} value={typeof value === 'string' ? value : ''} placeholder="选择服务器" onChange={event => onChange(event.target.value || undefined)}>
        <option value="">选择服务器</option>
        {serverChoices(field, servers).map(server => <option key={server.id} value={server.id}>{serverLabel(server)}</option>)}
      </Select>
    case 'servers':
      return <SearchableMultiSelect ariaLabel={field.label} placeholder="选择服务器" searchPlaceholder="搜索服务器" value={Array.isArray(value) ? value.map(String) : []} onChange={next => onChange(next)}
        options={serverChoices(field, servers).map(server => ({ value: server.id, label: serverLabel(server), keywords: server.region_code }))} />
    case 'secret':
      return <div className="plugin-secret-slot">
        <KeyRound size={15} aria-hidden="true" />
        <span>{secret?.configured ? '已配置，内容不可查看' : '未配置'}</span>
        {onSetSecret && <Button type="button" size="sm" variant="outline" disabled={disabled} onClick={() => onSetSecret(field.name)}>{secret?.configured ? '更换' : '设置'}</Button>}
      </div>
    default:
      return <Input {...common} type={field.type === 'url' ? 'url' : 'text'} placeholder={field.placeholder || (field.type === 'duration' ? '例如 5m' : undefined)}
        value={typeof value === 'string' ? value : ''} onChange={event => onChange(event.target.value === '' ? undefined : event.target.value)} />
  }
}

function hint(field: EnvField) {
  const parts: string[] = []
  if (field.type === 'duration' && (field.min_duration || field.max_duration)) parts.push(`范围 ${field.min_duration || '—'} ~ ${field.max_duration || '—'}`)
  if ((field.type === 'integer' || field.type === 'number') && (field.min !== undefined || field.max !== undefined)) parts.push(`范围 ${field.min ?? '—'} ~ ${field.max ?? '—'}`)
  if (field.max_length) parts.push(`最多 ${field.max_length} 字符`)
  if (field.max_items) parts.push(`最多 ${field.max_items} 项`)
  if (field.type === 'url' && field.schemes?.length) parts.push(`协议 ${field.schemes.join(' / ')}`)
  return parts.join(' · ')
}

export interface EnvironmentFormProps {
  fields: EnvField[]
  values: Record<string, unknown>
  onChange: (values: Record<string, unknown>) => void
  custom: CustomVar[]
  onCustomChange?: (custom: CustomVar[]) => void
  customTypes?: EnvType[]
  servers: ServerOption[]
  secrets?: Record<string, SecretSlot>
  onSetSecret?: (name: string) => void
  issues?: Record<string, string>
  disabled?: boolean
}

export function EnvironmentForm({ fields, values, onChange, custom, onCustomChange, customTypes = [], servers, secrets = {}, onSetSecret, issues = {}, disabled }: EnvironmentFormProps) {
  const base = useId()
  const active = fields.filter(field => fieldActive(field, values, fields))
  const setValue = (name: string, value: unknown) => {
    const next = { ...values }
    if (value === undefined) delete next[name]
    else next[name] = value
    onChange(next)
  }
  return <div className="plugin-env-form">
    {!fields.length && !onCustomChange && <p className="text-sm text-muted-foreground">此插件不需要环境变量。</p>}
    {active.map(field => {
      const id = `${base}-${field.name}`
      const errorID = `${id}-error`
      const hintText = hint(field)
      const error = issues[field.name]
      const current = values[field.name] !== undefined ? values[field.name] : field.default
      return <div key={field.name} className="plugin-env-field">
        <label htmlFor={id} className="plugin-env-label">
          <span>{field.label}{field.required && <span className="plugin-required" aria-hidden="true">*</span>}</span>
          <code>{field.name}</code>
        </label>
        {field.description && <p className="plugin-env-help">{field.description}</p>}
        <FieldInput id={id} field={field} value={current} onChange={value => setValue(field.name, value)} servers={servers} secret={secrets[field.name]}
          onSetSecret={onSetSecret} invalid={Boolean(error)} describedBy={error ? errorID : undefined} disabled={disabled} />
        {hintText && !error && <p className="plugin-env-help">{hintText}</p>}
        {error && <p id={errorID} className="plugin-field-error" role="alert">{error}</p>}
      </div>
    })}
    {onCustomChange && <CustomVariables custom={custom} onChange={onCustomChange} types={customTypes} servers={servers} secrets={secrets} onSetSecret={onSetSecret} issues={issues} disabled={disabled} reserved={fields.map(field => field.name)} />}
  </div>
}

function CustomVariables({ custom, onChange, types, servers, secrets, onSetSecret, issues, disabled, reserved }: {
  custom: CustomVar[]; onChange: (custom: CustomVar[]) => void; types: EnvType[]; servers: ServerOption[]
  secrets: Record<string, SecretSlot>; onSetSecret?: (name: string) => void; issues: Record<string, string>; disabled?: boolean; reserved: string[]
}) {
  const base = useId()
  const update = (index: number, patch: Partial<CustomVar>) => onChange(custom.map((item, i) => (i === index ? { ...item, ...patch } : item)))
  const nameError = (name: string) => {
    if (!name) return ''
    if (!/^[A-Z][A-Z0-9_]{0,63}$/.test(name)) return '名称需以大写字母开头，只含大写字母、数字与下划线'
    if (name.startsWith('OBOARD_')) return 'OBOARD_ 前缀为系统保留'
    if (reserved.includes(name) || custom.filter(item => item.name === name).length > 1) return '名称与已有变量重复'
    return ''
  }
  return <section className="plugin-custom-vars" aria-label="自定义变量">
    <div className="plugin-section-head">
      <div>
        <h4>自定义变量</h4>
        <p className="plugin-env-help">插件通过 env.get('名称') 读取。服务器类变量只是配置，仍需管理员授权。</p>
      </div>
      <Button type="button" size="sm" variant="outline" disabled={disabled || custom.length >= 32} onClick={() => onChange([...custom, { name: '', type: 'string' }])}><Plus size={14} />添加</Button>
    </div>
    {custom.map((item, index) => {
      const field: EnvField = { name: item.name, type: item.type, label: item.name || '新变量' }
      const localError = nameError(item.name)
      const serverError = item.name ? issues[item.name] || issues[`custom.${index}`] : issues[`custom.${index}`]
      return <div key={index} className="plugin-custom-row">
        <div className="plugin-custom-row-head">
          <Input aria-label="变量名" placeholder="VARIABLE_NAME" value={item.name} disabled={disabled} aria-invalid={Boolean(localError) || undefined}
            onChange={event => update(index, { name: event.target.value.toUpperCase() })} />
          <Select aria-label="变量类型" value={item.type} disabled={disabled} onChange={event => update(index, { type: event.target.value as EnvType, value: undefined })}>
            {types.map(type => <option key={type} value={type}>{envTypeLabels[type] || type}</option>)}
          </Select>
          <Button type="button" size="icon" variant="ghost" aria-label={`删除变量 ${item.name || index + 1}`} disabled={disabled} onClick={() => onChange(custom.filter((_, i) => i !== index))}><Trash2 size={15} /></Button>
        </div>
        <FieldInput id={`${base}-${index}`} field={field} value={item.value} onChange={value => update(index, { value })} servers={servers}
          secret={secrets[item.name]} onSetSecret={item.name && !localError ? onSetSecret : undefined} disabled={disabled} invalid={Boolean(serverError)} />
        {(localError || serverError) && <p className="plugin-field-error" role="alert">{localError || serverError}</p>}
      </div>
    })}
  </section>
}
