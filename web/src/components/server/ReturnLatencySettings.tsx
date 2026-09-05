import { useMemo, useState } from 'react'
import { Loader2, Search, X } from 'lucide-react'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import type { Server, LatencyProbeMode, LatencyProbeRegion, ConnectivityProbeTarget } from '../proxy-path/types'

export function ReturnLatencySettings({
  draft,
  serverCount,
  disabled,
  onSave,
  regions,
  loading,
  error,
}: {
  draft: any
  serverCount: number
  disabled?: boolean
  onSave: (patch: Partial<Server>) => void | Promise<void>
  regions: LatencyProbeRegion[]
  loading?: boolean
  error?: string
}) {
  const [values, setValues] = useState({
    latency_probe_enabled: draft.latency_probe_enabled !== false,
    latency_probe_mode: (draft.latency_probe_mode || 'tcp') as LatencyProbeMode,
    latency_probe_public_target: (draft.latency_probe_public_target || 'auto') as ConnectivityProbeTarget,
    latency_probe_interval_seconds: draft.latency_probe_interval_seconds || 60,
    latency_probe_sample_count: draft.latency_probe_sample_count || 3,
    latency_probe_max_targets: draft.latency_probe_max_targets || 64,
  })
  const [selected, setSelected] = useState<LatencyProbeRegion[]>(() =>
    Array.isArray(draft.latency_probe_regions) ? [...draft.latency_probe_regions] : []
  )
  const [searchKeyword, setSearchKeyword] = useState('')
  const [carrierFilter, setCarrierFilter] = useState('all')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const updateParam = (patch: Partial<typeof values>) => setValues(old => ({ ...old, ...patch }))

  const keyOf = (r: LatencyProbeRegion) => `${r.province}\u0000${r.carrier}`
  const availableKeys = useMemo(() => new Set(regions.map(keyOf)), [regions])
  const options = useMemo(() => {
    const map = new Map<string, LatencyProbeRegion>()
    for (const r of regions) map.set(keyOf(r), r)
    for (const r of selected) if (!map.has(keyOf(r))) map.set(keyOf(r), r)
    return Array.from(map.values()).sort((a, b) =>
      a.province.localeCompare(b.province, 'zh-CN') || a.carrier.localeCompare(b.carrier, 'zh-CN')
    )
  }, [regions, selected])

  const selectedKeys = useMemo(() => new Set(selected.map(keyOf)), [selected])

  const carrierOptions = useMemo(() => {
    const carrierSet = new Set(options.map(o => o.carrier))
    const priority = ['中国电信', '中国联通', '中国移动', '中国广电', '教育网']
    return Array.from(carrierSet).sort((a, b) => {
      const ia = priority.indexOf(a)
      const ib = priority.indexOf(b)
      if (ia !== -1 && ib !== -1) return ia - ib
      if (ia !== -1) return -1
      if (ib !== -1) return 1
      return a.localeCompare(b, 'zh-CN')
    })
  }, [options])

  const filteredOptions = useMemo(() => {
    const q = searchKeyword.trim().toLowerCase()
    return options.filter(r => {
      if (carrierFilter !== 'all' && r.carrier !== carrierFilter) return false
      if (!q) return true
      return r.province.toLowerCase().includes(q) || r.carrier.toLowerCase().includes(q)
    })
  }, [options, carrierFilter, searchKeyword])

  const grouped = useMemo(() => {
    return filteredOptions.reduce<Record<string, LatencyProbeRegion[]>>((acc, r) => {
      ;(acc[r.province] ||= []).push(r)
      return acc
    }, {})
  }, [filteredOptions])

  const toggle = (region: LatencyProbeRegion) => {
    const key = keyOf(region)
    if (selectedKeys.has(key)) {
      setSelected(curr => curr.filter(item => keyOf(item) !== key))
    } else {
      setSelected(curr => [...curr, region])
    }
  }

  const toggleProvince = (province: string) => {
    const provinceEntries = filteredOptions.filter(o => o.province === province)
    const allSelected = provinceEntries.length > 0 && provinceEntries.every(e => selectedKeys.has(keyOf(e)))
    if (allSelected) {
      const keysToRemove = new Set(provinceEntries.map(keyOf))
      setSelected(curr => curr.filter(item => !keysToRemove.has(keyOf(item))))
    } else {
      const missing = provinceEntries.filter(e => !selectedKeys.has(keyOf(e)))
      setSelected(curr => [...curr, ...missing])
    }
  }

  const selectAllFiltered = () => {
    const missing = filteredOptions.filter(e => !selectedKeys.has(keyOf(e)))
    setSelected(curr => [...curr, ...missing])
  }

  const deselectAllFiltered = () => {
    const keysToRemove = new Set(filteredOptions.map(keyOf))
    setSelected(curr => curr.filter(item => !keysToRemove.has(keyOf(item))))
  }

  const selectBigThree = () => {
    const bigThree = filteredOptions.filter(o => ['中国电信', '中国联通', '中国移动'].includes(o.carrier))
    const missing = bigThree.filter(e => !selectedKeys.has(keyOf(e)))
    setSelected(curr => [...curr, ...missing])
  }

  const clearAll = () => {
    setSelected([])
  }

  const automaticDomain = '按每台服务器地区自动选择'

  const save = async () => {
    if (saving || disabled || !serverCount || isOverLimit || invalidParams || unavailableCount > 0) return
    setSaving(true)
    setSaveError('')
    try {
      await onSave({
        latency_probe_enabled: values.latency_probe_enabled,
        latency_probe_mode: values.latency_probe_mode,
        latency_probe_public_target: values.latency_probe_public_target,
        latency_probe_interval_seconds: values.latency_probe_interval_seconds,
        latency_probe_sample_count: values.latency_probe_sample_count,
        latency_probe_max_targets: values.latency_probe_max_targets,
        latency_probe_regions: selected,
      })
    } catch (error: any) {
      setSaveError(error?.message || '应用失败，请重试')
    } finally {
      setSaving(false)
    }
  }

  const isFilteredAllSelected = filteredOptions.length > 0 && filteredOptions.every(e => selectedKeys.has(keyOf(e)))
  const totalAvailable = options.length
  const selectedCount = selected.length
  const maxTargets = values.latency_probe_max_targets
  const isOverLimit = selectedCount + 1 > maxTargets

  const unavailableCount = selected.filter(region => !availableKeys.has(keyOf(region))).length
  const invalidParams = ![[values.latency_probe_interval_seconds, 30, 86400], [values.latency_probe_sample_count, 1, 10], [values.latency_probe_max_targets, 1, 256]].every(([value, min, max]) => Number.isInteger(value) && value >= min && value <= max)

  return (
    <section className="return-latency-editor" aria-label="回程延迟配置">
      <header className="return-latency-section-head">
        <h2>2. 配置回程测试</h2>
        <p className="muted">选择省份与运营商，再将同一配置应用到所选服务器。</p>
      </header>
      <fieldset disabled={saving || disabled} className="return-latency-fields">
      <div className="dialog-body latency-dialog-body">
        <div className="latency-params-card">
          <div className="return-latency-section-head">
            <h3>探测参数</h3>
            <label className="return-latency-check"><input type="checkbox" checked={values.latency_probe_enabled} onChange={event => updateParam({ latency_probe_enabled: event.target.checked })} />启用自动测试</label>
          </div>
          <p className="muted">公网目标同时用于断线后的连通性判断；关闭自动测试也会停止该探测。</p>
          <div className="latency-params-grid">
            <FormField label="测试方式" hint="TCP 测试端口连接；ICMP 测试 Echo。">
              <Select
                aria-label="延迟测试方式"
                variant="segmented"
                value={values.latency_probe_mode}
                onChange={event => updateParam({ latency_probe_mode: event.target.value as LatencyProbeMode })}
              >
                <option value="tcp">TCP Ping</option>
                <option value="icmp">ICMP Ping</option>
              </Select>
            </FormField>

            <FormField label="公网目标" hint="断线期间判断公网连通性。">
              <Select
                aria-label="延迟测试公网目标"
                value={values.latency_probe_public_target}
                onChange={event => updateParam({ latency_probe_public_target: event.target.value as ConnectivityProbeTarget })}
              >
                <option value="auto">自动（当前：{automaticDomain}）</option>
                <option value="cloudflare">cp.cloudflare.com</option>
                <option value="12306">www.12306.cn</option>
                <option value="google">www.gstatic.com</option>
              </Select>
            </FormField>

            <FormField label="采样间隔（秒）" hint="自动测试周期（30–86400 秒）。">
              <input
                aria-label="延迟测试采样间隔（秒）"
                type="number"
                min={30}
                max={86400}
                placeholder="60"
                value={values.latency_probe_interval_seconds === '' ? '' : values.latency_probe_interval_seconds}
                onChange={e => updateParam({ latency_probe_interval_seconds: e.target.value === '' ? ('' as any) : Number(e.target.value) })}
                onBlur={e => {
                  const num = Number(e.target.value)
                  if (!e.target.value || isNaN(num) || num < 30) updateParam({ latency_probe_interval_seconds: 30 })
                  else if (num > 86400) updateParam({ latency_probe_interval_seconds: 86400 })
                }}
              />
            </FormField>

            <FormField label="每个目标样本数" hint="连续测试样本数（1–10）。">
              <input
                aria-label="每个延迟目标样本数"
                type="number"
                min={1}
                max={10}
                placeholder="3"
                value={values.latency_probe_sample_count === '' ? '' : values.latency_probe_sample_count}
                onChange={e => updateParam({ latency_probe_sample_count: e.target.value === '' ? ('' as any) : Number(e.target.value) })}
                onBlur={e => {
                  const num = Number(e.target.value)
                  if (!e.target.value || isNaN(num) || num < 1) updateParam({ latency_probe_sample_count: 1 })
                  else if (num > 10) updateParam({ latency_probe_sample_count: 10 })
                }}
              />
            </FormField>

            <FormField label="单次最多目标数" hint="含 1 个公网目标（1–256）。">
              <input
                aria-label="单次最多目标数"
                type="number"
                min={1}
                max={256}
                placeholder="64"
                value={values.latency_probe_max_targets === '' ? '' : values.latency_probe_max_targets}
                onChange={e => updateParam({ latency_probe_max_targets: e.target.value === '' ? ('' as any) : Number(e.target.value) })}
                onBlur={e => {
                  const num = Number(e.target.value)
                  if (!e.target.value || isNaN(num) || num < 1) updateParam({ latency_probe_max_targets: 1 })
                  else if (num > 256) updateParam({ latency_probe_max_targets: 256 })
                }}
              />
            </FormField>
          </div>
        </div>

        <div className="latency-regions-section">
          <div className="latency-regions-header">
            <div className="latency-regions-title-row">
              <h3>回程目标</h3>
              <div className="latency-stats-badge">
                <span>已选 <strong>{selectedCount}</strong> / 可用 {totalAvailable} 个节点</span>
                {selectedCount > 0 && <span>覆盖 <strong>{new Set(selected.map(s => s.province)).size}</strong> 个省份</span>}
              </div>
            </div>
            <div className="latency-toolbar">
              <div className="latency-search-wrap">
                <Search size={14} className="latency-search-icon" aria-hidden="true" />
                <input
                  type="search"
                  aria-label="搜索省份或运营商"
                  placeholder="搜索省份或运营商（如：广东 / 电信）..."
                  value={searchKeyword}
                  onChange={e => setSearchKeyword(e.target.value)}
                  className="latency-search-input"
                />
                {searchKeyword && (
                  <button type="button" className="ghost icon-button latency-search-clear" onClick={() => setSearchKeyword('')} aria-label="清空目标搜索" title="清空搜索">
                    <X size={12} />
                  </button>
                )}
              </div>

              <div className="latency-quick-actions">
                <div className="latency-carrier-filters" role="group" aria-label="运营商筛选">
                  <button
                    type="button"
                    className={`latency-pill-btn ${carrierFilter === 'all' ? 'active' : ''}`}
                    aria-pressed={carrierFilter === 'all'}
                    onClick={() => setCarrierFilter('all')}
                  >
                    全部运营商
                  </button>
                  {carrierOptions.map(c => (
                    <button
                      key={c}
                      type="button"
                      className={`latency-pill-btn ${carrierFilter === c ? 'active' : ''}`}
                      aria-pressed={carrierFilter === c}
                      onClick={() => setCarrierFilter(carrierFilter === c ? 'all' : c)}
                    >
                      {c}
                    </button>
                  ))}
                </div>

                <div className="latency-batch-btns">
                  <button type="button" className="ghost latency-action-btn" onClick={selectBigThree}>
                    选择当前三网
                  </button>
                  <button
                    type="button"
                    className="ghost latency-action-btn"
                    onClick={isFilteredAllSelected ? deselectAllFiltered : selectAllFiltered}
                  >
                    {isFilteredAllSelected ? (searchKeyword || carrierFilter !== 'all' ? '取消当前结果' : '取消全选') : (searchKeyword || carrierFilter !== 'all' ? '选择当前结果' : '选择全部目标')}
                  </button>
                  <button
                    type="button"
                    className="ghost latency-action-btn danger-hover"
                    onClick={clearAll}
                    disabled={selectedCount === 0}
                  >
                    清空已选
                  </button>
                </div>
              </div>
            </div>
          </div>

          {error && <p className="danger-text" role="alert">地区资源列表加载失败：{error}</p>}

          <div className="latency-region-cards-container" aria-busy={loading}>
            {loading && options.length === 0 ? (
              <div className="latency-loading-state">
                <Loader2 className="animate-spin" size={20} />
                <span>正在加载地区目标资源...</span>
              </div>
            ) : Object.keys(grouped).length === 0 ? (
              <div className="latency-empty-state">
                <span>未找到匹配的地区或运营商节点</span>
                {(searchKeyword || carrierFilter !== 'all') && (
                  <button type="button" className="ghost" onClick={() => { setSearchKeyword(''); setCarrierFilter('all') }}>
                    重置筛选条件
                  </button>
                )}
              </div>
            ) : (
              <div className="latency-province-grid">
                {Object.entries(grouped).map(([province, entries]) => {
                  const provinceAllEntries = filteredOptions.filter(o => o.province === province)
                  const provinceSelectedCount = provinceAllEntries.filter(e => selectedKeys.has(keyOf(e))).length
                  const isAllProvinceSelected = provinceAllEntries.length > 0 && provinceSelectedCount === provinceAllEntries.length
                  const hasSelectionInProvince = provinceSelectedCount > 0

                  return (
                    <div key={province} className={`latency-province-card ${hasSelectionInProvince ? 'has-selected' : ''}`}>
                      <div className="latency-province-head">
                        <div className="province-info">
                          <strong>{province}</strong>
                          <span className={`province-count-badge ${hasSelectionInProvince ? 'active' : ''}`}>
                            {provinceSelectedCount}/{provinceAllEntries.length}
                          </span>
                        </div>
                        <button
                          type="button"
                          className="ghost province-toggle-btn"
                          onClick={() => toggleProvince(province)}
                          title={isAllProvinceSelected ? `取消全选 ${province}` : `全选 ${province} 全部节点`}
                        >
                          {isAllProvinceSelected ? '取消当前省份' : '选择当前省份'}
                        </button>
                      </div>

                      <div className="latency-carrier-chips">
                        {entries.map(region => {
                          const key = keyOf(region)
                          const isSelected = selectedKeys.has(key)
                          const isAvailable = availableKeys.has(key)
                          return (
                            <label
                              key={key}
                              className={`latency-carrier-chip ${isSelected ? 'is-selected' : ''} ${!isAvailable ? 'is-removed' : ''}`}
                            >
                              <input
                                type="checkbox"
                                aria-label={`${region.province} ${region.carrier}`}
                                checked={isSelected}
                                onChange={() => toggle(region)}
                              />
                              <span className="carrier-label">{region.carrier}</span>
                              {!isAvailable && <span className="removed-flag">已移除</span>}
                            </label>
                          )
                        })}
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </div>
      </div>

      </fieldset>
      <footer className="dialog-actions latency-dialog-actions">
        <div className="latency-dialog-summary" aria-live="polite">
          <strong>将应用到 {serverCount} 台服务器 · {values.latency_probe_enabled ? '启用' : '关闭'}自动测试</strong>
          {saveError && <p className="danger-text" role="alert">{saveError}</p>}
          {invalidParams && <p className="danger-text">请在有效范围内填写整数参数。</p>}
          {!loading && unavailableCount > 0 && <p className="danger-text">{unavailableCount} 个已选目标已移除。<button type="button" className="ghost" disabled={saving || disabled} onClick={() => setSelected(current => current.filter(region => availableKeys.has(keyOf(region))))}>移除失效目标</button></p>}
          {isOverLimit ? (
            <span className="danger-text" role="alert">
              ⚠️ 已选 {selectedCount} 个目标，超过上限 {maxTargets - 1} 个（公网目标占用 1 个）
            </span>
          ) : (
            <span className="muted">
              已选 {selectedCount} 个目标节点 · 探测周期 {values.latency_probe_interval_seconds}s
            </span>
          )}
        </div>
        <div className="dialog-action-buttons">
          <button type="button" onClick={() => void save()} disabled={saving || disabled || !serverCount || isOverLimit || invalidParams || loading || Boolean(error) || unavailableCount > 0} aria-busy={saving}>
            {saving ? '应用中…' : `应用到 ${serverCount} 台服务器`}
          </button>
        </div>
      </footer>
    </section>
  )
}

