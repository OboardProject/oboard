import * as React from 'react'
import { Badge } from '../ui/badge'
import { Dialog } from '../ui/dialog'
import { AuthorizationStatusBadge } from '../authorization/AuthorizationStatusBadge'
import { nodeStatusLabels, nodeTypeLabel } from './catalog'

export type NodeDetailUser = { user_id: number; username: string; nickname?: string; effective: boolean; source?: string; plan_id?: number; plan_name?: string; effect?: string; reason?: string; expires_at?: string }
export type NodeDetail = {
  node: any
  users: NodeDetailUser[]
  plans: { plan_id: number; name: string; display_group?: string; display_name?: string; has_display_name_override?: boolean }[]
  exceptions: any[]
  runtime_authorization_mode: string
}

function userSource(user: NodeDetailUser) {
  if (user.source === 'exception_allow') return <Badge variant="success">允许例外</Badge>
  if (user.source === 'exception_deny') return <Badge variant="destructive">拒绝例外</Badge>
  if (user.source === 'plan') return <Badge variant="secondary">{user.plan_name || '套餐'}</Badge>
  return <span className="nw-muted">—</span>
}

export function NodeDetailDrawer({ detail, onClose }: { detail: NodeDetail | null; onClose: () => void }) {
  const node = detail?.node
  const path = (node?.path_summary || []) as string[]
  return <Dialog isOpen={detail !== null} onClose={onClose} title={node?.name || '节点详情'} placement="right" drawerSize="wide" className="nw-dialog nw-detail">
    {detail && <div className="nw-detail-body">
      <section>
        <h4>基本信息</h4>
        <dl className="nw-facts nw-facts-grid">
          <div><dt>类型</dt><dd>{nodeTypeLabel(node?.type)}</dd></div>
          <div><dt>状态</dt><dd>{nodeStatusLabels[node?.status] || node?.status || '—'}</dd></div>
          <div><dt>全局名称</dt><dd>{node?.effective_global_name || node?.name}</dd></div>
          <div><dt>来源名称</dt><dd>{node?.source_name || '—'}</dd></div>
          <div><dt>入口服务器</dt><dd>{node?.entry_server_name || '—'}</dd></div>
          <div><dt>协议</dt><dd className="nw-mono">{node?.entry_protocol || '—'}{node?.entry_port ? `:${node.entry_port}` : ''}</dd></div>
          <div><dt>出口地区</dt><dd>{node?.exit_region || '—'}</dd></div>
          <div><dt>授权方式</dt><dd>{node?.type === 'external_outbound'
            ? <AuthorizationStatusBadge status="external_credential" />
            : <AuthorizationStatusBadge status={detail.runtime_authorization_mode === 'legacy' ? 'upgrade_required' : 'authorized'} />}</dd></div>
          <div className="nw-facts-wide"><dt>稳定标识</dt><dd><code>{node?.key}</code></dd></div>
        </dl>
      </section>

      {path.length > 0 && <section>
        <h4>链路</h4>
        <ol className="nw-path">{path.map((step, index) => <React.Fragment key={`${index}-${step}`}>{index > 0 && <li className="nw-path-arrow" aria-hidden="true">→</li>}<li>{step}</li></React.Fragment>)}</ol>
      </section>}

      <section>
        <h4>所属套餐<small>{detail.plans?.length || 0}</small></h4>
        {!detail.plans?.length ? <p className="nw-muted">未加入任何套餐，用户无法通过套餐获得此节点。</p> : <ul className="nw-plan-list">
          {detail.plans.map(plan => <li key={plan.plan_id}>
            <strong>{plan.name}</strong>
            <span>{plan.display_name || node?.name}</span>
            <Badge variant={plan.has_display_name_override ? 'secondary' : 'outline'}>{plan.has_display_name_override ? '套餐内独立名称' : '继承全局名称'}</Badge>
          </li>)}
        </ul>}
      </section>

      <section>
        <h4>授权用户<small>{detail.users?.length || 0}</small></h4>
        {!detail.users?.length ? <p className="nw-muted">暂无有效用户。</p> : <div className="nw-table-wrap nw-detail-users">
          <table className="nw-table">
            <thead><tr><th>用户</th><th>来源</th><th>原因</th><th>到期</th></tr></thead>
            <tbody>{detail.users.map(user => <tr key={user.user_id}>
              <td><strong className="nw-cell-title">{user.username}</strong>{user.nickname && <span className="nw-cell-sub">{user.nickname}</span>}</td>
              <td>{userSource(user)}</td>
              <td className="nw-cell-muted">{user.reason || '—'}</td>
              <td className="nw-cell-muted">{user.expires_at ? new Date(user.expires_at).toLocaleString() : '—'}</td>
            </tr>)}</tbody>
          </table>
        </div>}
      </section>
    </div>}
  </Dialog>
}
