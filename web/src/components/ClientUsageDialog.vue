<template>
  <el-dialog
    :model-value="modelValue"
    :title="title"
    width="min(720px, 92vw)"
    @update:model-value="emit('update:modelValue', $event)"
    @opened="load"
    @closed="reset"
  >
    <div class="toolbar">
      <el-radio-group v-model="days" size="small" @change="load">
        <el-radio-button :value="7">7 天</el-radio-button>
        <el-radio-button :value="30">30 天</el-radio-button>
        <el-radio-button :value="90">90 天</el-radio-button>
      </el-radio-group>
      <div class="totals">
        <span>上传 <strong>{{ formatBytes(totals.up) }}</strong></span>
        <span>下载 <strong>{{ formatBytes(totals.down) }}</strong></span>
        <span>合计 <strong>{{ formatBytes(totals.up + totals.down) }}</strong></span>
      </div>
    </div>

    <div v-loading="loading" class="chart-wrap">
      <div v-if="!loading && !hasData" class="empty-state">该区间还没有流量记录</div>
      <div v-else ref="chartEl" class="chart"></div>
    </div>

    <template #footer>
      <el-button @click="emit('update:modelValue', false)">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '@/api'
import type { DailyUsage } from '@/types'
import { formatBytes } from '@/utils/format'
import { summarizeUsage } from '@/utils/usage'
import type { ECharts } from '@/utils/charts'

const props = defineProps<{ modelValue: boolean; clientId: number | null; clientName?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

const days = ref(30)
const loading = ref(false)
const usage = ref<DailyUsage[]>([])
const chartEl = ref<HTMLDivElement>()
const hasData = computed(() => usage.value.length > 0)
const totals = computed(() => summarizeUsage(usage.value))
const title = computed(() =>
  props.clientName ? `流量趋势 · ${props.clientName}` : '流量趋势',
)

let chart: ECharts | null = null
// echarts is imported lazily so opening the clients page does not pull the chart
// library into its route chunk.
let loadChart: (() => Promise<typeof import('@/utils/charts')>) | null = null

function onResize() {
  chart?.resize()
}

/** render builds the chart after the data has been applied. */
async function render() {
  if (!hasData.value) return
  // Wait for the canvas element: it only exists once hasData flipped.
  await nextTick()
  if (!chartEl.value) return
  if (!loadChart) {
    loadChart = () => import('@/utils/charts')
  }
  const { echarts } = await loadChart()
  if (!chart) {
    chart = echarts.init(chartEl.value)
    window.addEventListener('resize', onResize)
  }
  const labels = usage.value.map((item) => item.day.slice(5))
  chart.setOption({
    tooltip: { trigger: 'axis', valueFormatter: (value: number) => formatBytes(value) },
    legend: { data: ['上传', '下载'] },
    grid: { left: 10, right: 20, top: 40, bottom: 10, containLabel: true },
    xAxis: { type: 'category', data: labels, boundaryGap: false },
    yAxis: { type: 'value', axisLabel: { formatter: (value: number) => formatBytes(value) } },
    series: [
      { name: '上传', type: 'line', smooth: true, areaStyle: { opacity: 0.12 }, data: usage.value.map((item) => item.up_bytes) },
      { name: '下载', type: 'line', smooth: true, areaStyle: { opacity: 0.12 }, data: usage.value.map((item) => item.down_bytes) },
    ],
  }, true)
}

async function load() {
  if (!props.clientId) return
  loading.value = true
  try {
    const result = await api.clientUsage(props.clientId, days.value)
    usage.value = result.items || []
    await render()
  } catch (error: any) {
    ElMessage.error(error?.message || '加载流量趋势失败')
    usage.value = []
  } finally {
    loading.value = false
  }
}

function reset() {
  usage.value = []
  chart?.dispose()
  chart = null
  window.removeEventListener('resize', onResize)
}

onBeforeUnmount(reset)
</script>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.totals {
  display: flex;
  gap: 16px;
  color: var(--sspanel-text-muted);
  font-size: 13px;
}
</style>
