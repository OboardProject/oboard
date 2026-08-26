import React, { useState } from 'react'
import { Fingerprint } from 'lucide-react'
import { Dialog } from '../ui/dialog'
import { FormField } from '../ui/form-field'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

type StepUpAuthProps = {
  request: RequestFn
  purpose: string
  resourceType: string
  resourceId: string | number
  title: string
  warning: string
  onComplete: (token: string) => void
  onCancel: () => void
}

function base64URLToBytes(value: string) {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4)
  const binary = window.atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return bytes
}

function bytesToBase64URL(value: ArrayBuffer | ArrayBufferView | null) {
  if (!value) return ''
  const bytes = value instanceof ArrayBuffer
    ? new Uint8Array(value)
    : new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, Math.min(offset + 0x8000, bytes.length)))
  }
  return window.btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

async function getPasskeyCredential(options: any) {
  if (!window.isSecureContext || !('PublicKeyCredential' in window) || !navigator.credentials) {
    throw new Error('当前浏览器或访问方式不支持通行密钥')
  }
  const publicKey = {
    ...options.publicKey,
    challenge: base64URLToBytes(options.publicKey.challenge),
    allowCredentials: (options.publicKey.allowCredentials || []).map((credential: any) => ({ ...credential, id: base64URLToBytes(credential.id) })),
  }
  const credential = await navigator.credentials.get({ publicKey }) as PublicKeyCredential | null
  if (!credential) throw new Error('没有选择通行密钥')
  const response: any = credential.response
  return {
    id: credential.id,
    rawId: bytesToBase64URL(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment || undefined,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bytesToBase64URL(response.clientDataJSON),
      authenticatorData: bytesToBase64URL(response.authenticatorData),
      signature: bytesToBase64URL(response.signature),
      userHandle: response.userHandle ? bytesToBase64URL(response.userHandle) : undefined,
    },
  }
}

export function StepUpAuth({ request, purpose, resourceType, resourceId, title, warning, onComplete, onCancel }: StepUpAuthProps) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [challenge, setChallenge] = useState<any>(null)

  const begin = async () => {
    if (busy) return
    setBusy('begin')
    setError('')
    try {
      const result = await request('/auth/step-up/begin', {
        method: 'POST',
        body: JSON.stringify({ purpose, resource: { type: resourceType, id: resourceId } }),
      })
      setChallenge(result)
    } catch (item: any) {
      setError(item?.message || '无法开始重新认证')
    } finally {
      setBusy('')
    }
  }

  React.useEffect(() => { void begin() }, [])

  const finishPassword = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!challenge?.challenge_id || busy) return
    setBusy('password')
    setError('')
    try {
      const result = await request('/auth/step-up/password', {
        method: 'POST',
        body: JSON.stringify({ challenge_id: challenge.challenge_id, password }),
      })
      onComplete(result.step_up_token)
    } catch (item: any) {
      setError(item?.message || '密码验证失败')
    } finally {
      setBusy('')
    }
  }

  const finishPasskey = async () => {
    if (!challenge?.challenge_id || !challenge.passkey || busy) return
    setBusy('passkey')
    setError('')
    try {
      const credential = await getPasskeyCredential(challenge.passkey)
      const result = await request('/auth/step-up/passkey/finish', {
        method: 'POST',
        body: JSON.stringify({ challenge_id: challenge.challenge_id, credential }),
      })
      onComplete(result.step_up_token)
    } catch (item: any) {
      setError(item?.message || '通行密钥验证失败')
    } finally {
      setBusy('')
    }
  }

  return (
    <Dialog isOpen onClose={onCancel} title={title} size="sm">
      <p className="text-pretty text-sm text-muted-foreground">{warning}</p>
      {error ? <p className="mt-3 text-sm text-destructive" role="alert">{error}</p> : null}
      <form className="mt-4 flex flex-col gap-3" onSubmit={event => void finishPassword(event)}>
        <FormField label="管理员密码">
          <input type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} required />
        </FormField>
        <div className="dialog-actions">
          <button type="button" className="ghost" onClick={onCancel} disabled={Boolean(busy)}>取消</button>
          {challenge?.passkey_available ? (
            <button type="button" className="ghost" onClick={() => void finishPasskey()} disabled={Boolean(busy)}>
              <Fingerprint size={15} aria-hidden="true" />使用通行密钥
            </button>
          ) : null}
          <button type="submit" disabled={Boolean(busy) || !password}>{busy ? '验证中…' : '确认'}</button>
        </div>
      </form>
    </Dialog>
  )
}
