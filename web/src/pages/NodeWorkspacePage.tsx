import * as React from 'react'
import './node-workspace.css'
import { CloudDownload, Layers3, Link2, ListTree, Search, Send, UserRound } from 'lucide-react'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../components/ui/tabs'
import { NodeAssignmentsPage } from './NodeAssignmentsPage'
import { NodeLibraryPanel } from '../components/node-workspace/NodeLibraryPanel'
import { NodeGroupsPanel } from '../components/node-workspace/NodeGroupsPanel'
import { SubscriptionOutputsPanel } from '../components/node-workspace/SubscriptionOutputsPanel'
import { errorText, type Client, type LibraryNode, type Notify, type Workspace } from '../components/node-workspace/types'
import { hasManagementAccess } from '../permissions'
import { useRegisterPageRefresh } from '../page-refresh-context'

type AdminView = 'catalog' | 'distribution' | 'user'
type WorkspaceTab = 'library' | 'groups' | 'outputs' | 'subscription'

const adminViewIntro: Record<AdminView, string> = {
  catalog: '全部可分配节点及其套餐归属与授权用户。勾选节点可批量调整套餐。',
  distribution: '选择客户端格式后为用户复制订阅链接，并管理自定义路径与 SSH 凭证。',
  user: '以指定用户的身份查看和管理其节点库、节点组与组合订阅，修改会直接作用于该用户。',
}

export function NodeWorkspacePage({ data, client, load, notify, legacySubscriptions, sessionUser }: { data: any; client: Client; load: () => Promise<void>; notify?: Notify; legacySubscriptions?: React.ReactNode; sessionUser?: { role?: string } | null }) {
  // The role decides which workspace to mount, so it must be resolved before the
  // first paint. Page data arrives asynchronously; fall back to the restored
  // session so an administrator never sees the platform-user layout first.
  const role: string | undefined = data.session?.role || data.current_user?.role || sessionUser?.role || undefined
  const roleResolved = Boolean(role)
  const isAdmin = hasManagementAccess(role)
  const [adminView, setAdminView] = React.useState<AdminView>('catalog')
  const [userID, setUserID] = React.useState<number>(data.current_user?.id || data.account_user?.id || 0)
  const users = (data.users || []).filter((user: any) => user.status === 'active')

  React.useEffect(() => {
    if (!userID) setUserID(Number(data.current_user?.id || data.account_user?.id || 0))
  }, [data.current_user?.id, data.account_user?.id, userID])

  if (!roleResolved) return <div className="nw-page"><div className="nw-empty"><span>正在加载节点…</span></div></div>

  if (!isAdmin) {
    return <div className="nw-page">
      <PersonalWorkspace data={data} client={client} notify={notify} role={role!} subjectID={0} subscriptionPanel={legacySubscriptions} />
    </div>
  }

  const subject = users.find((user: any) => user.id === userID)
  return <div className="nw-page">
    <Tabs value={adminView} onValueChange={value => setAdminView(value as AdminView)}>
      <div className="nw-view-head">
        <TabsList className="nw-tabs" aria-label="节点视图">
          <TabsTrigger value="catalog"><ListTree size={16} aria-hidden="true" />节点目录</TabsTrigger>
          <TabsTrigger value="distribution"><Send size={16} aria-hidden="true" />订阅分发</TabsTrigger>
          <TabsTrigger value="user"><UserRound size={16} aria-hidden="true" />用户节点</TabsTrigger>
        </TabsList>
        <p className="nw-view-intro">{adminViewIntro[adminView]}</p>
      </div>
      <TabsContent value="catalog" className="nw-view">
        <NodeAssignmentsPage data={data} client={client} load={load} notify={notify} />
      </TabsContent>
      <TabsContent value="distribution" className="nw-view">
        {legacySubscriptions || <div className="nw-empty"><span>订阅分发暂不可用</span></div>}
      </TabsContent>
      <TabsContent value="user" className="nw-view">
        <div className="nw-subject-bar">
          <label className="nw-inline-field"><span>查看用户</span><select value={userID} onChange={event => setUserID(Number(event.target.value))} aria-label="选择用户视角">
            <option value={0}>选择用户</option>
            {users.map((user: any) => <option key={user.id} value={user.id}>{user.nickname || user.username} ({user.username})</option>)}
          </select></label>
          {subject && <span className="nw-subject-note">正在代管 <strong>{subject.nickname || subject.username}</strong> 的节点</span>}
        </div>
        {userID
          ? <PersonalWorkspace key={userID} data={data} client={client} notify={notify} role={role!} subjectID={userID} compactTabs />
          : <div className="nw-empty"><strong>选择一个用户</strong><span>选择后可查看该用户的节点库、节点组和组合订阅。</span></div>}
      </TabsContent>
    </Tabs>
  </div>
}

