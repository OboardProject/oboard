import type { createAPIClientFactory } from '../api-client'

export type APIClient = ReturnType<ReturnType<typeof createAPIClientFactory>>
