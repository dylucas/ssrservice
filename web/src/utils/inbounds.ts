import type { Inbound } from '@/types'

// Filtering and summarising for the inbound list.
//
// Unlike the client list, which pages and filters in SQL, GET /inbounds returns
// every inbound in one response (an inbound is a listener, not a row in a table
// that grows without bound). Narrowing the list therefore happens here, in a
// pure function, so the behaviour is unit tested instead of wired to a template.

/** Which rows the inbound list shows. */
export type InboundStatusFilter = 'all' | 'enabled' | 'disabled'

/** The view state that decides which inbounds are shown. */
export interface InboundFilter {
  status: InboundStatusFilter
  query: string
}

/**
 * inboundMatches reports whether one inbound satisfies the filter.
 *
 * The search covers the fields an operator recognises an inbound by - its
 * remark, listen address, port, method and share domain - and is
 * case-insensitive, because method names and domains are written in mixed case
 * while nobody types them that way.
 */
export function inboundMatches(item: Inbound, filter: InboundFilter): boolean {
  if (filter.status === 'enabled' && !item.enabled) return false
  if (filter.status === 'disabled' && item.enabled) return false
  const query = (filter.query ?? '').trim().toLowerCase()
  if (!query) return true
  const fields = [item.remark, item.listen, String(item.port), item.method, item.domain]
  return fields.some((field) => (field || '').toLowerCase().includes(query))
}

/** filterInbounds returns the inbounds that satisfy the filter, in list order. */
export function filterInbounds(items: Inbound[], filter: InboundFilter): Inbound[] {
  return items.filter((item) => inboundMatches(item, filter))
}

/** True when a filter is active, which changes the empty-state message. */
export function hasInboundFilter(filter: InboundFilter): boolean {
  return (filter.status ?? 'all') !== 'all' || (filter.query ?? '').trim() !== ''
}

/** Aggregate values shown above the list. */
export interface InboundTotals {
  total: number
  enabled: number
  clients: number
  up_bytes: number
  down_bytes: number
}

/**
 * summarizeInbounds adds up the visible rows.
 *
 * It is called with the filtered list on purpose: the summary sits above the
 * table and describes what the table is showing, so it must not disagree with it
 * while a filter is active.
 */
export function summarizeInbounds(items: Inbound[]): InboundTotals {
  const totals: InboundTotals = { total: 0, enabled: 0, clients: 0, up_bytes: 0, down_bytes: 0 }
  for (const item of items) {
    totals.total += 1
    if (item.enabled) totals.enabled += 1
    totals.clients += item.client_count || 0
    totals.up_bytes += item.up_bytes || 0
    totals.down_bytes += item.down_bytes || 0
  }
  return totals
}
