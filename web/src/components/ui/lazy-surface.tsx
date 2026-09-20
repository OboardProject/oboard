import * as React from 'react'
import { Dialog } from './dialog'
import './lazy-surface.css'

export function SurfaceLoading() {
  return <div className="surface-loading" role="status" aria-label="正在加载">
    <span /><span /><span />
  </div>
}

class SurfaceBoundary extends React.Component<{ children: React.ReactNode; fallback?: React.ReactNode }, { failed: boolean }> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  render() {
    if (this.state.failed) return this.props.fallback ?? <div className="surface-load-error" role="alert">
      <span>界面加载失败</span>
      <button type="button" className="ghost" onClick={() => window.location.reload()}>重新加载页面</button>
    </div>
    return this.props.children
  }
}

export function lazyDialog<Component extends React.ComponentType<any>>(load: () => Promise<{ default: Component }>, title: string) {
  const Component = React.lazy(load)
  return function LazyDialog(props: React.ComponentProps<Component>) {
    if (props.isOpen === false) return null
    const pending = <Dialog isOpen onClose={props.onClose} title={title}><SurfaceLoading /></Dialog>
    const failed = <Dialog isOpen onClose={props.onClose} title={title}>
      <div className="surface-load-error" role="alert">
        <span>界面加载失败</span>
        <button type="button" className="ghost" onClick={() => window.location.reload()}>重新加载页面</button>
      </div>
    </Dialog>
    return <SurfaceBoundary fallback={failed}><React.Suspense fallback={pending}><Component {...props} /></React.Suspense></SurfaceBoundary>
  }
}

export function lazySurface<Component extends React.ComponentType<any>>(load: () => Promise<{ default: Component }>) {
  const Component = React.lazy(load)
  return function LazySurface(props: React.ComponentProps<Component>) {
    return <SurfaceBoundary><React.Suspense fallback={<SurfaceLoading />}><Component {...props} /></React.Suspense></SurfaceBoundary>
  }
}
