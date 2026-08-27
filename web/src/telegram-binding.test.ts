import { describe, expect, it } from 'vitest'
import { telegramBindingInstruction } from './telegram-binding'

describe('Telegram binding instruction', () => {
  it('shows the temporary random code returned by the v1 response envelope', () => {
    expect(telegramBindingInstruction(1, { data: { code: 'RANDOM8X2P4' } })).toBe(
      '请在 10 分钟内向 OBoard Bot 发送：\n/bind 1 RANDOM8X2P4',
    )
  })

  it('does not render a bind command with an empty code', () => {
    expect(() => telegramBindingInstruction(1, { data: {} })).toThrow('服务器未返回 Telegram 临时绑定码')
  })
})
