<template>
  <div class="page">
    <PageHeader :title="detail?.inbound.remark || `入站 #${id}`" subtitle="该入站的运行状态与流量">
      <template #actions>
        <el-select :model-value="days" class="w-control-sm" @change="onWindowChange">
          <el-option v-for="option in ACCESS_WINDOWS" :key="option" :label="`最近 ${option} 天`" :value="option" />
        </el-select>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        <el-button @click="router.back()">返回</el-button>
      </template>
    </PageHeader>

    <el-alert
      v-if="detail?.inbound.last_error"
      type="warning"
      show-icon
      :closable="false"
      class="mb-4"
      title="该入站当前未能监听"
    >
      <template #default>{{ detail.inbound.last_error }}</template>
    </el-alert>

    <div v-if="error" class="card">
      <EmptyState :text="`加载失败：${error}`" :icon="Connection">
        <el-button size="small" @click="load">重试</el-button>
      </EmptyState>
    </div>

    <template v-else>
      <div class="stat-grid">
        <StatCard label="用户 / 启用" :value="`${totalUsers} / ${counts.active}`" hint="按当前状态" :icon="User" />
        <StatCard label="在线 IP" :value="String(detail?.inbound.online_ips || 0)" hint="实时连接" :icon="Connection" />
        <StatCard label="累计上传 / 下载" :value="`${formatBytes(detail?.inbound.up_bytes || 0)} / ${formatBytes(detail?.inbound.down_bytes || 0)}`" hint="已落库" :icon="DataLine" />
        <StatCard label="监听地址" :value="`${detail?.inbound.listen || '—'}:${detail?.inbound.port || ''}`" :hint="detail?.inbound.method || ''" :icon="Connection" />
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">流量趋势</h3>
          <span class="muted">最近 {{ days }} 天 · 仅该入站用户</span>
        </div>
        <TrafficChart :labels="chartLabels" :series="chartSeries" empty-text="该区间还没有流量记录" aria-label="该入站的上传与下载流量趋势" />
      </div>

      <div class="access-lists">
        <div class="card">
          <div class="card-head">
            <h3 class="card-title">最忙用户</h3>
            <span class="muted">按累计流量 · 点击看该用户</span>
          </div>
          <el-table :data="detail?.top_clients || []" v-loading="loading" row-key="id" class="access-clickable" @row-click="goClient">
            <template #empty>
              <EmptyState text="该入站还没有用户" :icon="User" />
            </template>
            <el-table-column label="用户" min-width="180">
              <template #default="{ row }">
                <span class="cell-strong ellipsis">{{ row.name }}</span>
                <el-tag v-if="!row.enabled" size="small" type="info" class="ml-1">已禁用</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="上传 / 下载" min-width="200" align="right">
              <template #default="{ row }">
                <span class="num">{{ formatBytes(row.up_bytes) }} / {{ formatBytes(row.down_bytes) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </div>

        <div class="card">
          <div class="card-head">
            <h3 class="card-title">状态分布</h3>
            <span class="muted">点击数字筛选用户</span>
          </div>
          <div class="status-list">
            <router-link
              v-for="item in statusItems"
              :key="item.status"
              :to="{ path: '/clients', query: { inbound: String(id), status: item.status } }"
              class="status-row"
            >
              <span class="status-label">{{ item.label }}</span>
              <span class="num">{{ item.value }}</span>
            </router-link>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">最近拦截与失败</h3>
          <span class="muted">最多 20 条 · 按时间倒序</span>
        </div>
        <el-table :data="detail?.recent_failures || []" v-loading="loading" row-key="id">
          <template #empty>
            <EmptyState
              v-if="!detail?.access_recording"
              text="访问记录未开启，开启后才能看到被拦截或失败的目标"
              :icon="DataAnalysis"
            >
              <el-button size="small" @click="router.push('/settings')">去设置</el-button>
            </EmptyState>
            <EmptyState v-else text="该入站最近没有拦截或失败的访问" :icon="Aim" />
          </template>
          <el-table-column label="时间" width="170">
            <template #default="{ row }">{{ formatDateTime(row.started_at) }}</template>
          </el-table-column>
          <el-table-column label="用户" width="150">
            <template #default="{ row }">{{ row.client_name || '已删除用户' }}</template>
          </el-table-column>
          <el-table-column label="目标" min-width="220">
            <template #default="{ row }">{{ formatHostPort(row.host, row.port) }}</template>
          </el-table-column>
          <el-table-column label="结果" width="100">
            <template #default="{ row }">
              <el-tag :type="outcomeTagType(row.outcome)" size="small">{{ outcomeLabel(row.outcome) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="原因" min-width="140">
            <template #default="{ row }">{{ reasonText(asEvent(row)) || '—' }}</template>
          </el-table-column>
        </el-table>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElAlert, ElOption, ElSelect, ElTable, ElTableColumn } from '@/element-parts'
import { Aim, Connection, DataAnalysis, DataLine, Refresh, User } from '@element-plus/icons-vue'
import { api } from '@/api'
import type { AccessEvent, InboundDetail } from '@/types'
import {
  ACCESS_WINDOWS,
  formatHostPort,
  outcomeLabel,
  outcomeTagType,
  reasonText,
} from '@/utils/access'
import { formatBytes, formatDateTime } from '@/utils/format'
import { inboundChart, inboundStatusItems, inboundUserTotal } from '@/utils/inbound'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatCard from '@/components/StatCard.vue'
import TrafficChart from '@/components/TrafficChart.vue'

function asEvent(row: unknown): AccessEvent {
  return row as AccessEvent
}

const route = useRoute()
const router = useRouter()
const id = Number(route.params.id)
const days = ref(30)
const detail = ref<InboundDetail | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const counts = computed(() => detail.value?.status_counts || {
  active: 0, disabled: 0, expired: 0, quota_exceeded: 0,
})

const statusItems = computed(() => inboundStatusItems(counts.value))

// The stat card counts the same four buckets the status list links to.
const totalUsers = computed(() => inboundUserTotal(counts.value))

const chart = computed(() => inboundChart(detail.value?.daily || []))
const chartLabels = computed(() => chart.value.labels)
const chartSeries = computed(() => chart.value.series)

let loadSeq = 0
async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const result = await api.inboundDetail(id, days.value)
    if (seq !== loadSeq) return
    detail.value = result
    error.value = null
  } catch (err: any) {
    if (seq !== loadSeq) return
    error.value = err?.message || '加载入站详情失败'
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function onWindowChange(value: number) {
  days.value = Number(value)
  void load()
}

function goClient(row: { id: number }) {
  void router.push({ path: '/clients', query: { inbound: String(id) } })
}

onMounted(() => { void load() })
</script>
