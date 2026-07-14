// Package config resolves runtime configuration from environment variables,
// falling back to sane compile-time defaults.
package config

import "os"

// Config holds the application's runtime settings.
type Config struct {
	// OllamaAPI is the full URL of the Ollama chat endpoint.
	OllamaAPI string
	// WebPort is the address the HTTP server listens on (e.g. ":8080").
	WebPort string
	// ModelName is the default Ollama model used for chat completions.
	ModelName string
}

// Defaults. Override any of these with the matching environment variable.
const (
	defaultOllamaAPI = "http://127.0.0.1:11434/api/chat"
	defaultWebPort   = ":8080"
	// Must match a model installed on the Ollama host (`ollama list`).
	// e.g. "llama3.1:latest", "mistral:latest", "qwen2.5:7b".
	defaultModelName = "llama3.1:latest"
)

// Load builds a Config from the environment, applying defaults where unset.
func Load() Config {
	return Config{
		OllamaAPI: env("OLLAMA_API", defaultOllamaAPI),
		WebPort:   NormalizePort(env("WEB_PORT", defaultWebPort)),
		ModelName: env("MODEL_NAME", defaultModelName),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
