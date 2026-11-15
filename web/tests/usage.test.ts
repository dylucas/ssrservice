import { describe, expect, it } from 'vitest'
import type { DailyUsage } from '../src/types'
import { summarizeTraffic, summarizeUsage } from '../src/utils/usage'
import { formatDateTime } from '../src/utils/format'

function day(overrides: Partial<DailyUsage>): DailyUsage {
  return { day: '2026-01-01', up_bytes: 0, down_bytes: 0, ...overrides }
}

describe('summarizeUsage', () => {
  it('sums the upload and download columns', () => {
    const totals = summarizeUsage([
      day({ up_bytes: 10, down_bytes: 20 }),
      day({ up_bytes: 1, down_bytes: 2 }),
    ])
    expect(totals).toEqual({ up: 11, down: 22 })
  })

  it('treats an empty series as zero', () => {
    expect(summarizeUsage([])).toEqual({ up: 0, down: 0 })
  })

  it('treats missing numbers as zero', () => {
    const totals = summarizeUsage([
      { day: '2026-01-01' } as DailyUsage,
      day({ up_bytes: 5, down_bytes: 6 }),
    ])
    expect(totals).toEqual({ up: 5, down: 6 })
  })
})

describe('formatDateTime', () => {
  it('renders a zero timestamp as a dash', () => {
    expect(formatDateTime(0)).toBe('—')
    expect(formatDateTime(null)).toBe('—')
  })

  it('renders a unix timestamp as local YYYY-MM-DD HH:mm:ss', () => {
    const stamp = Math.floor(new Date(2026, 0, 2, 3, 4, 5).getTime() / 1000)
    expect(formatDateTime(stamp)).toBe('2026-01-02 03:04:05')
  })
})

describe('summarizeTraffic', () => {
  const window = [
    day({ day: '2026-01-01', up_bytes: 100, down_bytes: 200 }),
    day({ day: '2026-01-02', up_bytes: 50, down_bytes: 50 }),
    day({ day: '2026-01-03', up_bytes: 0, down_bytes: 0 }),
  ]

  it('adds up both directions and the total', () => {
    const summary = summarizeTraffic(window)
    expect(summary.up).toBe(150)
    expect(summary.down).toBe(250)
    expect(summary.total).toBe(400)
    expect(summary.days).toBe(3)
  })

  it('averages over the days actually returned', () => {
    // 400 over 3 returned days, not over a nominal 30.
    expect(summarizeTraffic(window).average).toBe(133)
  })

  it('reports the busiest day', () => {
    expect(summarizeTraffic(window).peak).toBe(300)
  })

  it('reports zeros for an empty window', () => {
    // A fresh install has no history; the caller hides the strip rather than
    // printing "0 B / 0 B / 0 B".
    const empty = summarizeTraffic([])
    expect(empty.total).toBe(0)
    expect(empty.days).toBe(0)
    expect(empty.average).toBe(0)
    expect(empty.peak).toBe(0)
  })

  it('does not divide by zero', () => {
    expect(Number.isFinite(summarizeTraffic([]).average)).toBe(true)
  })
})
