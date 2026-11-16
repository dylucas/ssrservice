import type { Client, ClientListParams, ClientSummary } from '@/types'

export type ClientStatusFilter = 'all' | ClientSummary['status'] | 'expiring' | 'idle'

/** Number of rows the client list requests per page. */
export const CLIENT_PAGE_SIZE = 20

/** The view state that decides which slice of the client list to fetch. */
export interface ClientListQuery {
  inboundId?: number
  status?: ClientStatusFilter
  query?: string
  page: number
  pageSize: number
}

/**
 * buildClientListParams turns the view state into the query parameters of the
 * client list endpoints.
 *
 * Filtering and paging happen in SQL, so the view sends only what the operator
 * selected: a value that means "no filter" is omitted instead of sent empty.
 * The page is clamped to at least one, so a stale page number cannot turn into
 * a negative offset.
 */
export function buildClientListParams(input: ClientListQuery): ClientListParams {
  const pageSize = input.pageSize > 0 ? Math.trunc(input.pageSize) : CLIENT_PAGE_SIZE
  const page = Math.max(1, Math.trunc(input.page) || 1)
  const params: ClientListParams = {
    limit: pageSize,
    offset: (page - 1) * pageSize,
  }
  if (input.inboundId && input.inboundId > 0) {
    params.inbound_id = input.inboundId
  }
  const status = input.status ?? 'all'
  if (status !== 'all') {
    params.status = status
  }
  const query = (input.query ?? '').trim()
  if (query) {
    params.q = query
  }
  return params
}

/** True when a filter is active, which changes the empty-list message. */
export function hasClientFilter(input: Pick<ClientListQuery, 'status' | 'query'>): boolean {
  return (input.status ?? 'all') !== 'all' || (input.query ?? '').trim() !== ''
}

/** Total number of pages, at least 1. */
export function lastPage(total: number, pageSize: number): number {
  if (pageSize <= 0) return 1
  return Math.max(1, Math.ceil(total / pageSize))
}

/**
 * toSummary projects a single-client response onto the list shape.
 *
 * The list state must never hold credentials: the detail endpoint returns psk,
 * sub_token and the share links, and spreading it into a row would keep those
 * secrets in memory for no reason. Fields are copied explicitly so a new secret
 * added to the detail type cannot leak into the list by accident.
 */
export function toSummary(client: Client): ClientSummary {
  return {
    id: client.id,
    inbound_id: client.inbound_id,
    name: client.name,
    enabled: client.enabled,
    quota_bytes: client.quota_bytes,
    up_bytes: client.up_bytes,
    down_bytes: client.down_bytes,
    expires_at: client.expires_at,
    ip_limit: client.ip_limit,
    reset_policy: client.reset_policy,
    reset_day: client.reset_day,
    note: client.note,
    status: client.status,
    used_bytes: client.used_bytes,
    remaining_bytes: client.remaining_bytes,
    online_ips: client.online_ips,
    inbound_remark: client.inbound_remark,
    method: client.method,
  }
}
