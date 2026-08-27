import { describe, expect, it } from 'vitest'
import { telegramBindingCommand, telegramBindingInstruction } from './telegram-binding'

describe('Telegram binding instruction', () => {
  it('shows the temporary random code returned by the v1 response envelope', () => {
    const response = { data: { code: 'RANDOM8X2P4' } }
    expect(telegramBindingCommand(1, response)).toBe('/bind 1 RANDOM8X2P4')
    expect(telegramBindingInstruction(1, response)).toBe(
      '请在 10 分钟内向 OBoard Bot 发送：\n/bind 1 RANDOM8X2P4',
    )
  })

  it('does not render a bind command with an empty code', () => {
    expect(() => telegramBindingCommand(1, { data: {} })).toThrow('服务器未返回 Telegram 临时绑定码')
    expect(() => telegramBindingInstruction(1, { data: {} })).toThrow('服务器未返回 Telegram 临时绑定码')
  })
})
