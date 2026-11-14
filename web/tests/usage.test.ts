import { describe, expect, it } from 'vitest'
import type { DailyUsage } from '../src/types'
import { summarizeUsage, usageRangeLabel } from '../src/utils/usage'
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

describe('usageRangeLabel', () => {
  it('describes the supported ranges', () => {
    expect(usageRangeLabel(7)).toBe('最近 7 天')
    expect(usageRangeLabel(30)).toBe('最近 30 天')
    expect(usageRangeLabel(90)).toBe('最近 90 天')
    expect(usageRangeLabel(365)).toBe('最近 30 天')
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
