import { describe, expect, it } from 'vitest'
import type { Client } from '../src/types'
import { filterClients, lastPage, paginate } from '../src/utils/clients'

function client(overrides: Partial<Client>): Client {
  return {
    id: 1,
    inbound_id: 1,
    name: 'user',
    enabled: true,
    quota_bytes: 0,
    up_bytes: 0,
    down_bytes: 0,
    expires_at: 0,
    ip_limit: 0,
    reset_policy: 'none',
    reset_day: 0,
    last_reset_at: 0,
    sub_token: 'token',
    note: '',
    created_at: 0,
    updated_at: 0,
    status: 'active',
    used_bytes: 0,
    remaining_bytes: -1,
    online_ips: [],
    inbound_remark: '香港',
    method: '2022-blake3-aes-128-gcm',
    link: '',
    subscription_url: '',
    ...overrides,
  }
}

const clients: Client[] = [
  client({ id: 1, name: 'alice', note: 'laptop', status: 'active' }),
  client({ id: 2, name: 'Bob', note: 'phone', status: 'expired' }),
  client({ id: 3, name: 'carol', inbound_remark: '东京', status: 'quota_exceeded' }),
]

describe('filterClients', () => {
  it('returns every client without filters', () => {
    expect(filterClients(clients)).toHaveLength(3)
    expect(filterClients(clients, { query: '   ' })).toHaveLength(3)
    expect(filterClients(clients, { status: 'all' })).toHaveLength(3)
  })

  it('matches name, note, inbound remark and method case-insensitively', () => {
    expect(filterClients(clients, { query: 'ALICE' }).map((c) => c.id)).toEqual([1])
    expect(filterClients(clients, { query: 'phone' }).map((c) => c.id)).toEqual([2])
    expect(filterClients(clients, { query: '东京' }).map((c) => c.id)).toEqual([3])
    expect(filterClients(clients, { query: 'blake3' })).toHaveLength(3)
  })

  it('filters by status', () => {
    expect(filterClients(clients, { status: 'expired' }).map((c) => c.id)).toEqual([2])
    expect(filterClients(clients, { status: 'quota_exceeded' }).map((c) => c.id)).toEqual([3])
  })

  it('combines query and status', () => {
    expect(filterClients(clients, { query: 'bob', status: 'active' })).toEqual([])
    expect(filterClients(clients, { query: 'bob', status: 'expired' }).map((c) => c.id)).toEqual([2])
  })
})

describe('paginate', () => {
  const items = Array.from({ length: 45 }, (_, index) => index)

  it('slices the requested page', () => {
    expect(paginate(items, 1, 20)).toEqual(items.slice(0, 20))
    expect(paginate(items, 3, 20)).toEqual(items.slice(40, 45))
  })

  it('clamps out-of-range pages', () => {
    expect(paginate(items, 0, 20)).toEqual(items.slice(0, 20))
    expect(paginate(items, 99, 20)).toEqual(items.slice(40, 45))
    expect(paginate([], 5, 20)).toEqual([])
  })

  it('returns all items when the page size is not positive', () => {
    expect(paginate(items, 1, 0)).toEqual(items)
  })
})

describe('lastPage', () => {
  it('computes the page count', () => {
    expect(lastPage(0, 20)).toBe(1)
    expect(lastPage(1, 20)).toBe(1)
    expect(lastPage(20, 20)).toBe(1)
    expect(lastPage(21, 20)).toBe(2)
    expect(lastPage(40, 20)).toBe(2)
  })
})
