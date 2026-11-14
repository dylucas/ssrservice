import type { ClientSummary } from '@/types'

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
