<template>
  <div class="page">
    <PageHeader title="用户管理" subtitle="按用户统计流量、限制配额与到期时间">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="reload">刷新</el-button>
        <el-button type="primary" :icon="Plus" :disabled="!inbounds.length" @click="openCreate">
          新建用户
        </el-button>
      </template>
    </PageHeader>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">用户列表</h3>
        <span class="muted">共 {{ total }} 个用户</span>
      </div>

      <div class="toolbar toolbar--split card-toolbar">
        <div class="toolbar-group">
          <el-input
            v-model="search"
            :prefix-icon="Search"
            placeholder="搜索用户名 / 备注"
            clearable
            class="w-control-lg"
          />
          <el-select v-model="statusFilter" class="w-control-sm" @change="onFilterChange">
            <el-option label="全部状态" value="all" />
            <el-option v-for="(meta, key) in statusMeta" :key="key" :label="meta.label" :value="key" />
          </el-select>
          <el-select v-model="inboundFilter" class="w-control-md" @change="onFilterChange">
            <el-option label="全部入站" :value="0" />
            <el-option v-for="item in inbounds" :key="item.id" :label="`${item.remark || '入站'} (:${item.port})`" :value="item.id" />
          </el-select>
        </div>
        <div class="toolbar-group">
          <el-button :icon="Upload" :disabled="!inbounds.length" @click="openImport">导入</el-button>
          <el-button :icon="Download" @click="exportClients">导出</el-button>
        </div>
      </div>

      <el-table :data="clients" v-loading="loading" row-key="id">
        <template #empty>
          <EmptyState v-if="hasFilters" text="没有符合条件的用户" :icon="Search">
            <el-button size="small" @click="clearFilters">清除筛选条件</el-button>
          </EmptyState>
          <EmptyState v-else text="暂无用户，先创建一个" :icon="UserIcon">
            <el-button
              type="primary"
              size="small"
              :icon="Plus"
              :disabled="!inbounds.length"
              @click="openCreate"
            >
              新建用户
            </el-button>
          </EmptyState>
        </template>
        <el-table-column label="用户" min-width="150">
          <template #default="{ row }">
            <div>{{ row.name }}</div>
            <div class="muted">{{ row.inbound_remark }} · {{ row.method }}</div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="statusMeta[row.status]?.type || 'info'" size="small">
              {{ statusMeta[row.status]?.label || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="流量" min-width="200">
          <template #default="{ row }">
            <div class="progress-row">
              <el-progress
                v-if="row.quota_bytes"
                :percentage="percent(row.used_bytes, row.quota_bytes)"
                :status="percent(row.used_bytes, row.quota_bytes) >= 100 ? 'exception' : undefined"
                :stroke-width="8"
              />
              <div class="muted">{{ quotaLabel(asClient(row)) }}</div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="到期" width="160">
          <template #default="{ row }">{{ formatDate(row.expires_at) }}</template>
        </el-table-column>
        <el-table-column label="在线 IP" min-width="130">
          <template #default="{ row }">
            <span v-if="row.online_ips?.length" class="mono">{{ row.online_ips.join(', ') }}</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <el-button size="small" text type="primary" @click="openLink(asClient(row))">链接</el-button>
            <el-button size="small" text @click="openUsage(asClient(row))">趋势</el-button>
            <el-button size="small" text @click="openEdit(asClient(row))">编辑</el-button>
            <el-dropdown
              class="row-more"
              trigger="click"
              @command="(command: string) => onRowCommand(command, asClient(row))"
            >
              <el-button size="small" text>
                更多<el-icon class="el-icon--right"><ArrowDown /></el-icon>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="toggle">{{ row.enabled ? '禁用' : '启用' }}</el-dropdown-item>
                  <el-dropdown-item command="reset">重置流量</el-dropdown-item>
                  <el-dropdown-item command="kick">踢下线</el-dropdown-item>
                  <el-dropdown-item command="delete" divided>删除</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
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
          @current-change="load"
        />
      </div>
    </div>

    <ClientForm
      v-model="formVisible"
      :inbound-id="formInboundId"
      :client="editing"
      :method="editingMethod"
      @saved="onClientSaved"
    />
    <ClientLinkDialog v-model="linkVisible" :client-id="linkClientId" />
    <ClientImportDialog
      v-model="importVisible"
      :inbounds="inbounds"
      :default-inbound-id="importInboundId"
      @imported="onClientsImported"
    />
    <ClientUsageDialog
      v-model="usageVisible"
      :client-id="usageClientId"
      :client-name="usageClientName"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElOption, ElPagination, ElProgress, ElSelect, ElTable, ElTableColumn } from '@/element-parts'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ArrowDown,
  Download,
  Plus,
  Refresh,
  Search,
  Upload,
  User as UserIcon,
} from '@element-plus/icons-vue'
import { api } from '@/api'
import type { ClientListParams, ClientSummary, Inbound } from '@/types'
import { formatDate, percent, quotaLabel, statusMeta } from '@/utils/format'
import {
  CLIENT_PAGE_SIZE,
  buildClientListParams,
  hasClientFilter,
  lastPage,
  toSummary,
} from '@/utils/clients'
import ClientForm from '@/components/ClientForm.vue'
import ClientLinkDialog from '@/components/ClientLinkDialog.vue'
import ClientUsageDialog from '@/components/ClientUsageDialog.vue'
import ClientImportDialog from '@/components/ClientImportDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'

