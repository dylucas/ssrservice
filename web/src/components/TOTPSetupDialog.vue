<template>
  <el-dialog v-model="visible" title="启用两步验证" width="min(560px, 92vw)" @closed="onClosed">
    <!-- Step 1: prove the operator still knows the password that is about to be
         protected. -->
    <template v-if="step === 'password'">
      <p class="muted">
        两步验证会在登录时额外要求一次性验证码。请先输入当前密码，以生成绑定密钥。
      </p>
      <el-form label-width="auto" @submit.prevent>
        <el-form-item label="当前密码">
          <el-input
            v-model="passwordForm.password"
            type="password"
            show-password
            autocomplete="current-password"
            @keyup.enter="startSetup"
          />
        </el-form-item>
      </el-form>
    </template>

    <!-- Step 2: scan the QR code (or type the secret), then confirm one code so
         the server knows the authenticator really has it. -->
    <template v-else-if="step === 'scan'">
      <el-alert
        type="info"
        show-icon
        :closable="false"
        title="使用 Google Authenticator、1Password 或 Authy 扫描二维码；扫码失败时也可手动输入密钥。"
        class="mb-4"
      />
      <div class="qr-frame totp-qr-wrap">
        <img v-if="qr" :src="qr" alt="两步验证二维码" class="qr-image" />
        <div v-else class="muted">二维码生成失败，请改用下面的密钥手动添加。</div>
      </div>
      <el-form label-width="auto" class="totp-secret-form" @submit.prevent>
        <el-form-item label="密钥">
          <el-input :model-value="secret" readonly class="mono">
            <template #append>
              <el-button :disabled="!secret" @click="copy(secret)">复制</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item label="otpauth 链接">
          <el-input :model-value="otpauthURL" readonly class="mono">
            <template #append>
              <el-button :disabled="!otpauthURL" @click="copy(otpauthURL)">复制</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item label="验证码">
          <el-input
            v-model="codeForm.code"
            autocomplete="one-time-code"
            inputmode="numeric"
            placeholder="输入身份验证器显示的 6 位验证码"
            @keyup.enter="enable"
          />
        </el-form-item>
      </el-form>
    </template>

    <!-- Step 3: the recovery codes exist only in this response. -->
    <template v-else>
      <el-alert
        type="warning"
        show-icon
        :closable="false"
        title="恢复码仅显示这一次，请立即离线保存；关闭后无法再次查看。"
        class="mb-4"
      />
      <div class="mono totp-codes">{{ formattedCodes }}</div>
      <div class="totp-codes-actions">
        <el-button @click="copyCodes">复制全部</el-button>
        <el-button @click="downloadCodes">下载 .txt</el-button>
      </div>
      <p class="hint">无法使用身份验证器时，可在登录第二步输入任意一个恢复码；每个恢复码只能使用一次。</p>
    </template>

    <template #footer>
      <template v-if="step === 'codes'">
        <el-button type="primary" @click="visible = false">完成</el-button>
      </template>
      <template v-else-if="step === 'scan'">
        <el-button @click="visible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!codeForm.code" @click="enable">
          启用
        </el-button>
      </template>
      <template v-else>
        <el-button @click="visible = false">取消</el-button>
        <el-button
          type="primary"
          :loading="saving"
          :disabled="!passwordForm.password"
          @click="startSetup"
        >
          下一步
        </el-button>
      </template>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElAlert } from '@/element-parts'
import { ElMessage } from 'element-plus'
import { api } from '@/api'
import { copyText } from '@/utils/format'
import { formatRecoveryCodes, normalizeTOTPInput } from '@/utils/totp'
import { encodeQR } from '@/utils/qr'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'enabled'): void
}>()

const visible = ref(props.modelValue)
watch(
  () => props.modelValue,
  (value) => {
    visible.value = value
    // A previous attempt may have been interrupted by closing the dialog, so
    // every open starts at the password step again.
    if (value) {
      cancelPending()
      resetToPassword()
    }
  },
)
watch(visible, (value) => emit('update:modelValue', value))

