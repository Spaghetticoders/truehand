import type { Api } from './types'
import { HttpApi } from './http'

export * from './types'
export { ApiError } from './http'

// The real server is the default: only an explicit mock build can ship the fixture.
export const api: Api = import.meta.env.VITE_TRUEHAND_API === 'mock' ? new (await import('./fixture')).FixtureApi() : new HttpApi()
