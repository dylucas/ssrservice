<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">用户管理</h2>
        <div class="page-subtitle">按用户统计流量、限制配额与到期时间</div>
      </div>
      <div class="toolbar" style="margin: 0">
        <el-input
          v-model="search"
          :prefix-icon="Search"
          placeholder="搜索用户名 / 备注"
          clearable
          style="width: 200px"
        />
        <el-select v-model="statusFilter" style="width: 140px">
          <el-option label="全部状态" value="all" />
          <el-option v-for="(meta, key) in statusMeta" :key="key" :label="meta.label" :value="key" />
        </el-select>
        <el-select v-model="inboundFilter" style="width: 200px" @change="onInboundChange">
          <el-option label="全部入站" :value="0" />
          <el-option v-for="item in inbounds" :key="item.id" :label="`${item.remark || '入站'} (:${item.port})`" :value="item.id" />
        </el-select>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        <el-button :icon="Download" @click="exportClients">导出</el-button>
        <el-button :icon="Upload" :disabled="!inbounds.length" @click="openImport">导入</el-button>
        <el-button type="primary" :icon="Plus" :disabled="!inbounds.length" @click="openCreate">新建用户</el-button>
      </div>
    </div>

    <div class="card">
      <el-table :data="pagedClients" v-loading="loading" row-key="id" :empty-text="emptyText">
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
              <div class="muted">{{ quotaLabel(row) }}</div>
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
        <el-table-column label="操作" width="390" fixed="right">
          <template #default="{ row }">
            <el-button size="small" text type="primary" @click="openLink(row)">链接</el-button>
            <el-button size="small" text @click="openUsage(row)">趋势</el-button>
            <el-button size="small" text @click="openEdit(row)">编辑</el-button>
            <el-button size="small" text @click="toggle(row)">{{ row.enabled ? '禁用' : '启用' }}</el-button>
            <el-button size="small" text @click="resetTraffic(row)">重置流量</el-button>
            <el-button size="small" text @click="kick(row)">踢下线</el-button>
            <el-button size="small" text type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="pagination">
        <el-pagination
          v-model:current-page="page"
          :page-size="pageSize"
          :total="filteredClients.length"
          layout="total, prev, pager, next"
          background
          hide-on-single-page
        />
      </div>
    </div>

    <ClientForm
      v-model="formVisible"
      :inbound-id="formInboundId"
      :client="editing"
      :method="editingMethod"
      @saved="load"
    />
    <ClientLinkDialog v-model="linkVisible" :client-id="linkClientId" />
    <ClientImportDialog
      v-model="importVisible"
      :inbounds="inbounds"
      :default-inbound-id="importInboundId"
      @imported="load"
    />
    <ClientUsageDialog
      v-model="usageVisible"
      :client-id="usageClientId"
      :client-name="usageClientName"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Download, Plus, Refresh, Search, Upload } from '@element-plus/icons-vue'
import { api } from '@/api'
import type { ClientSummary, Inbound } from '@/types'
import { formatBytes, formatDate, percent, quotaLabel, statusMeta } from '@/utils/format'
import { filterClients, lastPage, paginate, toSummary } from '@/utils/clients'
import ClientForm from '@/components/ClientForm.vue'
import ClientLinkDialog from '@/components/ClientLinkDialog.vue'
import ClientUsageDialog from '@/components/ClientUsageDialog.vue'
import ClientImportDialog from '@/components/ClientImportDialog.vue'

const route = useRoute()
const loading = ref(false)
const inbounds = ref<Inbound[]>([])
const clients = ref<ClientSummary[]>([])
const inboundFilter = ref(0)
const search = ref('')
const statusFilter = ref<'all' | ClientSummary['status']>('all')
const page = ref(1)
const pageSize = 20

const filteredClients = computed(() =>
  filterClients(clients.value, { query: search.value, status: statusFilter.value }),
)
const pagedClients = computed(() => paginate(filteredClients.value, page.value, pageSize))
const emptyText = computed(() =>
  clients.value.length === 0 ? '还没有用户' : '没有符合条件的用户',
)

watch([search, statusFilter], () => {
  page.value = 1
})

watch(filteredClients, (items) => {
  const max = lastPage(items.length, pageSize)
  if (page.value > max) page.value = max
})

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

function onInboundChange() {
  page.value = 1
  load()
}

async function load() {
  loading.value = true
  try {
    const inboundResult = await api.inbounds()
    inbounds.value = inboundResult.items || []
    const result = await api.clients(inboundFilter.value || undefined)
    clients.value = result.items || []
  } catch (error: any) {
    ElMessage.error(error?.message || '加载失败')
  } finally {
    loading.value = false
  }
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

function openUsage(row: ClientSummary) {
  usageClientId.value = row.id
  usageClientName.value = row.name
  usageVisible.value = true
}

async function toggle(row: ClientSummary) {
  try {
    await api.toggleClient(row.id, !row.enabled)
    row.enabled = !row.enabled
    row.status = row.enabled ? 'active' : 'disabled'
    ElMessage.success(row.enabled ? '已启用' : '已禁用')
  } catch (error: any) {
    ElMessage.error(error?.message || '操作失败')
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
    // The reset clears the counters and can flip quota_exceeded back to active,
    // so refresh just this row: the server recomputes the status.
    const refreshed = toSummary(await api.client(row.id))
    const index = clients.value.findIndex((item) => item.id === row.id)
    if (index >= 0) {
      clients.value[index] = refreshed
    }
    ElMessage.success('流量已重置')
  } catch (error: any) {
    ElMessage.error(error?.message || '重置失败')
  }
}

async function kick(row: ClientSummary) {
  try {
    await api.kickClient(row.id)
    // Every session of the client is gone, so its online IP list is empty; no
    // need to refetch the whole (potentially large) client list for that.
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
    // Drop the row in place; totals and pagination derive from the array.
    clients.value = clients.value.filter((item) => item.id !== row.id)
    ElMessage.success('已删除')
  } catch (error: any) {
    ElMessage.error(error?.message || '删除失败')
  }
}

onMounted(async () => {
  const inboundParam = Number(route.query.inbound)
  if (Number.isFinite(inboundParam) && inboundParam > 0) {
    inboundFilter.value = inboundParam
  }
  await load()
})
</script>

