import type { InboundDaily, InboundStatusCounts } from '@/types'
import type { ChartSeries } from '@/utils/chart'

/**
 * Pure helpers for the inbound detail page. Keeping them out of the view lets
 * the chart and status-summary rules be tested without a DOM.
 */

/** The four status buckets, in the order the page shows them. */
export function inboundStatusItems(counts: InboundStatusCounts) {
  return [
    { status: 'active', label: '正常', value: counts.active },
    { status: 'disabled', label: '已禁用', value: counts.disabled },
    { status: 'expired', label: '已到期', value: counts.expired },
    { status: 'quota_exceeded', label: '超配额', value: counts.quota_exceeded },
  ]
}

/** Total users across the four buckets. */
export function inboundUserTotal(counts: InboundStatusCounts): number {
  return counts.active + counts.disabled + counts.expired + counts.quota_exceeded
}

/** The two chart series of an inbound's traffic series. */
export function inboundChart(daily: InboundDaily[]): {
  labels: string[]
  series: ChartSeries[]
} {
  return {
    // The server sends one row per day of the window, so the axis has exactly
    // as many labels as points and no quiet day is skipped.
    labels: daily.map((day) => day.day.slice(5)),
    series: [
      { name: '上传', variant: 'up', values: daily.map((day) => day.up_bytes) },
      { name: '下载', variant: 'down', values: daily.map((day) => day.down_bytes) },
    ],
  }
}

/** Share of the busiest user in the inbound's total traffic, 0..100. */
export function inboundTopShare(
  client: { up_bytes: number; down_bytes: number },
  totalUp: number,
  totalDown: number,
): number {
  const total = totalUp + totalDown
  if (total <= 0) return 0
  return Math.round(((client.up_bytes + client.down_bytes) / total) * 100)
}
