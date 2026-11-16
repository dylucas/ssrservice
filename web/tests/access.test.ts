import { describe, expect, it } from 'vitest'
import {
  ACCESS_AGGREGATE_DAYS,
  ACCESS_OTHER_HOST,
  ACCESS_RAW_DAYS,
  ACCESS_WINDOWS,
  accessTrend,
  blockedShare,
  destinationLabel,
  formatCount,
  formatElapsed,
  formatHostPort,
  hasAccessFilter,
  networkLabel,
  outcomeLabel,
  outcomeTagType,
  reasonLabel,
  reasonText,
  scopeLabel,
  windowLabel,
} from '../src/utils/access'
import { axisGutterWidth } from '../src/utils/chart'
import type { AccessDaily } from '../src/types'

// The access page maps stored identifiers to what an operator reads. The server
// treats those identifiers as API surface, so a renamed outcome or reason has to
// fail here rather than render an untranslated string in the table.

describe('access vocabulary', () => {
  it('labels every outcome', () => {
    expect(outcomeLabel('allowed')).toBe('放行')
    expect(outcomeLabel('blocked')).toBe('拦截')
    expect(outcomeLabel('failed')).toBe('失败')
    // An unknown value is shown verbatim instead of being swallowed.
    expect(outcomeLabel('mystery')).toBe('mystery')
  })

  it('colours outcomes by severity', () => {
    expect(outcomeTagType('allowed')).toBe('success')
    expect(outcomeTagType('blocked')).toBe('warning')
    expect(outcomeTagType('failed')).toBe('danger')
    expect(outcomeTagType('other')).toBe('info')
  })

  it('explains every reason the server can store', () => {
    const reasons = ['disabled', 'expired', 'quota', 'ip_limit', 'sessions', 'policy', 'resolve', 'dial']
    for (const reason of reasons) {
      const label = reasonLabel(reason)
      expect(label, `${reason} has no label`).not.toBe('')
      expect(label).not.toBe(reason)
    }
    expect(reasonLabel('')).toBe('')
    expect(reasonLabel('brand-new')).toBe('brand-new')
  })

  it('only prints a reason when there is one', () => {
    expect(reasonText({ outcome: 'allowed', reason: '' })).toBe('')
    expect(reasonText({ outcome: 'blocked', reason: 'policy' })).toBe('目标被策略拦截')
    // A blocked or failed row without a reason still shows that something
    // happened, rather than a blank cell.
    expect(reasonText({ outcome: 'blocked', reason: '' })).toBe('—')
  })

  it('names transports', () => {
    expect(networkLabel('tcp')).toBe('TCP')
    expect(networkLabel('udp')).toBe('UDP')
    expect(networkLabel('')).toBe('—')
  })
})

describe('formatHostPort', () => {
  it('joins a host and a port', () => {
    expect(formatHostPort('example.com', 443)).toBe('example.com:443')
    expect(formatHostPort('127.0.0.1', 8388)).toBe('127.0.0.1:8388')
  })

  it('brackets IPv6 literals', () => {
    expect(formatHostPort('2001:db8::1', 443)).toBe('[2001:db8::1]:443')
    expect(formatHostPort('[2001:db8::1]', 443)).toBe('[2001:db8::1]:443')
  })

  it('degrades without a port or a host', () => {
    expect(formatHostPort('example.com', 0)).toBe('example.com')
    expect(formatHostPort('', 443)).toBe(':443')
    expect(formatHostPort('', 0)).toBe('—')
  })
})

describe('formatElapsed', () => {
  it('keeps sub-second durations readable', () => {
    expect(formatElapsed(0)).toBe('0 ms')
    expect(formatElapsed(40)).toBe('40 ms')
    expect(formatElapsed(999)).toBe('999 ms')
  })

  it('switches to seconds, then minutes', () => {
    expect(formatElapsed(1000)).toBe('1.0 秒')
    expect(formatElapsed(9500)).toBe('9.5 秒')
    expect(formatElapsed(12000)).toBe('12 秒')
    // The boundaries are where rounding used to produce a 60 in the seconds
    // slot: "1 分 60 秒" for 119999 ms and "60.0 秒" for 59999 ms.
    expect(formatElapsed(59999)).toBe('1 分')
    expect(formatElapsed(119999)).toBe('2 分')
    expect(formatElapsed(60500)).toBe('1 分 1 秒')
    expect(formatElapsed(999)).toBe('999 ms')
    expect(formatElapsed(60500)).toBe('1 分 1 秒')
    expect(formatElapsed(120000)).toBe('2 分')
  })

  it('clamps negative input', () => {
    expect(formatElapsed(-5)).toBe('0 ms')
  })
})

