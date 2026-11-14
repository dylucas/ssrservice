import type { DailyUsage } from '@/types'

export interface UsageTotals {
  up: number
  down: number
}

/**
 * summarizeUsage sums one usage series.
 *
 * The dialog shows the totals of exactly the days returned, so this stays a
 * plain fold rather than reaching into the store: the endpoint already applied
 * the day window.
 */
export function summarizeUsage(days: DailyUsage[]): UsageTotals {
  let up = 0
  let down = 0
  for (const day of days) {
    up += day.up_bytes || 0
    down += day.down_bytes || 0
  }
  return { up, down }
}
