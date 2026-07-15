<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Bot, KeyRound, LoaderCircle } from 'lucide-vue-next'
import { AuthError, useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const email = ref('')
const password = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const error = ref('')

// Paso de cambio obligado. Si la sesión pendiente se restauró de la cookie
// (no hay contraseña del paso 1 en memoria), se pide la actual explícitamente.
const changing = computed(() => auth.isAuthenticated && auth.mustChangePassword)
const needCurrent = computed(() => changing.value && password.value === '')
const currentPassword = ref('')

function loginErrorMessage(e: unknown): string {
  if (e instanceof AuthError) {
    switch (e.status) {
      case 401:
        return t.auth.invalid
      case 423:
        return t.auth.locked
      case 429:
        return t.auth.rateLimited
    }
  }
  return t.auth.serverDown
}

async function onLogin() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    await auth.login(email.value.trim(), password.value)
    if (!auth.mustChangePassword) done()
  } catch (e) {
    error.value = loginErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function onChangePassword() {
  if (loading.value) return
  if (newPassword.value !== confirmPassword.value) {
    error.value = t.auth.mismatch
    return
  }
  loading.value = true
  error.value = ''
  try {
    const current = needCurrent.value ? currentPassword.value : password.value
    await auth.changePassword(current, newPassword.value)
    done()
  } catch (e) {
    if (e instanceof AuthError && e.code === 'weak_password') error.value = t.auth.weakPassword
    else if (e instanceof AuthError && e.status === 401) error.value = t.auth.wrongCurrent
    else error.value = t.auth.serverDown
  } finally {
    loading.value = false
  }
}

function done() {
  const r = route.query.r
  void router.push(typeof r === 'string' && r.startsWith('/') ? r : '/')
}
</script>

<template>
  <div class="grid min-h-dvh place-items-center bg-background px-4 text-foreground">
    <div class="w-full max-w-sm">
      <!-- Marca -->
      <div class="mb-8 flex flex-col items-center gap-3 text-center">
        <div class="grid h-12 w-12 place-items-center rounded-sm bg-primary text-primary-foreground">
          <Bot class="h-6 w-6" />
        </div>
        <div>
          <h1 class="text-2xl font-extrabold tracking-tight">{{ t.app.name }}</h1>
          <p class="mt-1 font-mono text-[11px] uppercase tracking-[0.2em] text-muted-foreground">
            {{ t.auth.subtitle }}
          </p>
        </div>
      </div>

      <div class="rounded-sm border border-border bg-background/80 p-6 shadow-sm">
        <!-- Paso 1: credenciales -->
        <form v-if="!changing" class="space-y-4" @submit.prevent="onLogin">
          <h2 class="text-sm font-bold">{{ t.auth.title }}</h2>
          <label class="block space-y-1.5">
            <span class="text-xs font-semibold text-muted-foreground">{{ t.auth.email }}</span>
            <input
              v-model="email"
              type="email"
              name="email"
              autocomplete="username"
              required
              autofocus
              class="h-10 w-full rounded-sm border border-border bg-transparent px-3 text-sm outline-none transition focus:border-primary"
            />
          </label>
          <label class="block space-y-1.5">
            <span class="text-xs font-semibold text-muted-foreground">{{ t.auth.password }}</span>
            <input
              v-model="password"
              type="password"
              name="password"
              autocomplete="current-password"
              required
              class="h-10 w-full rounded-sm border border-border bg-transparent px-3 text-sm outline-none transition focus:border-primary"
            />
          </label>
          <button
            type="submit"
            :disabled="loading || !email || !password"
            class="inline-flex h-10 w-full items-center justify-center gap-2 rounded-sm bg-primary text-sm font-semibold text-primary-foreground transition hover:opacity-90 disabled:opacity-50"
          >
            <LoaderCircle v-if="loading" class="h-4 w-4 animate-spin" />
            {{ loading ? t.auth.submitting : t.auth.submit }}
          </button>
        </form>

        <!-- Paso 2: cambio de contraseña obligado -->
        <form v-else class="space-y-4" @submit.prevent="onChangePassword">
          <div class="flex items-start gap-3">
            <KeyRound class="mt-0.5 h-4 w-4 shrink-0 text-[color:var(--epm-citrico)]" />
            <div>
              <h2 class="text-sm font-bold">{{ t.auth.changeTitle }}</h2>
              <p class="mt-1 text-xs text-muted-foreground">{{ t.auth.changeIntro }}</p>
            </div>
          </div>
          <label v-if="needCurrent" class="block space-y-1.5">
            <span class="text-xs font-semibold text-muted-foreground">{{ t.auth.currentPassword }}</span>
            <input
              v-model="currentPassword"
              type="password"
              autocomplete="current-password"
              required
              class="h-10 w-full rounded-sm border border-border bg-transparent px-3 text-sm outline-none transition focus:border-primary"
            />
          </label>
          <label class="block space-y-1.5">
            <span class="text-xs font-semibold text-muted-foreground">{{ t.auth.newPassword }}</span>
            <input
              v-model="newPassword"
              type="password"
              autocomplete="new-password"
              required
              minlength="12"
              maxlength="128"
              class="h-10 w-full rounded-sm border border-border bg-transparent px-3 text-sm outline-none transition focus:border-primary"
            />
          </label>
          <label class="block space-y-1.5">
            <span class="text-xs font-semibold text-muted-foreground">{{ t.auth.confirmPassword }}</span>
            <input
              v-model="confirmPassword"
              type="password"
              autocomplete="new-password"
              required
              class="h-10 w-full rounded-sm border border-border bg-transparent px-3 text-sm outline-none transition focus:border-primary"
            />
          </label>
          <button
            type="submit"
            :disabled="loading || !newPassword || !confirmPassword"
            class="inline-flex h-10 w-full items-center justify-center gap-2 rounded-sm bg-primary text-sm font-semibold text-primary-foreground transition hover:opacity-90 disabled:opacity-50"
          >
            <LoaderCircle v-if="loading" class="h-4 w-4 animate-spin" />
            {{ loading ? t.auth.changing : t.auth.change }}
          </button>
        </form>

        <p
          v-if="error"
          class="mt-4 rounded-sm border border-[color:color-mix(in_srgb,var(--signal-fault)_40%,transparent)] bg-[color:color-mix(in_srgb,var(--signal-fault)_10%,transparent)] px-3 py-2 text-xs text-[color:var(--signal-fault)]"
          role="alert"
        >
          {{ error }}
        </p>
      </div>
    </div>
  </div>
</template>
