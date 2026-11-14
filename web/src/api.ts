import { readRuntimeConfig } from './runtime-config'
import type { AuditResponse, DailyUsage } from './types'
import type { Client, ClientSummary } from './types'

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
    throw new ApiError(
      response.status,
      error?.code || 'request_failed',
      error?.message || response.statusText || 'request failed',
      error?.fields,
    )
  }
  return payload as T
}

export interface ListResponse<T> {
  items: T[]
  total: number
}

export const api = {
  login: (username: string, password: string) =>
    request<{ admin: any; expires_at: number }>('POST', '/auth/login', { username, password }),
  logout: () => request<{ ok: boolean }>('POST', '/auth/logout'),
  me: () => request<any>('GET', '/auth/me'),
  changePassword: (current_password: string, new_password: string) =>
    request<{ ok: boolean }>('PUT', '/auth/password', { current_password, new_password }),
  dashboard: () => request<any>('GET', '/dashboard'),
  system: () => request<any>('GET', '/system'),
  inbounds: () => request<any>('GET', '/inbounds'),
  createInbound: (input: unknown) => request<any>('POST', '/inbounds', input),
  updateInbound: (id: number, input: unknown) => request<any>('PUT', `/inbounds/${id}`, input),
  deleteInbound: (id: number) => request<{ ok: boolean }>('DELETE', `/inbounds/${id}`),
  toggleInbound: (id: number, enabled: boolean) =>
    request<{ ok: boolean }>('POST', `/inbounds/${id}/toggle`, { enabled }),
  restartInbound: (id: number) => request<{ ok: boolean }>('POST', `/inbounds/${id}/restart`),
  clients: (inboundId?: number) =>
    request<ListResponse<ClientSummary>>(
      'GET',
      inboundId ? `/clients?inbound_id=${inboundId}` : '/clients',
    ),
  client: (id: number) => request<Client>('GET', `/clients/${id}`),
  clientsOf: (inboundId: number) =>
    request<ListResponse<ClientSummary>>('GET', `/inbounds/${inboundId}/clients`),
  createClient: (inboundId: number, input: unknown) =>
    request<any>('POST', `/inbounds/${inboundId}/clients`, input),
  updateClient: (id: number, input: unknown) => request<any>('PUT', `/clients/${id}`, input),
  deleteClient: (id: number) => request<{ ok: boolean }>('DELETE', `/clients/${id}`),
  toggleClient: (id: number, enabled: boolean) =>
    request<{ ok: boolean }>('POST', `/clients/${id}/toggle`, { enabled }),
  resetClient: (id: number) => request<{ ok: boolean }>('POST', `/clients/${id}/reset`),
  resetClientToken: (id: number) => request<any>('POST', `/clients/${id}/token`),
  kickClient: (id: number) => request<{ ok: boolean }>('POST', `/clients/${id}/kick`),
  clientLink: (id: number, host?: string) =>
    request<any>('GET', `/clients/${id}/link${host ? `?host=${encodeURIComponent(host)}` : ''}`),
  clientUsage: (id: number, days = 30) =>
    request<{ items: DailyUsage[] }>('GET', `/clients/${id}/usage?days=${days}`),
  settings: () => request<any>('GET', '/settings'),
  updateSettings: (input: unknown) => request<any>('PUT', '/settings', input),
  backupURL: () => apiURL('/backup'),
  audit: (params: Record<string, string | number>) => {
    const query = new URLSearchParams()
    for (const [key, value] of Object.entries(params)) query.set(key, String(value))
    return request<AuditResponse>('GET', `/audit?${query.toString()}`)
  },
  subURL: (path: string) => `${basePath}${path}`,
}
