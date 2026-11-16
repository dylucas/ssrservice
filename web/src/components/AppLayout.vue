<template>
  <el-container class="layout">
    <el-aside :class="['aside', { 'aside--collapsed': collapsed }]">
      <div :class="['brand', { 'brand--collapsed': collapsed }]">
        <span class="brand-mark">SS</span>
        <span v-if="!collapsed" class="brand-name">sspanel</span>
      </div>
      <el-menu
        :default-active="activeMenu"
        :collapse="collapsed"
        :collapse-transition="false"
        router
      >
        <el-menu-item v-for="item in navItems" :key="item.path" :index="item.path">
          <el-icon><component :is="icons[item.icon]" /></el-icon>
          <template #title>{{ item.title }}</template>
        </el-menu-item>
      </el-menu>
      <div class="aside-footer">
        <span v-if="!collapsed">v{{ version }}</span>
      </div>
    </el-aside>
    <el-container>
      <el-header class="header">
        <div class="header-left">
          <el-button text :aria-label="collapsed ? '展开侧栏' : '收起侧栏'" @click="collapsed = !collapsed">
            <el-icon><Fold v-if="!collapsed" /><Expand v-else /></el-icon>
          </el-button>
          <span class="header-crumb">控制台</span>
          <span v-if="section" class="header-sep">/</span>
          <span v-if="section" class="header-title">{{ section }}</span>
        </div>
        <div class="header-right">
          <el-tag v-if="auth.admin?.must_change_password" type="warning" size="small">请修改默认密码</el-tag>
          <el-dropdown @command="onCommand">
            <span class="user">
              <span class="user-avatar">{{ initial }}</span>
              <span class="user-name">{{ auth.admin?.username || 'admin' }}</span>
              <el-icon><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="password">修改密码</el-dropdown-item>
                <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>
      <el-main class="main">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
  <PasswordDialog v-model="passwordVisible" />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  ArrowDown,
  Connection,
  DataAnalysis,
  Document,
  Expand,
  Fold,
  Odometer,
  Setting,
  User,
} from '@element-plus/icons-vue'
import { version } from '@/api'
import { MOBILE_BREAKPOINT } from '@/breakpoints'
import { matchNav, navItems, titleOf } from '@/nav'
import { useAuthStore } from '@/stores/auth'
import PasswordDialog from '@/components/PasswordDialog.vue'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
// A phone cannot afford the 208px rail, so the menu starts collapsed there.
// Resizing only overrides the operator's choice when the viewport actually
// crosses the breakpoint: otherwise a desktop user who collapsed the menu would
// see it pop back open on any resize.
const wasNarrow = ref(isNarrow())
const collapsed = ref(wasNarrow.value)

function isNarrow(): boolean {
  return typeof window !== 'undefined' && window.innerWidth <= MOBILE_BREAKPOINT
}

function onResize() {
  const narrow = isNarrow()
  if (narrow === wasNarrow.value) return
  wasNarrow.value = narrow
  collapsed.value = narrow
}

onMounted(() => window.addEventListener('resize', onResize))
onBeforeUnmount(() => window.removeEventListener('resize', onResize))

const passwordVisible = ref(false)

// nav.ts holds the menu; the icons stay here so that module remains plain data.
const icons: Record<string, Component> = {
  Odometer,
  Connection,
  User,
  Document,
  Setting,
}

// The active entry is the one the path resolves to, so a deep link such as
// /clients?inbound=3 still highlights 用户管理.
const activeMenu = computed(() => matchNav(route.path)?.path ?? '/')
const section = computed(() => titleOf(route.path))
const initial = computed(() => (auth.admin?.username || 'admin').slice(0, 1).toUpperCase())

async function onCommand(command: string) {
  if (command === 'password') {
    passwordVisible.value = true
    return
  }
  if (command === 'logout') {
    await auth.logout()
    ElMessage.success('已退出登录')
    router.push('/login')
  }
}
</script>

