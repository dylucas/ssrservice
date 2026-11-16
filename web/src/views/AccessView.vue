<template>
  <div class="page">
    <PageHeader title="访问分析" subtitle="每个用户访问了哪些目标、是否放行与传输量">
      <template #actions>
        <el-select :model-value="days" class="w-control-sm" @change="onWindowChange">
          <el-option v-for="option in ACCESS_WINDOWS" :key="option" :label="`最近 ${option} 天`" :value="option" />
        </el-select>
        <el-select
          :model-value="inboundFilter ?? 0"
          class="w-control-md"
          @change="(value: number) => setInbound(value || null)"
        >
          <el-option label="全部入站" :value="0" />
          <el-option
            v-for="inbound in inbounds"
            :key="inbound.id"
            :label="inbound.remark || `入站 #${inbound.id}`"
            :value="inbound.id"
          />
        </el-select>
        <el-button :icon="Refresh" :loading="loading" @click="reload">刷新</el-button>
      </template>
    </PageHeader>

    <!-- Recording is off by default and the page still shows whatever history
         exists; the alert is the only place that says why it may be empty. It is
         gated on the first successful load so it cannot flash before the answer. -->
    <el-alert
      v-if="summaryLoaded && !summary.enabled"
      type="info"
      show-icon
      :closable="false"
      class="mb-4"
      title="访问记录已关闭"
    >
      <template #default>
        <span>当前不会记录新的访问目标。历史数据仍可查看，保留 {{ summary.raw_retention_days }} 天明细与 {{ summary.aggregate_retention_days }} 天聚合。</span>
        <el-button size="small" class="access-alert-action" @click="goSettings">去设置开启</el-button>
      </template>
    </el-alert>

    <!-- A failed overview keeps the previous numbers on screen; this says they
         are stale instead of pretending the instance is empty. -->
    <el-alert
      v-if="summaryError"
      type="error"
      show-icon
      :closable="false"
      class="mb-4"
      :title="`访问概览加载失败：${summaryError}`"
    >
      <template #default>
        <span>下面的数字可能是上一次成功加载的结果。</span>
        <el-button size="small" class="access-alert-action" @click="retry">重试</el-button>
      </template>
    </el-alert>

    <div class="stat-grid">
      <StatCard
        label="总访问"
        :value="summary.total_hits.toLocaleString()"
        :hint="scopeLabel"
        :icon="DataAnalysis"
      />
      <StatCard
        label="放行 / 拦截"
        :value="`${summary.allowed_hits.toLocaleString()} / ${summary.blocked_hits.toLocaleString()}`"
        :hint="outcomeHint"
        :icon="View"
      />
      <StatCard
        label="独立目标"
        :value="summary.unique_hosts.toLocaleString()"
        hint="按域名或 IP 去重"
        :icon="Aim"
      />
      <StatCard
        label="上传 / 下载"
        :value="`${formatBytes(summary.up_bytes)} / ${formatBytes(summary.down_bytes)}`"
        :hint="droppedHint"
        :icon="DataLine"
      />
    </div>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">每日趋势</h3>
        <span class="muted">{{ scopeLabel }}</span>
      </div>
      <TrafficChart
        :labels="trendLabels"
        :series="trendSeries"
        :format="formatHits"
        empty-text="该区间还没有访问记录"
        aria-label="每日访问次数与拦截失败次数"
      />
    </div>

    <div class="access-lists">
      <div class="card">
        <div class="card-head">
          <h3 class="card-title">Top 目标</h3>
          <span class="muted">按访问次数</span>
        </div>
        <el-table :data="summary.top_hosts" v-loading="summaryLoading" row-key="host">
          <template #empty>
            <EmptyState v-if="summary.enabled" text="该窗口还没有访问记录" :icon="Aim" />
            <EmptyState v-else text="开启访问记录后这里会开始有数据" :icon="Aim" />
          </template>
          <el-table-column label="目标" min-width="220">
            <template #default="{ row }">
              <span class="mono ellipsis access-target" :title="row.host">{{ destinationLabel(row.host) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="次数" width="90" align="right">
            <template #default="{ row }">
              <span class="num">{{ row.hits.toLocaleString() }}</span>
            </template>
          </el-table-column>
          <el-table-column label="拦截" width="80" align="right">
            <template #default="{ row }">
              <span :class="['num', { 'access-blocked-count': row.blocked_hits > 0 }]">
                {{ row.blocked_hits.toLocaleString() }}
              </span>
            </template>
          </el-table-column>
          <el-table-column label="流量" width="120" align="right">
            <template #default="{ row }">
              <span class="num">{{ formatBytes(row.up_bytes + row.down_bytes) }}</span>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">Top 用户</h3>
          <span class="muted">点击可按用户筛选</span>
        </div>
        <el-table
          :data="summary.top_clients"
          v-loading="summaryLoading"
          :row-key="topClientKey"
          class="access-clickable"
          @row-click="(row: AccessTopClient) => filterByClient(row)"
        >
          <template #empty>
            <EmptyState v-if="summary.enabled" text="该窗口还没有访问记录" :icon="User" />
            <EmptyState v-else text="开启访问记录后这里会开始有数据" :icon="User" />
          </template>
          <el-table-column label="用户" min-width="180">
            <template #default="{ row }">
              <span class="cell-strong ellipsis">{{ row.name || '已删除用户' }}</span>
              <span class="muted mono ellipsis cell-sub">{{ row.inbound_remark || `入站 #${row.inbound_id}` }}</span>
            </template>
          </el-table-column>
          <el-table-column label="次数" width="90" align="right">
            <template #default="{ row }">
              <span class="num">{{ row.hits.toLocaleString() }}</span>
            </template>
          </el-table-column>
          <el-table-column label="流量" width="120" align="right">
            <template #default="{ row }">
              <span class="num">{{ formatBytes(row.up_bytes + row.down_bytes) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="" width="96" align="right">
            <template #default="{ row }">
              <!-- The row itself is clickable for the mouse; this button is the
                   keyboard path to the same action. -->
              <el-button size="small" text @click.stop="filterByClient(asTopClient(row))">筛选</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">最近访问</h3>
        <span class="muted">
          共 {{ total }} 条 · 明细保留 {{ summary.raw_retention_days }} 天
        </span>
      </div>

      <div class="toolbar toolbar--split card-toolbar">
        <div class="toolbar-group">
          <el-input
            v-model="filters.query"
            :prefix-icon="Search"
            placeholder="搜索目标域名 / IP"
            clearable
            class="w-control-md"
            @keyup.enter="applyFilters"
            @clear="applyFilters"
          />
          <el-select v-model="filters.outcome" class="w-control-sm" @change="applyFilters">
            <el-option label="全部结果" value="all" />
            <el-option label="放行" value="allowed" />
            <el-option label="拦截" value="blocked" />
            <el-option label="失败" value="failed" />
          </el-select>
          <el-select v-model="filters.network" class="w-control-sm" @change="applyFilters">
            <el-option label="全部协议" value="all" />
            <el-option label="TCP" value="tcp" />
            <el-option label="UDP" value="udp" />
          </el-select>
          <el-date-picker
            v-model="range"
            type="daterange"
            value-format="YYYY-MM-DD"
            start-placeholder="开始日期"
            end-placeholder="结束日期"
            class="w-control-lg"
            @change="applyFilters"
          />
          <span v-if="scope" class="access-filter-chip">
            <span>用户：{{ scope.name || '已删除用户' }}<template v-if="scope.inboundRemark || scope.inboundId"> @ {{ scope.inboundRemark || `入站 #${scope.inboundId}` }}</template></span>
            <el-button size="small" text @click="clearScope">清除</el-button>
          </span>
          <span v-else-if="inboundFilter" class="access-filter-chip">
            <span>入站：{{ inboundName }}</span>
            <el-button size="small" text @click="setInbound(null)">清除</el-button>
          </span>
        </div>
        <div class="toolbar-group">
          <el-button :icon="Download" @click="exportCSV">导出 CSV</el-button>
          <el-button :disabled="!hasFilters" @click="clearFilters">清除筛选</el-button>
        </div>
      </div>

      <el-table :data="events" v-loading="eventsLoading" row-key="id">
        <template #empty>
          <!-- Before the first answer there is nothing to say yet: without this
               branch a fresh page briefly claims recording is off. -->
          <EmptyState v-if="!eventsLoaded && !eventsError" text="正在加载访问明细…" :icon="Search" />
          <EmptyState v-else-if="eventsError" :text="`明细加载失败：${eventsError}`" :icon="Search">
            <el-button size="small" @click="retry">重试</el-button>
          </EmptyState>
          <EmptyState v-else-if="hasFilters" text="没有符合条件的访问记录" :icon="Search">
            <el-button size="small" @click="clearFilters">清除筛选条件</el-button>
          </EmptyState>
          <EmptyState
            v-else-if="summary.total_hits > 0"
            text="最近几天没有明细；更早的记录只保留按天聚合，可在上方汇总里查看"
            :icon="Aim"
          />
          <EmptyState v-else-if="summary.enabled" text="还没有访问记录，先让用户连一次" :icon="Aim" />
          <EmptyState v-else text="访问记录已关闭，这里是开启前的历史" :icon="Aim" />
        </template>
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatDateTime(row.started_at) }}</template>
        </el-table-column>
        <el-table-column label="用户" width="150">
          <template #default="{ row }">
            <span class="ellipsis">{{ row.client_name || '已删除用户' }}</span>
            <span class="muted mono ellipsis cell-sub">
              {{ row.inbound_remark || `入站 #${row.inbound_id}` }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="目标" min-width="220">
          <template #default="{ row }">
            <span class="mono ellipsis access-target" :title="formatHostPort(row.host, row.port)">
              {{ formatHostPort(row.host, row.port) }}
            </span>
            <span v-if="row.ip && row.ip !== row.host" class="muted mono ellipsis cell-sub">
              → {{ row.ip }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="协议" width="80">
          <template #default="{ row }">{{ networkLabel(row.network) }}</template>
        </el-table-column>
        <el-table-column label="结果" width="100">
          <template #default="{ row }">
            <el-tag :type="outcomeTagType(row.outcome)" size="small">{{ outcomeLabel(row.outcome) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="原因" min-width="140">
          <template #default="{ row }">
            <span v-if="reasonText(asEvent(row))" class="muted">{{ reasonText(asEvent(row)) }}</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="流量" width="120" align="right">
          <template #default="{ row }">
            <span class="num">{{ formatBytes(row.up_bytes + row.down_bytes) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90" align="right">
          <template #default="{ row }">
            <span class="num muted">{{ formatElapsed(row.duration_ms) }}</span>
          </template>
        </el-table-column>
      </el-table>

      <div class="pagination">
        <el-pagination
          v-model:current-page="page"
          :page-size="pageSize"
          :total="total"
          layout="total, prev, pager, next"
          background
          hide-on-single-page
          @current-change="loadEvents"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
// Page-specific Element Plus components; see src/element-parts.ts.
import {
  ElAlert,
  ElDatePicker,
  ElOption,
  ElPagination,
  ElSelect,
  ElTable,
  ElTableColumn,
} from '@/element-parts'
import { ElMessage } from 'element-plus'
import { Aim, DataAnalysis, DataLine, Download, Refresh, Search, User, View } from '@element-plus/icons-vue'
import { api } from '@/api'
import type { AccessEvent, AccessTopClient } from '@/types'
import {
  ACCESS_WINDOWS,
  accessTrend,
  blockedShare,
  destinationLabel,
  formatCount,
  formatHostPort,
  formatElapsed,
  networkLabel,
  outcomeLabel,
  outcomeTagType,
  reasonText,
} from '@/utils/access'
import { formatBytes, formatDateTime } from '@/utils/format'
import { useAccessPage } from '@/composables/useAccessPage'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatCard from '@/components/StatCard.vue'
import TrafficChart from '@/components/TrafficChart.vue'

// el-table's slot row is typed as DefaultRow, but both tables are always bound
// to their declared row types. Narrowing here keeps the helpers strongly typed
// instead of widening their signatures to `any`.
function asEvent(row: unknown): AccessEvent {
  return row as AccessEvent
}

function asTopClient(row: unknown): AccessTopClient {
  return row as AccessTopClient
}

/** topClientKey keeps rows unique when one user spans two inbounds. */
function topClientKey(row: AccessTopClient): string {
  return `${row.client_id}:${row.inbound_id}`
}

const router = useRouter()
const route = useRoute()
// A detail page links here with ?inbound_id=; validate before it becomes state.
const inboundParam = Number(route.query.inbound_id)
const initialInboundId = Number.isInteger(inboundParam) && inboundParam > 0 ? inboundParam : undefined

const {
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
  eventsLoaded,
  eventsError,
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
} = useAccessPage({
  api: {
    summary: (window, clientId, inboundId) => api.accessSummary(window, clientId, inboundId),
    events: (params) => api.accessEvents(params),
    inbounds: async () => (await api.inbounds()).items || [],
  },
  onError: (message) => ElMessage.error(message),
  initialInboundId,
})

/** exportCSV downloads the filtered detail list without paging. */
function exportCSV() {
  const params: Record<string, string | number> = {}
  const current = scope.value
  if (current) {
    params.client_id = current.clientId
    if (current.inboundId) params.inbound_id = current.inboundId
  }
  if (filters.outcome !== 'all') params.outcome = filters.outcome
  if (filters.network !== 'all') params.network = filters.network
  const query = filters.query.trim()
  if (query) params.q = query
  if (range.value?.length === 2) {
    params.since_day = range.value[0]
    params.until_day = range.value[1]
  }
  window.location.href = api.exportAccessURL(params)
}

const droppedHint = computed(() =>
  summary.value.dropped_records > 0
    ? `丢弃 ${summary.value.dropped_records.toLocaleString()} 条`
    : '按目标累计',
)

// outcomeHint keeps the "how much of it was blocked" reading next to the
// absolute counts, which is the number an operator actually reacts to.
const outcomeHint = computed(() => {
  const share = blockedShare(summary.value)
  if (summary.value.failed_hits > 0) {
    return `拦截占 ${share}% · 另有 ${summary.value.failed_hits.toLocaleString()} 次连接失败`
  }
  return `拦截占 ${share}%`
})

// The trend reuses the dashboard's chart with a count formatter instead of a
// byte formatter. The server sends one row per day of the window, so the axis
// never compresses a quiet day.
const trend = computed(() => accessTrend(summary.value.daily))
const trendLabels = computed(() => trend.value.labels)
const trendSeries = computed(() => trend.value.series)
const formatHits = formatCount

/** onWindowChange applies the selected window to both halves of the page. */
function onWindowChange(value: number) {
  setDays(Number(value))
}

function goSettings() {
  router.push('/settings')
}

onMounted(() => {
  void loadInbounds()
  void loadSummary()
  void loadEvents()
})
</script>
