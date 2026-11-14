import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { installElement } from './element'
import './styles.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
// Element Plus is registered per component in ./element.ts so the bundle only
// carries the components and styles this panel actually uses.
installElement(app)
app.mount('#app')
