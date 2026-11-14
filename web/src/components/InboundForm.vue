<template>
  <el-dialog
    :model-value="modelValue"
    :title="inbound ? '编辑入站' : '新建入站'"
    width="min(560px, 92vw)"
    @update:model-value="emit('update:modelValue', $event)"
    @closed="reset"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="96px">
      <el-form-item label="备注" prop="remark">
        <el-input v-model="form.remark" maxlength="64" placeholder="例如：香港节点 8388" />
      </el-form-item>
      <el-form-item label="监听地址" prop="listen">
        <el-input v-model="form.listen" placeholder="0.0.0.0 或 ::（IPv6 双栈）" />
      </el-form-item>
      <el-form-item label="端口" prop="port">
        <el-input-number v-model="form.port" :min="1" :max="65535" controls-position="right" />
      </el-form-item>
      <el-form-item label="加密方法" prop="method">
        <el-select v-model="form.method" style="width: 100%" @change="onMethodChange">
          <el-option v-for="item in methods" :key="item" :label="item" :value="item" />
        </el-select>
        <div class="muted method-hint">{{ methodHint }}</div>
      </el-form-item>
      <el-form-item v-if="needsServerPSK" label="服务器 PSK" prop="server_psk">
        <el-input v-model="form.server_psk" placeholder="留空自动生成">
          <template #append>
            <el-button @click="generateServerPSK">生成</el-button>
          </template>
        </el-input>
        <div class="muted">base64 编码，长度由方法决定</div>
      </el-form-item>
      <el-form-item label="UDP 转发">
        <el-switch v-model="form.udp_enabled" />
      </el-form-item>
      <el-form-item label="启用">
        <el-switch v-model="form.enabled" />
      </el-form-item>
      <el-alert
        v-if="inbound"
        type="info"
        :closable="false"
        show-icon
        title="修改方法或 PSK 后，已有连接需要重连才能生效。"
      />
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
import type { Inbound } from '@/types'

const props = defineProps<{ modelValue: boolean; inbound: Inbound | null; methods: string[] }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void; (e: 'saved'): void }>()

const formRef = ref<FormInstance>()
const saving = ref(false)
const form = reactive({
  remark: '',
  listen: '0.0.0.0',
  port: 8388,
  method: '2022-blake3-aes-128-gcm',
  server_psk: '',
  udp_enabled: true,
  enabled: true,
})

const rules: FormRules = {
  remark: [{ max: 64, message: '备注最多 64 个字符', trigger: 'blur' }],
  listen: [{ required: true, message: '请输入监听地址', trigger: 'blur' }],
  port: [{ required: true, message: '请输入端口', trigger: 'change' }],
  method: [{ required: true, message: '请选择加密方法', trigger: 'change' }],
}

const needsServerPSK = computed(
  () => form.method.startsWith('2022-') && form.method !== '2022-blake3-chacha20-poly1305',
)

const methodHint = computed(() => {
  if (form.method === '2022-blake3-chacha20-poly1305') return 'SS2022 单用户方法：一个入站只能有一个启用用户'
  if (form.method.startsWith('2022-')) return 'SS2022 多用户方法：同一端口按用户 PSK 隔离'
  return '传统 AEAD 方法：库会逐个尝试用户密码，建议每入站用户数不超过 100'
})

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return
    if (props.inbound) {
      Object.assign(form, {
        remark: props.inbound.remark,
        listen: props.inbound.listen,
        port: props.inbound.port,
        method: props.inbound.method,
        server_psk: props.inbound.server_psk || '',
        udp_enabled: props.inbound.udp_enabled,
        enabled: props.inbound.enabled,
      })
    } else {
      Object.assign(form, {
        remark: '',
        listen: '0.0.0.0',
        port: randomPort(),
        method: '2022-blake3-aes-128-gcm',
        server_psk: '',
        udp_enabled: true,
        enabled: true,
      })
    }
  },
)

function randomPort(): number {
  return 10000 + Math.floor(Math.random() * 50000)
}

function onMethodChange() {
  if (!needsServerPSK.value) form.server_psk = ''
}

function generateServerPSK() {
  const size = form.method === '2022-blake3-aes-256-gcm' ? 32 : 16
  const bytes = new Uint8Array(size)
  crypto.getRandomValues(bytes)
  form.server_psk = btoa(String.fromCharCode(...bytes))
}

function reset() {
  formRef.value?.clearValidate()
}

async function submit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    if (props.inbound) {
      await api.updateInbound(props.inbound.id, { ...form })
      ElMessage.success('入站已更新')
    } else {
      await api.createInbound({ ...form })
      ElMessage.success('入站已创建')
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
.method-hint {
  margin-top: 4px;
}
</style>
