// Package server wires the HTTP routes, serves the embedded SPA, and exposes
// the JSON/SSE API used by the frontend.
package server

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"goGioIa/internal/config"
	"goGioIa/internal/ollama"
	"goGioIa/internal/rag"
	"goGioIa/internal/store"
	"goGioIa/web"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg    config.Config
	ollama *ollama.Client
	store  *store.Store
	rag    *rag.Service
	dist   fs.FS
	index  []byte
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
	// Bootstrap del esquema en segundo plano: si Oracle está caído en el
	// arranque, cada petición RAG lo reintenta vía EnsureReady.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := st.EnsureReady(ctx); err != nil {
			log.Printf("aviso: oracle no disponible aún: %v", err)
		}
	}()
	return &Server{
		cfg:    cfg,
		ollama: ol,
		store:  st,
		rag:    rag.New(cfg, st, ol),
		dist:   dist,
		index:  index,
	}
}

// Handler builds the application's HTTP router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/pdf", s.handlePDF)

	// RAG: base de conocimiento en Oracle 23ai + asistente con retrieval.
	mux.HandleFunc("GET /api/rag/health", s.handleRagHealth)
	mux.HandleFunc("GET /api/rag/documents", s.handleRagDocuments)
	mux.HandleFunc("POST /api/rag/documents", s.handleRagUpload)
	mux.HandleFunc("DELETE /api/rag/documents/{id}", s.handleRagDeleteDocument)
	mux.HandleFunc("POST /api/rag/ask", s.handleRagAsk)
	mux.HandleFunc("POST /api/rag/feedback", s.handleRagFeedback)

	// Everything else is the SPA (static assets + client-side routes).
	mux.Handle("/", s.staticHandler())

	return withLogging(mux)
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

// withLogging is a tiny request-logging middleware.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
