<template>
  <el-dialog v-model="visible" title="修改密码" width="min(420px, 92vw)" @closed="reset">
    <el-form ref="formRef" :model="form" :rules="rules" label-width="92px">
      <el-form-item label="当前密码" prop="current">
        <el-input v-model="form.current" type="password" show-password autocomplete="current-password" />
      </el-form-item>
      <el-form-item label="新密码" prop="next">
        <el-input v-model="form.next" type="password" show-password autocomplete="new-password" />
      </el-form-item>
      <el-form-item label="确认新密码" prop="confirm">
        <el-input v-model="form.confirm" type="password" show-password autocomplete="new-password" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

const auth = useAuthStore()
const visible = ref(props.modelValue)
watch(
  () => props.modelValue,
  (value) => (visible.value = value),
)
watch(visible, (value) => emit('update:modelValue', value))

const formRef = ref<FormInstance>()
const saving = ref(false)
const form = reactive({ current: '', next: '', confirm: '' })
const rules: FormRules = {
  current: [{ required: true, message: '请输入当前密码', trigger: 'blur' }],
  next: [
    { required: true, message: '请输入新密码', trigger: 'blur' },
    { min: 8, message: '密码至少 8 位', trigger: 'blur' },
  ],
  confirm: [
    {
      validator: (_rule, value, callback) => {
        if (!value) return callback(new Error('请再次输入新密码'))
        if (value !== form.next) return callback(new Error('两次输入的密码不一致'))
        callback()
      },
      trigger: 'blur',
    },
  ],
}

function reset() {
  form.current = ''
  form.next = ''
  form.confirm = ''
  formRef.value?.clearValidate()
}

async function submit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    await auth.changePassword(form.current, form.next)
    ElMessage.success('密码已更新')
    visible.value = false
  } catch (error: any) {
    ElMessage.error(error?.message || '修改失败')
  } finally {
    saving.value = false
  }
}
</script>
