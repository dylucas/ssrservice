<template>
  <el-config-provider :locale="zhCn">
    <router-view />
  </el-config-provider>
</template>

<script setup lang="ts">
// Element Plus is registered per component in main.ts; the config provider
// supplies the global locale (date picker labels, pagination text, ...) that
// app.use(ElementPlus, { locale }) used to provide.
import zhCn from 'element-plus/es/locale/lang/zh-cn'
</script>
