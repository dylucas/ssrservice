<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">仪表盘</h2>
        <div class="page-subtitle">运行状态与最近 30 天流量</div>
      </div>
      <div class="page-actions">
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <div class="stat-grid">
      <div class="stat-card">
        <div class="stat-label">总上传 / 下载</div>
        <div class="stat-value">{{ formatBytes(data.up_bytes) }} / {{ formatBytes(data.down_bytes) }}</div>
        <div class="stat-hint">累计流量</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">入站 / 用户</div>
        <div class="stat-value">{{ data.inbounds }} / {{ data.clients }}</div>
        <div class="stat-hint">启用中 {{ data.enabled_inbounds }} / {{ data.enabled_clients }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">在线 IP / 会话</div>
        <div class="stat-value">{{ data.online_ips }} / {{ data.sessions }}</div>
        <div class="stat-hint">实时连接</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">运行时长</div>
        <div class="stat-value">{{ formatDuration(data.uptime_seconds) }}</div>
        <div class="stat-hint">版本 {{ data.version }}</div>
      </div>
    </div>

    <div class="card">
      <h3 class="card-title">流量趋势（30 天）</h3>
      <TrafficChart
        :labels="chartLabels"
        :series="chartSeries"
        aria-label="最近 30 天上传与下载流量"
      />
    </div>

    <div class="card">
      <h3 class="card-title">系统资源</h3>
      <el-descriptions :column="3" border>
        <el-descriptions-item label="操作系统">{{ data.system.os }} / {{ data.system.arch }}</el-descriptions-item>
        <el-descriptions-item label="CPU 核心">{{ data.system.num_cpu }}</el-descriptions-item>
        <el-descriptions-item label="CPU 使用率">
          {{ data.system.host_metrics ? data.system.cpu_percent.toFixed(1) + '%' : '不支持' }}
        </el-descriptions-item>
        <el-descriptions-item label="内存">
          <template v-if="data.system.mem_total">
            {{ formatBytes(data.system.mem_used) }} / {{ formatBytes(data.system.mem_total) }}
          </template>
          <template v-else>不支持</template>
        </el-descriptions-item>
        <el-descriptions-item label="Go 堆内存">{{ formatBytes(data.system.heap_alloc) }}</el-descriptions-item>
        <el-descriptions-item label="Goroutine">{{ data.system.goroutines }}</el-descriptions-item>
      </el-descriptions>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElDescriptions, ElDescriptionsItem } from '@/element-parts'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { api } from '@/api'
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
