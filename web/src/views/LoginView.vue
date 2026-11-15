<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-head">
        <span class="brand-mark">SS</span>
        <div>
          <div class="login-brand">sspanel</div>
          <div class="login-sub">Shadowsocks 服务端管理面板</div>
        </div>
      </div>

      <!-- Step one: username + password. When the account has TOTP enabled the
           server answers with a challenge instead of a session cookie. -->
      <el-form
        v-if="step === 'password'"
        ref="formRef"
        :model="form"
        :rules="rules"
        @keyup.enter="submitPassword"
      >
        <el-form-item prop="username">
          <el-input v-model="form.username" size="large" placeholder="用户名" :prefix-icon="User" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input
            v-model="form.password"
            size="large"
            type="password"
            show-password
            placeholder="密码"
            :prefix-icon="Lock"
          />
        </el-form-item>
        <el-button class="w-full" type="primary" size="large" :loading="loading" @click="submitPassword">
          登录
        </el-button>
      </el-form>

      <!-- Step two: one input that accepts either an authenticator code or one
           of the recovery codes; the server tells the two apart. -->
      <el-form
        v-else
        ref="totpFormRef"
        :model="totpForm"
        :rules="totpRules"
        @keyup.enter="submitTOTP"
      >
        <div class="login-totp-head">
          <div class="login-totp-title">两步验证</div>
          <div class="login-totp-hint">
            请输入身份验证器中的 6 位验证码，或任意一个未使用的恢复码。
          </div>
        </div>
        <el-form-item prop="code">
          <el-input
            ref="codeInputRef"
            v-model="totpForm.code"
            size="large"
            autocomplete="one-time-code"
            inputmode="numeric"
            placeholder="6 位验证码或恢复码"
            :prefix-icon="Key"
          />
        </el-form-item>
        <div class="login-actions">
          <el-button class="w-full" type="primary" size="large" :loading="loading" @click="submitTOTP">
            验证
          </el-button>
          <el-button class="w-full" text :disabled="loading" @click="backToPassword">返回上一步</el-button>
        </div>
      </el-form>

      <div class="login-footer">默认仅监听 127.0.0.1，请通过 SSH 隧道或反向代理访问</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules, type InputInstance } from 'element-plus'
import { Key, Lock, User } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import type { Admin } from '@/types'
import { normalizeTOTPInput } from '@/utils/totp'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const formRef = ref<FormInstance>()
const totpFormRef = ref<FormInstance>()
const codeInputRef = ref<InputInstance>()
const loading = ref(false)

// The credentials and second-factor steps are one page with two states, so a
// failed code must not throw away the challenge the server issued.
const step = ref<'password' | 'totp'>('password')
const challenge = ref('')

const form = reactive({ username: 'admin', password: '' })
const rules: FormRules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }],
}
const totpForm = reactive({ code: '' })
const totpRules: FormRules = {
  code: [{ required: true, message: '请输入验证码或恢复码', trigger: 'blur' }],
}

async function submitPassword() {
  if (!formRef.value || loading.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  loading.value = true
  try {
    const result = await auth.login(form.username, form.password)
    // A 2FA account gets a challenge instead of a session: the admin object
    // only exists once the second step succeeded.
    if (result.totp_required === true) {
      challenge.value = result.challenge
      step.value = 'totp'
      form.password = ''
      totpForm.code = ''
      await nextTick()
      codeInputRef.value?.focus()
      return
    }
    await finishLogin(result.admin)
  } catch (error: any) {
    ElMessage.error(loginErrorMessage(error))
  } finally {
    loading.value = false
  }
}

async function submitTOTP() {
  if (!totpFormRef.value || loading.value) return
  const valid = await totpFormRef.value.validate().catch(() => false)
  if (!valid) return
  loading.value = true
  try {
    const admin = await auth.loginTOTP(challenge.value, normalizeTOTPInput(totpForm.code))
    await finishLogin(admin)
  } catch (error: any) {
    if (error?.code === 'totp_challenge_expired' || error?.code === 'totp_challenge_invalid') {
      // The challenge is gone (expired, password changed, server restarted):
      // staying on the code step would keep failing until the page reloads.
      ElMessage.error(error?.message || '验证已失效，请重新输入密码')
      backToPassword()
      return
    }
    if (error?.code === 'invalid_totp') {
      ElMessage.error('验证码或恢复码无效')
      return
    }
    ElMessage.error(loginErrorMessage(error))
  } finally {
    loading.value = false
  }
}

/** backToPassword drops the challenge and returns to the credentials step. */
function backToPassword() {
  step.value = 'password'
  challenge.value = ''
  totpForm.code = ''
  form.password = ''
  totpFormRef.value?.clearValidate()
}

/** loginErrorMessage maps the shared throttling answer to actionable copy. */
function loginErrorMessage(error: any): string {
  if (error?.status === 429 || error?.code === 'too_many_attempts') {
    return '尝试次数过多，请稍后再试'
  }
  return error?.message || '登录失败'
}

/** finishLogin is shared by both steps so 2FA cannot change the landing route. */
async function finishLogin(admin: Admin) {
  if (admin.must_change_password) {
    ElMessage.warning('首次登录，请先修改密码')
    await router.push('/settings?force=password')
    return
  }
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
  await router.push(redirect)
}
</script>

<style scoped>
.login-totp-head {
  margin-bottom: var(--sspanel-space-6);
}

.login-totp-title {
  font-size: var(--sspanel-fs-lg);
  font-weight: 600;
}

.login-totp-hint {
  margin-top: var(--sspanel-space-2);
  color: var(--sspanel-text-muted);
  font-size: var(--sspanel-fs-sm);
}

/* The back button is a text button directly under the primary one; Element's
 * adjacent-button margin would offset it from the full-width column. */
.login-actions {
  display: flex;
  flex-direction: column;
  gap: var(--sspanel-space-3);
}

.login-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}
</style>
