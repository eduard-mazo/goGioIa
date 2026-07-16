// Package config resolves runtime configuration from environment variables,
// falling back to sane compile-time defaults.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds the application's runtime settings.
type Config struct {
	// OllamaAPI is the full URL of the Ollama chat endpoint.
	OllamaAPI string
	// WebPort is the address the HTTP server listens on (e.g. ":8080").
	WebPort string
	// ModelName is the default Ollama model used for chat completions.
	ModelName string

	// ── RAG ────────────────────────────────────────────────────────────────
	// EmbedModel is the Ollama embeddings model (768 dims → VECTOR(768)).
	EmbedModel string
	// RAGModel is the LLM used to answer questions with retrieved context.
	RAGModel string
	// RAGTopK is how many chunks are retrieved per question.
	RAGTopK int
	// ChunkSize / ChunkOverlap control document chunking (in characters).
	ChunkSize    int
	ChunkOverlap int
	// EmbedBatch is how many chunks are vectorised per Ollama /api/embed call.
	// Lower it if the Ollama host resets connections under sustained load.
	EmbedBatch int
	// RAGWorkers is how many queue workers process ingest jobs concurrently.
	RAGWorkers int
	// OllamaMaxConcurrent caps simultaneous generation (chat) calls to Ollama.
	OllamaMaxConcurrent int
	// HistoryRetentionDays purges rag_queries older than this (0 disables).
	HistoryRetentionDays int
	// HistoryWindow is how many stored conversation messages feed the prompt.
	HistoryWindow int
	// RAGCache enables the semantic answer cache (RAG_CACHE=0 disables).
	RAGCache bool
	// RAGCacheSim is the minimum cosine similarity to reuse a cached answer.
	RAGCacheSim float64
	// RAGCacheTTLHours expires cached answers after this many hours.
	RAGCacheTTLHours int
	// RAGDebug logs verbose diagnostics: every chunk stored at ingest and
	// every retrieved source per query (RAG_DEBUG=1 enables).
	RAGDebug bool

	// ── Oracle 23ai (vector store) ─────────────────────────────────────────
	OracleUser     string
	OraclePassword string
	OracleHost     string
	OraclePort     int
	OracleSID      string
}

// Defaults. Override any of these with the matching environment variable.
const (
	defaultOllamaAPI = "http://127.0.0.1:11434/api/chat"
	defaultWebPort   = ":8080"
	// Must match a model installed on the Ollama host (`ollama list`).
	// e.g. "llama3.1:latest", "mistral:latest", "qwen2.5:7b".
	defaultModelName = "llama3.1:latest"

	defaultEmbedModel           = "nomic-embed-text" // 768 dimensiones
	defaultRAGModel             = "mistral:latest"
	defaultRAGTopK              = 5
	defaultChunkSize            = 1800 // ~450 tokens por chunk
	defaultChunkOverlap         = 250
	defaultEmbedBatch           = 8
	defaultRAGWorkers           = 2
	defaultOllamaMaxConcurrent  = 2
	defaultHistoryRetentionDays = 180
	defaultHistoryWindow        = 12
	defaultRAGCacheSim          = 0.97 // conservador: mejor perder hits que responder mal
	defaultRAGCacheTTLHours     = 168  // una semana

	defaultOracleUser     = ""
	defaultOraclePassword = ""
	defaultOracleHost     = "127.0.0.1"
	defaultOraclePort     = 1521
	defaultOracleSID      = "orcl"
)

// Load builds a Config from the environment, applying defaults where unset.
func Load() Config {
	return Config{
		OllamaAPI: env("OLLAMA_API", defaultOllamaAPI),
		WebPort:   NormalizePort(env("WEB_PORT", defaultWebPort)),
		ModelName: env("MODEL_NAME", defaultModelName),

		EmbedModel:   env("EMBED_MODEL", defaultEmbedModel),
		RAGModel:     env("RAG_MODEL", defaultRAGModel),
		RAGTopK:      envInt("RAG_TOP_K", defaultRAGTopK),
		ChunkSize:    envInt("RAG_CHUNK_SIZE", defaultChunkSize),
		ChunkOverlap: envInt("RAG_CHUNK_OVERLAP", defaultChunkOverlap),
		EmbedBatch:   envInt("RAG_EMBED_BATCH", defaultEmbedBatch),
		RAGWorkers:   envInt("RAG_WORKERS", defaultRAGWorkers),

		OllamaMaxConcurrent:  envInt("OLLAMA_MAX_CONCURRENT", defaultOllamaMaxConcurrent),
		HistoryRetentionDays: envIntAllowZero("RAG_HISTORY_RETENTION_DAYS", defaultHistoryRetentionDays),
		HistoryWindow:        envInt("HISTORY_WINDOW", defaultHistoryWindow),
		RAGCache:             envBool("RAG_CACHE", true),
		RAGCacheSim:          envFloat("RAG_CACHE_THRESHOLD", defaultRAGCacheSim),
		RAGCacheTTLHours:     envInt("RAG_CACHE_TTL_HOURS", defaultRAGCacheTTLHours),
		RAGDebug:             envBool("RAG_DEBUG", false),

		OracleUser:     env("ORACLE_USER", defaultOracleUser),
		OraclePassword: env("ORACLE_PASSWORD", defaultOraclePassword),
		OracleHost:     env("ORACLE_HOST", defaultOracleHost),
		OraclePort:     envInt("ORACLE_PORT", defaultOraclePort),
		OracleSID:      env("ORACLE_SID", defaultOracleSID),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// envBool interpreta "0", "false" y "no" como falso; "1", "true", "yes" como
// verdadero; cualquier otra cosa deja el valor por defecto.
func envBool(key string, fallback bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	}
	return fallback
}

// envFloat admite valores en (0, 1] (umbral de similitud).
func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return fallback
}

// envIntAllowZero admite 0 como valor válido (p.ej. «desactivado»).
func envIntAllowZero(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// NormalizePort ensures the port string is prefixed with ":".
func NormalizePort(p string) string {
	if p == "" {
		return defaultWebPort
	}
	if p[0] != ':' {
		return ":" + p
	}
	return p
}
