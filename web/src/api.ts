import { readRuntimeConfig } from './runtime-config'
import type {
  AuditResponse,
  Client,
  ClientInput,
  ClientLinkResponse,
  ClientListParams,
  ClientListResponse,
  Dashboard,
  ImportResult,
  Inbound,
  InboundInput,
  InboundsResponse,
  LoginResponse,
  LoginResult,
  MeResponse,
  ResetTokenResponse,
  Settings,
  TOTPDisableResponse,
  TOTPEnableResponse,
  TOTPRecoveryCodesResponse,
  TOTPSetupResponse,
  UsageResponse,
} from './types'

const config = readRuntimeConfig()
export const basePath = config.basePath && config.basePath !== '/' ? config.basePath.replace(/\/$/, '') : ''
export const version = config.version || 'dev'

function apiURL(path: string): string {
  return `${basePath}/api/v1${path}`
}

export class ApiError extends Error {
  code: string
  status: number
  fields?: Record<string, string>

  constructor(status: number, code: string, message: string, fields?: Record<string, string>) {
    super(message)
    this.status = status
    this.code = code
    this.fields = fields
  }
}

// Session-level failures are handled centrally instead of in every view: a
// view only knows how to show a toast, but an expired session has to leave the
// page. api.ts must not import the router (router imports basePath from here),
// so main.ts registers the navigation callbacks after creating the router.
type SessionHandler = () => void

let unauthorizedHandler: SessionHandler | null = null
let passwordRequiredHandler: SessionHandler | null = null

export function setUnauthorizedHandler(handler: SessionHandler | null): void {
  unauthorizedHandler = handler
}

export function setPasswordRequiredHandler(handler: SessionHandler | null): void {
  passwordRequiredHandler = handler
}

/**
 * The login endpoints' own 401 means bad credentials or a bad second factor,
 * not an expired session: redirecting on it would drop the password step and the
 * challenge before the login view can show the error.
 */
function isLoginRequest(path: string): boolean {
  return path === '/auth/login' || path === '/auth/login/totp'
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = {
    method,
    credentials: 'same-origin',
    headers: {},
  }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  const response = await fetch(apiURL(path), init)
  if (response.status === 204) {
    return undefined as T
  }
  const text = await response.text()
  let payload: any = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }
  if (!response.ok) {
    const error = payload?.error
    const apiError = new ApiError(
      response.status,
      error?.code || 'request_failed',
      error?.message || response.statusText || 'request failed',
      error?.fields,
    )
    // Notify before throwing so the view still sees the error (its spinner has
    // to stop) while the redirect runs in the background.
    if (apiError.status === 401 && !isLoginRequest(path)) {
      unauthorizedHandler?.()
    } else if (apiError.status === 403 && apiError.code === 'password_change_required') {
      passwordRequiredHandler?.()
    }
    throw apiError
  }
  return payload as T
}


/**
 * clientListQuery renders the paged client list query. Empty filters are left
 * out so the URL only says what the operator actually selected.
 */
function clientListQuery(params: ClientListParams): string {
  const search = new URLSearchParams()
  if (params.inbound_id) search.set('inbound_id', String(params.inbound_id))
  if (params.status) search.set('status', params.status)
  if (params.q) search.set('q', params.q)
  search.set('limit', String(params.limit))
  search.set('offset', String(params.offset))
  return search.toString()
}

export const api = {
  login: (username: string, password: string) =>
    request<LoginResult>('POST', '/auth/login', { username, password }),
  loginTOTP: (challenge: string, code: string) =>
    request<LoginResponse>('POST', '/auth/login/totp', { challenge, code }),
  totpSetup: (password: string) =>
    request<TOTPSetupResponse>('POST', '/auth/totp/setup', { password }),
  totpEnable: (code: string) => request<TOTPEnableResponse>('POST', '/auth/totp/enable', { code }),
  totpDisable: (password: string, code: string) =>
    request<TOTPDisableResponse>('POST', '/auth/totp/disable', { password, code }),
  totpRecoveryCodes: (password: string, code: string) =>
    request<TOTPRecoveryCodesResponse>('POST', '/auth/totp/recovery-codes', { password, code }),
  logout: () => request<{ ok: boolean }>('POST', '/auth/logout'),
  me: () => request<MeResponse>('GET', '/auth/me'),
  changePassword: (current_password: string, new_password: string) =>
    request<{ ok: boolean }>('PUT', '/auth/password', { current_password, new_password }),
  dashboard: () => request<Dashboard>('GET', '/dashboard'),
  inbounds: () => request<InboundsResponse>('GET', '/inbounds'),
  createInbound: (input: InboundInput) => request<Inbound>('POST', '/inbounds', input),
  updateInbound: (id: number, input: InboundInput) => request<Inbound>('PUT', `/inbounds/${id}`, input),
  deleteInbound: (id: number) => request<{ ok: boolean }>('DELETE', `/inbounds/${id}`),
  toggleInbound: (id: number, enabled: boolean) =>
    request<{ ok: boolean }>('POST', `/inbounds/${id}/toggle`, { enabled }),
  restartInbound: (id: number) => request<{ ok: boolean }>('POST', `/inbounds/${id}/restart`),
  clients: (params: ClientListParams) =>
    request<ClientListResponse>('GET', `/clients?${clientListQuery(params)}`),
  client: (id: number) => request<Client>('GET', `/clients/${id}`),
  createClient: (inboundId: number, input: ClientInput) =>
    request<Client>('POST', `/inbounds/${inboundId}/clients`, input),
  updateClient: (id: number, input: ClientInput) => request<Client>('PUT', `/clients/${id}`, input),
  deleteClient: (id: number) => request<{ ok: boolean }>('DELETE', `/clients/${id}`),
  toggleClient: (id: number, enabled: boolean) =>
    request<{ ok: boolean }>('POST', `/clients/${id}/toggle`, { enabled }),
  resetClient: (id: number) => request<{ ok: boolean }>('POST', `/clients/${id}/reset`),
  resetClientToken: (id: number) => request<ResetTokenResponse>('POST', `/clients/${id}/token`),
  kickClient: (id: number) => request<{ ok: boolean }>('POST', `/clients/${id}/kick`),
  clientLink: (id: number, host?: string) =>
    request<ClientLinkResponse>('GET', `/clients/${id}/link${host ? `?host=${encodeURIComponent(host)}` : ''}`),
  clientUsage: (id: number, days = 30) =>
    request<UsageResponse>('GET', `/clients/${id}/usage?days=${days}`),
  settings: () => request<Settings>('GET', '/settings'),
  updateSettings: (input: Settings) => request<Settings>('PUT', '/settings', input),
  backupURL: () => apiURL('/backup'),
  exportClientsURL: (inboundId?: number) =>
    apiURL(inboundId ? `/export/clients?inbound_id=${inboundId}` : '/export/clients'),
  importClients: (inboundId: number, payload: unknown) =>
    request<ImportResult>('POST', `/inbounds/${inboundId}/clients/import`, payload),
  audit: (params: Record<string, string | number>) => {
    const query = new URLSearchParams()
    for (const [key, value] of Object.entries(params)) query.set(key, String(value))
    return request<AuditResponse>('GET', `/audit?${query.toString()}`)
  },
}
