export function formatBytes(value: number | null | undefined): string {
  const bytes = Number(value || 0)
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const size = bytes / Math.pow(1024, index)
  return `${size >= 100 ? size.toFixed(0) : size.toFixed(2)} ${units[index]}`
}

export function formatDate(unix: number | null | undefined): string {
  if (!unix) return '永不过期'
  const date = new Date(unix * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function formatDuration(seconds: number | null | undefined): string {
  let value = Math.max(0, Math.floor(Number(seconds || 0)))
  const days = Math.floor(value / 86400)
  value %= 86400
  const hours = Math.floor(value / 3600)
  value %= 3600
  const minutes = Math.floor(value / 60)
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${minutes} 分`
  // Under a minute the minute field would read "0 分", which on a freshly
  // started panel looks like missing data rather than "just started".
  if (minutes > 0) return `${minutes} 分`
  return `${value % 60} 秒`
}

export function percent(used: number, total: number): number {
  if (!total || total <= 0) return 0
  return Math.min(100, Math.round((used / total) * 100))
}

export const statusMeta: Record<string, { label: string; type: 'success' | 'info' | 'warning' | 'danger' }> = {
  active: { label: '正常', type: 'success' },
  disabled: { label: '已禁用', type: 'info' },
  expired: { label: '已到期', type: 'warning' },
  quota_exceeded: { label: '超配额', type: 'danger' },
  // Filter-only values: the dashboard links here, and a row's own status is
  // never one of these. They need labels so the filter dropdown and the chip in
  // the table header can describe them.
  expiring: { label: '即将到期', type: 'warning' },
  idle: { label: '从未连接', type: 'info' },
}

export function quotaLabel(client: { quota_bytes: number; used_bytes: number }): string {
  if (!client.quota_bytes) return '不限'
  return `${formatBytes(client.used_bytes)} / ${formatBytes(client.quota_bytes)}`
}

export async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value)
    return
  }
  const area = document.createElement('textarea')
  area.value = value
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  document.execCommand('copy')
  document.body.removeChild(area)
}

/** formatDateTime renders a unix timestamp as YYYY-MM-DD HH:mm:ss in local time. */
export function formatDateTime(unix: number | null | undefined): string {
  if (!unix) return '—'
  const date = new Date(unix * 1000)
  const pad = (value: number) => String(value).padStart(2, '0')
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ` +
    `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
  )
}

/**
 * describeExpiry turns an expiry timestamp into something scannable.
 *
 * The list used to print an absolute date in every row, which is precise but
 * useless for the question the column exists to answer ("does this expire
 * soon?"). The absolute date stays available as the cell's title.
 *
 *   describeExpiry(0)                -> { text: '永不过期', tone: 'none' }
 *   describeExpiry(now + 30d)        -> { text: '30 天后到期', tone: 'ok' }
 *   describeExpiry(now + 5d)         -> { text: '5 天后到期', tone: 'soon' }
 *   describeExpiry(now - 2d)         -> { text: '已过期 2 天', tone: 'expired' }
 *
 * `now` is injectable so the boundaries are testable without freezing time.
 */
export function describeExpiry(
  unix: number | null | undefined,
  now: number = Math.floor(Date.now() / 1000),
): { text: string; tone: 'none' | 'ok' | 'soon' | 'expired' } {
  const expires = Number(unix || 0)
  if (!expires) return { text: '永不过期', tone: 'none' }
  const diff = expires - now
  if (diff <= 0) {
    const days = Math.max(1, Math.ceil(-diff / 86400))
    return { text: `已过期 ${days} 天`, tone: 'expired' }
  }
  // Round a partial day up so "1 天后" never means "in an hour".
  const days = Math.ceil(diff / 86400)
  // A week is the window an operator can still act in (renew, extend, warn);
  // the text is the same either side of it and only the emphasis changes.
  return { text: `${days} 天后到期`, tone: days <= 7 ? 'soon' : 'ok' }
}
