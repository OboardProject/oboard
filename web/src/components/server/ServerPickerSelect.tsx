import { useMemo, useState } from 'react'
import { Search } from 'lucide-react'
import { CustomSelect } from '../ui/CustomSelect'
import { RegionFlag, regionLabel, serverRegionCode } from '../ui/RegionFlag'
import { orderServerRegions } from '../../region-order'

type PickerServer = { id: number; name: string; status?: string; region_mode?: string; region_code?: string; detected_region_code?: string }

export function ServerPickerSelect({ servers, value, onChange, ariaLabel, placeholder = '选择服务器', required, disabled }: {
  servers: PickerServer[]
  value: number
  onChange: (serverID: number) => void
  ariaLabel: string
  placeholder?: string
  required?: boolean
  disabled?: boolean
}) {
  const [query, setQuery] = useState('')
  const [region, setRegion] = useState('all')
  const regions = useMemo(() => orderServerRegions(servers, regionLabel), [servers])
  const normalizedQuery = query.trim().toLocaleLowerCase('zh-CN')
  const filtered = servers.filter(server => {
    const code = serverRegionCode(server)
    if (region !== 'all' && code !== region) return false
    if (!normalizedQuery) return true
    return [server.name, `server-${server.id}`, code, regionLabel(code)]
      .some(item => String(item || '').toLocaleLowerCase('zh-CN').includes(normalizedQuery))
  })
  const label = (server: PickerServer) => {
    const online = String(server.status || '').toLowerCase() === 'online'
    return <div className="entry-server-option-label">
      <span className={`entry-server-status ${online ? 'online' : 'offline'}`} aria-label={online ? '在线' : '离线'} />
      <RegionFlag code={serverRegionCode(server)} size={20} />
      <span className="entry-server-name">{server.name || `server-${server.id}`}</span>
    </div>
  }
  const selected = servers.find(server => server.id === value)
  const header = <div className="entry-server-menu-tools">
    <label className="entry-server-search">
      <Search size={15} aria-hidden="true" />
      <input autoFocus type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索服务器或地区" aria-label={`搜索${ariaLabel}`} />
    </label>
    {regions.length > 1 && <div className="entry-server-region-filters" role="group" aria-label={`按地区筛选${ariaLabel}`}>
      <button type="button" className={region === 'all' ? 'selected' : ''} aria-pressed={region === 'all'} onClick={() => setRegion('all')}>全部</button>
      {regions.map(({ code, count }) => <button
        key={code || 'pending'}
        type="button"
        className={region === code ? 'selected' : ''}
        aria-pressed={region === code}
        onClick={() => setRegion(code)}
        title={`${regionLabel(code)}，${count} 台服务器`}
      >
        <RegionFlag code={code} size={16} />
        <span>{code || '待检测'}</span>
      </button>)}
    </div>}
  </div>
  return <CustomSelect
    className="server-picker-select"
    value={value ? String(value) : ''}
    onChange={next => {
      onChange(Number(next))
      setQuery('')
      setRegion('all')
    }}
    options={filtered.map(server => ({ value: String(server.id), label: label(server) }))}
    selectedLabel={selected ? label(selected) : undefined}
    placeholder={placeholder}
    menuHeader={header}
    menuMinWidth={320}
    emptyMessage="没有匹配的服务器"
    ariaLabel={ariaLabel}
    required={required}
    disabled={disabled}
  />
}
