// Package server wires the HTTP routes, serves the embedded SPA, and exposes
// the JSON/SSE API used by the frontend.
package server

import (
	"context"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"goGioIa/internal/audit"
	"goGioIa/internal/auth"
	"goGioIa/internal/config"
	"goGioIa/internal/httpmw"
	"goGioIa/internal/ollama"
	"goGioIa/internal/pgstore"
	"goGioIa/internal/rag"
	"goGioIa/internal/session"
	"goGioIa/internal/store"
	"goGioIa/web"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg    config.Config
	ollama *ollama.Client
	store  *store.Store
	pg     *pgstore.Store
	rag    *rag.Service
	dist   fs.FS
	index  []byte
	logger *slog.Logger

	auditor      *audit.Auditor
	auth         *auth.Service
	sessions     *session.Manager
	loginLimiter *httpmw.RateLimiter
}

// New constructs a Server, loading the embedded frontend assets.
func New(cfg config.Config) *Server {
	dist, err := web.DistFS()
	if err != nil {
		log.Fatalf("load embedded assets: %v", err)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		log.Printf("warning: embedded index.html missing (did you build the frontend?): %v", err)
	}
	ol := ollama.New(cfg.OllamaAPI)
	st, err := store.Open(cfg)
	if err != nil {
		log.Fatalf("open Oracle vector store: %v", err)
	}
	pg, err := pgstore.Open(cfg)
	if err != nil {
		log.Fatalf("open PostgreSQL app store: %v", err)
	}
	// Bootstrap de los esquemas en segundo plano: si Oracle/PostgreSQL están
	// caídos en el arranque, cada uso posterior reintenta vía EnsureReady.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := st.EnsureReady(ctx); err != nil {
			log.Printf("aviso: oracle no disponible aún: %v", err)
		}
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := pg.EnsureReady(ctx); err != nil {
			log.Printf("aviso: postgres no disponible aún: %v", err)
		}
	}()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	auditor := audit.New(pg.Pool(), logger)
	return &Server{
		cfg:      cfg,
		ollama:   ol,
		store:    st,
		pg:       pg,
		rag:      rag.New(cfg, st, ol),
		dist:     dist,
		index:    index,
		logger:   logger,
		auditor:  auditor,
		auth:     auth.NewService(pg.Pool(), pg, auditor),
		sessions: session.NewManager(pg),
		// 10 intentos de login por minuto y por IP.
		loginLimiter: httpmw.NewRateLimiter(10.0/60.0, 10),
	}
}

// Handler builds the application's HTTP router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Público: salud del servicio y login.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Cuenta del usuario autenticado.
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("POST /api/auth/refresh", s.requireAuth(s.handleRefresh))
	mux.HandleFunc("POST /api/auth/password", s.requireAuth(s.handleChangePassword))

	// Funcionalidad de chat: requiere sesión.
	mux.HandleFunc("GET /api/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("GET /api/models", s.requireAuth(s.handleModels))
	mux.HandleFunc("POST /api/chat", s.requireAuth(s.handleChat))
	mux.HandleFunc("POST /api/pdf", s.requireAuth(s.handlePDF))

	// RAG: consulta para usuarios; gestión de documentos solo admin.
	mux.HandleFunc("GET /api/rag/health", s.requireAuth(s.handleRagHealth))
	mux.HandleFunc("GET /api/rag/documents", s.requireAuth(s.handleRagDocuments))
	mux.HandleFunc("POST /api/rag/ask", s.requireAuth(s.handleRagAsk))
	mux.HandleFunc("POST /api/rag/feedback", s.requireAuth(s.handleRagFeedback))
	mux.HandleFunc("POST /api/rag/documents", s.requireAdmin(s.handleRagUpload))
	mux.HandleFunc("DELETE /api/rag/documents/{id}", s.requireAdmin(s.handleRagDeleteDocument))

	// Administración de usuarios y sesiones.
	mux.HandleFunc("GET /api/admin/users", s.requireAdmin(s.handleAdminListUsers))
	mux.HandleFunc("POST /api/admin/users", s.requireAdmin(s.handleAdminCreateUser))
	mux.HandleFunc("PATCH /api/admin/users/{id}", s.requireAdmin(s.handleAdminUpdateUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}/sessions", s.requireAdmin(s.handleAdminRevokeUserSessions))
	mux.HandleFunc("GET /api/admin/sessions", s.requireAdmin(s.handleAdminListSessions))
	mux.HandleFunc("DELETE /api/admin/sessions/{id}", s.requireAdmin(s.handleAdminRevokeSession))

	// Everything else is the SPA (static assets + client-side routes).
	mux.Handle("/", s.staticHandler())

	return httpmw.Chain(mux,
		httpmw.RequestID,
		httpmw.Logging(s.logger),
		httpmw.Recover(s.logger),
		httpmw.SecureHeaders,
		httpmw.OriginCheck,
	)
}

// staticHandler serves embedded files and falls back to index.html for
// client-side routes (SPA behaviour).
func (s *Server) staticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" {
			s.serveIndex(w)
			return
		}
		if f, err := s.dist.Open(clean); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		s.serveIndex(w)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	if s.index == nil {
		http.Error(w, "frontend not built — run `make build-web`", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.index)
}
