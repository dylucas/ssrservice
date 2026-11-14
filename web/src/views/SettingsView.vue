<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2 class="page-title">系统设置</h2>
        <div class="page-subtitle">分享链接、订阅与安全策略</div>
      </div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-alert
      v-if="auth.admin?.must_change_password"
      type="warning"
      show-icon
      :closable="false"
      title="当前仍在使用初始密码，请立即修改。"
      style="margin-bottom: 16px"
    />

    <div class="card">
      <h3 class="card-title">分享与订阅</h3>
      <el-form ref="formRef" :model="form" label-width="150px" style="max-width: 640px">
        <el-form-item label="公共主机名">
          <el-input v-model="form.public_host" placeholder="例如 node.example.com，留空使用入站监听地址" />
        </el-form-item>
        <el-form-item label="公共端口">
          <el-input-number v-model="form.public_port" :min="0" :max="65535" controls-position="right" />
          <span class="hint">0 表示使用入站端口</span>
        </el-form-item>
        <el-form-item label="启用订阅">
          <el-switch v-model="form.subscription_enabled" />
        </el-form-item>
        <el-form-item label="允许内网目标">
          <el-switch v-model="form.allow_private_targets" />
          <div class="hint">开启后客户端可经代理访问内网；云元数据地址始终拒绝。</div>
        </el-form-item>
        <el-form-item label="全局月度重置日">
          <el-input-number v-model="form.reset_day" :min="0" :max="28" controls-position="right" />
          <span class="hint">0 表示不自动重置，1–28 日 00:00</span>
        </el-form-item>
        <el-form-item label="时区">
          <el-select v-model="form.timezone" filterable allow-create style="width: 260px">
            <el-option v-for="zone in zones" :key="zone" :label="zone" :value="zone" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存设置</el-button>
        </el-form-item>
      </el-form>
    </div>

    <div class="card">
      <h3 class="card-title">数据备份</h3>
      <p class="muted">下载 SQLite 数据库快照。恢复时请停止服务，替换数据库文件后重新启动。</p>
      <el-button :icon="Download" @click="downloadBackup">下载备份</el-button>
    </div>

    <div class="card">
      <h3 class="card-title">安全</h3>
      <el-button @click="passwordVisible = true">修改管理员密码</el-button>
    </div>

    <PasswordDialog v-model="passwordVisible" />
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Download, Refresh } from '@element-plus/icons-vue'
import { api } from '@/api'
import { useAuthStore } from '@/stores/auth'
import type { Settings } from '@/types'
import PasswordDialog from '@/components/PasswordDialog.vue'

const auth = useAuthStore()
const formRef = ref()
const loading = ref(false)
const saving = ref(false)
const passwordVisible = ref(false)
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

async function load() {
  loading.value = true
  try {
    Object.assign(form, await api.settings())
  } catch (error: any) {
    ElMessage.error(error?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

async function save() {
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

function downloadBackup() {
  window.location.href = api.backupURL()
}

onMounted(load)
</script>

