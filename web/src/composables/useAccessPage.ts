import { computed, reactive, ref } from 'vue'
import { hasAccessFilter, inboundScopeLabel, scopeLabel as scopeLabelOf } from '@/utils/access'
import type {
  AccessEvent,
  AccessEventParams,
  AccessEventsResponse,
  AccessSummary,
  AccessTopClient,
  Inbound,
} from '@/types'

// The access page's state and behaviour, kept out of the component so it can be
// tested without a DOM.
//
// The page has two requests that answer different questions (the overview and
// the detail list) and two filters that narrow both (the window and the client),
// which makes it easy to end up showing numbers from one filter next to rows from
// another. Everything that decides what is on screen lives here, so the
// invariants are covered by tests instead of by reading the template.

/** AccessDataSource is the slice of the API the page needs, injectable for tests. */
export interface AccessDataSource {
  summary(days: number, clientId?: number, inboundId?: number): Promise<AccessSummary>
  events(params: AccessEventParams): Promise<AccessEventsResponse>
  inbounds(): Promise<Inbound[]>
}

/** AccessScope is the client (and inbound) the page is narrowed to. */
export interface AccessScope {
  clientId: number
  /** Empty when the user has been deleted; the view renders a fallback. */
  name: string
  /** The rollup groups by client and inbound, so a filter can name one. */
  inboundId: number
  inboundRemark: string
}

/** AccessPageOptions configures the page. */
export interface AccessPageOptions {
  api: AccessDataSource
  /** onError receives a user-facing message; the view turns it into a toast. */
  onError?: (message: string) => void
  /** pageSize is the number of detail rows per page. */
  pageSize?: number
  /** An inbound id from the URL, applied before the first load. */
  initialInboundId?: number
}

/** emptySummary is what the cards show before the first successful load. */
function emptySummary(): AccessSummary {
  return {
    enabled: false,
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
  }
}

/** errorMessage unwraps whatever the request layer threw. */
function errorMessage(error: unknown, fallback: string): string {
  const message = (error as { message?: unknown } | null)?.message
  return typeof message === 'string' && message ? message : fallback
}

