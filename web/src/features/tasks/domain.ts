import { formatBytes, labelValue, timeCorrectionModeLabel } from '../../shared/presentation'
import type { TimeCorrectionMode } from '../../components/proxy-path/types'

export function redactTaskJSON(value: any): any {
  if (value == null) return value
  if (Array.isArray(value)) return value.map(redactTaskJSON)
  if (typeof value !== 'object') return value
  const out: Record<string, any> = {}
  Object.entries(value).forEach(([key, item]) => {
    const lower = key.toLowerCase()
    if (lower.includes('token') || lower.includes('password') || lower.includes('secret') || lower.includes('private_key') || lower === 'config') {
      if (lower === 'config' && typeof item === 'string') {
        out[key] = `[config ${formatBytes(item.length)}]`
      } else {
        out[key] = '***'
      }
      return
    }
    out[key] = redactTaskJSON(item)
  })
  return out
}

export function taskSummaryFromPayload(type: string, payload: any) {
  if (type === 'apply_deployment') {
    const count = [payload?.time_check, payload?.config, payload?.port_forwards, payload?.inbound_probe, payload?.port_forward_probe, payload?.external_egress_probe, payload?.tunnels, payload?.dns_benchmark, payload?.mtu_detection].filter(Boolean).length
    return `${count || 1} 个部署步骤`
  }
  if (type === 'apply_core_config' && payload?.skipped) return '配置未变化，已跳过'
  if (type === 'apply_core_config' && payload?.config) return `配置体积 ${formatBytes(String(payload.config).length)}`
  if (type === 'update_agent') return payload?.source ? `来源 ${labelValue(payload.source)}` : '更新 Agent 与内核'
  if (type === 'uninstall_agent') return payload?.purge ? '卸载 Agent 并清理本机数据' : '卸载 Agent'
  if (type === 'update_agent_config') return '同步 Agent 本机配置'
  if (type === 'detect_mtu') return payload?.mode ? `模式 ${labelValue(payload.mode)}` : 'MTU 检测'
  if (type === 'check_time') return `模式 ${timeCorrectionModeLabel(payload?.correction_mode as TimeCorrectionMode)}`
  if (type === 'probe_inbounds' || type === 'probe_inbounds_external') return payload?.entry_targets?.length ? `${payload.entry_targets.length} 个入口` : '入口端口探测'
  if (type === 'probe_port_forwards') return payload?.rules?.length ? `${payload.rules.length} 条规则` : '端口转发探测'
  if (type === 'probe_external_egress') return payload?.targets?.length ? `${payload.targets.length} 条分支` : '第三方出口探测'
  if (type === 'collect_logs') return payload?.services ? `服务 ${payload.services}` : '拉取日志'
  if (type === 'manage_logs') return `${payload?.action === 'clear' ? '清空' : '轮转'} ${payload?.services || 'all'} 日志`
  if (payload && typeof payload === 'object') return '等待 Agent 执行'
  return '展开查看详情'
}