function PersonalWorkspace({ data, client, notify, role, subjectID, subscriptionPanel, compactTabs = false }: {
  data: any
  client: Client
  notify?: Notify
  role: string
  subjectID: number
  subscriptionPanel?: React.ReactNode
  compactTabs?: boolean
}) {
  const [tab, setTab] = React.useState<WorkspaceTab>('library')
  const [workspace, setWorkspace] = React.useState<Workspace | null>(null)
  const [nodes, setNodes] = React.useState<LibraryNode[]>([])
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')
  const subjectQuery = subjectID ? `?user_id=${subjectID}` : ''
  const scope = `${role}:${subjectID || data.current_user?.id || data.account_user?.id || 0}`
  const currentScope = React.useRef(scope)
  currentScope.current = scope
  const readSequence = React.useRef(0)
  const mutationInFlight = React.useRef(false)
  const [loadedScope, setLoadedScope] = React.useState('')
  const visibleWorkspace = loadedScope === scope ? workspace : null

  const refresh = React.useCallback(async () => {
    const sequence = ++readSequence.current
    const isCurrent = () => currentScope.current === scope && sequence === readSequence.current
    setBusy(true)
    setError('')
    try {
      const [workspaceResult, libraryResult] = await Promise.all([
        client.request<Workspace>(`/node-workspace${subjectQuery}`),
        client.request<{ nodes: LibraryNode[] }>(`/node-library${subjectQuery}`),
      ])
      if (!isCurrent()) return
      setWorkspace(workspaceResult)
      setLoadedScope(scope)
      setNodes(libraryResult.nodes || [])
    } catch (requestError) {
      if (isCurrent()) setError(errorText(requestError))
    } finally {
      if (isCurrent()) setBusy(false)
    }
  }, [client, subjectQuery, scope])

  React.useEffect(() => { void refresh() }, [refresh])
  useRegisterPageRefresh(() => refresh())

  const message: Notify = (value, tone = 'success') => notify?.(value, tone)
  const mutate = async (path: string, init: RequestInit, success: string): Promise<boolean> => {
    if (mutationInFlight.current) return false
    mutationInFlight.current = true
    setBusy(true)
    try {
      await client.request(path + subjectQuery, init)
      if (currentScope.current === scope) { message(success); void refresh() }
      return true
    } catch (requestError) {
      if (currentScope.current === scope) setError(errorText(requestError))
      return false
    } finally {
      mutationInFlight.current = false
      if (currentScope.current === scope) setBusy(false)
    }
  }

  const groups = visibleWorkspace?.node_groups || []
  const outputs = visibleWorkspace?.subscription_outputs || []
  const counts: Partial<Record<WorkspaceTab, number>> = visibleWorkspace ? { library: nodes.length, groups: groups.length, outputs: outputs.length } : {}
  const tabLabel = (value: WorkspaceTab, label: string) => <>{label}{counts[value] !== undefined && <span className="nw-tab-count" aria-hidden="true">{counts[value]}</span>}</>

  return <Tabs value={tab} onValueChange={value => setTab(value as WorkspaceTab)} className="nw-workspace">
    <TabsList className={compactTabs ? 'nw-tabs nw-tabs-compact' : 'nw-tabs'} aria-label="节点工作台">
      <TabsTrigger value="library"><Search size={16} aria-hidden="true" />{tabLabel('library', '节点库')}</TabsTrigger>
      <TabsTrigger value="groups"><Layers3 size={16} aria-hidden="true" />{tabLabel('groups', '节点组')}</TabsTrigger>
      <TabsTrigger value="outputs"><CloudDownload size={16} aria-hidden="true" />{tabLabel('outputs', '组合订阅')}</TabsTrigger>
      {subscriptionPanel && <TabsTrigger value="subscription"><Link2 size={16} aria-hidden="true" />订阅链接</TabsTrigger>}
    </TabsList>
    {error && <div className="nw-alert" role="alert">{error}</div>}
    {tab === 'subscription' && subscriptionPanel
      ? <TabsContent value="subscription" className="nw-view">{subscriptionPanel}</TabsContent>
      : !visibleWorkspace
        ? <div className="nw-empty"><span>{busy ? '正在加载节点…' : '暂时无法加载节点'}</span></div>
        : <React.Fragment key={scope}>
          <TabsContent value="library" className="nw-view">
            <NodeLibraryPanel nodes={nodes} groups={groups} busy={busy} onCopy={async node => {
              try {
                const result = await client.request<{ url: string }>(`/node-library/share${subjectQuery}`, { method: 'POST', body: JSON.stringify({ node_id: node.id }) })
                await navigator.clipboard.writeText(result.url)
                message('节点链接已复制')
              } catch (requestError) { message(errorText(requestError), 'error') }
            }} onEdit={async (node, patch) => {
              await client.request(`/node-library/${node.id}${subjectQuery}`, { method: 'PATCH', body: JSON.stringify(patch) })
              void refresh()
            }} />
          </TabsContent>
          <TabsContent value="groups" className="nw-view">
            <NodeGroupsPanel workspace={visibleWorkspace} busy={busy} mutate={mutate} subjectQuery={subjectQuery} client={client} refresh={refresh} message={message} />
          </TabsContent>
          <TabsContent value="outputs" className="nw-view">
            <SubscriptionOutputsPanel workspace={visibleWorkspace} data={data} client={client} subjectQuery={subjectQuery} busy={busy} refresh={refresh} message={message} />
          </TabsContent>
        </React.Fragment>}
  </Tabs>
}
