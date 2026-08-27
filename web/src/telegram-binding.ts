export type TelegramBindingCodeResponse = {
  data: {
    code?: string
  }
}

export const TELEGRAM_BINDING_PROMPT = '请在 10 分钟内向 OBoard Bot 发送：'

export function telegramBindingCommand(channelID: number, response: TelegramBindingCodeResponse): string {
  const code = String(response.data?.code || '').trim()
  if (!code) throw new Error('服务器未返回 Telegram 临时绑定码')
  return `/bind ${channelID} ${code}`
}

export function telegramBindingInstruction(channelID: number, response: TelegramBindingCodeResponse): string {
  return `${TELEGRAM_BINDING_PROMPT}\n${telegramBindingCommand(channelID, response)}`
}
