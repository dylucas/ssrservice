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
import { api } from '@/api'
import { copyText } from '@/utils/format'

const props = defineProps<{ modelValue: boolean; clientId: number | null }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

const loading = ref(false)
const link = ref('')
const subURL = ref('')
const qr = ref('')

const absoluteSub = computed(() => (subURL.value ? new URL(api.subURL(subURL.value), window.location.origin).toString() : ''))

// The QR encoder is ~26 kB that only this dialog needs, so it is loaded on
// demand instead of riding along in the clients route chunk.
type QREncoder = (text: string, options: { margin: number; width: number }) => Promise<string>

let loadQRCode: (() => Promise<typeof import('qrcode')>) | null = null

/**
 * resolveEncoder picks toDataURL out of the module namespace.
 *
 * qrcode is CommonJS (`module.exports = require('./server')`), and a dynamic
 * import of it can surface the function either as a named export or under
 * `default` depending on the interop pass, so both shapes are accepted.
 */
function resolveEncoder(mod: typeof import('qrcode')): QREncoder | null {
  const named = (mod as { toDataURL?: QREncoder }).toDataURL
  if (named) return named
  const interop = (mod as { default?: { toDataURL?: QREncoder } }).default
  return interop?.toDataURL ?? null
}

/** renderQR fills the image, leaving it empty if the encoder cannot be loaded. */
async function renderQR(value: string) {
  try {
    if (!loadQRCode) loadQRCode = () => import('qrcode')
    const encode = resolveEncoder(await loadQRCode())
    if (!encode) throw new Error('qrcode encoder unavailable')
    qr.value = await encode(value, { margin: 1, width: 220 })
  } catch {
    // The link and the subscription URL are the content; a missing QR image must
    // not turn the whole dialog into an error.
    qr.value = ''
  }
}

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
      await renderQR(link.value)
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