/** useAccessPage builds the access page state. */
export function useAccessPage(options: AccessPageOptions) {
  const { api, onError } = options
  const pageSize = options.pageSize ?? 50

  const days = ref(7)
  const page = ref(1)

  const summary = ref<AccessSummary>(emptySummary())
  // summaryLoaded distinguishes "the server said there is nothing" from "the
  // request has not answered yet": the empty states and the "recording is off"
  // alert must not flash before the first answer, and a failed request must not
  // be presented as an empty instance.
  const summaryLoaded = ref(false)
  const summaryLoading = ref(false)
  const summaryError = ref<string | null>(null)

  const events = ref<AccessEvent[]>([])
  const total = ref(0)
  const eventsLoading = ref(false)
  const eventsError = ref<string | null>(null)
  const eventsLoaded = ref(false)

  // The date picker yields YYYY-MM-DD: days are what the operator picks, and only
  // the server knows which zone those days belong to, so they are sent as days
  // and resolved there.
  const range = ref<[string, string] | null>(null)
  const scope = ref<AccessScope | null>(null)
  // The inbound scope is independent of the client scope: the page can show one
  // node without narrowing to a user. Clicking a Top client still carries both,
  // so the two have a single derived "effective inbound" below.
  const inbounds = ref<Inbound[]>([])
  const inboundFilter = ref<number | null>(options.initialInboundId ?? null)
  const filters = reactive({ query: '', outcome: 'all', network: 'all' })

  // The last request wins: changing a filter can start a second request before
  // the first answers, and the older answer must not overwrite the newer one.
  let summarySeq = 0
  let eventsSeq = 0

  const loading = computed(() => eventsLoading.value || summaryLoading.value)

  // One source of truth for the inbound sent to the API: a client scope also
  // names an inbound (the rollup groups by client+inbound), so it wins when set.
  const effectiveInbound = computed(() => scope.value?.inboundId || inboundFilter.value || undefined)

  // The same predicate the empty states use, so "the filters are clear" and
  // "'clear filters' is enabled" cannot disagree.
  const hasFilters = computed(() =>
    hasAccessFilter({
      outcome: filters.outcome,
      network: filters.network,
      query: filters.query,
      clientId: scope.value?.clientId,
      inboundId: effectiveInbound.value,
      since: range.value?.[0],
      until: range.value?.[1],
    }),
  )

  /** The display name of the selected inbound, or a fallback for a deleted one. */
  const inboundName = computed(() => {
    const id = inboundFilter.value
    if (!id) return ''
    return inbounds.value.find((item) => item.id === id)?.remark || `入站 #${id}`
  })

  /** scopeLabel says what the cards and the trend describe. */
  const scopeLabel = computed(() => {
    const current = scope.value
    if (!current) {
      return inboundFilter.value
        ? inboundScopeLabel(days.value, inboundName.value)
        : scopeLabelOf(days.value)
    }
    const where = current.inboundRemark || (current.inboundId ? `入站 #${current.inboundId}` : '')
    return scopeLabelOf(days.value, current.name || '已删除用户', where)
  })

  /** loadInbounds fills the selector. A failure is reported but not fatal: the
   * page still works, and a deep-linked numeric id can be requested by itself. */
  async function loadInbounds(): Promise<void> {
    try {
      inbounds.value = await api.inbounds()
    } catch (error) {
      onError?.(errorMessage(error, '加载入站列表失败'))
    }
  }

  /** setInbound scopes both halves to one inbound, or to all when id is null. */
  function setInbound(id: number | null): void {
    // A client scope from another inbound would contradict the new selection.
    scope.value = null
    inboundFilter.value = id && id > 0 ? id : null
    reload()
  }

  async function loadSummary(): Promise<void> {
    const seq = ++summarySeq
    summaryLoading.value = true
    try {
      const result = await api.summary(days.value, scope.value?.clientId, effectiveInbound.value)
      if (seq !== summarySeq) return
      summary.value = result
      summaryLoaded.value = true
      summaryError.value = null
    } catch (error) {
      if (seq !== summarySeq) return
      // Keep the numbers that were on screen: they were true for the previous
      // filter, and replacing them with zeroes would read as "nothing happened".
      summaryError.value = errorMessage(error, '加载访问概览失败')
      onError?.(summaryError.value)
    } finally {
      if (seq === summarySeq) summaryLoading.value = false
    }
  }

  async function loadEvents(): Promise<void> {
    const seq = ++eventsSeq
    eventsLoading.value = true
    try {
      const result = await api.events({
        client_id: scope.value?.clientId,
        inbound_id: effectiveInbound.value,
        outcome: filters.outcome === 'all' ? undefined : filters.outcome,
        network: filters.network === 'all' ? undefined : filters.network,
        q: filters.query.trim() || undefined,
        since_day: range.value?.[0],
        until_day: range.value?.[1],
        limit: pageSize,
        offset: (page.value - 1) * pageSize,
      })
      if (seq !== eventsSeq) return
      events.value = result.items || []
      total.value = result.total || 0
      eventsLoaded.value = true
      eventsError.value = null
    } catch (error) {
      if (seq !== eventsSeq) return
      // The rows that were on screen belong to the previous filter; leaving them
      // under a new filter label is worse than showing nothing.
      events.value = []
      total.value = 0
      eventsError.value = errorMessage(error, '加载访问明细失败')
      onError?.(eventsError.value)
    } finally {
      if (seq === eventsSeq) eventsLoading.value = false
    }
  }

  /** reload refreshes both halves: the window and the scope apply to each. */
  function reload(): void {
    page.value = 1
    void loadSummary()
    void loadEvents()
  }

  /** applyFilters restarts the detail list at page one, where a new filter starts. */
  function applyFilters(): void {
    page.value = 1
    void loadEvents()
  }

  /** clearFilters drops every filter, including the client scope, and reloads both. */
  function clearFilters(): void {
    filters.query = ''
    filters.outcome = 'all'
    filters.network = 'all'
    range.value = null
    scope.value = null
    inboundFilter.value = null
    reload()
  }

  /** filterByClient narrows the overview and the list to one client (and inbound). */
  function filterByClient(row: AccessTopClient): void {
    scope.value = {
      clientId: row.client_id,
      name: row.name,
      inboundId: row.inbound_id,
      inboundRemark: row.inbound_remark,
    }
    // Keep the selector in step with the row that was clicked.
    inboundFilter.value = row.inbound_id || null
    reload()
  }

  /** clearScope keeps the other filters and widens the overview back to everything. */
  function clearScope(): void {
    scope.value = null
    reload()
  }

  /** setDays changes the window; the detail list has to follow the overview. */
  function setDays(value: number): void {
    days.value = value
    reload()
  }

  /** retry reloads whatever failed, leaving what succeeded alone. */
  function retry(): void {
    if (summaryError.value) void loadSummary()
    if (eventsError.value) void loadEvents()
  }

  return {
    pageSize,
    days,
    page,
    summary,
    summaryLoaded,
    summaryLoading,
    summaryError,
    events,
    total,
    eventsLoading,
    eventsError,
    eventsLoaded,
    range,
    scope,
    inbounds,
    inboundFilter,
    inboundName,
    effectiveInbound,
    filters,
    loading,
    hasFilters,
    scopeLabel,
    loadInbounds,
    loadSummary,
    loadEvents,
    reload,
    applyFilters,
    clearFilters,
    filterByClient,
    setInbound,
    clearScope,
    setDays,
    retry,
  }
}
