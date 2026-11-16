<template>
  <div class="page">
    <PageHeader title="入站管理" subtitle="每个入站是一个独立端口上的 Shadowsocks 监听器">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openCreate">新建入站</el-button>
      </template>
    </PageHeader>

    <!-- Summary of what is on screen. It follows the filter below, so the numbers
         and the table always agree; it disappears rather than showing a row of
         zeros when a filter matches nothing. -->
    <div v-if="visible.length" class="summary-bar">
      <template v-for="(item, index) in summary" :key="item.label">
        <span v-if="index" class="summary-sep" />
        <span class="summary-item">
          <span class="summary-label">{{ item.label }}</span>
          <span class="summary-value">{{ item.value }}</span>
        </span>
      </template>
    </div>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">入站列表</h3>
        <span class="muted">{{ countLabel }}</span>
      </div>

      <div class="toolbar card-toolbar">
        <!-- 200px, not the 260px "large" step: the box sits alone at the left of
             an otherwise empty toolbar, where the extra 60px read as an
             oversized field. The placeholder lists the fields that fit at this
             width; the search still covers the method and the domain. -->
        <el-input
          v-model="search"
          :prefix-icon="Search"
          placeholder="搜索备注 / 端口"
          clearable
          class="w-control-md"
        />
        <el-select v-model="statusFilter" class="w-control-sm">
          <el-option label="全部状态" value="all" />
          <el-option label="已启用" value="enabled" />
          <el-option label="已停用" value="disabled" />
        </el-select>
      </div>

      <el-table
        :data="visible"
        v-loading="loading"
        row-key="id"
        :row-class-name="rowClassName"
      >
        <template #empty>
          <EmptyState
            v-if="filtered"
            text="没有符合条件的入站"
            :icon="Search"
          >
            <el-button size="small" @click="clearFilters">清除筛选条件</el-button>
          </EmptyState>
          <EmptyState v-else text="暂无入站，先创建一个监听器" :icon="Connection">
            <el-button type="primary" size="small" :icon="Plus" @click="openCreate">
              新建入站
            </el-button>
          </EmptyState>
        </template>

        <el-table-column label="入站" min-width="220">
          <template #default="{ row }">
            <router-link class="cell-strong inbound-link" :to="`/inbounds/${row.id}`">
              {{ row.remark || '未命名入站' }}
            </router-link>
            <!-- The method is what decides whether the port supports several
                 users or just one, so it belongs next to the name rather than in
                 a column of its own: that keeps the table to six columns. -->
            <div class="muted mono ellipsis cell-sub" :title="`#${row.id} · ${row.method}`">
              #{{ row.id }} · {{ row.method }}
            </div>
          </template>
        </el-table-column>

        <el-table-column label="监听" min-width="200">
          <template #default="{ row }">
            <div class="mono">{{ row.listen }}:{{ row.port }}</div>
            <div class="muted cell-sub">{{ row.udp_enabled ? 'TCP + UDP' : '仅 TCP' }}</div>
            <!-- Only the configured value is shown: the resolution order lives in
                 the service, so the UI does not re-implement it. -->
            <div v-if="row.domain" class="muted mono ellipsis" :title="row.domain">
              域名 {{ row.domain }}<template v-if="row.domain_port">:{{ row.domain_port }}</template>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="用户" width="150">
          <template #default="{ row }">
            <div class="num">
              {{ row.client_count }}<span class="muted"> / {{ row.enabled_client_count }} 启用</span>
            </div>
            <div class="cell-sub">
              <span v-if="row.online_ips" class="chip chip--live">在线 {{ row.online_ips }} IP</span>
              <span v-else class="muted">—</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="流量" width="140" align="right">
          <template #default="{ row }">
            <div class="num">↑ {{ formatBytes(row.up_bytes) }}</div>
            <div class="num">↓ {{ formatBytes(row.down_bytes) }}</div>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <!-- The switch is the control; the row is dimmed when it is off, so
                 the state is readable without walking the column. -->
            <el-switch
              :model-value="row.enabled"
              :aria-label="`${row.remark || '入站'} 启用开关`"
              size="small"
              @change="(value: string | number | boolean) => toggle(asInbound(row), value === true)"
            />
          </template>
        </el-table-column>

        <el-table-column label="操作" width="230" fixed="right">
          <template #default="{ row }">
            <el-button size="small" text type="primary" @click="goDetail(asInbound(row))">详情</el-button>
            <el-button size="small" text @click="goClients(asInbound(row))">用户</el-button>
            <el-button size="small" text @click="openEdit(asInbound(row))">编辑</el-button>
            <el-dropdown
              class="row-more"
              trigger="click"
              @command="(command: string) => onRowCommand(command, asInbound(row))"
            >
              <el-button size="small" text>
                更多<el-icon class="el-icon--right"><ArrowDown /></el-icon>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="restart">重启监听</el-dropdown-item>
                  <el-dropdown-item command="delete" divided>删除</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <InboundForm
      v-model="formVisible"
      :inbound="editing"
      :methods="methods"
      @saved="load"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElOption, ElSelect, ElSwitch, ElTable, ElTableColumn } from '@/element-parts'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, Connection, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '@/api'