// el-table's slot row is typed as DefaultRow, but this table is always bound to
// ClientSummary rows. Narrowing here keeps the handlers strongly typed instead of
// widening every signature to `any` (which is what the previous global
// registration effectively did).
function asClient(row: unknown): ClientSummary {
  return row as ClientSummary
}

const route = useRoute()
const loading = ref(false)
const inbounds = ref<Inbound[]>([])
// clients holds one page, not the whole table: filtering and paging happen in
// SQL, which is what keeps the list cheap on a deployment with thousands of
// users.
const clients = ref<ClientSummary[]>([])
const total = ref(0)
const inboundFilter = ref(0)
const search = ref('')
const statusFilter = ref<'all' | ClientSummary['status']>('all')
const page = ref(1)
const pageSize = CLIENT_PAGE_SIZE

// hasFilters drives the empty state: "nothing matches" and "nothing exists yet"
// are different dead ends, and only the first one has a way out.
const hasFilters = computed(
  () =>
    hasClientFilter({ status: statusFilter.value, query: search.value }) ||
    inboundFilter.value > 0,
)

// loadSeq makes the last request win: typing in the search box can start a
// second request before the first one returns, and a late response must not
// overwrite the newer page.
let loadSeq = 0

const formVisible = ref(false)
const formInboundId = ref(0)
const editing = ref<ClientSummary | null>(null)
const editingMethod = ref('')

const linkVisible = ref(false)
const linkClientId = ref<number | null>(null)

const importVisible = ref(false)
const importInboundId = ref(0)

const usageVisible = ref(false)
const usageClientId = ref<number | null>(null)
const usageClientName = ref('')

const editingMethodComputed = computed(() => {
  if (!editing.value) return ''
  const inbound = inbounds.value.find((item) => item.id === editing.value?.inbound_id)
  return inbound?.method || editing.value.method
})

function currentParams(): ClientListParams {
  return buildClientListParams({
    inboundId: inboundFilter.value,
    status: statusFilter.value,
    query: search.value,
    page: page.value,
    pageSize,
  })
}

/**
 * loadInbounds refreshes the inbound filter and the form's method lookup.
 *
 * It reports failure instead of throwing: it runs inside reload(), which is
 * called fire-and-forget from the save/import handlers, and an unhandled
 * rejection there would leave the operator with no feedback at all.
 */
async function loadInbounds(): Promise<boolean> {
  try {
    const result = await api.inbounds()
    inbounds.value = result.items || []
    return true
  } catch (error: any) {
    ElMessage.error(error?.message || '加载入站列表失败')
    return false
  }
}

