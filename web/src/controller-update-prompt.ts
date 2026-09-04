import { useEffect, useRef } from 'react'

export const CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS = 10_000

export function useControllerUpdatePromptAutoDismiss(
  visible: boolean,
  autoUpdateEnabled: boolean,
  dialogOpen: boolean,
  onDismiss: () => void,
) {
  const onDismissRef = useRef(onDismiss)
  onDismissRef.current = onDismiss

  useEffect(() => {
    if (!visible || !autoUpdateEnabled || dialogOpen) return
    const timer = window.setTimeout(() => onDismissRef.current(), CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS)
    return () => window.clearTimeout(timer)
  }, [visible, autoUpdateEnabled, dialogOpen])
}
