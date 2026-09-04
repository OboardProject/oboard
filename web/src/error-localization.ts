export function errorProtocolLabel(protocol: string) {
  switch (String(protocol || '').trim().toLowerCase()) {
    case 'shadowsocks': return 'SS'
    case 'hy2': return 'HY2'
    case 'anytls': return 'AnyTLS'
    case 'mieru': return 'Mieru'
    case 'vless': return 'VLESS'
    case 'socks': return 'SOCKS'
    case 'snell': return 'Snell'
    case 'ssh': return 'SSH'
    default: return String(protocol || '').trim() || '协议'
  }
}

export function localizeManagedPublicPortExhaustion(raw: string) {
  const match = /^server (.+?) has no available port in the managed public range (\d+)-(\d+) for shared (.+?) chain service$/i.exec(raw)
  if (!match) return null
  const [, server, start, end, protocol] = match
  return [
    '公网端口不足',
    `服务器「${server}」的公网端口范围 ${start}–${end} 已满，无法再分配共享 ${errorProtocolLabel(protocol)} 链式服务。`,
    '请到该服务器设置中扩大公网端口范围后重试。',
  ].join('\n')
}

export function localizeRelayUpdateFailure(raw: string) {
  const match = /^relay update failed:\s*(.+)$/i.exec(raw)
  if (!match) return null
  const detail = match[1].trim()
  if (!detail) return '订阅中继更新失败'
  if (/^exit status \d+$/i.test(detail)) {
    return [
      '订阅中继更新失败',
      `安装脚本异常退出（${detail}）。`,
      '请到中继主机查看 updater 日志后重试，或先开启主控直连订阅恢复访问。',
    ].join('\n')
  }
  return [
    '订阅中继更新失败',
    detail,
    '可先开启主控直连订阅恢复访问，再根据上方原因处理后重试更新。',
  ].join('\n')
}
