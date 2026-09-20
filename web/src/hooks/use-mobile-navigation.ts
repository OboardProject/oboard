import { useLayoutEffect, useRef, type RefObject } from 'react'

export function useMobileNavigation(ref: RefObject<HTMLElement | null>, open: boolean, onClose: () => void) {
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  useLayoutEffect(() => {
    const panel = ref.current
    if (!open || !panel) return
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const controls = () => Array.from(panel.querySelectorAll<HTMLElement>('button:not([disabled]), a[href], [tabindex="0"]'))
      .filter(element => !element.closest('[inert], [hidden], [aria-hidden="true"]'))
    const first = () => controls()[0]?.focus()
    first()
    const keydown = (event: KeyboardEvent) => {
      if (document.querySelector('[data-modal-top="true"]')) return
      if (event.key === 'Escape') {
        event.preventDefault()
        closeRef.current()
      } else if (event.key === 'Tab') {
        const elements = controls()
        const last = elements[elements.length - 1]
        const target = event.shiftKey ? last : elements[0]
        if (document.activeElement === (event.shiftKey ? elements[0] : last)) {
          event.preventDefault()
          target?.focus()
        }
      }
    }
    const focusin = (event: FocusEvent) => {
      if (!panel.contains(event.target as Node) && !document.querySelector('[data-modal-top="true"]')) first()
    }
    document.addEventListener('keydown', keydown)
    document.addEventListener('focusin', focusin)
    return () => {
      document.removeEventListener('keydown', keydown)
      document.removeEventListener('focusin', focusin)
      document.body.style.overflow = overflow
      if (previous?.isConnected && !previous.closest('[inert]')) previous.focus()
    }
  }, [ref, open])
}
