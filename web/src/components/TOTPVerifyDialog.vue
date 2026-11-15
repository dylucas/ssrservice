<template>
  <el-dialog v-model="visible" :title="title" width="min(480px, 92vw)" @closed="onClosed">
    <template v-if="step === 'verify'">
      <p class="muted">{{ description }}</p>
      <el-form label-width="auto" @submit.prevent>
        <el-form-item label="当前密码">
          <el-input
            v-model="passwordForm.password"
            type="password"
            show-password
            autocomplete="current-password"
          />
        </el-form-item>
        <el-form-item label="验证码">
          <el-input
            v-model="codeForm.code"
            autocomplete="one-time-code"
            inputmode="numeric"
            placeholder="6 位验证码或恢复码"
            @keyup.enter="submit"
          />
        </el-form-item>
      </el-form>
    </template>

    <template v-else>
      <el-alert
        type="warning"
        show-icon
        :closable="false"
        title="旧的恢复码已全部失效；新的恢复码仅显示这一次，请立即离线保存。"
        class="mb-4"
      />
      <div class="mono totp-codes">{{ formattedCodes }}</div>
      <div class="totp-codes-actions">
        <el-button @click="copyCodes">复制全部</el-button>
        <el-button @click="downloadCodes">下载 .txt</el-button>
      </div>
    </template>

    <template #footer>
      <template v-if="step === 'codes'">
        <el-button type="primary" @click="visible = false">完成</el-button>
      </template>
      <template v-else>
        <el-button @click="visible = false">取消</el-button>
        <el-button
          :type="action === 'disable' ? 'danger' : 'primary'"
          :loading="saving"
          :disabled="!passwordForm.password || !codeForm.code"
          @click="submit"
        >
          {{ confirmLabel }}
        </el-button>
      </template>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElAlert } from '@/element-parts'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '@/api'
import { copyText } from '@/utils/format'
import { formatRecoveryCodes, normalizeTOTPInput } from '@/utils/totp'

// One dialog covers the two re-auth actions: turning the second factor off and
// rotating the recovery codes. Both need the current password plus a valid code,
// and both must not leave the operator without a usable second factor by
// accident, so the destructive one asks for confirmation first.
const props = defineProps<{ modelValue: boolean; action: 'disable' | 'recovery' }>()
const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'done', enabled: boolean): void
}>()

const visible = ref(props.modelValue)
watch(
  () => props.modelValue,
  (value) => {
    visible.value = value
    // Reopening resets the previous attempt, and any response still in flight
    // for the old session must not land in this one.
    if (value) {
      cancelPending()
      reset()
    }
  },
)
watch(visible, (value) => emit('update:modelValue', value))

const step = ref<'verify' | 'codes'>('verify')
const saving = ref(false)
const passwordForm = reactive({ password: '' })
const codeForm = reactive({ code: '' })
const recoveryCodes = ref<string[]>([])
let requestSeq = 0

const title = computed(() => (props.action === 'disable' ? '关闭两步验证' : '重新生成恢复码'))
const description = computed(() =>
  props.action === 'disable'
    ? '关闭后登录不再需要一次性验证码，账号仅受密码保护。请输入当前密码与验证码或恢复码。'
    : '重新生成后，旧的恢复码立即全部失效。请输入当前密码与验证码或恢复码。',
)
const confirmLabel = computed(() => (props.action === 'disable' ? '关闭两步验证' : '生成恢复码'))
const formattedCodes = computed(() => formatRecoveryCodes(recoveryCodes.value))

async function submit() {
  const password = passwordForm.password
  const code = normalizeTOTPInput(codeForm.code)
  if (!password || !code || saving.value) return
  if (props.action === 'disable') {
    try {
      await ElMessageBox.confirm(
        '关闭后登录不再需要验证码，账号将只受密码保护。确定继续吗？',
        '关闭两步验证',
        { type: 'warning', confirmButtonText: '关闭', cancelButtonText: '取消' },
      )
    } catch {
      return
    }
  }
  saving.value = true
  const seq = ++requestSeq
  try {
    if (props.action === 'disable') {
      await api.totpDisable(password, code)
      if (seq !== requestSeq) return
      ElMessage.success('两步验证已关闭')
      emit('done', false)
      visible.value = false
      return
    }
    const result = await api.totpRecoveryCodes(password, code)
    if (seq !== requestSeq) return
    recoveryCodes.value = result.recovery_codes || []
    step.value = 'codes'
    ElMessage.success('已生成新的恢复码，旧恢复码立即失效')
    emit('done', true)
  } catch (error: any) {
    if (seq !== requestSeq) return
    handleError(error)
  } finally {
    if (seq === requestSeq) saving.value = false
  }
}

function handleError(error: any) {
  if (error?.code === 'invalid_password') {
    ElMessage.error('当前密码不正确')
    return
  }
  if (error?.code === 'invalid_totp') {
    ElMessage.error('验证码或恢复码无效')
    return
  }
  if (error?.code === 'totp_not_enabled') {
    // The server state is the truth: the card should stop claiming it is on.
    ElMessage.warning('两步验证未启用')
    emit('done', false)
    visible.value = false
    return
  }
  if (error?.code === 'password_change_required') {
    ElMessage.warning('请先修改初始密码，再管理两步验证')
    visible.value = false
    return
  }
  if (error?.status === 429 || error?.code === 'too_many_attempts') {
    ElMessage.error('尝试次数过多，请稍后再试')
    return
  }
  ElMessage.error(error?.message || '操作失败')
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

async function copyCodes() {
  await copy(formattedCodes.value)
}

/** downloadCodes saves the new recovery codes as a plain text file. */
function downloadCodes() {
  if (!recoveryCodes.value.length) return
  const blob = new Blob([`${formattedCodes.value}\n`], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = 'sspanel-recovery-codes.txt'
  link.click()
  URL.revokeObjectURL(url)
}

function reset() {
  step.value = 'verify'
  saving.value = false
  passwordForm.password = ''
  codeForm.code = ''
  recoveryCodes.value = []
}

/** cancelPending invalidates a request whose dialog session just ended. */
function cancelPending() {
  requestSeq++
  saving.value = false
}

function onClosed() {
  cancelPending()
  reset()
}
</script>

