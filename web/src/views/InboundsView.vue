<template>
  <div class="page">
    <PageHeader title="入站管理" subtitle="每个入站是一个独立端口上的 Shadowsocks 监听器">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openCreate">新建入站</el-button>
      </template>
    </PageHeader>

    <div class="card">
      <div class="card-head">
        <h3 class="card-title">入站列表</h3>
        <span class="muted">共 {{ inbounds.length }} 个</span>
      </div>
      <el-table :data="inbounds" v-loading="loading" row-key="id">
        <template #empty>
          <EmptyState text="暂无入站，先创建一个监听器" :icon="Connection">
            <el-button type="primary" size="small" :icon="Plus" @click="openCreate">
              新建入站
            </el-button>
          </EmptyState>
        </template>
        <el-table-column prop="remark" label="备注" min-width="140">
          <template #default="{ row }">
            <div>{{ row.remark || '未命名' }}</div>
            <div class="muted mono">#{{ row.id }}</div>
          </template>
        </el-table-column>
        <el-table-column label="监听" min-width="170">
          <template #default="{ row }">
            <div class="mono">{{ row.listen }}:{{ row.port }}</div>
            <div class="muted">{{ row.udp_enabled ? 'TCP + UDP' : '仅 TCP' }}</div>
            <!-- Only the configured value is shown: the resolution order lives in
                 the service, so the UI does not re-implement it. -->
            <div v-if="row.domain" class="muted mono">域名 {{ row.domain }}<template v-if="row.domain_port">:{{ row.domain_port }}</template></div>
          </template>
        </el-table-column>
        <el-table-column prop="method" label="加密方法" min-width="190" show-overflow-tooltip />
        <el-table-column label="用户" width="110">
          <template #default="{ row }">
            {{ row.client_count }}
            <span class="muted">/ {{ row.enabled_client_count }} 启用</span>
          </template>
        </el-table-column>
        <el-table-column label="流量" min-width="150">
          <template #default="{ row }">
            <div>↑ {{ formatBytes(row.up_bytes) }}</div>
            <div>↓ {{ formatBytes(row.down_bytes) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="在线 IP" width="90">
          <template #default="{ row }">{{ row.online_ips }}</template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-switch
              :model-value="row.enabled"
              size="small"
              @change="(value: string | number | boolean) => toggle(asInbound(row), value === true)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button size="small" text type="primary" @click="goClients(asInbound(row))">用户</el-button>
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
import { onMounted, ref } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElSwitch, ElTable, ElTableColumn } from '@/element-parts'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, Connection, Plus, Refresh } from '@element-plus/icons-vue'
import { api } from '@/api'
import type { Inbound } from '@/types'
import { formatBytes } from '@/utils/format'
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
