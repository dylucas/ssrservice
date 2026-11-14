<template>
  <el-container class="layout">
    <el-aside :width="collapsed ? '64px' : '208px'" class="aside">
      <div class="brand">
        <span class="brand-mark">SS</span>
        <span v-if="!collapsed" class="brand-name">sspanel</span>
      </div>
      <el-menu
        :default-active="activeMenu"
        :collapse="collapsed"
        :collapse-transition="false"
        router
      >
        <el-menu-item index="/">
          <el-icon><Odometer /></el-icon>
          <template #title>仪表盘</template>
        </el-menu-item>
        <el-menu-item index="/inbounds">
          <el-icon><Connection /></el-icon>
          <template #title>入站管理</template>
        </el-menu-item>
        <el-menu-item index="/clients">
          <el-icon><User /></el-icon>
          <template #title>用户管理</template>
        </el-menu-item>
        <el-menu-item index="/audit">
          <el-icon><Document /></el-icon>
          <template #title>审计日志</template>
        </el-menu-item>
        <el-menu-item index="/settings">
          <el-icon><Setting /></el-icon>
          <template #title>系统设置</template>
        </el-menu-item>
      </el-menu>
      <div class="aside-footer">
        <span v-if="!collapsed">v{{ version }}</span>
      </div>
    </el-aside>
    <el-container>
      <el-header class="header">
        <div class="header-left">
          <el-button text @click="collapsed = !collapsed">
            <el-icon><Fold v-if="!collapsed" /><Expand v-else /></el-icon>
          </el-button>
          <span class="header-title">Shadowsocks 控制台</span>
        </div>
        <div class="header-right">
          <el-tag v-if="auth.admin?.must_change_password" type="warning" size="small">请修改默认密码</el-tag>
          <el-dropdown @command="onCommand">
            <span class="user">
              <el-icon><Avatar /></el-icon>
              {{ auth.admin?.username || 'admin' }}
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
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  ArrowDown,
  Avatar,
  Connection,
  Document,
  Expand,
  Fold,
  Odometer,
  Setting,
  User,
} from '@element-plus/icons-vue'
import { version } from '@/api'
import { useAuthStore } from '@/stores/auth'
import PasswordDialog from '@/components/PasswordDialog.vue'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const collapsed = ref(false)
const passwordVisible = ref(false)

const activeMenu = computed(() => {
  if (route.path.startsWith('/inbounds')) return '/inbounds'
  if (route.path.startsWith('/clients')) return '/clients'
  if (route.path.startsWith('/settings')) return '/settings'
  if (route.path.startsWith('/audit')) return '/audit'
  return '/'
})

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
  background: var(--sspanel-aside-bg);
  display: flex;
  flex-direction: column;
  transition: width 0.2s;
}
.brand {
  height: var(--sspanel-header-height);
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 18px;
  color: var(--sspanel-surface);
  font-weight: 700;
  font-size: 17px;
  border-bottom: 1px solid var(--sspanel-brand-divider);
}
.brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 8px;
  background: linear-gradient(135deg, var(--sspanel-primary-soft), var(--sspanel-primary));
  font-size: 13px;
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
}
.aside-footer {
  padding: 12px 18px;
  color: var(--sspanel-aside-muted);
  font-size: 12px;
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
  gap: 8px;
}
.header-title {
  font-weight: 600;
}
.header-right {
  display: flex;
  align-items: center;
  gap: 12px;
}
.user {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  color: var(--sspanel-text);
}
.main {
  padding: 0;
  background: var(--sspanel-bg);
}
</style>
