import { describe, expect, it } from 'vitest'
import { formatBytes, formatDate, formatDuration, percent, quotaLabel } from '../src/utils/format'

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
    expect(formatDuration(59)).toBe('0 分')
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
