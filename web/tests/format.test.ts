import { describe, expect, it } from 'vitest'
import {
  describeExpiry,
  formatBytes,
  formatDate,
  formatDuration,
  percent,
  quotaLabel,
} from '../src/utils/format'

describe('formatBytes', () => {
  it('handles empty values', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(undefined)).toBe('0 B')
    expect(formatBytes(null)).toBe('0 B')
  })

  it('formats units', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1.00 KB')
    expect(formatBytes(1024 * 1024)).toBe('1.00 MB')
    expect(formatBytes(5 * 1024 ** 3)).toBe('5.00 GB')
    expect(formatBytes(120 * 1024)).toBe('120 KB')
  })
})

describe('formatDate', () => {
  it('shows never for zero', () => {
    expect(formatDate(0)).toBe('永不过期')
  })

  it('formats unix seconds', () => {
    const value = formatDate(1700000000)
    expect(value).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
  })
})

describe('formatDuration', () => {
  it('formats days', () => {
    expect(formatDuration(90061)).toBe('1 天 1 小时')
  })

  it('formats hours and minutes', () => {
    expect(formatDuration(3660)).toBe('1 小时 1 分')
    expect(formatDuration(120)).toBe('2 分')
  })

  it('shows seconds under a minute', () => {
    // "0 分" on a freshly started panel reads as missing data.
    expect(formatDuration(59)).toBe('59 秒')
    expect(formatDuration(1)).toBe('1 秒')
    expect(formatDuration(0)).toBe('0 秒')
  })
})

describe('percent', () => {
  it('clamps and handles unlimited', () => {
    expect(percent(50, 100)).toBe(50)
    expect(percent(200, 100)).toBe(100)
    expect(percent(5, 0)).toBe(0)
  })
})

describe('quotaLabel', () => {
  it('describes unlimited quota', () => {
    expect(quotaLabel({ quota_bytes: 0, used_bytes: 100 })).toBe('不限')
    expect(quotaLabel({ quota_bytes: 2048, used_bytes: 1024 })).toBe('1.00 KB / 2.00 KB')
  })
})

describe('describeExpiry', () => {
  const now = 1_700_000_000
  const day = 86400

  it('marks a permanent account', () => {
    expect(describeExpiry(0, now)).toEqual({ text: '永不过期', tone: 'none' })
    expect(describeExpiry(null, now)).toEqual({ text: '永不过期', tone: 'none' })
  })

  it('counts days until expiry', () => {
    expect(describeExpiry(now + 30 * day, now)).toEqual({ text: '30 天后到期', tone: 'ok' })
    expect(describeExpiry(now + 1 * day, now)).toEqual({ text: '1 天后到期', tone: 'soon' })
  })

  it('flags the last week, where the operator can still act', () => {
    expect(describeExpiry(now + 7 * day, now).tone).toBe('soon')
    expect(describeExpiry(now + 8 * day, now).tone).toBe('ok')
  })

  it('reports an elapsed expiry as a positive number of days overdue', () => {
    expect(describeExpiry(now - 2 * day, now)).toEqual({ text: '已过期 2 天', tone: 'expired' })
    // Something that expired a minute ago is not "0 天".
    expect(describeExpiry(now - 60, now)).toEqual({ text: '已过期 1 天', tone: 'expired' })
  })

  it('rounds a partial day up, so "1 天后" never means "in an hour"', () => {
    expect(describeExpiry(now + 3600, now)).toEqual({ text: '1 天后到期', tone: 'soon' })
  })
})
