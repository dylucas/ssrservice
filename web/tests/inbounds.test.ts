import { describe, expect, it } from 'vitest'
import {
  filterInbounds,
  hasInboundFilter,
  inboundMatches,
  summarizeInbounds,
} from '../src/utils/inbounds'
import type { Inbound } from '../src/types'

// The inbound list is filtered in the browser (GET /inbounds returns the whole
// list), so these are the checks that stand in for a SQL WHERE clause: which
// fields the search looks at, and what the summary counts.

function inbound(overrides: Partial<Inbound> = {}): Inbound {
  return {
    id: 1,
    remark: '香港 01 · 主力线路',
    listen: '0.0.0.0',
    port: 8388,
    method: '2022-blake3-aes-128-gcm',
    domain: 'hk01.example.com',
    domain_port: 443,
    udp_enabled: true,
    enabled: true,
    created_at: 0,
    updated_at: 0,
    client_count: 8,
    enabled_client_count: 6,
    up_bytes: 1024,
    down_bytes: 2048,
    online_ips: 3,
    ...overrides,
  }
}

const all = { status: 'all' as const, query: '' }

describe('inboundMatches', () => {
  it('matches everything when the filter is empty', () => {
    expect(inboundMatches(inbound(), all)).toBe(true)
  })

  it('filters by status', () => {
    expect(inboundMatches(inbound({ enabled: true }), { status: 'enabled', query: '' })).toBe(true)
    expect(inboundMatches(inbound({ enabled: true }), { status: 'disabled', query: '' })).toBe(false)
    expect(inboundMatches(inbound({ enabled: false }), { status: 'disabled', query: '' })).toBe(true)
    expect(inboundMatches(inbound({ enabled: false }), { status: 'enabled', query: '' })).toBe(false)
  })

  it('searches the fields an operator recognises an inbound by', () => {
    const item = inbound()
    for (const query of ['香港', '8388', '0.0.0.0', 'blake3', 'hk01', 'example.com']) {
      expect(inboundMatches(item, { status: 'all', query }), `query ${query}`).toBe(true)
    }
  })

  it('does not match unrelated text', () => {
    expect(inboundMatches(inbound(), { status: 'all', query: '东京' })).toBe(false)
  })

  it('ignores case and surrounding whitespace', () => {
    expect(inboundMatches(inbound(), { status: 'all', query: '  AES-128  ' })).toBe(true)
    expect(inboundMatches(inbound(), { status: 'all', query: 'HK01.EXAMPLE.COM' })).toBe(true)
  })

  it('combines the status and the query', () => {
    expect(inboundMatches(inbound({ enabled: false }), { status: 'disabled', query: '香港' })).toBe(true)
    expect(inboundMatches(inbound({ enabled: true }), { status: 'disabled', query: '香港' })).toBe(false)
  })

  it('treats a missing remark or domain as an empty field', () => {
    // The API can return an inbound whose remark was left blank.
    const item = inbound({ remark: '', domain: '' })
    expect(inboundMatches(item, { status: 'all', query: '香港' })).toBe(false)
    expect(inboundMatches(item, { status: 'all', query: '8388' })).toBe(true)
  })
})

describe('filterInbounds', () => {
  const items = [
    inbound({ id: 1, port: 8388, enabled: true }),
    inbound({ id: 2, port: 18388, enabled: false, remark: '东京中转' }),
    inbound({ id: 3, port: 28388, enabled: true, remark: '新加坡' }),
  ]

  it('keeps the list order', () => {
    expect(filterInbounds(items, all).map((item) => item.id)).toEqual([1, 2, 3])
  })

  it('narrows to the matching rows', () => {
    expect(filterInbounds(items, { status: 'enabled', query: '' }).map((i) => i.id)).toEqual([1, 3])
    expect(filterInbounds(items, { status: 'all', query: '东京' }).map((i) => i.id)).toEqual([2])
  })

  it('matches a port as a substring, like the client search does', () => {
    // "8388" matches 8388, 18388 and 28388. That is deliberate: the client list
    // search on the server is a LIKE %q%, and a search box that changes rule
    // depending on whether the text happens to be digits is worse than one that
    // occasionally shows a neighbour row.
    expect(filterInbounds(items, { status: 'all', query: '8388' }).map((i) => i.id)).toEqual([1, 2, 3])
    expect(filterInbounds(items, { status: 'all', query: '28388' }).map((i) => i.id)).toEqual([3])
  })

  it('returns an empty list when nothing matches', () => {
    expect(filterInbounds(items, { status: 'all', query: 'nowhere' })).toEqual([])
  })
})

describe('hasInboundFilter', () => {
  it('is false for the default state', () => {
    expect(hasInboundFilter(all)).toBe(false)
    expect(hasInboundFilter({ status: 'all', query: '   ' })).toBe(false)
  })

  it('is true once either control is used', () => {
    expect(hasInboundFilter({ status: 'enabled', query: '' })).toBe(true)
    expect(hasInboundFilter({ status: 'all', query: 'x' })).toBe(true)
  })
})

describe('summarizeInbounds', () => {
  it('adds up the visible rows', () => {
    const items = [
      inbound({ id: 1, enabled: true, client_count: 8, up_bytes: 100, down_bytes: 200 }),
      inbound({ id: 2, enabled: false, client_count: 3, up_bytes: 10, down_bytes: 20 }),
    ]
    expect(summarizeInbounds(items)).toEqual({
      total: 2,
      enabled: 1,
      clients: 11,
      up_bytes: 110,
      down_bytes: 220,
    })
  })

  it('is all zeros for an empty list', () => {
    expect(summarizeInbounds([])).toEqual({
      total: 0,
      enabled: 0,
      clients: 0,
      up_bytes: 0,
      down_bytes: 0,
    })
  })
})
