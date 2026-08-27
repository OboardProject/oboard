export type TelegramBindingCodeResponse = {
  data: {
    code?: string
  }
}

export function telegramBindingInstruction(channelID: number, response: TelegramBindingCodeResponse): string {
  const code = String(response.data?.code || '').trim()
  if (!code) throw new Error('服务器未返回 Telegram 临时绑定码')
  return `请在 10 分钟内向 OBoard Bot 发送：\n/bind ${channelID} ${code}`
}
