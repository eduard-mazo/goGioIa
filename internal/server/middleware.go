package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"goGioIa/internal/session"
)

// Nombres de las cookies de autenticación.
const (
	cookieSession = "sid"  // token de sesión opaco (HttpOnly)
	cookieCSRF    = "csrf" // token CSRF de doble envío (legible por JS)
)

type sessionCtxKey struct{}

// sessionFrom devuelve la sesión autenticada del request (nil si no hay).
func sessionFrom(r *http.Request) *session.Session {
	s, _ := r.Context().Value(sessionCtxKey{}).(*session.Session)
	return s
}

// errJSON emite un error con el formato uniforme de la API.
func errJSON(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

// requireAuth valida la cookie de sesión y, en métodos mutantes, el token
// CSRF de doble envío. Deja la sesión en el contexto del request.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieSession)
		if err != nil || c.Value == "" {
			errJSON(w, http.StatusUnauthorized, "unauthorized", "sesión requerida")
			return
		}
		sess, err := s.sessions.Validate(r.Context(), c.Value)
		if err != nil {
			if errors.Is(err, session.ErrInvalid) {
				errJSON(w, http.StatusUnauthorized, "unauthorized", "sesión inválida o expirada")
				return
			}
			errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "base de datos no disponible")
			return
		}
		if sess.Kind != session.KindFull {
			// Sesión mfa_pending: solo válida para completar el segundo factor.
			errJSON(w, http.StatusUnauthorized, "unauthorized", "sesión pendiente de segundo factor")
			return
		}

		if isMutating(r.Method) && !validCSRF(r) {
			errJSON(w, http.StatusForbidden, "csrf", "token CSRF ausente o inválido")
			return
		}

		// Con contraseña provisional solo se permiten las rutas de cuenta
		// imprescindibles, hasta que el usuario fije la definitiva.
		if sess.MustChangePassword && !allowedWithPendingPassword(r) {
			errJSON(w, http.StatusForbidden, "password_change_required", "debes cambiar la contraseña antes de continuar")
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, sess)))
	}
}

// requireAdmin exige sesión válida con rol admin.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if sessionFrom(r).Role != "admin" {
			errJSON(w, http.StatusForbidden, "forbidden", "se requiere rol administrador")
			return
		}
		next(w, r)
	})
}

func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// validCSRF implementa el patrón de doble envío: la cookie legible y la
// cabecera X-CSRF-Token deben coincidir (comparación en tiempo constante).
func validCSRF(r *http.Request) bool {
	c, err := r.Cookie(cookieCSRF)
	if err != nil || c.Value == "" {
		return false
	}
	h := r.Header.Get("X-CSRF-Token")
	if h == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(h)) == 1
}

func allowedWithPendingPassword(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/auth/password", "/api/auth/me", "/api/auth/logout", "/api/auth/refresh":
		return true
	}
	return false
}
