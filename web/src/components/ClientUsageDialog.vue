<template>
  <el-dialog
    :model-value="modelValue"
    :title="title"
    width="min(720px, 92vw)"
    @update:model-value="emit('update:modelValue', $event)"
    @opened="load"
    @closed="reset"
  >
    <div class="toolbar toolbar--split card-toolbar">
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
      <!-- The empty state lives in TrafficChart: it owns the "no traffic in this
           window" decision, which is the same one the dashboard needs. -->
      <TrafficChart
        :labels="chartLabels"
        :series="chartSeries"
        aria-label="该用户的上传与下载流量趋势"
      />
    </div>

    <template #footer>
      <el-button @click="emit('update:modelValue', false)">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElRadioButton, ElRadioGroup } from '@/element-parts'
import { ElMessage } from 'element-plus'
import { api } from '@/api'
import type { DailyUsage } from '@/types'
import { formatBytes } from '@/utils/format'
import { summarizeUsage } from '@/utils/usage'
import TrafficChart from '@/components/TrafficChart.vue'

const props = defineProps<{ modelValue: boolean; clientId: number | null; clientName?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

const days = ref(30)
const loading = ref(false)
const usage = ref<DailyUsage[]>([])
const totals = computed(() => summarizeUsage(usage.value))
const chartLabels = computed(() => usage.value.map((item) => item.day.slice(5)))
const chartSeries = computed(() => [
  { name: '上传', variant: 'up' as const, values: usage.value.map((item) => item.up_bytes) },
  { name: '下载', variant: 'down' as const, values: usage.value.map((item) => item.down_bytes) },
])
const title = computed(() =>
  props.clientName ? `流量趋势 · ${props.clientName}` : '流量趋势',
)

// loadSeq makes the last request win. Switching 7/30/90 days quickly can have
// the slower earlier request return last, which used to leave the chart showing
// a window the buttons no longer selected.
let loadSeq = 0

async function load() {
  if (!props.clientId) return
  const seq = ++loadSeq
  const clientId = props.clientId
  const requestedDays = days.value
  loading.value = true
  try {
    const result = await api.clientUsage(clientId, requestedDays)
    if (seq !== loadSeq || clientId !== props.clientId || requestedDays !== days.value) return
    usage.value = result.items || []
  } catch (error: any) {
    if (seq !== loadSeq) return
    ElMessage.error(error?.message || '加载流量趋势失败')
    usage.value = []
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function reset() {
  // Invalidate any in-flight request so it cannot refill the cleared chart.
  loadSeq++
  usage.value = []
}
</script>

<style scoped>
.totals {
  display: flex;
  gap: var(--sspanel-space-7);
  color: var(--sspanel-text-muted);
  font-size: var(--sspanel-fs-sm);
}
</style>