// The dialog is a three-step wizard; closing at any point throws the pending
// setup away, so the state is only kept while it is open.
const step = ref<'password' | 'scan' | 'codes'>('password')
const saving = ref(false)
const passwordForm = reactive({ password: '' })
const codeForm = reactive({ code: '' })
const secret = ref('')
const otpauthURL = ref('')
const qr = ref('')
const recoveryCodes = ref<string[]>([])
// enabled is emitted on @closed so the settings card never flips while the
// recovery codes the operator has to save are still on screen.
let enabled = false
// requestSeq makes a response from a dialog session that was closed meanwhile
// not write into the next one.
let requestSeq = 0

const formattedCodes = computed(() => formatRecoveryCodes(recoveryCodes.value))

async function startSetup() {
  const password = passwordForm.password
  if (!password || saving.value) return
  const seq = ++requestSeq
  saving.value = true
  try {
    const result = await api.totpSetup(password)
    if (seq !== requestSeq) return
    secret.value = result.secret || ''
    otpauthURL.value = result.otpauth_url || ''
    passwordForm.password = ''
    step.value = 'scan'
    const image = otpauthURL.value ? await encodeQR(otpauthURL.value) : ''
    if (seq !== requestSeq) return
    qr.value = image
  } catch (error: any) {
    if (seq !== requestSeq) return
    if (error?.code === 'totp_already_enabled') {
      // The card was stale (enabled elsewhere). Tell it to flip now: the dialog
      // is closing, so waiting for @closed would lose the update.
      ElMessage.info('两步验证已启用')
      visible.value = false
      emit('enabled')
      return
    }
    if (error?.code === 'password_change_required') {
      // The backend only allows the forced password change first; the global
      // handler already routed there, so just explain why this dialog closed.
      ElMessage.warning('请先修改初始密码，再设置两步验证')
      visible.value = false
      return
    }
    ElMessage.error(
      error?.code === 'invalid_password' ? '当前密码不正确' : error?.message || '生成绑定密钥失败',
    )
  } finally {
    if (seq === requestSeq) saving.value = false
  }
}

async function enable() {
  const code = normalizeTOTPInput(codeForm.code)
  if (!code || saving.value) return
  const seq = ++requestSeq
  saving.value = true
  try {
    const result = await api.totpEnable(code)
    if (seq !== requestSeq) return
    recoveryCodes.value = result.recovery_codes || []
    enabled = true
    step.value = 'codes'
  } catch (error: any) {
    if (seq !== requestSeq) return
    if (error?.code === 'totp_already_enabled') {
      ElMessage.info('两步验证已启用')
      visible.value = false
      emit('enabled')
    } else if (error?.code === 'invalid_totp') {
      ElMessage.error('验证码不正确，请检查身份验证器后重试')
    } else if (error?.code === 'totp_not_setup') {
      ElMessage.error('绑定密钥已失效，请重新开始')
      resetToPassword()
    } else {
      ElMessage.error(error?.message || '启用两步验证失败')
    }
  } finally {
    if (seq === requestSeq) saving.value = false
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

async function copyCodes() {
  await copy(formattedCodes.value)
}

/** downloadCodes saves the recovery codes as a plain text file. */
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

/** resetToPassword returns the wizard to its first step. */
function resetToPassword() {
  step.value = 'password'
  passwordForm.password = ''
  codeForm.code = ''
  secret.value = ''
  otpauthURL.value = ''
  qr.value = ''
  recoveryCodes.value = []
}

/** cancelPending invalidates a request whose dialog session just ended. */
function cancelPending() {
  requestSeq++
  saving.value = false
}

function onClosed() {
  const wasEnabled = enabled
  enabled = false
  cancelPending()
  resetToPassword()
  if (wasEnabled) emit('enabled')
}
</script>

<style scoped>
/* The shared QR frame centres the image; this wrapper only adds the gap to the
 * form underneath. */
.totp-qr-wrap {
  margin-bottom: var(--sspanel-space-6);
}

.totp-secret-form {
  margin-top: var(--sspanel-space-5);
}
</style>
