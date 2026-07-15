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
	// RAGModel is the LLM used to answer questions with retrieved context.
	RAGModel string
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

	// ── PostgreSQL 16 (datos de aplicación: auth, sesiones, configuración,
	// uso de tokens, auditoría) ────────────────────────────────────────────
	PGHost     string
	PGPort     int
	PGUser     string
	PGPassword string
	PGDatabase string
	PGSSLMode  string
	// AdminEmail/AdminPassword siembran el usuario admin inicial en el primer
	// arranque. Si la contraseña queda vacía se genera una aleatoria y se
	// imprime una única vez en el log.
	AdminEmail    string
	AdminPassword string
}

// PGConnString arma el DSN de PostgreSQL en formato URL para pgx.
func (c Config) PGConnString() string {
	return "postgres://" + c.PGUser + ":" + c.PGPassword + "@" +
		c.PGHost + ":" + strconv.Itoa(c.PGPort) + "/" + c.PGDatabase +
		"?sslmode=" + c.PGSSLMode
}

// Defaults. Override any of these with the matching environment variable.
const (
	defaultOllamaAPI = "http://127.0.0.1:11434/api/chat"
	defaultWebPort   = ":8080"
	// Must match a model installed on the Ollama host (`ollama list`).
	// e.g. "llama3.1:latest", "mistral:latest", "qwen2.5:7b".
	defaultModelName = "llama3.1:latest"

	defaultEmbedModel   = "nomic-embed-text" // 768 dimensiones
	defaultRAGModel     = "mistral:latest"
	defaultRAGTopK      = 5
	defaultChunkSize    = 1800 // ~450 tokens por chunk
	defaultChunkOverlap = 250

	defaultOracleUser     = ""
	defaultOraclePassword = ""
	defaultOracleHost     = "127.0.0.1"
	defaultOraclePort     = 1521
	defaultOracleSID      = "orcl"

	// PostgreSQL 16. La contraseña NO tiene default a propósito (no debe
	// vivir en el repositorio): definir POSTGRES_PASSWORD en el entorno o
	// cargar deploy/dev.local.env en desarrollo.
	defaultPGHost     = "localhost"
	defaultPGPort     = 5432
	defaultPGUser     = "admin"
	defaultPGDatabase = "mydb"
	defaultPGSSLMode  = "disable"
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

		OracleUser:     env("ORACLE_USER", defaultOracleUser),
		OraclePassword: env("ORACLE_PASSWORD", defaultOraclePassword),
		OracleHost:     env("ORACLE_HOST", defaultOracleHost),
		OraclePort:     envInt("ORACLE_PORT", defaultOraclePort),
		OracleSID:      env("ORACLE_SID", defaultOracleSID),

		PGHost:        env("POSTGRES_HOST", defaultPGHost),
		PGPort:        envInt("POSTGRES_PORT", defaultPGPort),
		PGUser:        env("POSTGRES_USER", defaultPGUser),
		PGPassword:    env("POSTGRES_PASSWORD", ""),
		PGDatabase:    env("POSTGRES_DB", defaultPGDatabase),
		PGSSLMode:     env("POSTGRES_SSLMODE", defaultPGSSLMode),
		AdminEmail:    env("GOGIOIA_ADMIN_EMAIL", "admin@gogioia.local"),
		AdminPassword: env("GOGIOIA_ADMIN_PASSWORD", ""),
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
