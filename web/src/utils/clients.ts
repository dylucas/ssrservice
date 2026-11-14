import type { Client, ClientSummary } from '@/types'

export type ClientStatusFilter = 'all' | ClientSummary['status']

export interface ClientFilter {
  query?: string
  status?: ClientStatusFilter
}

/**
 * Filters clients by a free-text query (name, note, inbound remark, method)
 * and by computed status. The query is case-insensitive and trimmed.
 */
export function filterClients(
  clients: ClientSummary[],
  filter: ClientFilter = {},
): ClientSummary[] {
  const query = (filter.query ?? '').trim().toLowerCase()
  const status = filter.status ?? 'all'
  return clients.filter((client) => {
    if (status !== 'all' && client.status !== status) return false
    if (!query) return true
    const haystack = [client.name, client.note, client.inbound_remark, client.method]
      .filter((value): value is string => typeof value === 'string' && value.length > 0)
      .join(' ')
      .toLowerCase()
    return haystack.includes(query)
  })
}

/** Returns one page of items, clamped to valid bounds. */
export function paginate<T>(items: T[], page: number, pageSize: number): T[] {
  if (pageSize <= 0) return items
  const current = Math.min(Math.max(1, page), lastPage(items.length, pageSize))
  const start = (current - 1) * pageSize
  return items.slice(start, start + pageSize)
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
