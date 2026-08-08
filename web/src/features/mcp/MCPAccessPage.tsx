import { OAuthClientList } from './OAuthClientList'
import { OAuthGrantList } from './OAuthGrantList'
import type { MCPAccessPageProps } from './types'

// MCPAccessPage is the OAuth/MCP management surface extracted from the main
// dashboard: OAuth 2.1 client identity records and the user consent grants.
// Client records never carry permission ceilings; grants are the authorization
// source. Web copy is concise and outcome-focused.
export function MCPAccessPage({ requestV2, notify, confirm }: MCPAccessPageProps) {
  return (
    <>
      <div className="automation-grid">
        <OAuthClientList requestV2={requestV2} notify={notify} confirm={confirm} />
      </div>
      <OAuthGrantList requestV2={requestV2} notify={notify} confirm={confirm} />
    </>
  )
}
