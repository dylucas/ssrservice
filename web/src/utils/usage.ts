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

/** Totals plus the derived figures the dashboard shows above its chart. */
export interface UsageSummary extends UsageTotals {
  /** up + down over the window. */
  total: number
  /** How many days the window covers, for the daily average. */
  days: number
  /** total / days, rounded; 0 for an empty window. */
  average: number
  /** The busiest single day, up + down. 0 for an empty window. */
  peak: number
}

/**
 * summarizeTraffic derives the three figures above the dashboard chart.
 *
 * The average divides by the number of days actually returned rather than by a
 * nominal 30: a fresh install returns a short window, and dividing by 30 there
 * would report an average that no day matches. An empty window reports zeros so
 * the caller can hide the strip instead of printing "0 B / 0 B".
 */
export function summarizeTraffic(days: DailyUsage[]): UsageSummary {
  const totals = summarizeUsage(days)
  const total = totals.up + totals.down
  let peak = 0
  for (const day of days) {
    const sum = (day.up_bytes || 0) + (day.down_bytes || 0)
    if (sum > peak) peak = sum
  }
  return {
    ...totals,
    total,
    days: days.length,
    average: days.length > 0 ? Math.round(total / days.length) : 0,
    peak,
  }
}
