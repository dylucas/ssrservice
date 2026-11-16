import type { AccessDaily, AccessEvent } from '@/types'
import type { ChartSeries } from '@/utils/chart'

// Helpers for the access analytics page.
//
// The overview and the event list share one vocabulary (outcomes, reasons,
// transports), and the raw value the server stores is a stable identifier rather
// than a label: the mapping lives here so the tables, the filter dropdown and the
// tags cannot disagree about what "blocked" means.

export type OutcomeTone = 'success' | 'warning' | 'danger' | 'info'

/** outcomeLabel is the Chinese label for a stored outcome. */
export function outcomeLabel(outcome: string): string {
  switch (outcome) {
    case 'allowed':
      return '放行'
    case 'blocked':
      return '拦截'
    case 'failed':
      return '失败'
    default:
      return outcome || '未知'
  }
}

/** outcomeTagType is the Element tag colour for an outcome. */
export function outcomeTagType(outcome: string): OutcomeTone {
  switch (outcome) {
    case 'allowed':
      return 'success'
    case 'blocked':
      return 'warning'
    case 'failed':
      return 'danger'
    default:
      return 'info'
  }
}

/**
 * reasonLabel explains why a request was blocked or failed. An unknown reason is
 * shown verbatim instead of being hidden: a new server-side value is better
 * visible than invisible.
 */
export function reasonLabel(reason: string): string {
  switch (reason) {
    case '':
      return ''
    case 'disabled':
      return '用户已禁用'
    case 'expired':
      return '已到期'
    case 'quota':
      return '超出配额'
    case 'ip_limit':
      return 'IP 数超限'
    case 'sessions':
      return '会话数超限'
    case 'policy':
      return '目标被策略拦截'
    case 'resolve':
      return '域名解析失败'
    case 'dial':
      return '连接目标失败'
    default:
      return reason
  }
}

/** reasonText is the cell text: the outcome's explanation, or a dash. */
export function reasonText(event: Pick<AccessEvent, 'outcome' | 'reason'>): string {
  if (event.reason) return reasonLabel(event.reason)
  return event.outcome === 'allowed' ? '' : '—'
}

/** networkLabel is the transport's display name. */
export function networkLabel(network: string): string {
  return network === 'udp' ? 'UDP' : network === 'tcp' ? 'TCP' : network || '—'
}

/**
 * ACCESS_OTHER_HOST is the rollup row that carries every destination folded away
 * by the server-side cap (internal/model.AccessMaxHostsPerKey). The raw event
 * list never contains it, because a real record always names the destination.
 */
export const ACCESS_OTHER_HOST = '<other>'

/**
 * destinationLabel renders a rollup destination. The overflow row is a real row
 * with an internal name, so it is presented as what it means rather than as a
 * destination nobody could have visited.
 */
export function destinationLabel(host: string): string {
  return host === ACCESS_OTHER_HOST ? '其他目标（超出单日上限）' : host
}

/** formatHostPort renders a destination the way a client would type it. */
export function formatHostPort(host: string, port: number): string {
  if (!host) return port > 0 ? `:${port}` : '—'
  // IPv6 literals need brackets before a port can be appended.
  const rendered = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
  return port > 0 ? `${rendered}:${port}` : rendered
}

/**
 * formatElapsed renders an access duration. A connection's duration is measured
 * in milliseconds and is often under a second, where the shared seconds
 * formatter would print "0 秒" for every row.
 */
export function formatElapsed(ms: number): string {
  const value = Math.max(0, Math.round(Number(ms) || 0))
  if (value < 1000) return `${value} ms`
  // Round once, in tenths of a second, and carry from there: rounding the
  // remainder on its own printed "1 分 60 秒" for 119999 ms and "60.0 秒" for
  // 59999 ms, because 60 seconds is not a valid remainder.
  const tenths = Math.round(value / 100)
  if (tenths < 600) {
    return tenths < 100 ? `${(tenths / 10).toFixed(1)} 秒` : `${Math.round(tenths / 10)} 秒`
  }
  const totalSeconds = Math.round(value / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return seconds > 0 ? `${minutes} 分 ${seconds} 秒` : `${minutes} 分`
}

/** windowLabel names a summary window. */
export function windowLabel(days: number): string {
  return days > 0 ? `最近 ${days} 天` : '窗口'
}

/** accessWindowOptions are the windows the page offers. */
export const ACCESS_WINDOWS = [7, 30, 90] as const

// Retention shown in the settings toggle. The values are the server's
// (internal/service/access.go); the settings page mirrors them so the operator
// does not have to read the API documentation to know what enabling costs.
export const ACCESS_RAW_DAYS = 7
export const ACCESS_AGGREGATE_DAYS = 90

/**
 * hasAccessFilter reports whether the event list is narrowed at all.
 *
 * It drives the empty state: "nothing matched" has a way out, "nothing recorded"
 * means the feature is off or nobody has connected yet.
 */
export function hasAccessFilter(input: {
  outcome?: string
  network?: string
  query?: string
  clientId?: number | null
  inboundId?: number | null
  /** Either bound of the window: an absolute instant or a YYYY-MM-DD day. */
  since?: number | string | null
  until?: number | string | null
}): boolean {
  return Boolean(
    (input.outcome && input.outcome !== 'all') ||
      (input.network && input.network !== 'all') ||
      (input.query && input.query.trim() !== '') ||
      input.clientId ||
      input.inboundId ||
      input.since ||
      input.until,
  )
}

/**
 * summarizeAccess derives the four headline figures from the overview. The
 * server already sends the totals; this exists so the page can render a
 * consistent "blocked share" without recomputing it in the template.
 */
export function blockedShare(summary: { blocked_hits: number; total_hits: number }): number {
  if (!summary.total_hits) return 0
  return Math.round((summary.blocked_hits / summary.total_hits) * 100)
}

/**
 * accessTrend is the daily series the page draws: total requests against the
 * ones that did not get through. Both come from the same rollup row, so the two
 * lines always describe the same window.
 */
export function accessTrend(daily: AccessDaily[]): { labels: string[]; series: ChartSeries[] } {
  return {
    labels: daily.map((day) => day.day.slice(5)),
    series: [
      { name: '访问次数', variant: 'up', values: daily.map((day) => day.hits) },
      {
        name: '拦截 / 失败次数',
        variant: 'down',
        values: daily.map((day) => day.blocked_hits + day.failed_hits),
      },
    ],
  }
}

/** formatCount renders an axis tick or tooltip value for a request count. */
export function formatCount(value: number): string {
  return `${Math.round(Number(value) || 0).toLocaleString()} 次`
}

/**
 * inboundScopeLabel describes a view narrowed to one inbound but not to a user.
 *
 * The client scope already names an inbound, so the page uses scopeLabel when a
 * user is selected; this variant covers the "show me this node" case, where
 * naming a user would be wrong.
 */
export function inboundScopeLabel(days: number, inboundName: string): string {
  return `${windowLabel(days)} · 仅入站 ${inboundName || '未知入站'}`
}

/** scopeLabel says what a set of figures covers, including a client filter. */
export function scopeLabel(days: number, clientName?: string, inboundName?: string): string {
  const window = windowLabel(days)
  if (!clientName) return window
  return inboundName ? `${window} · 仅 ${clientName} @ ${inboundName}` : `${window} · 仅 ${clientName}`
}
