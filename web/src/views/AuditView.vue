<template>
  <div class="page">
    <PageHeader title="审计日志" subtitle="面板写操作与登录记录，保留 90 天（最多 5 万条）">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="reload">刷新</el-button>
      </template>
    </PageHeader>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">操作记录</h3>
        <span class="muted">共 {{ total }} 条</span>
      </div>

      <div class="toolbar toolbar--split card-toolbar">
        <div class="toolbar-group">
          <el-select v-model="filters.action" placeholder="全部动作" clearable class="w-control-md">
            <el-option v-for="action in actions" :key="action" :label="actionLabel(action)" :value="action" />
          </el-select>
          <el-input
            v-model="filters.username"
            placeholder="操作人"
            clearable
            class="w-control-md"
            @keyup.enter="reload"
          />
          <el-date-picker
            v-model="range"
            type="daterange"
            value-format="x"
            start-placeholder="开始日期"
            end-placeholder="结束日期"
            class="w-control-lg"
          />
        </div>
        <div class="toolbar-group">
          <el-button type="primary" @click="reload">查询</el-button>
        </div>
      </div>

      <el-table :data="logs" v-loading="loading" row-key="id">
        <template #empty>
          <EmptyState v-if="hasFilters" text="没有符合条件的记录" :icon="Search">
            <el-button size="small" @click="clearFilters">清除筛选条件</el-button>
          </EmptyState>
          <EmptyState v-else text="暂无审计记录" :icon="Document" />
        </template>
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatDateTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column prop="username" label="操作人" width="120" />
        <el-table-column label="动作" width="180">
          <template #default="{ row }">
            <el-tag size="small" :type="actionTagType(row.action)">{{ actionLabel(row.action) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="对象" width="150">
          <template #default="{ row }">
            <span v-if="row.target_type">{{ row.target_type }}<span v-if="row.target_id"> #{{ row.target_id }}</span></span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="详情" min-width="260">
          <template #default="{ row }">
            <span class="mono muted">{{ row.detail || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="ip" label="IP" width="140" />
      </el-table>

      <div class="pagination">
        <el-pagination
          v-model:current-page="page"
          :page-size="pageSize"
          :total="total"
          layout="total, prev, pager, next"
          background
          hide-on-single-page
          @current-change="load"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElDatePicker, ElOption, ElPagination, ElSelect, ElTable, ElTableColumn } from '@/element-parts'
import { ElMessage } from 'element-plus'
import { Document, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '@/api'
import type { AuditLog } from '@/types'
import { formatDateTime } from '@/utils/format'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'

const loading = ref(false)
const logs = ref<AuditLog[]>([])
const actions = ref<string[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 100
const range = ref<[string, string] | null>(null)
const filters = reactive({ action: '', username: '' })

// hasFilters picks the empty-state copy: an empty result under a filter is a
// dead end with a way out, an empty log is just a new installation.
const hasFilters = computed(
  () => filters.action !== '' || filters.username.trim() !== '' || range.value !== null,
)

const actionLabels: Record<string, string> = {
  'auth.login': '登录',
  'auth.login.failed': '登录失败',
  'auth.logout': '退出登录',
  'admin.password': '修改密码',
  'inbound.create': '创建入站',
  'inbound.update': '更新入站',
  'inbound.delete': '删除入站',
  'inbound.toggle': '启停入站',
  'inbound.restart': '重启入站',
  'settings.update': '修改设置',
  'client.create': '创建用户',
  'client.update': '更新用户',
  'client.delete': '删除用户',
  'client.toggle': '启停用户',
  'client.reset': '重置流量',
  'client.token': '轮换订阅 token',
  'client.kick': '踢下线',
  'client.import': '导入用户',
  'client.export': '导出用户',
}

function actionLabel(action: string): string {
  return actionLabels[action] || action
}

function actionTagType(action: string): 'success' | 'warning' | 'danger' | 'info' {
  if (action.endsWith('.delete') || action === 'auth.login.failed') return 'danger'
  if (action === 'auth.login' || action.endsWith('.create')) return 'success'
  if (action.endsWith('.toggle') || action.endsWith('.reset') || action.endsWith('.kick')) return 'warning'
  return 'info'
}

// loadSeq makes the last request win: paging and filtering can start a second
// request before the first returns, and a late response must not overwrite the
// newer page.
let loadSeq = 0

async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const params: Record<string, string | number> = {
      limit: pageSize,
      offset: (page.value - 1) * pageSize,
    }
    if (filters.action) params.action = filters.action
    if (filters.username) params.username = filters.username
    if (range.value?.length === 2) {
      // The picker yields millisecond timestamps; the API filters on seconds.
      params.since = Math.floor(Number(range.value[0]) / 1000)
      params.until = Math.floor(Number(range.value[1]) / 1000) + 86400
    }
    const result = await api.audit(params)
    if (seq !== loadSeq) return
    logs.value = result.items || []
    total.value = result.total || 0
    actions.value = result.actions || []
  } catch (error: any) {
    if (seq !== loadSeq) return
    ElMessage.error(error?.message || '加载审计日志失败')
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function reload() {
  page.value = 1
  load()
}

/** clearFilters drops every filter and re-queries the first page. */
function clearFilters() {
  filters.action = ''
  filters.username = ''
  range.value = null
  page.value = 1
  load()
}

watch([() => filters.action, () => filters.username], () => {
  page.value = 1
})

onMounted(load)
</script>