describe('window labels and filters', () => {
  it('names the window', () => {
    expect(windowLabel(7)).toBe('最近 7 天')
    expect(windowLabel(90)).toBe('最近 90 天')
    expect(windowLabel(0)).toBe('窗口')
  })

  it('offers only windows the rollup can answer', () => {
    expect([...ACCESS_WINDOWS]).toEqual([7, 30, 90])
    for (const days of ACCESS_WINDOWS) {
      expect(days).toBeLessThanOrEqual(ACCESS_AGGREGATE_DAYS)
    }
    expect(ACCESS_RAW_DAYS).toBeLessThanOrEqual(ACCESS_AGGREGATE_DAYS)
  })

  it('detects each kind of filter', () => {
    expect(hasAccessFilter({})).toBe(false)
    expect(hasAccessFilter({ outcome: 'all', network: 'all', query: '   ' })).toBe(false)
    expect(hasAccessFilter({ outcome: 'blocked' })).toBe(true)
    expect(hasAccessFilter({ network: 'udp' })).toBe(true)
    expect(hasAccessFilter({ query: 'example.com' })).toBe(true)
    expect(hasAccessFilter({ clientId: 3 })).toBe(true)
    expect(hasAccessFilter({ inboundId: 2 })).toBe(true)
    expect(hasAccessFilter({ since: 100 })).toBe(true)
    expect(hasAccessFilter({ until: 200 })).toBe(true)
  })

  it('derives the blocked share', () => {
    expect(blockedShare({ blocked_hits: 0, total_hits: 0 })).toBe(0)
    expect(blockedShare({ blocked_hits: 1, total_hits: 4 })).toBe(25)
    expect(blockedShare({ blocked_hits: 3, total_hits: 3 })).toBe(100)
  })
})

describe('trend and scope', () => {
  const daily: AccessDaily[] = [
    { day: '2026-11-14', hits: 10, allowed_hits: 7, blocked_hits: 2, failed_hits: 1, up_bytes: 1, down_bytes: 2 },
    { day: '2026-11-15', hits: 4, allowed_hits: 4, blocked_hits: 0, failed_hits: 0, up_bytes: 3, down_bytes: 4 },
  ]

  it('plots total requests against the ones that did not get through', () => {
    const trend = accessTrend(daily)
    // Labels are month-day: the axis has no room for a year.
    expect(trend.labels).toEqual(['11-14', '11-15'])
    expect(trend.series.map((entry) => entry.name)).toEqual(['访问次数', '拦截 / 失败次数'])
    expect(trend.series[0].values).toEqual([10, 4])
    // Blocked and failed share one line: both mean "the request did not land",
    // and a third line would make the plot unreadable at this size.
    expect(trend.series[1].values).toEqual([3, 0])
  })

  it('survives an empty window', () => {
    const trend = accessTrend([])
    expect(trend.labels).toEqual([])
    expect(trend.series[0].values).toEqual([])
  })

  it('formats a request count as a count, not as bytes', () => {
    expect(formatCount(0)).toBe('0 次')
    expect(formatCount(1234)).toBe('1,234 次')
    expect(formatCount(Number.NaN)).toBe('0 次')
  })

  it('names what the figures cover', () => {
    expect(scopeLabel(7)).toBe('最近 7 天')
    expect(scopeLabel(30, 'alice')).toBe('最近 30 天 · 仅 alice')
    // A client can be reached through more than one inbound, and the rollup
    // groups by that pair, so the label names it.
    expect(scopeLabel(7, 'alice', 'edge')).toBe('最近 7 天 · 仅 alice @ edge')
  })
})

describe('destinationLabel', () => {
  it('names the folded overflow row', () => {
    // The rollup folds destinations past the per-key cap into one row. That row
    // is not a destination anyone visited, so it must not be shown as one.
    expect(destinationLabel(ACCESS_OTHER_HOST)).toBe('其他目标（超出单日上限）')
    expect(destinationLabel('example.com')).toBe('example.com')
    expect(ACCESS_OTHER_HOST).toBe('<other>')
  })
})

describe('axisGutterWidth', () => {
  it('leaves room for count labels, not just byte labels', () => {
    // The chart's left gutter was sized for "1.4 MB"; "20,000 次" is wider and
    // used to be clipped by the viewBox.
    expect(axisGutterWidth(['1.4 MB'])).toBe(56)
    expect(axisGutterWidth(['20,000 次'])).toBeGreaterThan(56)
    // A wide (CJK) glyph counts double, an ASCII one about 6.2px.
    expect(axisGutterWidth(['0000000000 次'])).toBeGreaterThan(axisGutterWidth(['0000000000X']))
  })
})
