// Package httpmw agrupa el middleware HTTP transversal de la aplicación:
// request-id, logging estructurado, recuperación de panics y cabeceras de
// seguridad. El orden recomendado (de fuera hacia dentro) es:
//
//	RequestID → Logging → Recover → SecureHeaders → router
package httpmw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 0

// Chain envuelve h con los middlewares dados, aplicando el primero como capa
// más externa.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ── Request ID ─────────────────────────────────────────────────────────────

// RequestID asigna un identificador único por request (UUID v4), disponible
// vía GetRequestID y devuelto en la cabecera X-Request-ID para correlacionar
// logs de cliente y servidor.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newUUIDv4()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// GetRequestID devuelve el id asignado por RequestID ("" si no hay).
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante RFC 4122
	dst := make([]byte, 36)
	hex.Encode(dst, b[:4])
	dst[8] = '-'
	hex.Encode(dst[9:], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], b[10:])
	return string(dst)
}

// ── Logging ────────────────────────────────────────────────────────────────

// statusWriter captura el código de estado sin romper el streaming SSE:
// reexpone Flush y Unwrap (para http.ResponseController).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Logging registra cada request de la API en formato estructurado. Las rutas
// de assets estáticos se omiten para no ensuciar el journal.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}
			sw := &statusWriter{ResponseWriter: w}
			start := time.Now()
			next.ServeHTTP(sw, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http",
				slog.String("request_id", GetRequestID(r.Context())),
				slog.String("ip", ClientIP(r)),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
		})
	}
}

// ClientIP extrae la IP del cliente. Solo se confía en X-Forwarded-For si el
// peer es local (reverse proxy en la misma máquina); si el proxy corre en
// otro host, ajustar aquí la lista de proxies confiables.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	return host
}

// ── Origin check ───────────────────────────────────────────────────────────

// OriginCheck rechaza peticiones mutantes a /api/ cuyo Origin no coincide con
// el Host servido (defensa CSRF complementaria al doble-submit de cookie).
// Las peticiones sin Origin (curl, scripts) se dejan pasar: la protección
// real para navegadores es la cabecera X-CSRF-Token.
func OriginCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
				if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
					http.Error(w, `{"error":"forbidden","message":"origin no permitido"}`, http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ── Recover ────────────────────────────────────────────────────────────────

// Recover convierte panics en 500 y los deja en el log con su request-id, en
// lugar de tumbar el proceso.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.LogAttrs(r.Context(), slog.LevelError, "panic recuperado",
						slog.String("request_id", GetRequestID(r.Context())),
						slog.String("path", r.URL.Path),
						slog.Any("panic", rec),
					)
					// Si ya se emitieron bytes (p. ej. SSE) no se puede
					// escribir la cabecera; el cliente verá el corte.
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ── Cabeceras de seguridad ─────────────────────────────────────────────────

// SecureHeaders aplica las cabeceras defensivas para la SPA embebida. La CSP
// permite solo recursos propios (todo el frontend está embebido en el
// binario); 'unsafe-inline' en estilos es necesario por los estilos en línea
// que genera Vue.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self'; font-src 'self' data:; "+
				"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
