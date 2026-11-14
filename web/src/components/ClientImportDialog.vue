<template>
  <el-dialog
    :model-value="modelValue"
    title="导入用户"
    width="min(720px, 92vw)"
    @update:model-value="emit('update:modelValue', $event)"
    @closed="reset"
  >
    <el-form label-width="96px">
      <el-form-item label="目标入站">
        <el-select v-model="inboundId" style="width: 100%">
          <el-option
            v-for="item in inbounds"
            :key="item.id"
            :label="`${item.remark || '入站'} (:${item.port}) · ${item.method}`"
            :value="item.id"
          />
        </el-select>
      </el-form-item>
      <el-form-item label="格式">
        <el-radio-group v-model="format">
          <el-radio-button value="auto">自动识别</el-radio-button>
          <el-radio-button value="csv">CSV</el-radio-button>
          <el-radio-button value="ss">ss:// 链接</el-radio-button>
        </el-radio-group>
      </el-form-item>
      <el-form-item label="内容">
        <el-input
          v-model="content"
          type="textarea"
          :rows="10"
          class="mono"
          :placeholder="placeholder"
        />
        <div class="hint">
          CSV 表头：<span class="mono">name,psk,enabled,quota_bytes,expires_at,ip_limit,reset_policy,reset_day,note</span>；
          ss:// 每行一条，方法与目标入站必须一致。单次最多 1000 行。
        </div>
      </el-form-item>
    </el-form>

    <div v-if="result" class="result">
      <el-alert
        :title="`已创建 ${result.created} 个用户${result.failed.length ? `，${result.failed.length} 行失败` : ''}`"
        :type="result.failed.length ? 'warning' : 'success'"
        :closable="false"
        show-icon
      />
      <el-table v-if="result.failed.length" :data="result.failed" size="small" max-height="220">
        <el-table-column prop="line" label="行" width="70" />
        <el-table-column prop="name" label="用户" width="140" />
        <el-table-column prop="error" label="原因" />
      </el-table>
    </div>

    <template #footer>
      <el-button @click="emit('update:modelValue', false)">关闭</el-button>
      <el-button type="primary" :loading="submitting" :disabled="!content.trim()" @click="submit">
        开始导入
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElAlert, ElOption, ElRadioButton, ElRadioGroup, ElSelect, ElTable, ElTableColumn } from '@/element-parts'
import { ElMessage } from 'element-plus'
import { api } from '@/api'
import { resolveImportInbound } from '@/utils/import-target'
import type { ImportResult, Inbound } from '@/types'

const props = defineProps<{
  modelValue: boolean
  inbounds: Inbound[]
  defaultInboundId: number
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'imported'): void
}>()

const inboundId = ref<number>(props.defaultInboundId)
const format = ref<'auto' | 'csv' | 'ss'>('auto')
const content = ref('')
const submitting = ref(false)
const result = ref<ImportResult | null>(null)

// Re-read the parent's current inbound on every open. The setup-time value is
// stale as soon as the operator switches the list filter between two imports.
watch(
  () => props.modelValue,
  (visible) => {
    if (visible) inboundId.value = resolveImportInbound(props.defaultInboundId, props.inbounds)
  },
)

const placeholder = `name,psk,enabled,quota_bytes,expires_at,ip_limit,reset_policy,reset_day,note
alice,,true,0,0,0,none,0,laptop
bob,,false,1073741824,0,2,monthly,1,

或：
ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@203.0.113.5:8388#alice`

async function submit() {
  if (!inboundId.value) {
    ElMessage.warning('请选择目标入站')
    return
  }
  submitting.value = true
  try {
    result.value = await api.importClients(inboundId.value, {
      format: format.value,
      content: content.value,
    })
    if (result.value.created > 0) {
      emit('imported')
    }
    if (result.value.failed.length === 0 && result.value.created > 0) {
      ElMessage.success(`已导入 ${result.value.created} 个用户`)
    }
  } catch (error: any) {
    ElMessage.error(error?.message || '导入失败')
  } finally {
    submitting.value = false
  }
}

function reset() {
  content.value = ''
  result.value = null
  format.value = 'auto'
  inboundId.value = resolveImportInbound(props.defaultInboundId, props.inbounds)
}
</script>

<style scoped>
.result {
  margin-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
