<template>
  <el-dialog
    :model-value="modelValue"
    title="分享链接"
    width="min(560px, 92vw)"
    @update:model-value="emit('update:modelValue', $event)"
    @opened="load"
  >
    <div v-loading="loading">
      <el-form label-width="88px">
        <el-form-item label="ss:// 链接">
          <el-input :model-value="link" readonly type="textarea" :rows="3" class="mono" />
        </el-form-item>
        <el-form-item label="订阅地址">
          <el-input :model-value="absoluteSub" readonly>
            <template #append>
              <el-button :disabled="!subURL" @click="copy(subURL)">复制</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item label="二维码">
          <div class="qr-wrap">
            <img v-if="qr" :src="qr" alt="分享二维码" class="qr" />
            <div v-else class="muted">暂无二维码</div>
          </div>
        </el-form-item>
      </el-form>
    </div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">关闭</el-button>
      <el-button type="primary" :disabled="!link" @click="copy(link)">复制链接</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import QRCode from 'qrcode'
import { api } from '@/api'
import { copyText } from '@/utils/format'

const props = defineProps<{ modelValue: boolean; clientId: number | null }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

const loading = ref(false)
const link = ref('')
const subURL = ref('')
const qr = ref('')

const absoluteSub = computed(() => (subURL.value ? new URL(api.subURL(subURL.value), window.location.origin).toString() : ''))

async function load() {
  if (!props.clientId) return
  loading.value = true
  link.value = ''
  subURL.value = ''
  qr.value = ''
  try {
    const result = await api.clientLink(props.clientId)
    link.value = result.link || ''
    subURL.value = result.subscription_url || ''
    if (link.value) {
      qr.value = await QRCode.toDataURL(link.value, { margin: 1, width: 220 })
    }
  } catch (error: any) {
    ElMessage.error(error?.message || '获取链接失败')
  } finally {
    loading.value = false
  }
}

async function copy(value: string) {
  if (!value) return
  try {
    await copyText(value)
    ElMessage.success('已复制')
  } catch {
    ElMessage.error('复制失败，请手动选择文本')
  }
}
</script>

<style scoped>
.qr-wrap {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
}
.qr {
  width: 220px;
  height: 220px;
  border: 1px solid var(--sspanel-border);
  border-radius: var(--sspanel-radius-sm);
  padding: 6px;
  background: var(--sspanel-surface);
}
</style>
