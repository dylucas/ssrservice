import { describe, expect, it } from 'vitest'
import type { Client, ClientSummary } from '../src/types'
import {
  CLIENT_PAGE_SIZE,
  buildClientListParams,
  hasClientFilter,
  lastPage,
  toSummary,
} from '../src/utils/clients'

function client(overrides: Partial<ClientSummary>): ClientSummary {
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
    note: '',
    status: 'active',
    used_bytes: 0,
    remaining_bytes: -1,
    online_ips: [],
    inbound_remark: '香港',
    method: '2022-blake3-aes-128-gcm',
    ...overrides,
  }
}

describe('buildClientListParams', () => {
  it('always sends a limit and an offset', () => {
    const params = buildClientListParams({ page: 1, pageSize: CLIENT_PAGE_SIZE })
    expect(params).toEqual({ limit: CLIENT_PAGE_SIZE, offset: 0 })
    expect(buildClientListParams({ page: 3, pageSize: 20 })).toEqual({ limit: 20, offset: 40 })
  })

  it('omits filters that mean "everything"', () => {
    expect(buildClientListParams({ page: 1, pageSize: 20, inboundId: 0 }).inbound_id).toBeUndefined()
    expect(buildClientListParams({ page: 1, pageSize: 20, status: 'all' }).status).toBeUndefined()
    expect(buildClientListParams({ page: 1, pageSize: 20, query: '   ' }).q).toBeUndefined()
    expect(buildClientListParams({ page: 1, pageSize: 20, query: '' }).q).toBeUndefined()
  })

  it('sends the filters the operator selected', () => {
    const params = buildClientListParams({
      page: 2,
      pageSize: 20,
      inboundId: 7,
      status: 'quota_exceeded',
      query: '  alice  ',
    })
    expect(params).toEqual({
      inbound_id: 7,
      status: 'quota_exceeded',
      q: 'alice',
      limit: 20,
      offset: 20,
    })
  })

  it('clamps a page below one and keeps a stale page usable', () => {
    expect(buildClientListParams({ page: 0, pageSize: 20 }).offset).toBe(0)
    expect(buildClientListParams({ page: -5, pageSize: 20 }).offset).toBe(0)
    expect(buildClientListParams({ page: 1.9, pageSize: 20 }).offset).toBe(0)
    expect(buildClientListParams({ page: 2.9, pageSize: 20 }).offset).toBe(20)
  })

  it('falls back to the default page size', () => {
    expect(buildClientListParams({ page: 1, pageSize: 0 }).limit).toBe(CLIENT_PAGE_SIZE)
    expect(buildClientListParams({ page: 1, pageSize: -1 }).limit).toBe(CLIENT_PAGE_SIZE)
  })
})

describe('hasClientFilter', () => {
  it('reports whether anything narrows the list', () => {
    expect(hasClientFilter({})).toBe(false)
    expect(hasClientFilter({ status: 'all', query: '   ' })).toBe(false)
    expect(hasClientFilter({ status: 'expired' })).toBe(true)
    expect(hasClientFilter({ query: 'alice' })).toBe(true)
  })
})

describe('lastPage', () => {
  it('computes the page count', () => {
    expect(lastPage(0, 20)).toBe(1)
    expect(lastPage(1, 20)).toBe(1)
    expect(lastPage(20, 20)).toBe(1)
    expect(lastPage(21, 20)).toBe(2)
    expect(lastPage(40, 20)).toBe(2)
    expect(lastPage(101, 20)).toBe(6)
  })
})

describe('toSummary', () => {
  it('drops credentials and keeps the list fields', () => {
    const full: Client = {
      ...client({ id: 7, name: 'alice' }),
      psk: 'secret-psk',
      sub_token: 'secret-token',
      last_reset_at: 123,
      created_at: 456,
      updated_at: 789,
      link: 'ss://secret-link',
      subscription_url: '/sub/secret-token',
    }
    const summary = toSummary(full)
    expect(summary.id).toBe(7)
    expect(summary.name).toBe('alice')
    expect(summary.status).toBe('active')
    for (const secret of ['psk', 'sub_token', 'link', 'subscription_url']) {
      expect(summary).not.toHaveProperty(secret)
    }
    expect(summary).not.toHaveProperty('created_at')
    expect(summary).not.toHaveProperty('updated_at')
    expect(summary).not.toHaveProperty('last_reset_at')
  })
})
