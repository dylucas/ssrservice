<template>
  <el-dialog
    :model-value="modelValue"
    title="分享链接"
    width="min(560px, 92vw)"
    @update:model-value="emit('update:modelValue', $event)"
    @opened="load"
  >
    <div v-loading="loading">
      <el-form label-width="auto">
        <el-form-item label="ss:// 链接">
          <el-input :model-value="link" readonly type="textarea" :rows="3" class="mono" />
        </el-form-item>
        <el-form-item label="订阅地址">
          <el-input :model-value="absoluteSub" readonly>
            <template #append>
              <el-button :disabled="!absoluteSub" @click="copy(absoluteSub)">复制</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item label="二维码">
          <div class="qr-frame">
            <img v-if="qr" :src="qr" alt="分享二维码" class="qr-image" />
            <div v-else class="muted">暂无二维码</div>
          </div>
        </el-form-item>
      </el-form>
    </div>
    <template #footer>
      <el-button type="danger" plain :loading="resetting" :disabled="!clientId" @click="resetToken">
        重置订阅
      </el-button>
      <el-button @click="emit('update:modelValue', false)">关闭</el-button>
      <el-button type="primary" :disabled="!link" @click="copy(link)">复制链接</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, basePath } from '@/api'
import { copyText } from '@/utils/format'
import { encodeQR } from '@/utils/qr'
import { absoluteURL } from '@/utils/subscription'

const props = defineProps<{ modelValue: boolean; clientId: number | null }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

const loading = ref(false)
const resetting = ref(false)
const link = ref('')
const subURL = ref('')
const qr = ref('')

// The server hands back a root-relative path; the box and the clipboard both
// need the absolute URL the operator can paste into a client, including the
// deployment base path a sub-path install is served under.
const absoluteSub = computed(() =>
  subURL.value ? absoluteURL(basePath, subURL.value, window.location.origin) : '',
)

// loadSeq makes the last request win: clicking "链接" on one row and then on
// another must not leave the first (slower) response on screen with the second
// client's title.
let loadSeq = 0

async function load() {
  if (!props.clientId) return
  const seq = ++loadSeq
  const clientId = props.clientId
  loading.value = true
  link.value = ''
  subURL.value = ''
  qr.value = ''
  try {
    const result = await api.clientLink(clientId)
    if (seq !== loadSeq || clientId !== props.clientId) return
    link.value = result.link || ''
    subURL.value = result.subscription_url || ''
    if (link.value) {
      // The encoder is loaded lazily, so this await can outlive a newer load;
      // only the request that still owns the dialog may write the image.
      const image = await encodeQR(link.value)
      if (seq !== loadSeq) return
      qr.value = image
    }
  } catch (error: any) {
    if (seq !== loadSeq) return
    ElMessage.error(error?.message || '获取链接失败')
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

/** resetToken rotates the subscription token, then reloads link and QR code. */
async function resetToken() {
  if (!props.clientId) return
  try {
    await ElMessageBox.confirm(
      '重置后旧订阅地址立即失效，客户端需要重新导入新地址。确定继续吗？',
      '重置订阅',
      { type: 'warning', confirmButtonText: '重置', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  resetting.value = true
  try {
    await api.resetClientToken(props.clientId)
    // The link endpoint re-renders the QR code from the new token, so reloading
    // it is what refreshes both the URL and the image.
    await load()
    ElMessage.success('订阅地址已重置')
  } catch (error: any) {
    ElMessage.error(error?.message || '重置订阅失败')
  } finally {
    resetting.value = false
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

