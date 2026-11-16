import { describe, expect, it } from 'vitest'
import { inboundChart, inboundStatusItems, inboundTopShare, inboundUserTotal } from '../src/utils/inbound'
import type { InboundDaily, InboundStatusCounts } from '../src/types'

describe('inbound status summary', () => {
  const counts: InboundStatusCounts = {
    active: 3, disabled: 1, expired: 2, quota_exceeded: 4,
  }

  it('lists all four buckets in a stable order', () => {
    const items = inboundStatusItems(counts)
    expect(items.map((item) => item.status)).toEqual([
      'active', 'disabled', 'expired', 'quota_exceeded',
    ])
    expect(items.map((item) => item.value)).toEqual([3, 1, 2, 4])
    // Each label is also what the client-list filter understands.
    for (const item of items) expect(item.label.length).toBeGreaterThan(0)
  })

  it('sums to the total user count', () => {
    expect(inboundUserTotal(counts)).toBe(10)
    expect(inboundUserTotal({ active: 0, disabled: 0, expired: 0, quota_exceeded: 0 })).toBe(0)
  })
})

describe('inbound traffic chart', () => {
  const daily: InboundDaily[] = [
    { day: '2026-11-14', up_bytes: 0, down_bytes: 0 },
    { day: '2026-11-15', up_bytes: 100, down_bytes: 200 },
    { day: '2026-11-16', up_bytes: 50, down_bytes: 300 },
  ]

  it('keeps one label per day, including quiet days', () => {
    const chart = inboundChart(daily)
    expect(chart.labels).toEqual(['11-14', '11-15', '11-16'])
    expect(chart.series).toHaveLength(2)
    expect(chart.series[0].values).toEqual([0, 100, 50])
    expect(chart.series[1].values).toEqual([0, 200, 300])
  })

  it('handles an empty window without inventing labels', () => {
    const chart = inboundChart([])
    expect(chart.labels).toEqual([])
    expect(chart.series[0].values).toEqual([])
  })
})

describe('inbound top share', () => {
  it('is a rounded percentage of the inbound total', () => {
    expect(inboundTopShare({ up_bytes: 250, down_bytes: 250 }, 1000, 1000)).toBe(25)
    expect(inboundTopShare({ up_bytes: 1000, down_bytes: 1000 }, 1000, 1000)).toBe(100)
  })

  it('is zero when the inbound has no traffic', () => {
    expect(inboundTopShare({ up_bytes: 0, down_bytes: 0 }, 0, 0)).toBe(0)
  })
})
