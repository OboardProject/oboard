// One viewport subscription per modal stack; iOS keyboards resize the visual
// viewport without necessarily changing CSS viewport units.
export function trackModalViewport() {
  const viewport = window.visualViewport
  if (!viewport) return () => undefined
  const root = document.documentElement
  const properties = ['--dialog-viewport-height', '--dialog-viewport-top'] as const
  const previous = properties.map(name => ({ value: root.style.getPropertyValue(name), priority: root.style.getPropertyPriority(name) }))
  const compactBefore = root.getAttribute('data-dialog-compact-viewport')
  let frame = 0
  const update = () => {
    frame = 0
    const height = viewport.height
    const top = viewport.offsetTop
    if (height <= 0) return
    root.style.setProperty(properties[0], `${height}px`)
    root.style.setProperty(properties[1], `${Math.max(0, top)}px`)
    root.toggleAttribute('data-dialog-compact-viewport', height < 480)
  }
  const schedule = () => {
    if (!frame) frame = window.requestAnimationFrame(update)
  }
  update()
  viewport.addEventListener('resize', schedule)
  viewport.addEventListener('scroll', schedule)
  return () => {
    viewport.removeEventListener('resize', schedule)
    viewport.removeEventListener('scroll', schedule)
    window.cancelAnimationFrame(frame)
    properties.forEach((name, index) => {
      const { value, priority } = previous[index]
      if (value) root.style.setProperty(name, value, priority)
      else root.style.removeProperty(name)
    })
    if (compactBefore === null) root.removeAttribute('data-dialog-compact-viewport')
    else root.setAttribute('data-dialog-compact-viewport', compactBefore)
  }
}
