export function formatBytes(value: number | null | undefined): string {
  const bytes = Number(value || 0)
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const size = bytes / Math.pow(1024, index)
  return `${size >= 100 ? size.toFixed(0) : size.toFixed(2)} ${units[index]}`
}

export function formatNumber(value: number | null | undefined): string {
  return Number(value || 0).toLocaleString('zh-CN')
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
  return `${minutes} 分`
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
