import { describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { useAccessPage, type AccessDataSource } from '../src/composables/useAccessPage'
import type { AccessEvent, AccessEventParams, AccessSummary } from '../src/types'

// The access page keeps two requests (overview and detail) in sync with two
// filters (window and scope). These tests cover the invariants that are easy to
// break from the template: clearing a filter has to refresh both halves, a failed
// request must not be presented as an empty instance, and an older response must
// never overwrite a newer one.

function summaryOf(overrides: Partial<AccessSummary> = {}): AccessSummary {
  return {
    enabled: true,
    days: 7,
    raw_retention_days: 7,
    aggregate_retention_days: 90,
    dropped_records: 0,
    total_hits: 0,
    allowed_hits: 0,
    blocked_hits: 0,
    failed_hits: 0,
    unique_hosts: 0,
    up_bytes: 0,
    down_bytes: 0,
    daily: [],
    top_hosts: [],
    top_clients: [],
    ...overrides,
  }
}

function eventOf(id: number, overrides: Partial<AccessEvent> = {}): AccessEvent {
  return {
    id,
    started_at: 1_700_000_000 + id,
    duration_ms: 12,
    client_id: 1,
    client_name: 'alice',
    inbound_id: 1,
    inbound_remark: 'main',
    network: 'tcp',
    host: 'example.com',
    port: 443,
    ip: '93.184.216.34',
    outcome: 'allowed',
    reason: '',
    up_bytes: 10,
    down_bytes: 20,
    ...overrides,
  }
}

interface FakeSources {
  api: AccessDataSource
  summaryCalls: { days: number; clientId?: number; inboundId?: number }[]
  eventCalls: AccessEventParams[]
}

function fakeApi(overrides: Partial<AccessDataSource> = {}): FakeSources {
  const summaryCalls: FakeSources['summaryCalls'] = []
  const eventCalls: FakeSources['eventCalls'] = []
  const api: AccessDataSource = {
    summary: async (days, clientId, inboundId) => {
      summaryCalls.push({ days, clientId, inboundId })
      return summaryOf({ days, total_hits: 5 })
    },
    events: async (params) => {
      eventCalls.push(params)
      return { items: [eventOf(1)], total: 1, limit: params.limit, offset: params.offset }
    },
    ...overrides,
  }
  return { api, summaryCalls, eventCalls }
}

describe('access page state', () => {
  it('starts empty and does not claim recording is off before the first answer', () => {
    const { api } = fakeApi()
    const page = useAccessPage({ api })
    expect(page.summaryLoaded.value).toBe(false)
    expect(page.summary.value.enabled).toBe(false)
    expect(page.events.value).toEqual([])
  })

  it('loads both halves and records that the overview answered', async () => {
    const { api, summaryCalls, eventCalls } = fakeApi()
    const page = useAccessPage({ api })
    await Promise.all([page.loadSummary(), page.loadEvents()])
    expect(page.summaryLoaded.value).toBe(true)
    expect(page.summary.value.total_hits).toBe(5)
    expect(page.events.value).toHaveLength(1)
    expect(summaryCalls).toEqual([{ days: 7, clientId: undefined, inboundId: undefined }])
    expect(eventCalls[0].limit).toBe(50)
    expect(eventCalls[0].offset).toBe(0)
  })

  it('clearing the filters refreshes the overview as well as the list', async () => {
    const { api, summaryCalls } = fakeApi()
    const page = useAccessPage({ api })
    await Promise.all([page.loadSummary(), page.loadEvents()])

    page.filterByClient({ client_id: 7, name: 'bob', inbound_id: 3, inbound_remark: 'edge', hits: 1, up_bytes: 0, down_bytes: 0 })
    await nextTick()
    const scopedCalls = summaryCalls.length
    expect(page.scope.value?.clientId).toBe(7)
    expect(page.scopeLabel.value).toContain('bob')

    page.clearFilters()
    await nextTick()
    // The overview is requested again, and this time without the client: a chip
    // that disappears while the cards keep the user's numbers is the bug this
    // guards.
    expect(summaryCalls.length).toBeGreaterThan(scopedCalls)
    expect(summaryCalls[summaryCalls.length - 1].clientId).toBeUndefined()
    expect(page.scope.value).toBeNull()
    expect(page.hasFilters.value).toBe(false)
  })

  it('scopes the detail list to the client and inbound of a top-user row', async () => {
    const { api, eventCalls } = fakeApi()
    const page = useAccessPage({ api })
    page.filterByClient({ client_id: 4, name: 'carol', inbound_id: 9, inbound_remark: 'hk', hits: 2, up_bytes: 0, down_bytes: 0 })
    await nextTick()
    const last = eventCalls[eventCalls.length - 1]
    expect(last.client_id).toBe(4)
    expect(last.inbound_id).toBe(9)
  })

  it('sends the picked days as days, not as browser-local timestamps', async () => {
    const { api, eventCalls } = fakeApi()
    const page = useAccessPage({ api })
    page.range.value = ['2026-11-10', '2026-11-16']
    page.applyFilters()
    await nextTick()
    const last = eventCalls[eventCalls.length - 1]
    expect(last.since_day).toBe('2026-11-10')
    expect(last.until_day).toBe('2026-11-16')
    // The absolute-instant form is left out: only the server knows the zone the
    // picked days belong to.
    expect(last.since).toBeUndefined()
    expect(last.until).toBeUndefined()
  })

  it('keeps the last good overview when the summary request fails', async () => {
    let fail = false
    const sources = fakeApi({
      summary: async () => {
        if (fail) throw new Error('network down')
        return summaryOf({ total_hits: 42 })
      },
    })
    const errors: string[] = []
    const page = useAccessPage({ api: sources.api, onError: (message) => errors.push(message) })
    await page.loadSummary()
    expect(page.summary.value.total_hits).toBe(42)

    fail = true
    await page.loadSummary()
    // The number stays (it was true a moment ago) and the page says it is stale
    // instead of showing zeroes under a "recording is off" alert.
    expect(page.summary.value.total_hits).toBe(42)
    expect(page.summaryError.value).toContain('network down')
    expect(page.summaryLoading.value).toBe(false)
    expect(errors).toEqual(['network down'])
  })

  it('clears the stale rows when the detail request fails', async () => {
    let fail = false
    const sources = fakeApi({
      events: async (params) => {
        if (fail) throw new Error('boom')
        return { items: [eventOf(1), eventOf(2)], total: 2, limit: params.limit, offset: params.offset }
      },
    })
    const page = useAccessPage({ api: sources.api })
    await page.loadEvents()
    expect(page.events.value).toHaveLength(2)

    fail = true
    await page.loadEvents()
    // Leaving the previous filter's rows on screen under a new filter label is
    // worse than showing an error.
    expect(page.events.value).toEqual([])
    expect(page.total.value).toBe(0)
    expect(page.eventsError.value).toBe('boom')
  })

  it('retries only what failed', async () => {
    const sources = fakeApi()
    let summaryCalls = 0
    let eventCalls = 0
    const api: AccessDataSource = {
      summary: async (days, clientId, inboundId) => {
        summaryCalls++
        return sources.api.summary(days, clientId, inboundId)
      },
      events: async (params) => {
        eventCalls++
        return sources.api.events(params)
      },
    }
    const page = useAccessPage({ api })
    await Promise.all([page.loadSummary(), page.loadEvents()])
    summaryCalls = 0
    eventCalls = 0
    page.retry()
    await nextTick()
    expect(summaryCalls).toBe(0)
    expect(eventCalls).toBe(0)

    // Mark only the detail list as failed.
    page.eventsError.value = 'boom'
    page.retry()
    await nextTick()
    expect(summaryCalls).toBe(0)
    expect(eventCalls).toBe(1)
  })

  it('lets the newest request win when filters change mid-flight', async () => {
    const pending: (() => void)[] = []
    let calls = 0
    const api: AccessDataSource = {
      summary: async () => summaryOf(),
      events: (params) =>
        new Promise((resolve) => {
          const id = ++calls
          pending.push(() =>
            resolve({ items: [eventOf(id)], total: id, limit: params.limit, offset: params.offset }),
          )
        }),
    }
    const page = useAccessPage({ api })
    const first = page.loadEvents()
    page.filters.outcome = 'blocked'
    const second = page.loadEvents()
    // Answer the newer request first, then the older one: the older answer must
    // not overwrite it.
    pending[1]()
    await second
    pending[0]()
    await first
    await nextTick()
    expect(page.eventsError.value).toBeNull()
    expect(page.events.value[0].id).toBe(2)
    expect(page.total.value).toBe(2)
    expect(page.eventsLoading.value).toBe(false)
  })

  it('follows the window on both halves when it changes', async () => {
    const { api, summaryCalls, eventCalls } = fakeApi()
    const page = useAccessPage({ api })
    page.setDays(30)
    await nextTick()
    expect(summaryCalls[summaryCalls.length - 1].days).toBe(30)
    expect(page.days.value).toBe(30)
    expect(page.page.value).toBe(1)
    expect(eventCalls.length).toBeGreaterThan(0)
  })

  it('reports the scope in the label the cards use', async () => {
    const { api } = fakeApi()
    const page = useAccessPage({ api })
    expect(page.scopeLabel.value).toBe('最近 7 天')
    page.filterByClient({ client_id: 1, name: '', inbound_id: 2, inbound_remark: '', hits: 0, up_bytes: 0, down_bytes: 0 })
    await nextTick()
    // A deleted user still has history: the label says so instead of going blank.
    expect(page.scopeLabel.value).toContain('已删除用户')
    expect(page.scopeLabel.value).toContain('入站 #2')
  })
})

describe('request payloads', () => {
  it('omits empty filters instead of sending "undefined" values', async () => {
    const { api, eventCalls } = fakeApi()
    const page = useAccessPage({ api })
    page.filters.query = '   '
    page.applyFilters()
    await nextTick()
    const last = eventCalls[eventCalls.length - 1]
    expect(last.q).toBeUndefined()
    expect(last.outcome).toBeUndefined()
    expect(last.network).toBeUndefined()
  })

  it('sends the active filters', async () => {
    const { api, eventCalls } = fakeApi()
    const page = useAccessPage({ api })
    page.filters.query = 'example.com'
    page.filters.outcome = 'blocked'
    page.filters.network = 'udp'
    page.applyFilters()
    await nextTick()
    const last = eventCalls[eventCalls.length - 1]
    expect(last.q).toBe('example.com')
    expect(last.outcome).toBe('blocked')
    expect(last.network).toBe('udp')
  })

  it('pages the details without touching the overview', async () => {
    const { api, eventCalls, summaryCalls } = fakeApi()
    const page = useAccessPage({ api, pageSize: 25 })
    await Promise.all([page.loadSummary(), page.loadEvents()])
    const summaries = summaryCalls.length
    page.page.value = 3
    await page.loadEvents()
    const last = eventCalls[eventCalls.length - 1]
    expect(last.limit).toBe(25)
    expect(last.offset).toBe(50)
    expect(summaryCalls.length).toBe(summaries)
  })
})

describe('drop-in for the view', () => {
  it('reports loading as the union of the two requests', async () => {
    let release: (() => void) | null = null
    const api: AccessDataSource = {
      summary: () => new Promise((resolve) => (release = () => resolve(summaryOf()))),
      events: async (params) => ({ items: [], total: 0, limit: params.limit, offset: params.offset }),
    }
    const page = useAccessPage({ api })
    expect(page.loading.value).toBe(false)
    const pending = page.loadSummary()
    expect(page.loading.value).toBe(true)
    release?.()
    await pending
    expect(page.loading.value).toBe(false)
  })

  it('does not call the error handler on success', async () => {
    const onError = vi.fn()
    const { api } = fakeApi()
    const page = useAccessPage({ api, onError })
    await Promise.all([page.loadSummary(), page.loadEvents()])
    expect(onError).not.toHaveBeenCalled()
  })
})

describe('filter predicate', () => {
  it('treats a picked day range and a client scope as filters', async () => {
    const { api } = fakeApi()
    const page = useAccessPage({ api })
    expect(page.hasFilters.value).toBe(false)
    page.range.value = ['2026-11-10', '2026-11-16']
    await nextTick()
    expect(page.hasFilters.value).toBe(true)
    page.range.value = null
    page.filterByClient({ client_id: 2, name: 'bob', inbound_id: 1, inbound_remark: '', hits: 1, up_bytes: 0, down_bytes: 0 })
    await nextTick()
    expect(page.hasFilters.value).toBe(true)
    // The shared predicate is what the template asks, so clearing it has to be
    // enough to re-enable the empty state's way out.
    page.clearFilters()
    await nextTick()
    expect(page.hasFilters.value).toBe(false)
  })
})

describe('first load', () => {
  it('does not report a state before the detail request answers', () => {
    const { api } = fakeApi()
    const page = useAccessPage({ api })
    // The view uses eventsLoaded to avoid claiming "recording is off" during
    // the very first load; the flag has to stay false until a real answer.
    expect(page.eventsLoaded.value).toBe(false)
    expect(page.summaryLoaded.value).toBe(false)
  })
})
