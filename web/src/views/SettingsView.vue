<template>
  <div class="page">
    <PageHeader title="系统设置" subtitle="分享链接、订阅与安全策略">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" :disabled="forced" @click="load">刷新</el-button>
        <el-button
          v-if="!forced"
          type="primary"
          :loading="saving"
          :disabled="!loaded || !dirty"
          @click="save"
        >
          保存设置
        </el-button>
      </template>
    </PageHeader>

    <el-alert
      v-if="auth.admin?.must_change_password"
      type="warning"
      show-icon
      :closable="false"
      title="当前仍在使用初始密码，请立即修改。"
      class="mb-4"
    />

    <!-- Share links and subscriptions. The preview under the address fields is
         the point of this card: "host:port" is what actually ends up in a
         client, and the fallback chain behind it is invisible otherwise. -->
    <el-form
      v-if="!forced"
      class="settings-grid"
      :model="form"
      label-width="auto"
      @submit.prevent
    >
      <div class="card">
        <div class="card-head">
          <h3 class="card-title">分享与订阅</h3>
          <span class="muted">决定 ss:// 链接与订阅里的地址</span>
        </div>

        <el-form-item label="公共主机名">
          <el-input
            v-model="form.public_host"
            placeholder="例如 node.example.com"
            clearable
          />
          <div class="hint">留空则使用入站自己的监听地址。单个入站还可在「入站管理 → 编辑 → 关联域名」中覆盖。</div>
        </el-form-item>

        <el-form-item label="公共端口">
          <el-input-number
            v-model="form.public_port"
            :min="0"
            :max="65535"
            controls-position="right"
            class="w-control-sm"
          />
          <span class="unit">0 表示使用入站端口</span>
        </el-form-item>

        <el-form-item label="启用订阅">
          <el-switch v-model="form.subscription_enabled" />
          <span class="unit">
            {{ form.subscription_enabled ? '客户端可用订阅地址自动拉取节点' : '只提供单条 ss:// 链接' }}
          </span>
        </el-form-item>

        <div class="settings-preview">
          <span class="settings-preview-label">示例</span>
          <span class="settings-preview-value">{{ endpointPreview }}</span>
          <span class="settings-preview-hint">{{ portSource }}</span>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">安全策略</h3>
          <span class="muted">作用于全部入站</span>
        </div>

        <el-form-item label="允许内网目标">
          <el-switch v-model="form.allow_private_targets" />
          <div class="hint">开启后客户端可经代理访问内网与回环地址；云元数据地址始终拒绝。</div>
        </el-form-item>

        <el-form-item label="全局月度重置日">
          <el-input-number
            v-model="form.reset_day"
            :min="0"
            :max="28"
            controls-position="right"
            class="w-control-sm"
          />
          <span class="unit">{{ resetDayHint }}</span>
        </el-form-item>

        <el-form-item label="时区">
          <el-select v-model="form.timezone" filterable allow-create class="w-control-lg">
            <el-option v-for="zone in zones" :key="zone" :label="zone" :value="zone" />
          </el-select>
          <div class="hint">用于到期判断、月度重置与流量按天归属；改动后立即生效。</div>
        </el-form-item>

        <el-form-item label="访问分析">
          <el-switch v-model="form.access_log_enabled" />
          <div class="hint">
            记录每个用户访问的目标域名/IP、端口、结果与字节数。原始明细保留 {{ ACCESS_RAW_DAYS }} 天、按天聚合保留
            {{ ACCESS_AGGREGATE_DAYS }} 天；不记录页面内容或来源 IP。默认关闭，开启后数据库会增长。
          </div>
        </el-form-item>
      </div>
    </el-form>

    <!-- Actions sit outside the card grid so "save" is not buried between
         cards, and they are only offered when there is something to save. -->
    <div v-if="!forced" class="form-actions settings-actions">
      <el-button type="primary" :loading="saving" :disabled="!loaded || !dirty" @click="save">
        保存设置
      </el-button>
      <el-button v-if="dirty" :disabled="saving" @click="revert">放弃修改</el-button>
      <span class="muted">{{ dirty ? '有未保存的修改' : '已与服务器一致' }}</span>
    </div>

    <div :class="['settings-grid', { 'settings-grid--single': forced }]">
      <div class="card">
        <div class="card-head">
          <h3 class="card-title">管理员密码</h3>
        </div>
        <p v-if="forced" class="muted">首次登录必须先修改初始密码，之后才能读取或修改其他设置。</p>
        <p v-else class="muted">建议定期更换；修改后当前会话会立即轮换。</p>
        <el-button @click="passwordVisible = true">修改管理员密码</el-button>
      </div>

      <!-- Two-factor auth. Kept visible in the forced-change state as well: the
           forced flag only gates the settings form, not the security cards. -->
      <div class="card">
        <div class="card-head">
          <h3 class="card-title">两步验证（TOTP）</h3>
          <el-tag :type="totpEnabled ? 'success' : 'info'" size="small">
            {{ totpEnabled ? '已启用' : '未启用' }}
          </el-tag>
        </div>
        <p class="muted">登录时额外输入一次性验证码；恢复码仅显示一次，请离线保存。</p>
        <div v-if="totpEnabled" class="settings-actions">
          <el-button @click="openTOTPVerify('recovery')">重新生成恢复码</el-button>
          <el-button type="danger" plain @click="openTOTPVerify('disable')">关闭两步验证</el-button>
        </div>
        <el-button v-else @click="totpSetupVisible = true">启用</el-button>
      </div>

      <div v-if="!forced" class="card">
        <div class="card-head">
          <h3 class="card-title">数据备份</h3>
        </div>
        <p class="muted">
          下载 SQLite 数据库快照，使用在线备份，不会阻塞写入。恢复时请停止服务，替换数据库文件后重新启动。
        </p>
        <div class="settings-actions">
          <el-button :icon="Download" @click="downloadBackup">下载备份</el-button>
          <span class="muted">备份含 PSK 与密码哈希，请妥善保管</span>
        </div>
      </div>

      <!-- Configuration migration: settings + listeners + users + admin, but
           no history. Kept separate from the database backup because the two
           solve different problems (cross-version moves vs. full restore). -->
      <div v-if="!forced" class="card">
        <div class="card-head">
          <h3 class="card-title">配置迁移</h3>
          <el-tag type="warning" size="small">含全部凭据</el-tag>
        </div>
        <p class="muted">
          导出 JSON 配置（设置、入站、用户与管理员凭据），用于在不同机器或版本间迁移；不含流量、审计与访问历史——那些请用上方的数据库备份。导入会覆盖当前全部配置并使所有会话失效。
        </p>
        <div class="settings-actions">
          <el-button :icon="Download" @click="downloadConfig">导出配置 JSON</el-button>
          <el-button :icon="Upload" :loading="importing" @click="pickConfigFile">导入配置</el-button>
          <input
            ref="configFileInput"
            type="file"
            :accept="CONFIG_FILE_ACCEPT"
            class="config-file-input"
            @change="onConfigFile"
          />
        </div>
        <ul v-if="importErrors.length" class="config-import-errors">
          <li v-for="(message, index) in importErrors" :key="index">{{ message }}</li>
        </ul>
        <p v-if="importSummary" class="config-import-summary">{{ importSummary }}</p>
      </div>
    </div>

    <PasswordDialog v-model="passwordVisible" @saved="onPasswordSaved" />
    <TOTPSetupDialog v-model="totpSetupVisible" @enabled="onTOTPEnabled" />
    <TOTPVerifyDialog
      v-model="totpVerifyVisible"
      :action="totpVerifyAction"
      @done="onTOTPDone"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
