<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">仪表盘</h2>
        <div class="page-subtitle">运行状态与最近 30 天流量</div>
      </div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
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
      <div ref="chartEl" class="chart"></div>
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
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import * as echarts from 'echarts'
import { api } from '@/api'
import type { Dashboard } from '@/types'
import { formatBytes, formatDuration } from '@/utils/format'

const loading = ref(false)
const chartEl = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null
let timer: number | undefined

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

function renderChart() {
  if (!chartEl.value) return
  if (!chart) chart = echarts.init(chartEl.value)
  const days = data.traffic_30d.map((item) => item.day.slice(5))
  const up = data.traffic_30d.map((item) => item.up_bytes)
  const down = data.traffic_30d.map((item) => item.down_bytes)
  chart.setOption({
    tooltip: {
      trigger: 'axis',
      valueFormatter: (value: number) => formatBytes(value),
    },
    legend: { data: ['上传', '下载'] },
    grid: { left: 10, right: 20, top: 40, bottom: 10, containLabel: true },
    xAxis: { type: 'category', data: days, boundaryGap: false },
    yAxis: { type: 'value', axisLabel: { formatter: (value: number) => formatBytes(value) } },
    series: [
      { name: '上传', type: 'line', smooth: true, areaStyle: { opacity: 0.12 }, data: up },
      { name: '下载', type: 'line', smooth: true, areaStyle: { opacity: 0.12 }, data: down },
    ],
  })
}

async function load() {
  loading.value = true
  try {
    Object.assign(data, await api.dashboard())
    renderChart()
  } catch (error: any) {
    ElMessage.error(error?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

function onResize() {
  chart?.resize()
}

onMounted(() => {
  load()
  window.addEventListener('resize', onResize)
  timer = window.setInterval(load, 15000)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  if (timer) window.clearInterval(timer)
  chart?.dispose()
})
</script>
