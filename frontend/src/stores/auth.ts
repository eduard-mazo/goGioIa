import { defineStore } from 'pinia'

export interface AuthUser {
  id: string
  email: string
  display_name: string
  role: 'admin' | 'user'
}

/** Error de la API de auth con el código y estado HTTP originales. */
export class AuthError extends Error {
  constructor(
    public code: string,
    public status: number,
  ) {
    super(code)
  }
}

async function authFetch(url: string, body?: unknown): Promise<Response> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string }
    throw new AuthError(data.error ?? 'unknown', res.status)
  }
  return res
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    user: null as AuthUser | null,
    mustChangePassword: false,
    /** true cuando ya se consultó /api/auth/me al arrancar la SPA. */
    bootstrapped: false,
  }),

  getters: {
    isAuthenticated: (s) => s.user !== null,
    isAdmin: (s) => s.user?.role === 'admin',
  },

  actions: {
    /** Restaura la sesión de la cookie al cargar la app (best-effort). */
    async bootstrap() {
      if (this.bootstrapped) return
      try {
        const res = await fetch('/api/auth/me')
        if (res.ok) {
          const data = await res.json()
          this.user = data.user
          this.mustChangePassword = data.must_change_password
        }
      } catch {
        /* backend caído: se trata como no autenticado */
      }
      this.bootstrapped = true
    },

    async login(email: string, password: string) {
      const res = await authFetch('/api/auth/login', { email, password })
      const data = await res.json()
      this.user = data.user
      this.mustChangePassword = data.must_change_password
      this.bootstrapped = true
    },

    async changePassword(current: string, newPassword: string) {
      await authFetch('/api/auth/password', { current, new: newPassword })
      this.mustChangePassword = false
    },

    async logout() {
      try {
        await authFetch('/api/auth/logout')
      } catch {
        /* la cookie puede haber caducado ya; el estado local se limpia igual */
      }
      this.clear()
    },

    /** Limpia el estado local (p. ej. tras un 401 interceptado). */
    clear() {
      this.user = null
      this.mustChangePassword = false
    },
  },
})
