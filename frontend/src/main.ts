import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { useAuthStore } from './stores/auth'
import { installFetchInterceptor } from './lib/http'
import './style.css'
// Syntax-highlighting theme for fenced code blocks (bundled, offline).
import 'highlight.js/styles/github-dark.css'

const pinia = createPinia()

// Todo 401 de la API (sesión caducada/revocada) limpia el estado y vuelve al
// login; el CSRF de doble envío se inyecta aquí para todas las mutaciones.
installFetchInterceptor(() => {
  useAuthStore(pinia).clear()
  if (router.currentRoute.value.name !== 'login') {
    void router.push({ name: 'login' })
  }
})

createApp(App).use(pinia).use(router).mount('#app')
