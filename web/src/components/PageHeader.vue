<template>
  <div class="page-header">
    <div class="page-heading">
      <h2 class="page-title">{{ title }}</h2>
      <div v-if="subtitle" class="page-subtitle">{{ subtitle }}</div>
    </div>
    <div class="page-actions">
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup lang="ts">
// The one page header. Every view used to spell this block out itself, which is
// how the five of them drifted apart (bare button vs toolbar row, subtitle or
// not). Layout comes from .page-header / .page-heading in styles.css, so this
// component carries no styles of its own.
defineProps<{ title: string; subtitle?: string }>()
</script>
