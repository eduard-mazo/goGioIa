package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"goGioIa/internal/audit"
	"goGioIa/internal/auth"
	"goGioIa/internal/httpmw"
	"goGioIa/internal/session"
)

// reqMeta arma los metadatos de auditoría a partir del request.
func reqMeta(r *http.Request) auth.Meta {
	return auth.Meta{
		IP:        httpmw.ClientIP(r),
		UserAgent: r.UserAgent(),
		RequestID: httpmw.GetRequestID(r.Context()),
	}
}

// userPayload es la vista del usuario que consume el frontend.
func userPayload(u *auth.User) map[string]any {
	return map[string]any{
		"id":           u.ID,
		"email":        u.Email,
		"display_name": u.DisplayName,
		"role":         u.Role,
	}
}

// handleLogin autentica email+contraseña y emite la cookie de sesión.
//
//	POST /api/auth/login  {"email": "...", "password": "..."}
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := httpmw.ClientIP(r)
	if !s.loginLimiter.Allow(ip) {
		errJSON(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos, espera un momento")
		return
	}
	if err := s.pg.EnsureReady(r.Context()); err != nil {
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "base de datos no disponible")
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil ||
		req.Email == "" || req.Password == "" {
		errJSON(w, http.StatusBadRequest, "bad_request", "se requieren email y password")
		return
	}

	u, err := s.auth.Login(r.Context(), req.Email, req.Password, reqMeta(r))
	switch {
	case errors.Is(err, auth.ErrLocked):
		retry := int(time.Until(u.LockedUntil).Seconds())
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		errJSON(w, http.StatusLocked, "locked", "cuenta bloqueada temporalmente por intentos fallidos")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		errJSON(w, http.StatusUnauthorized, "invalid_credentials", "email o contraseña incorrectos")
		return
	case err != nil:
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo validar las credenciales")
		return
	}

	token, expires, err := s.sessions.Create(r.Context(), u.ID, session.KindFull, httpmw.ClientIP(r), r.UserAgent())
	if err != nil {
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo crear la sesión")
		return
	}
	setAuthCookies(w, r, token, expires)

	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 userPayload(u),
		"must_change_password": u.MustChangePassword,
		"expires_at":           expires.UTC().Format(time.RFC3339),
	})
}

// handleLogout revoca la sesión actual y limpia las cookies.
//
//	POST /api/auth/logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if _, err := s.sessions.Revoke(r.Context(), sess.ID, sess.UserID, "logout"); err != nil {
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo cerrar la sesión")
		return
	}
	m := reqMeta(r)
	s.auditor.Log(r.Context(), audit.Event{Type: "auth.logout",
		ActorID: sess.UserID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID})
	clearAuthCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe devuelve la identidad de la sesión actual.
//
//	GET /api/auth/me
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":           sess.UserID,
			"email":        sess.Email,
			"display_name": sess.DisplayName,
			"role":         sess.Role,
		},
		"must_change_password": sess.MustChangePassword,
		"expires_at":           sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleRefresh renueva la ventana de inactividad (Validate ya toca
// last_seen_at) y devuelve la expiración efectiva.
//
//	POST /api/auth/refresh
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"expires_at": sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleChangePassword fija una contraseña nueva y revoca las demás sesiones
// del usuario (la actual sobrevive).
//
//	POST /api/auth/password  {"current": "...", "new": "..."}
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil ||
		req.Current == "" || req.New == "" {
		errJSON(w, http.StatusBadRequest, "bad_request", "se requieren current y new")
		return
	}

	err := s.auth.ChangePassword(r.Context(), sess.UserID, req.Current, req.New, reqMeta(r))
	switch {
	case errors.Is(err, auth.ErrWeakPassword):
		errJSON(w, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		errJSON(w, http.StatusUnauthorized, "invalid_credentials", "la contraseña actual no es correcta")
		return
	case err != nil:
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo cambiar la contraseña")
		return
	}

	// Cerrar cualquier otra sesión abierta con la contraseña anterior.
	_, _ = s.sessions.RevokeAllForUser(r.Context(), sess.UserID, sess.ID, sess.UserID, "password_changed")
	w.WriteHeader(http.StatusNoContent)
}

// ── Cookies ────────────────────────────────────────────────────────────────

// secureCookies decide el flag Secure: solo si el request llegó por TLS
// (directo o vía proxy). El despliegue objetivo puede ser HTTP plano en
// intranet; con Secure incondicional el navegador descartaría la cookie.
func secureCookies(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func setAuthCookies(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	secure := secureCookies(r)
	http.SetCookie(w, &http.Cookie{
		Name: cookieSession, Value: token, Path: "/",
		Expires: expires, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	// Cookie CSRF legible por el frontend, que la reenvía en X-CSRF-Token.
	http.SetCookie(w, &http.Cookie{
		Name: cookieCSRF, Value: newCSRFToken(), Path: "/",
		Expires: expires, HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	secure := secureCookies(r)
	for _, name := range []string{cookieSession, cookieCSRF} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: name == cookieSession, Secure: secure, SameSite: http.SameSiteLaxMode,
		})
	}
}

func newCSRFToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