// Page-specific Element Plus components; see src/element-parts.ts.
import { ElAlert, ElInputNumber, ElOption, ElSelect, ElSwitch } from '@/element-parts'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Download, Refresh, Upload } from '@element-plus/icons-vue'
import { api } from '@/api'
import { useAuthStore } from '@/stores/auth'
import type { Settings } from '@/types'
import { describeEndpoint, endpointSource, isSettingsDirty } from '@/utils/settings'
import { ACCESS_AGGREGATE_DAYS, ACCESS_RAW_DAYS } from '@/utils/access'
import {
  CONFIG_FILE_ACCEPT,
  IMPORT_CONFIRM_MESSAGE,
  configImportError,
  configImportSummary,
  parseConfigFile,
} from '@/utils/config-migration'
import PageHeader from '@/components/PageHeader.vue'
import PasswordDialog from '@/components/PasswordDialog.vue'
import TOTPSetupDialog from '@/components/TOTPSetupDialog.vue'
import TOTPVerifyDialog from '@/components/TOTPVerifyDialog.vue'

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
const totpSetupVisible = ref(false)
const totpVerifyVisible = ref(false)
const totpVerifyAction = ref<'disable' | 'recovery'>('disable')
const totpEnabled = computed(() => auth.admin?.totp_enabled === true)
const configFileInput = ref<HTMLInputElement | null>(null)
const importing = ref(false)
const importErrors = ref<string[]>([])
const importSummary = ref('')

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
  access_log_enabled: false,
})

