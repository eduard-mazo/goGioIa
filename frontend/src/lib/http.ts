// Interceptor global de fetch para la API propia: inyecta el token CSRF de
// doble envío en las peticiones mutantes y notifica los 401 para que la app
// redirija al login. Se instala una sola vez en main.ts.

const MUTATING = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

/** Lee una cookie por nombre ('' si no existe). */
export function readCookie(name: string): string {
  for (const part of document.cookie.split(';')) {
    const [k, ...v] = part.trim().split('=')
    if (k === name) return decodeURIComponent(v.join('='))
  }
  return ''
}

export function installFetchInterceptor(onUnauthorized: () => void): void {
  const original = window.fetch.bind(window)
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url
    const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase()

    if (url.startsWith('/api/') && MUTATING.has(method)) {
      const csrf = readCookie('csrf')
      if (csrf) {
        const headers = new Headers(
          init?.headers ?? (input instanceof Request ? input.headers : undefined),
        )
        headers.set('X-CSRF-Token', csrf)
        init = { ...init, headers }
      }
    }

    const res = await original(input, init)

    // Sesión caducada o revocada: cualquier 401 de la API (salvo el propio
    // login y el sondeo de sesión) manda al usuario a la pantalla de acceso.
    if (
      res.status === 401 &&
      url.startsWith('/api/') &&
      url !== '/api/auth/login' &&
      url !== '/api/auth/me'
    ) {
      onUnauthorized()
    }
    return res
  }
}
