<template>
  <el-dialog
    :model-value="modelValue"
    :title="client ? '编辑用户' : '新建用户'"
    width="580px"
    @update:model-value="emit('update:modelValue', $event)"
    @closed="reset"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="104px">
      <el-form-item label="用户名" prop="name">
        <el-input v-model="form.name" maxlength="64" placeholder="例如：alice" />
      </el-form-item>
      <el-form-item label="密码 / PSK" prop="psk">
        <el-input v-model="form.psk" :placeholder="client ? '留空自动生成' : '留空自动生成'">
          <template #append>
            <el-button @click="generatePSK">生成</el-button>
          </template>
        </el-input>
        <div class="muted">{{ pskHint }}</div>
      </el-form-item>
      <el-form-item label="流量配额">
        <el-input-number v-model="quotaGB" :min="0" :precision="2" :step="10" controls-position="right" />
        <span class="unit">GB（0 表示不限）</span>
      </el-form-item>
      <el-form-item label="到期时间">
        <el-date-picker
          v-model="expireDate"
          type="datetime"
          placeholder="永久有效"
          clearable
          style="width: 100%"
        />
      </el-form-item>
      <el-form-item label="IP 限制">
        <el-input-number v-model="form.ip_limit" :min="0" :max="10000" controls-position="right" />
        <span class="unit">个不同 IP（0 表示不限）</span>
      </el-form-item>
      <el-form-item label="月度重置">
        <el-select v-model="form.reset_policy" style="width: 140px">
          <el-option label="不重置" value="none" />
          <el-option label="每月重置" value="monthly" />
        </el-select>
        <el-input-number
          v-if="form.reset_policy === 'monthly'"
          v-model="form.reset_day"
          :min="0"
          :max="28"
          style="margin-left: 10px"
          controls-position="right"
        />
        <span v-if="form.reset_policy === 'monthly'" class="unit">日（0 表示跟随全局）</span>
      </el-form-item>
      <el-form-item label="备注">
        <el-input v-model="form.note" maxlength="255" type="textarea" :rows="2" />
      </el-form-item>
      <el-form-item label="启用">
        <el-switch v-model="form.enabled" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { api } from '@/api'
import type { ClientSummary } from '@/types'

const props = defineProps<{
  modelValue: boolean
  inboundId: number
  client: ClientSummary | null
  method: string
}>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void; (e: 'saved'): void }>()

const formRef = ref<FormInstance>()
const saving = ref(false)
const quotaGB = ref(0)
const expireDate = ref<Date | null>(null)
const form = reactive({
  name: '',
  psk: '',
  enabled: true,
  ip_limit: 0,
  reset_policy: 'none' as 'none' | 'monthly',
  reset_day: 0,
  note: '',
})

const isSS2022 = computed(() => props.method.startsWith('2022-'))
const pskHint = computed(() =>
  isSS2022.value ? 'SS2022：base64 编码，长度由方法决定' : '传统 AEAD：任意非空密码',
)

const rules: FormRules = {
  name: [
    { required: true, message: '请输入用户名', trigger: 'blur' },
    { max: 64, message: '用户名最多 64 个字符', trigger: 'blur' },
  ],
}

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return
    if (props.client) {
      form.name = props.client.name
      form.psk = ''
      form.enabled = props.client.enabled
      form.ip_limit = props.client.ip_limit
      form.reset_policy = props.client.reset_policy
      form.reset_day = props.client.reset_day
      form.note = props.client.note
      quotaGB.value = props.client.quota_bytes ? props.client.quota_bytes / 1024 / 1024 / 1024 : 0
      expireDate.value = props.client.expires_at ? new Date(props.client.expires_at * 1000) : null
    } else {
      form.name = ''
      form.psk = ''
      form.enabled = true
      form.ip_limit = 0
      form.reset_policy = 'none'
      form.reset_day = 0
      form.note = ''
      quotaGB.value = 0
      expireDate.value = null
    }
  },
)

function generatePSK() {
  const size = props.method.endsWith('aes-128-gcm') ? 16 : 32
  if (isSS2022.value) {
    const bytes = new Uint8Array(size)
    crypto.getRandomValues(bytes)
    form.psk = btoa(String.fromCharCode(...bytes))
    return
  }
  const alphabet = 'abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  form.psk = Array.from(bytes, (b) => alphabet[b % alphabet.length]).join('')
}

function reset() {
  formRef.value?.clearValidate()
}

async function submit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  const payload = {
    name: form.name,
    psk: form.psk,
    enabled: form.enabled,
    quota_bytes: Math.round(quotaGB.value * 1024 * 1024 * 1024),
    expires_at: expireDate.value ? Math.floor(expireDate.value.getTime() / 1000) : 0,
    ip_limit: form.ip_limit,
    reset_policy: form.reset_policy,
    reset_day: form.reset_policy === 'monthly' ? form.reset_day : 0,
    note: form.note,
  }
  try {
    if (props.client) {
      await api.updateClient(props.client.id, payload)
      ElMessage.success('用户已更新')
    } else {
      await api.createClient(props.inboundId, payload)
      ElMessage.success('用户已创建')
    }
    emit('update:modelValue', false)
    emit('saved')
  } catch (error: any) {
    ElMessage.error(error?.message || '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.unit {
  margin-left: 8px;
  color: #8a94a6;
  font-size: 12px;
}
</style>