<style scoped>
.layout {
  min-height: 100vh;
}
.aside {
  width: var(--sspanel-aside-width);
  background: var(--sspanel-aside-bg);
  display: flex;
  flex-direction: column;
  transition: width 0.2s;
}
.aside--collapsed {
  width: var(--sspanel-aside-collapsed-width);
}
.brand {
  height: var(--sspanel-header-height);
  display: flex;
  align-items: center;
  gap: var(--sspanel-space-4);
  padding: 0 var(--sspanel-space-7);
  color: var(--sspanel-surface);
  font-weight: 700;
  font-size: var(--sspanel-fs-lg);
  border-bottom: 1px solid var(--sspanel-brand-divider);
}
/* A collapsed rail is 64px wide; the 30px mark plus both paddings does not fit,
 * so the mark is centred instead of padded. */
.brand--collapsed {
  padding: 0;
  justify-content: center;
}
/* The side menu is themed through Element Plus' own CSS variables rather than the
 * background-color/text-color props: those props are mixed in JavaScript to derive
 * hover shades, which a var() reference cannot survive. */
.aside :deep(.el-menu) {
  border-right: none;
  flex: 1;
  --el-menu-bg-color: var(--sspanel-aside-bg);
  --el-menu-text-color: var(--sspanel-aside-text);
  --el-menu-active-color: var(--sspanel-surface);
  --el-menu-hover-bg-color: var(--sspanel-aside-hover);
  --el-menu-hover-text-color: var(--sspanel-surface);
  --el-menu-item-height: var(--sspanel-menu-item-height);
}
/* The current section is a filled row with a brand-coloured bar, so it reads as
 * "you are here" from across the room. The bar is drawn on the item's own
 * positioning context so it does not shift the icon. */
.aside :deep(.el-menu-item.is-active) {
  background: var(--sspanel-aside-active);
}
.aside :deep(.el-menu-item.is-active::before) {
  content: '';
  position: absolute;
  left: 0;
  top: var(--sspanel-space-4);
  bottom: var(--sspanel-space-4);
  width: 3px;
  border-radius: var(--sspanel-radius-xs);
  background: var(--sspanel-primary-soft);
}
.aside-footer {
  padding: var(--sspanel-space-5) var(--sspanel-space-7);
  color: var(--sspanel-aside-muted);
  font-size: var(--sspanel-fs-xs);
}
.header {
  height: var(--sspanel-header-height);
  background: var(--sspanel-surface);
  border-bottom: 1px solid var(--sspanel-border);
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.header-left {
  display: flex;
  align-items: center;
  gap: var(--sspanel-space-3);
}
/* Breadcrumb root: a quiet label that gives the page title a place to hang. */
.header-crumb {
  color: var(--sspanel-text-hint);
  font-size: var(--sspanel-fs-sm);
}
.header-sep {
  color: var(--sspanel-text-hint);
  font-size: var(--sspanel-fs-sm);
}
.header-title {
  font-weight: 600;
}
.header-right {
  display: flex;
  align-items: center;
  gap: var(--sspanel-space-5);
}
.user {
  display: inline-flex;
  align-items: center;
  gap: var(--sspanel-space-2);
  cursor: pointer;
  color: var(--sspanel-text);
}
.user-name {
  max-width: var(--sspanel-control-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.user-avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: var(--sspanel-radius-chip);
  background: var(--sspanel-primary-tint);
  color: var(--sspanel-primary-strong);
  font-size: var(--sspanel-fs-xs);
  font-weight: 600;
}
.main {
  padding: 0;
  background: var(--sspanel-bg);
}
/* On a phone the breadcrumb root, its separator and the account name would push
 * the header past the viewport (measured: 14px of document overflow). The page
 * title already says where you are, and the avatar identifies the account. */
@media (max-width: 782px) {
  .header-crumb,
  .header-sep,
  .user-name {
    display: none;
  }
}
</style>
