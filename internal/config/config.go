// Package config resolves runtime configuration from environment variables,
// falling back to sane compile-time defaults.
package config

import (
	"os"
	"strconv"
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
	// EmbedMaxTokens is the embedding model's context window (n_ctx_train);
	// inputs are validated against it and num_ctx is pinned to this value.
	EmbedMaxTokens int
	// EmbedBatchSize is how many chunks are sent per /api/embed call.
	EmbedBatchSize int
	// EmbedConcurrency caps simultaneous embedding calls (1 fits a 2 GiB GPU).
	EmbedConcurrency int
	// EmbedKeepAlive keeps the embeddings model loaded between calls ("10m").
	EmbedKeepAlive string
	// RAGModel is the LLM used to answer questions with retrieved context.
	RAGModel string
	// RAGNumCtx is the context window requested from the generation model.
	RAGNumCtx int
	// RAGTopK is how many chunks are retrieved per question.
	RAGTopK int
	// ChunkSize / ChunkOverlap control document chunking (in characters).
	ChunkSize    int
	ChunkOverlap int

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

	defaultEmbedModel       = "nomic-embed-text" // 768 dimensiones
	defaultEmbedMaxTokens   = 2048               // n_ctx_train de nomic-embed-text
	defaultEmbedBatch       = 8
	defaultEmbedConcurrency = 1 // GPU de 2 GiB: una llamada de embeddings a la vez
	defaultEmbedKeepAlive   = "10m"
	defaultRAGModel         = "mistral:latest"
	defaultRAGNumCtx        = 8192 // solo generación; el modelo de embeddings usa EmbedMaxTokens
	defaultRAGTopK          = 5
	defaultChunkSize        = 1800 // ~450 tokens por chunk
	defaultChunkOverlap     = 250

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

		EmbedModel:       env("EMBED_MODEL", defaultEmbedModel),
		EmbedMaxTokens:   envInt("EMBED_MAX_TOKENS", defaultEmbedMaxTokens),
		EmbedBatchSize:   envInt("EMBED_BATCH", defaultEmbedBatch),
		EmbedConcurrency: envInt("EMBED_CONCURRENCY", defaultEmbedConcurrency),
		EmbedKeepAlive:   env("EMBED_KEEP_ALIVE", defaultEmbedKeepAlive),
		RAGModel:         env("RAG_MODEL", defaultRAGModel),
		RAGNumCtx:        envInt("RAG_NUM_CTX", defaultRAGNumCtx),
		RAGTopK:          envInt("RAG_TOP_K", defaultRAGTopK),
		ChunkSize:        envInt("RAG_CHUNK_SIZE", defaultChunkSize),
		ChunkOverlap:     envInt("RAG_CHUNK_OVERLAP", defaultChunkOverlap),

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
