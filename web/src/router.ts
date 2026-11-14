import { createRouter, createWebHistory } from 'vue-router'
import { basePath } from '@/api'
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
        { path: 'clients', name: 'clients', component: () => import('@/views/ClientsView.vue') },
        { path: 'settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
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

export default router
