<template>
  <div class="stat-card">
    <div class="stat-head">
      <div class="stat-label">{{ label }}</div>
      <el-icon class="stat-icon"><component :is="icon" /></el-icon>
    </div>
    <div class="stat-value">{{ value }}</div>
    <div v-if="hint" class="stat-hint">{{ hint }}</div>
  </div>
</template>

<script setup lang="ts">
import type { Component } from 'vue'

// One statistic in the dashboard grid. Values are passed pre-formatted: the
// formatters (bytes, duration) already live in utils/format.ts and are unit
// tested there. Like PageHeader this is a layout component and has no styles:
// .stat-card and friends are shared utilities.
defineProps<{
  label: string
  value: string
  hint?: string
  /** Icon component from @element-plus/icons-vue. */
  icon: Component
}>()
</script>
