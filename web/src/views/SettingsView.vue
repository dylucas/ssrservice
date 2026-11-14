<template>
  <div class="page">
    <PageHeader title="系统设置" subtitle="分享链接、订阅与安全策略">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" :disabled="forced" @click="load">刷新</el-button>
      </template>
    </PageHeader>

    <div class="form-narrow">
      <el-alert
        v-if="auth.admin?.must_change_password"
        type="warning"
        show-icon
        :closable="false"
        title="当前仍在使用初始密码，请立即修改。"
        class="mb-4"
      />

      <el-form v-if="!forced" :model="form" label-width="auto">
        <div class="card">
          <div class="card-head">
            <h3 class="card-title">分享与订阅</h3>
          </div>
          <el-form-item label="公共主机名">
            <el-input v-model="form.public_host" placeholder="例如 node.example.com，留空使用入站监听地址" />
            <div class="hint">全局默认值。单个入站可在「入站管理 → 编辑 → 关联域名」中覆盖它。</div>
          </el-form-item>
          <el-form-item label="公共端口">
            <el-input-number v-model="form.public_port" :min="0" :max="65535" controls-position="right" />
            <span class="hint">0 表示使用入站端口</span>
          </el-form-item>
          <el-form-item label="启用订阅">
            <el-switch v-model="form.subscription_enabled" />
          </el-form-item>
        </div>

        <div class="card">
          <div class="card-head">
            <h3 class="card-title">安全策略</h3>
          </div>
          <el-form-item label="允许内网目标">
            <el-switch v-model="form.allow_private_targets" />
            <div class="hint">开启后客户端可经代理访问内网；云元数据地址始终拒绝。</div>
          </el-form-item>
          <el-form-item label="全局月度重置日">
            <el-input-number v-model="form.reset_day" :min="0" :max="28" controls-position="right" />
            <span class="hint">0 表示不自动重置，1–28 日 00:00</span>
          </el-form-item>
          <el-form-item label="时区">
            <el-select v-model="form.timezone" filterable allow-create class="w-control-lg">
              <el-option v-for="zone in zones" :key="zone" :label="zone" :value="zone" />
            </el-select>
          </el-form-item>
        </div>
      </el-form>

      <div v-if="!forced" class="form-actions">
        <el-button type="primary" :loading="saving" :disabled="!loaded" @click="save">保存设置</el-button>
        <span class="muted">改动会立即生效，无需重启服务。</span>
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">安全</h3>
        </div>
        <p v-if="forced" class="muted">首次登录必须先修改初始密码，之后才能读取或修改其他设置。</p>
        <el-button @click="passwordVisible = true">修改管理员密码</el-button>
      </div>

      <div v-if="!forced" class="card">
        <div class="card-head">
          <h3 class="card-title">数据备份</h3>
        </div>
        <p class="muted">下载 SQLite 数据库快照。恢复时请停止服务，替换数据库文件后重新启动。</p>
        <el-button :icon="Download" @click="downloadBackup">下载备份</el-button>
      </div>
    </div>

    <PasswordDialog v-model="passwordVisible" @saved="onPasswordSaved" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElAlert, ElInputNumber, ElOption, ElSelect, ElSwitch } from '@/element-parts'
import { ElMessage } from 'element-plus'
import { Download, Refresh } from '@element-plus/icons-vue'
import { api } from '@/api'
import { useAuthStore } from '@/stores/auth'
import type { Settings } from '@/types'
import PageHeader from '@/components/PageHeader.vue'
import PasswordDialog from '@/components/PasswordDialog.vue'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const loading = ref(false)
// loaded guards the save button: the reactive defaults below look like a valid
// settings object, so saving before a successful GET would overwrite the real
// configuration with public_host='' and friends.
const loaded = ref(false)
const saving = ref(false)
const passwordVisible = ref(false)

// must_change_password is the authoritative state. The query flag only covers
// the moment right after login, before the admin object is filled; once the
// flag is false a leftover ?force=password must not lock the page forever (for
// example after changing the password from the header menu).
const forced = computed(
  () =>
    auth.admin?.must_change_password === true ||
    (route.query.force === 'password' && auth.admin?.must_change_password !== false),
)
const form = reactive<Settings>({
  public_host: '',
  public_port: 0,
  subscription_enabled: true,
  allow_private_targets: false,
  reset_day: 1,
  timezone: 'Local',
})

const zones = [
  'Local',
  'UTC',
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Asia/Hong_Kong',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
]

// A forced change and the watch below can both start a load right after the
// password was saved; the sequence keeps the last request's values.
let loadSeq = 0

async function load() {
  // The backend rejects everything but /auth/me, /auth/password and
  // /auth/logout in this state, so asking would only produce a 403 toast.
  if (forced.value) return
  const seq = ++loadSeq
  loading.value = true
  try {
    const settings = await api.settings()
    if (seq !== loadSeq) return
    Object.assign(form, settings)
    loaded.value = true
  } catch (error: any) {
    if (seq !== loadSeq) return
    ElMessage.error(error?.message || '加载失败')
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

async function save() {
  if (!loaded.value || forced.value) return
  saving.value = true
  try {
    Object.assign(form, await api.updateSettings({ ...form }))
    ElMessage.success('设置已保存')
  } catch (error: any) {
    ElMessage.error(error?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

/** onPasswordSaved returns to the normal settings page after a forced change. */
async function onPasswordSaved() {
  passwordVisible.value = false
  if (route.query.force === 'password') {
    await router.replace({ query: {} })
  }
  await load()
}

function downloadBackup() {
  window.location.href = api.backupURL()
}

// The password can also be changed from the header menu while this page is
// open. Once the flag clears the form becomes reachable, so fetch the real
// values instead of leaving the defaults behind a disabled button.
watch(forced, (value) => {
  if (!value && !loaded.value) load()
})

onMounted(() => {
  if (forced.value) {
    passwordVisible.value = true
    return
  }
  load()
})
</script>
