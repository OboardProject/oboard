import type { OAuthClient, OAuthGrant } from './types'

export type RequestV2 = (path: string, init?: RequestInit) => Promise<any>

export function listClients(requestV2: RequestV2): Promise<OAuthClient[]> {
  return requestV2('/oauth-clients')
}

export function listGrants(requestV2: RequestV2): Promise<OAuthGrant[]> {
  return requestV2('/oauth-grants')
}

export function createClient(requestV2: RequestV2, input: { client_name: string; redirect_uris: string[]; metadata_uri?: string }): Promise<OAuthClient> {
  return requestV2('/oauth-clients', { method: 'POST', body: JSON.stringify(input) })
}

export function updateClient(requestV2: RequestV2, id: string, input: { client_name?: string; redirect_uris?: string[]; metadata_uri?: string; enabled?: boolean }): Promise<OAuthClient> {
  return requestV2(`/oauth-clients/${id}`, { method: 'PATCH', body: JSON.stringify(input) })
}

export function deleteClient(requestV2: RequestV2, id: string): Promise<{ deleted: boolean }> {
  return requestV2(`/oauth-clients/${id}`, { method: 'DELETE' })
}

export function revokeGrant(requestV2: RequestV2, id: string): Promise<{ revoked: boolean }> {
  return requestV2(`/oauth-grants/${id}`, { method: 'DELETE' })
}

export function revokeOfflineAccess(requestV2: RequestV2, id: string): Promise<{ offline_access: boolean }> {
  return requestV2(`/oauth-grants/${id}/revoke-offline-access`, { method: 'POST' })
}
