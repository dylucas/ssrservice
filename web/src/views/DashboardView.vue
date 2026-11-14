<template>
  <div class="page">
    <PageHeader title="仪表盘" subtitle="运行状态与最近 30 天流量">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </template>
    </PageHeader>

    <div class="stat-grid">
      <StatCard
        label="总上传 / 下载"
        :value="`${formatBytes(data.up_bytes)} / ${formatBytes(data.down_bytes)}`"
        hint="累计流量"
        :icon="DataLine"
      />
      <StatCard
        label="入站 / 用户"
        :value="`${data.inbounds} / ${data.clients}`"
        :hint="`启用中 ${data.enabled_inbounds} / ${data.enabled_clients}`"
        :icon="Connection"
      />
      <StatCard
        label="在线 IP / 会话"
        :value="`${data.online_ips} / ${data.sessions}`"
        hint="实时连接"
        :icon="UserFilled"
      />
      <StatCard
        label="运行时长"
        :value="formatDuration(data.uptime_seconds)"
        :hint="`版本 ${data.version}`"
        :icon="Timer"
      />
    </div>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">流量趋势（30 天）</h3>
      </div>
      <TrafficChart
        :labels="chartLabels"
        :series="chartSeries"
        aria-label="最近 30 天上传与下载流量"
      />
    </div>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">系统资源</h3>
        <span class="muted">{{ data.system.os }} / {{ data.system.arch }}</span>
      </div>
      <div class="resource-grid">
        <div class="resource-item">
          <div class="resource-label">CPU 使用率</div>
          <div class="resource-value">
            {{ data.system.host_metrics ? `${data.system.cpu_percent.toFixed(1)}%` : '不支持' }}
          </div>
          <div v-if="data.system.host_metrics" class="resource-bar">
            <el-progress :percentage="cpuPercent" :show-text="false" :stroke-width="6" />
          </div>
          <div class="resource-hint">
            逻辑核心 {{ data.system.num_cpu }} 个 · Goroutine {{ data.system.goroutines }}
          </div>
        </div>
        <div class="resource-item">
          <div class="resource-label">内存</div>
          <div class="resource-value">
            <template v-if="data.system.mem_total">
              {{ formatBytes(data.system.mem_used) }} / {{ formatBytes(data.system.mem_total) }}
            </template>
            <template v-else>不支持</template>
          </div>
          <div v-if="data.system.mem_total" class="resource-bar">
            <el-progress :percentage="memPercent" :show-text="false" :stroke-width="6" />
          </div>
          <div class="resource-hint">宿主机内存占用</div>
        </div>
        <div class="resource-item">
          <div class="resource-label">Go 堆内存</div>
          <div class="resource-value">{{ formatBytes(data.system.heap_alloc) }}</div>
          <div class="resource-hint">运行时分配</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElProgress } from '@/element-parts'
import { ElMessage } from 'element-plus'
import { Connection, DataLine, Refresh, Timer, UserFilled } from '@element-plus/icons-vue'
import { api } from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import StatCard from '@/components/StatCard.vue'
import TrafficChart from '@/components/TrafficChart.vue'
import type { Dashboard } from '@/types'
import { formatBytes, formatDuration } from '@/utils/format'

const loading = ref(false)
let timer: number | undefined
// inFlight prevents the manual refresh button and the polling timer from
// stacking requests on a slow link.
let inFlight = false

const data = reactive<Dashboard>({
  version: '',
  uptime_seconds: 0,
  inbounds: 0,
  clients: 0,
  enabled_inbounds: 0,
  enabled_clients: 0,
  online_ips: 0,
  sessions: 0,
  up_bytes: 0,
  down_bytes: 0,
  traffic_30d: [],
  system: {
    os: '',
    arch: '',
    num_cpu: 0,
    goroutines: 0,
    cpu_percent: 0,
    mem_used: 0,
    mem_total: 0,
    heap_alloc: 0,
    host_metrics: false,
  },
})

// The chart is a pure function of the response, so polling only has to update
// the data: the two computeds below rebuild the SVG on every tick.
const chartLabels = computed(() => data.traffic_30d.map((item) => item.day.slice(5)))
const chartSeries = computed(() => [
  { name: '上传', variant: 'up' as const, values: data.traffic_30d.map((item) => item.up_bytes) },
  { name: '下载', variant: 'down' as const, values: data.traffic_30d.map((item) => item.down_bytes) },
])

// el-progress wants an integer percentage and clamps to 0..100 itself, but a
// host that reports no metrics at all would otherwise show a 0% bar that looks
// like "idle" rather than "unknown": the bars are only rendered when the value
// exists.
const cpuPercent = computed(() => Math.round(data.system.cpu_percent))
const memPercent = computed(() =>
  data.system.mem_total > 0
    ? Math.round((data.system.mem_used / data.system.mem_total) * 100)
    : 0,
)

async function load() {
  if (inFlight) return
  inFlight = true
  loading.value = true
  try {
    Object.assign(data, await api.dashboard())
  } catch (error: any) {
    ElMessage.error(error?.message || '加载失败')
  } finally {
    loading.value = false
    inFlight = false
  }
}

// tick runs on the polling interval and skips background tabs, so a panel left
// open in a hidden tab does not keep querying the server.
function tick() {
  if (document.hidden) return
  load()
}

function onVisibilityChange() {
  if (!document.hidden) load()
}

onMounted(() => {
  load()
  document.addEventListener('visibilitychange', onVisibilityChange)
  timer = window.setInterval(tick, 15000)
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibilityChange)
  if (timer) window.clearInterval(timer)
})
</script>
