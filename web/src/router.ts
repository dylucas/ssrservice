import { createRouter, createWebHistory } from 'vue-router'
import { basePath } from '@/api'
import { appTitle, titleOf } from '@/nav'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(basePath || '/'),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('@/components/AppLayout.vue'),
      children: [
        { path: '', name: 'dashboard', component: () => import('@/views/DashboardView.vue') },
        { path: 'inbounds', name: 'inbounds', component: () => import('@/views/InboundsView.vue') },
        { path: 'inbounds/:id', name: 'inbound-detail', component: () => import('@/views/InboundDetailView.vue') },
        { path: 'clients', name: 'clients', component: () => import('@/views/ClientsView.vue') },
        { path: 'access', name: 'access', component: () => import('@/views/AccessView.vue') },
        { path: 'settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
        { path: 'audit', name: 'audit', component: () => import('@/views/AuditView.vue') },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (to.meta.public) {
    return true
  }
  if (!auth.checked) {
    await auth.fetchMe()
  }
  if (!auth.admin) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (auth.admin.must_change_password && to.name !== 'settings') {
    return { name: 'settings', query: { force: 'password' } }
  }
  return true
})

// Keep the document title in step with the sidebar. The menu lives in nav.ts, so
// the title and the highlighted entry cannot disagree.
router.afterEach((to) => {
  const section = titleOf(to.path)
  document.title = section ? `${section} · sspanel` : appTitle
})

export default router
