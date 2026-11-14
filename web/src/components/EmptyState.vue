<template>
  <!-- The root is a <span>: el-table renders its #empty slot inside
       <span class="el-table__empty-text">, and a <div> there is invalid HTML. -->
  <span class="table-empty">
    <el-icon v-if="icon" class="table-empty-icon"><component :is="icon" /></el-icon>
    <span class="table-empty-text">{{ text }}</span>
    <slot />
  </span>
</template>

<script setup lang="ts">
import type { Component } from 'vue'

// The empty state for every table. It carries the optional follow-up action as
// its default slot (for example "清除筛选条件"), which is what turns a dead end
// into something the operator can act on.
defineProps<{
  text: string
  /** Icon component from @element-plus/icons-vue. */
  icon?: Component
}>()
</script>
