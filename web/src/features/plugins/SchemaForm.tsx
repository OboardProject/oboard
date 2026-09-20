import React from 'react'
import { Input } from '../../components/ui/input'
import type { ConfigSchema } from './types'

export function schemaDefaults(schema: ConfigSchema): Record<string, unknown> {
  const result: Record<string, unknown> = {}
  for (const [key, field] of Object.entries(schema.properties || {})) {
    if (field.default !== undefined) result[key] = field.default
    else if (field.type === 'object') result[key] = schemaDefaults(field)
    else if (field.type === 'boolean' && schema.required?.includes(key)) result[key] = false
  }
  return result
}

export function SchemaForm({ schema, value, onChange, prefix = 'config' }: {
  schema: ConfigSchema; value: Record<string, unknown>; onChange: (value: Record<string, unknown>) => void; prefix?: string
}) {
  return <div className="space-y-3">{Object.entries(schema.properties || {}).map(([key, field]) => {
    const id = `${prefix}-${key}`
    const required = schema.required?.includes(key)
    const update = (next: unknown) => onChange({ ...value, [key]: next })
    if (field.type === 'object') return <fieldset key={key} className="rounded-xl border border-border p-3"><legend>{field.title || key}</legend><SchemaForm schema={field} prefix={id} value={(value[key] as Record<string, unknown>) || {}} onChange={update} /></fieldset>
    const current = value[key]
    return <div key={key} className="space-y-1 text-sm">
      <label htmlFor={id}>{field.title || key}{required ? ' *' : ''}</label>
      {field.enum ? <select id={id} className="w-full rounded-xl border border-border bg-background p-2" required={required} value={current === undefined ? '' : String(field.enum.findIndex(item => item === current))} onChange={e => update(e.target.value === '' ? undefined : field.enum![Number(e.target.value)])}>
        <option value="">请选择</option>{field.enum.map((item, index) => <option key={index} value={index}>{String(item)}</option>)}
      </select> : field.type === 'boolean' ? <input id={id} className="ml-2" type="checkbox" checked={current === true} onChange={e => update(e.target.checked)} />
        : field.type === 'array' ? <ArrayField id={id} required={required} value={current} onChange={update} />
          : <Input id={id} required={required} type={field.type === 'number' || field.type === 'integer' ? 'number' : 'text'} step={field.type === 'integer' ? 1 : 'any'} min={field.minimum} max={field.maximum} minLength={field.minLength} maxLength={field.maxLength} pattern={field.pattern} value={current == null ? '' : String(current)} onChange={e => update(e.target.value === '' ? undefined : field.type === 'number' || field.type === 'integer' ? Number(e.target.value) : e.target.value)} />}
      {field.description && <p className="text-xs text-muted-foreground">{field.description}</p>}
    </div>
  })}</div>
}

function ArrayField({ id, required, value, onChange }: { id: string; required?: boolean; value: unknown; onChange: (value: unknown[]) => void }) {
  const [text, setText] = React.useState(JSON.stringify(value ?? []))
  return <Input id={id} value={text} required={required} onChange={e => {
    setText(e.target.value)
    try {
      const next = JSON.parse(e.target.value)
      if (!Array.isArray(next)) throw new Error('请输入数组')
      e.target.setCustomValidity(''); onChange(next)
    } catch { e.target.setCustomValidity('请输入有效的 JSON 数组') }
  }} />
}
