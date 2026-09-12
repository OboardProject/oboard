import React, { useState, useEffect, useMemo } from 'react'
import { Switch } from './ui/switch'
import { Select } from './ui/select'
import { SettingsDisclosure, SettingsGroup, SettingsRow, SettingsSwitchRow } from './settings/SettingsLayout'
import type { DialogApi } from './ui/dialog-context'

export interface AgentSettingsPanelProps {
  data: any
  client: any
  load: (section?: string, options?: any) => Promise<void>
  notify: (message: string, tone?: 'success' | 'warning' | 'danger' | 'error' | 'info') => void
  confirm?: DialogApi['confirm']
}

type TimeCorrectionMode = 'off' | 'auto' | 'ntp'

const mtuModes = ['disabled', 'detect', 'apply']
const mtuLabels: Record<string, string> = {
  disabled: '已禁用',
  detect: '仅检测',
  apply: '检测并应用',
}

const trafficTimezones = [
  'Asia/Shanghai', 'UTC', 'Asia/Hong_Kong', 'Asia/Taipei', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Singapore', 'Asia/Bangkok', 'Asia/Jakarta', 'Asia/Kolkata', 'Asia/Dubai',
  'Australia/Sydney', 'Pacific/Auckland',
  'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Moscow',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'America/Toronto', 'America/Vancouver', 'America/Mexico_City', 'America/Sao_Paulo',
  'Africa/Johannesburg',
]

const trafficTimezoneNames: Record<string, string> = {
  UTC: '协调世界时',
  'Asia/Shanghai': '北京时间',
  'Asia/Hong_Kong': '香港时间',
  'Asia/Taipei': '台北时间',
  'Asia/Tokyo': '东京时间',
  'Asia/Seoul': '首尔时间',
  'Asia/Singapore': '新加坡时间',
  'Asia/Bangkok': '曼谷时间',
  'Asia/Jakarta': '雅加达时间',
  'Asia/Kolkata': '印度时间',
  'Asia/Dubai': '迪拜时间',
  'Australia/Sydney': '悉尼时间',
  'Pacific/Auckland': '奥克兰时间',
  'Europe/London': '伦敦时间',
  'Europe/Paris': '巴黎时间',
  'Europe/Berlin': '柏林时间',
  'Europe/Moscow': '莫斯科时间',
  'America/New_York': '美国东部时间',
  'America/Chicago': '美国中部时间',
  'America/Denver': '美国山地时间',
  'America/Los_Angeles': '美国西部时间',
  'America/Toronto': '多伦多时间',
  'America/Vancouver': '温哥华时间',
  'America/Mexico_City': '墨西哥城时间',
  'America/Sao_Paulo': '圣保罗时间',
  'Africa/Johannesburg': '约翰内斯堡时间',
}

function getTrafficTimezoneLabel(timezone: string) {
  try {
    const offset = new Intl.DateTimeFormat('zh-CN', { timeZone: timezone, timeZoneName: 'longOffset' as any })
      .formatToParts(new Date())
      .find(part => part.type === 'timeZoneName')?.value
      ?.replace('GMT', 'UTC')
    return [trafficTimezoneNames[timezone], timezone, offset].filter(Boolean).join(' · ')
  } catch {
    return timezone
  }
}

const defaultTimeCheckNTPServers = ['time.cloudflare.com', 'time.google.com', 'ntp.aliyun.com']
const monitoringRetentionOptions = [1, 3, 7, 15, 30]

function parseNTPServers(value: unknown): string[] {
  if (!Array.isArray(value) || value.length !== 3) return [...defaultTimeCheckNTPServers]
  return value.map(item => String(item || ''))
}

