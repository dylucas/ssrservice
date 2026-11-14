import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { installElement } from './element'
import { setPasswordRequiredHandler, setUnauthorizedHandler } from './api'
import { useAuthStore } from './stores/auth'
import './styles.css'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(router)

// Session failures are global: every view would otherwise just show a toast and
// leave the operator on a page whose data can no longer be refreshed.
//
// The flags dedupe a burst of parallel requests that all fail at once; the route
// check keeps a failure on the target page from pushing the same route again.
let redirectingToLogin = false
let redirectingToPassword = false

setUnauthorizedHandler(() => {
  useAuthStore(pinia).clear()
  const current = router.currentRoute.value
  if (current.name === 'login' || redirectingToLogin) return
  redirectingToLogin = true
  // fullPath is lost across a full page load, so it is carried in the query for
  // the login view to return to.
  const redirect = current.fullPath && current.fullPath !== '/' ? current.fullPath : undefined
  void router
    .push({ name: 'login', query: redirect ? { redirect } : {} })
    .finally(() => {
      redirectingToLogin = false
    })
})

setPasswordRequiredHandler(() => {
  const current = router.currentRoute.value
  if (redirectingToPassword) return
  if (current.name === 'settings' && current.query.force === 'password') return
  redirectingToPassword = true
  void router
    .push({ name: 'settings', query: { force: 'password' } })
    .finally(() => {
      redirectingToPassword = false
    })
})

// Element Plus is registered per component in ./element.ts so the bundle only
// carries the components and styles this panel actually uses.
installElement(app)
app.mount('#app')