/** load fetches one page; it resolves false (and reports) instead of throwing. */
async function load(): Promise<boolean> {
  const seq = ++loadSeq
  loading.value = true
  try {
    const result = await api.clients(currentParams())
    if (seq !== loadSeq) return true
    clients.value = result.items || []
    total.value = result.total || 0
    return true
  } catch (error: any) {
    if (seq !== loadSeq) return false
    ElMessage.error(error?.message || '加载失败')
    return false
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

/**
 * reload refreshes both the pager contents and the inbound aggregates.
 *
 * It never rejects: callers treat it as a notification ("something changed,
 * refresh") and an unhandled rejection there would hide the failure. Each leg
 * reports its own error, and the return value lets a caller tell the operator
 * that a completed action could not be followed by a refresh.
 */
async function reload(): Promise<boolean> {
  const [inboundsOK, clientsOK] = await Promise.all([loadInbounds(), load()])
  return inboundsOK && clientsOK
}

/** onFilterChange returns to the first page, which is where a new filter starts. */
function onFilterChange() {
  page.value = 1
  load()
}

/**
 * clearFilters resets every filter at once.
 *
 * Resetting the search box also wakes its debounce watcher, which would fire a
 * second request 300ms behind the one below; the flag makes that flush a no-op.
 */
function clearFilters() {
  clearingFilters = true
  search.value = ''
  statusFilter.value = 'all'
  inboundFilter.value = 0
  page.value = 1
  void nextTick(() => {
    clearingFilters = false
  })
  load()
}

function openImport() {
  importInboundId.value = inboundFilter.value || inbounds.value[0]?.id || 0
  importVisible.value = true
}

function exportClients() {
  // A plain navigation lets the browser handle the attachment download.
  window.location.href = api.exportClientsURL(inboundFilter.value || undefined)
}

function openCreate() {
  editing.value = null
  formInboundId.value = inboundFilter.value || inbounds.value[0]?.id || 0
  editingMethod.value = inbounds.value.find((item) => item.id === formInboundId.value)?.method || ''
  formVisible.value = true
}

function openEdit(row: ClientSummary) {
  editing.value = row
  formInboundId.value = row.inbound_id
  editingMethod.value = editingMethodComputed.value || row.method
  formVisible.value = true
}

function openLink(row: ClientSummary) {
  linkClientId.value = row.id
  linkVisible.value = true
}

/** onClientSaved shows a newly created user by reloading at the last page. */
function onClientSaved() {
  if (!editing.value && !hasClientFilter({ status: statusFilter.value, query: search.value })) {
    page.value = lastPage(total.value + 1, pageSize)
  }
  reload()
}

/** onClientsImported refreshes the list after a bulk import. */
function onClientsImported() {
  reload()
}

/** onRowCommand dispatches the "more" menu to the existing row handlers. */
function onRowCommand(command: string, row: ClientSummary) {
  switch (command) {
    case 'toggle':
      toggle(row)
      break
    case 'reset':
      resetTraffic(row)
      break
    case 'kick':
      kick(row)
      break
    case 'delete':
      remove(row)
      break
  }
}

function openUsage(row: ClientSummary) {
  usageClientId.value = row.id
  usageClientName.value = row.name
  usageVisible.value = true
}

async function toggle(row: ClientSummary) {
  try {
    await api.toggleClient(row.id, !row.enabled)
  } catch (error: any) {
    ElMessage.error(error?.message || '操作失败')
    return
  }
  // Flipping enabled can move the row in or out of a status filter, so the page
  // is refetched instead of patched in place. The toggle already succeeded, so a
  // failed refresh is reported as such instead of as a failed operation.
  if (await load()) {
    ElMessage.success(row.enabled ? '已禁用' : '已启用')
  } else {
    ElMessage.warning('操作已生效，但刷新列表失败')
  }
}

async function resetTraffic(row: ClientSummary) {
  try {
    await ElMessageBox.confirm(`确定将用户「${row.name}」的流量清零吗？`, '重置流量', {
      type: 'warning',
      confirmButtonText: '重置',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await api.resetClient(row.id)
  } catch (error: any) {
    ElMessage.error(error?.message || '重置失败')
    return
  }
  // A reset can move the row out of an active status filter or change the
  // matching total, so a single-row patch would leave the rows and the total
  // disagreeing. Refetch the page in that case, patch in place otherwise.
  if (statusFilter.value === 'all') {
    try {
      // The reset clears the counters and can flip quota_exceeded back to
      // active, so refresh just this row: the server recomputes the status.
      const refreshed = toSummary(await api.client(row.id))
      const index = clients.value.findIndex((item) => item.id === row.id)
      if (index >= 0) {
        clients.value[index] = refreshed
      }
      ElMessage.success('流量已重置')
    } catch (error: any) {
      // The reset itself succeeded; only the row refresh failed.
      ElMessage.warning(`流量已重置，但刷新该行失败：${error?.message || '未知错误'}`)
    }
    return
  }
  if (await load()) {
    ElMessage.success('流量已重置')
  } else {
    ElMessage.warning('流量已重置，但刷新列表失败')
  }
}

async function kick(row: ClientSummary) {
  try {
    await api.kickClient(row.id)
    // Every session of the client is gone, so its online IP list is empty; no
    // need to refetch the page for that.
    row.online_ips = []
    ElMessage.success('已断开该用户的连接')
  } catch (error: any) {
    ElMessage.error(error?.message || '操作失败')
  }
}

async function remove(row: ClientSummary) {
  try {
    await ElMessageBox.confirm(`删除用户「${row.name}」及其流量记录，且不可恢复。`, '确认删除', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await api.deleteClient(row.id)
  } catch (error: any) {
    ElMessage.error(error?.message || '删除失败')
    return
  }
  // The row is gone from the server's count; step back a page when the last row
  // of the last page was just deleted.
  if (clients.value.length === 1 && page.value > 1) {
    page.value -= 1
  }
  // The delete already succeeded, so a failed refresh is reported as its own
  // problem instead of masquerading as a failed delete.
  if (await reload()) {
    ElMessage.success('已删除')
  } else {
    ElMessage.warning('已删除，但刷新列表失败')
  }
}

// Debounce the search box: one request per meaningful pause instead of one per
// keystroke, which matters because every request is now a filtered page query.
let searchTimer: number | undefined
// clearingFilters suppresses the one debounced request a programmatic reset
// would otherwise queue behind the explicit load() that clearFilters runs.
let clearingFilters = false

watch(search, () => {
  if (clearingFilters) return
  if (searchTimer) window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    page.value = 1
    load()
  }, 300)
})

onMounted(async () => {
  const inboundParam = Number(route.query.inbound)
  if (Number.isFinite(inboundParam) && inboundParam > 0) {
    inboundFilter.value = inboundParam
  }
  await reload()
})

onBeforeUnmount(() => {
  if (searchTimer) window.clearTimeout(searchTimer)
})
</script>
