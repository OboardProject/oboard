// Controller DNS list and policy validation errors are English wire text such
// as "bootstrap resolvers: candidate[0]: dns server must be a public address".
// localizeDNSError turns them into operator copy, or returns '' when the text
// is not a DNS validation error.

const groupPrefixes: Array<[string, string]> = [
  ['encrypted resolvers: ', '加密解析'],
  ['bootstrap resolvers: ', '基础解析'],
  ['encrypted_candidates: ', '加密解析'],
  ['bootstrap_candidates: ', '基础解析'],
  ['encrypted_list_id: ', '加密解析'],
  ['bootstrap_list_id: ', '基础解析'],
]

const exactMessages: Record<string, string> = {
  'dns server must be a public address': '解析服务地址必须是公网地址',
  'dns server must be a unicast address': '解析服务地址不能是 0.0.0.0、:: 或组播地址',
  'dns path is invalid': 'DoH 路径无效',
  'dns tag is invalid': '解析服务名称无效',
  'dns tag must be non-empty and unique': '解析服务名称不能为空且不能重复',
  'encrypted list only supports doh, dot, or doq': '加密解析只支持 DoH、DoT 或 DoQ',
  'bootstrap list only supports udp or tcp': '基础解析只支持 UDP 或 TCP',
  'bootstrap server must be a public ip literal': '基础解析必须填写公网 IP 地址',
  'bootstrap server must be an ip literal': '基础解析必须填写 IP 地址，不能填写域名',
  'custom dns resolvers must contain between 1 and 32 candidates': '自定义解析服务需要填写 1–32 个',
  'dns list must contain between 2 and 32 candidates': '每个解析列表需要 2–32 个解析服务',
  'choose either a shared dns list or custom resolvers, not both': '同一组只能选择共享列表或自定义解析服务，不能同时使用',
  'a bootstrap dns list or custom bootstrap resolvers are required': '请选择基础解析列表或填写自定义基础解析服务',
  'bootstrap_list_id or bootstrap_candidates are required': '请选择基础解析列表或填写自定义基础解析服务',
  'dns policy cannot select a disabled list': '不能选择已停用的解析列表',
  'dns policy list kinds do not match': '解析列表类型不匹配',
  "dns list holds another server's custom resolvers": '该解析列表是其他服务器的自定义解析服务',
  'a shared dns list is required': '请选择共享解析列表',
  'dns list id must not be negative': '解析列表编号无效',
  'dns list ids must not be negative': '解析列表编号无效',
  "dns list holds a server's custom resolvers and is managed through that server's dns policy": '这是服务器的自定义解析服务，请在该服务器的 DNS 策略中修改',
  'dns list name is required': '请填写解析列表名称',
  'host is required': '请填写解析服务地址',
  'host is too long': '解析服务地址过长',
  'host contains unsafe characters': '解析服务地址包含不允许的字符',
  'invalid ipv6 host': 'IPv6 地址格式无效',
  'periodic dns test interval must be at least 300 seconds': '自动测试间隔不能少于 5 分钟',
}

function localizeDNSDetail(raw: string): string {
  const text = raw.trim()
  const lower = text.toLowerCase()
  if (exactMessages[lower]) return exactMessages[lower]
  const nested: Array<[string, string]> = [['dns server: ', '解析服务地址'], ['dns tls_name: ', 'TLS 名称'], ['dns port: ', '端口']]
  for (const [prefix, label] of nested) {
    if (!lower.startsWith(prefix)) continue
    const inner = localizeDNSDetail(text.slice(prefix.length))
    return inner ? (inner.startsWith(label) ? inner : `${label}：${inner}`) : `${label}无效`
  }
  if (lower.startsWith('unsupported dns transport')) return '不支持的解析协议'
  if (/^dns list \d+ does not exist$/.test(lower)) return '所选解析列表已不存在，请刷新后重新选择'
  if (lower.startsWith('dns list name prefix')) return '列表名称不能以 @server/ 开头，该前缀保留给服务器自定义解析'
  return ''
}

export function localizeDNSError(message: string): string {
  let text = message.trim()
  let group = ''
  for (const [prefix, label] of groupPrefixes) {
    if (text.toLowerCase().startsWith(prefix)) {
      group = label
      text = text.slice(prefix.length)
      break
    }
  }
  let position = ''
  const candidate = /^candidate\[(\d+)\]:\s*/i.exec(text)
  if (candidate) {
    position = `第 ${Number(candidate[1]) + 1} 个解析服务`
    text = text.slice(candidate[0].length)
  }
  const detail = localizeDNSDetail(text)
  if (!detail) return ''
  const subject = [group, position].filter(Boolean).join('：')
  return subject ? `${subject}：${detail}` : detail
}