import type { Inbound } from '@/types'
import { formatBytes } from '@/utils/format'
import {
  filterInbounds,
  hasInboundFilter,
  summarizeInbounds,
  type InboundStatusFilter,
} from '@/utils/inbounds'
import EmptyState from '@/components/EmptyState.vue'
import InboundForm from '@/components/InboundForm.vue'
import PageHeader from '@/components/PageHeader.vue'

// el-table's slot row is typed as DefaultRow, but this table is always bound to
// Inbound rows. Narrowing here keeps the handlers strongly typed instead of
// widening every signature to `any`.
function asInbound(row: unknown): Inbound {
  return row as Inbound
}

const router = useRouter()
const loading = ref(false)
const inbounds = ref<Inbound[]>([])
const methods = ref<string[]>([])
const formVisible = ref(false)
const editing = ref<Inbound | null>(null)

const search = ref('')
const statusFilter = ref<InboundStatusFilter>('all')

// The API returns the whole list (there is no paging for inbounds), so the
// filters run here and respond instantly - no request per keystroke, and no
// debounce to get wrong.
const filtered = computed(() => hasInboundFilter({ status: statusFilter.value, query: search.value }))
const visible = computed(() => filterInbounds(inbounds.value, { status: statusFilter.value, query: search.value }))

const summary = computed(() => {
  const totals = summarizeInbounds(visible.value)
  return [
    { label: '入站', value: String(totals.total) },
    { label: '已启用', value: String(totals.enabled) },
    { label: '用户', value: String(totals.clients) },
    { label: '上传', value: formatBytes(totals.up_bytes) },
    { label: '下载', value: formatBytes(totals.down_bytes) },
  ]
})

const countLabel = computed(() =>
  filtered.value ? `显示 ${visible.value.length} / ${inbounds.value.length} 个` : `共 ${inbounds.value.length} 个`,
)

/** rowClassName dims a stopped inbound so the live ones stand out. */
function rowClassName({ row }: { row: Inbound }): string {
  return row.enabled ? '' : 'row-disabled'
}

function clearFilters() {
  search.value = ''
  statusFilter.value = 'all'
}

// loadSeq makes the last request win: the refresh button and a form save can
// start overlapping requests, and a late response must not overwrite the newer
// list.
let loadSeq = 0

/** load refreshes the table; it resolves false (and reports) instead of throwing. */
async function load(): Promise<boolean> {
  const seq = ++loadSeq
  loading.value = true
  try {
    const result = await api.inbounds()
    if (seq !== loadSeq) return true
    inbounds.value = result.items || []
    methods.value = result.methods || []
    return true
  } catch (error: any) {
    if (seq !== loadSeq) return false
    ElMessage.error(error?.message || '加载失败')
    return false
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function openCreate() {
  editing.value = null
  formVisible.value = true
}

function openEdit(row: Inbound) {
  editing.value = row
  formVisible.value = true
}

function goClients(row: Inbound) {
  router.push({ name: 'clients', query: { inbound: String(row.id) } })
}

/** goDetail opens the per-inbound detail page. */
function goDetail(row: Inbound) {
  void router.push(`/inbounds/${row.id}`)
}

/** onRowCommand dispatches the "more" menu to the existing row handlers. */
function onRowCommand(command: string, row: Inbound) {
  switch (command) {
    case 'restart':
      restart(row)
      break
    case 'delete':
      remove(row)
      break
  }
}

async function toggle(row: Inbound, enabled: boolean) {
  try {
    await api.toggleInbound(row.id, enabled)
    row.enabled = enabled
    ElMessage.success(enabled ? '已启用' : '已停用')
  } catch (error: any) {
    ElMessage.error(error?.message || '操作失败')
    await load()
  }
}

async function restart(row: Inbound) {
  try {
    await api.restartInbound(row.id)
    ElMessage.success('监听器已重启')
  } catch (error: any) {
    ElMessage.error(error?.message || '重启失败')
  }
}

async function remove(row: Inbound) {
  try {
    await ElMessageBox.confirm(
      `删除入站「${row.remark || row.id}」会同时删除其全部用户和流量记录，且不可恢复。`,
      '确认删除',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    await api.deleteInbound(row.id)
  } catch (error: any) {
    ElMessage.error(error?.message || '删除失败')
    return
  }
  // The delete already succeeded; a failed refresh afterwards is a different
  // problem and must not be reported as "删除失败".
  if (await load()) {
    ElMessage.success('已删除')
  } else {
    ElMessage.warning('已删除，但刷新列表失败')
  }
}

onMounted(load)
</script>
