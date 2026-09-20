import { useRef } from 'react'
import { useDialogs } from '../ui/dialog-context'

export function useSettingsEditorClose({ busy, dirty, onClose }: { busy: boolean; dirty: boolean; onClose: () => void }) {
  const dialogs = useDialogs()
  const confirming = useRef(false)
  return async () => {
    if (busy || confirming.current) return
    if (!dirty) { onClose(); return }
    confirming.current = true
    try {
      if (await dialogs.confirm({
        title: '放弃未保存的修改？',
        message: '关闭后将丢弃本次修改。',
        confirmText: '放弃修改',
        tone: 'danger',
      })) onClose()
    } finally {
      confirming.current = false
    }
  }
}
