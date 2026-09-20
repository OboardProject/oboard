import { useState, type ReactNode } from 'react'
import { Dialog } from '../ui/dialog'

export function AccountFormDialog({ title, dirty, busy, onClose, children, footer }: {
  title: string
  dirty: boolean
  busy: boolean
  onClose: () => void
  children: ReactNode
  footer: ReactNode
}) {
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  const requestClose = () => {
    if (busy) return
    if (dirty) setConfirmDiscard(true)
    else onClose()
  }

  return <>
    <Dialog isOpen title={title} onClose={requestClose} className="signal-account-dialog" footer={<>
      <button type="button" className="ghost" disabled={busy} onClick={requestClose}>取消</button>
      {footer}
    </>}>
      {children}
    </Dialog>
    <Dialog isOpen={confirmDiscard} title="放弃未保存的修改？" onClose={() => setConfirmDiscard(false)} className="signal-account-dialog" size="sm" footer={<>
      <button type="button" className="secondary-btn" onClick={() => setConfirmDiscard(false)}>继续编辑</button>
      <button type="button" className="danger" onClick={onClose}>放弃修改</button>
    </>}>
      <p>关闭后，本次修改不会保存。</p>
    </Dialog>
  </>
}