export function AgentSettingsPanel({ data, client, load, notify, confirm }: AgentSettingsPanelProps) {
  const [serverDefaultMTUMode, setServerDefaultMTUMode] = useState<string>(String(data.settings?.server_default_mtu_mode || 'detect'))
  const [serverDefaultBBREnabled, setServerDefaultBBREnabled] = useState<boolean>(String(data.settings?.server_default_bbr_enabled ?? 'true') === 'true')
  const [serverDefaultTimeCorrectionMode, setServerDefaultTimeCorrectionMode] = useState<TimeCorrectionMode>((data.settings?.server_default_time_correction_mode || 'auto') as TimeCorrectionMode)
  const [timeCheckNTPServers, setTimeCheckNTPServers] = useState<string[]>(() => parseNTPServers(data.settings?.time_check_ntp_servers))
  const [trafficTimezone, setTrafficTimezone] = useState<string>(data.settings?.traffic_timezone || 'Asia/Shanghai')
  const [monitoringRetentionDays, setMonitoringRetentionDays] = useState<number>(Number(data.settings?.server_monitoring_retention_days) || 7)

  const [savingKey, setSavingKey] = useState<string>('')

  useEffect(() => {
    setServerDefaultMTUMode(String(data.settings?.server_default_mtu_mode || 'detect'))
    setServerDefaultBBREnabled(String(data.settings?.server_default_bbr_enabled ?? 'true') === 'true')
    setServerDefaultTimeCorrectionMode((data.settings?.server_default_time_correction_mode || 'auto') as TimeCorrectionMode)
    setTimeCheckNTPServers(parseNTPServers(data.settings?.time_check_ntp_servers))
  }, [data.settings?.server_default_mtu_mode, data.settings?.server_default_bbr_enabled, data.settings?.server_default_time_correction_mode, data.settings?.time_check_ntp_servers])

  useEffect(() => {
    setTrafficTimezone(data.settings?.traffic_timezone || 'Asia/Shanghai')
    setMonitoringRetentionDays(Number(data.settings?.server_monitoring_retention_days) || 7)
  }, [data.settings?.traffic_timezone, data.settings?.server_monitoring_retention_days])

  const originalNTPServers = useMemo(() => parseNTPServers(data.settings?.time_check_ntp_servers), [data.settings?.time_check_ntp_servers])
  const isNTPDirty = useMemo(() => {
    return timeCheckNTPServers.some((val, idx) => val.trim() !== (originalNTPServers[idx] || '').trim())
  }, [timeCheckNTPServers, originalNTPServers])

  const autoSaveSetting = async (payload: Record<string, any>, successMessage: string) => {
    if (savingKey) return
    setSavingKey('auto-save')
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify(payload) })
      await load()
      notify(successMessage, 'success')
    } catch (error: any) {
      notify(error?.message || String(error), 'error')
    } finally {
      setSavingKey('')
    }
  }

  const handleMTUChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const val = e.target.value
    setServerDefaultMTUMode(val)
    void autoSaveSetting({ server_default_mtu_mode: val }, 'MTU 设置已保存')
  }

  const handleBBRChange = (checked: boolean) => {
    setServerDefaultBBREnabled(checked)
    void autoSaveSetting({ server_default_bbr_enabled: checked }, 'BBR + FQ 设置已保存')
  }

  const handleTimeCorrectionChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const val = e.target.value as TimeCorrectionMode
    setServerDefaultTimeCorrectionMode(val)
    void autoSaveSetting({ server_default_time_correction_mode: val }, '时间校准设置已保存')
  }

  const handleTimezoneChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const val = e.target.value
    setTrafficTimezone(val)
    void autoSaveSetting({ traffic_timezone: val }, '统计时区已保存')
  }

  const handleMonitoringRetentionChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const days = Number(e.target.value) || 30
    setMonitoringRetentionDays(days)
    void autoSaveSetting({ server_monitoring_retention_days: days }, '监控数据保留时间已保存')
  }

  const refreshAllRuntime = async () => {
    if (savingKey) return
    const ok = await confirm?.({
      title: '刷新全部节点配置？',
      message: '会重建每台已接入 Agent 的运行配置并重启内核，现有连接会短暂中断。授权凭证也会重新从主控下发。',
      confirmText: '刷新全部节点',
      tone: 'danger',
    })
    if (!ok) return
    setSavingKey('refresh-runtime')
    try {
      const result = await client.request('/deployments/refresh-runtime', {
        method: 'POST',
        body: JSON.stringify({ confirm: true }),
      })
      const count = Number(result?.delivery_retried ?? 0)
      notify(`已向 ${count} 台已接入服务器重新下发配置与授权`, 'success')
      await load()
    } catch (error: any) {
      notify(error?.message || String(error), 'error')
    } finally {
      setSavingKey('')
    }
  }

  const saveNTPServers = async () => {
    if (savingKey) return
    setSavingKey('ntp-servers')
    try {
      await client.request('/settings', {
        method: 'POST',
        body: JSON.stringify({
          time_check_ntp_servers: timeCheckNTPServers.map(v => v.trim()),
        }),
      })
      await load()
      notify('NTP 时间源已保存', 'success')
    } catch (error: any) {
      notify(error?.message || String(error), 'error')
    } finally {
      setSavingKey('')
    }
  }

  return (
    <section className="settings-card agent-settings-card">
      <SettingsGroup title="资源下载" description="适用于 Agent、内核和端口转发组件的安装与更新。订阅中继始终从主控下载。">
        <SettingsSwitchRow label="优先从 GitHub 下载" description="默认从主控下载。开启后优先从 GitHub 获取主控指定的正式版或开发构建，资源不可用时回退主控。" checked={data.settings?.resource_download_source === 'github'} onChange={checked => void autoSaveSetting({ resource_download_source: checked ? 'github' : 'controller' }, '资源下载来源已保存')} disabled={Boolean(savingKey)} ariaLabel="优先从 GitHub 下载资源" />
        <SettingsSwitchRow label="中国大陆服务器优先从主控下载" description="开启后，按下载请求 IP 识别为中国大陆的服务器直接从主控下载，不受 GitHub 优先设置影响。其他地区或地区未知时沿用原设置。" checked={data.settings?.resource_download_cn_controller !== false && data.settings?.resource_download_cn_controller !== 'false'} onChange={checked => void autoSaveSetting({ resource_download_cn_controller: checked }, '中国大陆下载偏好已保存')} disabled={Boolean(savingKey)} ariaLabel="中国大陆服务器优先从主控下载" />
      </SettingsGroup>
      <SettingsGroup title="新服务器默认值" description="创建服务器时自动带入，可在创建窗口中单独修改。">
        <SettingsRow label="MTU" description="根据节点网络环境检测 MTU，并决定是否自动应用检测结果。">
          <Select variant="segmented" value={serverDefaultMTUMode} onChange={handleMTUChange} disabled={Boolean(savingKey)}>
            {mtuModes.map(mode => <option key={mode} value={mode}>{mtuLabels[mode] || mode}</option>)}
          </Select>
        </SettingsRow>
        <SettingsSwitchRow label="BBR + FQ" description="在支持的 Linux 节点上启用 BBR 与 FQ 网络优化。" checked={serverDefaultBBREnabled} onChange={handleBBRChange} disabled={Boolean(savingKey)} ariaLabel="新服务器默认启用 BBR + FQ" />
        <SettingsRow label="时间校准" description="控制 Agent 的系统时间同步策略。">
          <Select variant="segmented" value={serverDefaultTimeCorrectionMode} onChange={handleTimeCorrectionChange} disabled={Boolean(savingKey)} aria-label="时间校准模式">
            <option value="off">关闭</option><option value="auto">自动</option><option value="ntp">逻辑校时</option>
          </Select>
        </SettingsRow>
        <SettingsDisclosure title="NTP 时间源" description="低频修改项，默认使用三组公共时间源。" summary={isNTPDirty ? '有未保存修改' : '已配置 3 个时间源'}>
          <div className="agent-ntp-list">
            {timeCheckNTPServers.map((value, index) => <input key={index} value={value} onChange={event => {
              const newVal = event.target.value
              setTimeCheckNTPServers(current => current.map((item, itemIndex) => itemIndex === index ? newVal : item))
            }} placeholder={defaultTimeCheckNTPServers[index]} aria-label={`NTP 时间源 ${index + 1}`} className="agent-input-field" />)}
          </div>
          <div className="agent-ntp-actions"><button type="button" onClick={() => void saveNTPServers()} disabled={!isNTPDirty || Boolean(savingKey)}>{savingKey === 'ntp-servers' ? '保存中...' : '保存 NTP 时间源'}</button></div>
        </SettingsDisclosure>
      </SettingsGroup>
      <SettingsGroup title="流量控制" description="达量后自动断开现有连接并拒绝新连接；面板暂时不可达时，节点仍按已下发额度执行。">
        <SettingsRow label="统计时区" description="用于计算流量重置时间。">
          <Select value={trafficTimezone} onChange={handleTimezoneChange} disabled={Boolean(savingKey)} aria-label="统计时区" className="agent-select-field">
            {!trafficTimezones.includes(trafficTimezone) && <option value={trafficTimezone}>{getTrafficTimezoneLabel(trafficTimezone)}</option>}
            {trafficTimezones.map(timezone => <option key={timezone} value={timezone}>{getTrafficTimezoneLabel(timezone)}</option>)}
          </Select>
        </SettingsRow>
      </SettingsGroup>
      <SettingsGroup title="监控数据" description="统一管理负载、公网延迟和地区延迟的历史数据。">
        <SettingsRow label="保留时间" description="缩短后，超出期限的数据会在下一次数据库维护时删除；已删除的数据无法恢复。" htmlFor="server-monitoring-retention-days">
          <Select id="server-monitoring-retention-days" value={monitoringRetentionDays} onChange={handleMonitoringRetentionChange} disabled={Boolean(savingKey)} aria-label="服务器监控数据保留时间" aria-describedby="server-monitoring-retention-help">
            {monitoringRetentionOptions.map(days => <option key={days} value={days}>{days} 天</option>)}
          </Select>
        </SettingsRow>
      </SettingsGroup>
      <SettingsGroup title="运行配置" description="更新后或节点异常时，可清理 Agent 本地运行配置并从主控重新下发。">
        <SettingsRow label="刷新全部节点" description="重建内核、转发、隧道、SSH 与授权凭证。每台已接入节点会短暂中断连接。">
          <div className="settings-actions">
            <button type="button" className="danger-ghost" onClick={() => void refreshAllRuntime()} disabled={Boolean(savingKey)}>
              {savingKey === 'refresh-runtime' ? '下发中...' : '刷新全部节点配置'}
            </button>
          </div>
        </SettingsRow>
      </SettingsGroup>
    </section>
  )
}