// saved mirrors the server's copy, so the page can tell an edit from a reload
// and can undo one without a round trip.
const saved = ref<Settings>({ ...form })
const dirty = computed(() => loaded.value && isSettingsDirty(form, saved.value))

// The example address describes the fallback chain the server implements. The
// inbound port is unknown here (an inbound may override both values), so the
// preview names the cases rather than inventing a number.
const endpointPreview = computed(() => describeEndpoint(form.public_host, form.public_port, 8388))
const portSource = computed(() => endpointSource(form.public_port))

const resetDayHint = computed(() =>
  form.reset_day > 0 ? `每月 ${form.reset_day} 日 00:00 清零已用流量` : '不自动重置，仅手动清零',
)

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
    saved.value = { ...settings }
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
    const updated = await api.updateSettings({ ...form })
    Object.assign(form, updated)
    saved.value = { ...updated }
    ElMessage.success('设置已保存')
  } catch (error: any) {
    ElMessage.error(error?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

/** revert throws away local edits without bothering the server. */
function revert() {
  Object.assign(form, saved.value)
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

/** downloadConfig streams the JSON snapshot as a browser attachment. */
function downloadConfig() {
  window.location.href = api.exportConfigURL()
}

/** pickConfigFile opens the native file picker through the hidden input. */
function pickConfigFile() {
  importErrors.value = []
  importSummary.value = ''
  configFileInput.value?.click()
}

/**
 * onConfigFile reads and imports the chosen file.
 *
 * The confirmation is explicit and comes before the request: the endpoint's
 * `confirm` flag is only set after the operator has acknowledged the overwrite.
 */
async function onConfigFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // Reset the value so selecting the same file twice still fires change.
  input.value = ''
  if (!file) return
  let config
  try {
    config = parseConfigFile(await file.text())
  } catch (error: any) {
    importErrors.value = [error?.message || '读取配置文件失败']
    return
  }
  try {
    await ElMessageBox.confirm(IMPORT_CONFIRM_MESSAGE, '确认导入配置', {
      type: 'warning',
      confirmButtonText: '确认覆盖',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  importing.value = true
  try {
    const result = await api.importConfig(config, true)
    importSummary.value = configImportSummary(result)
    importErrors.value = result.warnings || []
    ElMessage.success('配置已导入，请重新登录')
    // The password hash and TOTP state may have changed: refresh the session so
    // the page does not keep showing the old admin identity.
    await auth.fetchMe()
  } catch (error: any) {
    importErrors.value = configImportError(error)
    importSummary.value = ''
  } finally {
    importing.value = false
  }
}

/** openTOTPVerify picks the re-auth action before the dialog opens. */
function openTOTPVerify(action: 'disable' | 'recovery') {
  totpVerifyAction.value = action
  totpVerifyVisible.value = true
}

/** onTOTPEnabled mirrors the setup dialog's success into the auth store. */
function onTOTPEnabled() {
  auth.setTOTPEnabled(true)
}

/** onTOTPDone mirrors a disable/recovery result into the auth store. */
function onTOTPDone(enabled: boolean) {
  auth.setTOTPEnabled(enabled)
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
