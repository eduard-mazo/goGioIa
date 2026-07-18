// Command server boots the goGioIa web application: an offline, self-contained
// chat UI (embedded assets) backed by a local Ollama instance.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goGioIa/internal/config"
	"goGioIa/internal/server"
)

func main() {
	// Flags default to the env/compile-time config, so precedence is
	// flag > environment variable > default.
	cfg := config.Load()
	port := flag.String("port", cfg.WebPort, "HTTP listen address (e.g. :8080 or 8080)")
	ollama := flag.String("ollama", cfg.OllamaAPI, "Ollama chat endpoint URL")
	model := flag.String("model", cfg.ModelName, "default Ollama model name")
	flag.Parse()

	// Al sobrescribir por flag, se refleja la procedencia (página de
	// configuración del dashboard de operaciones).
	if p := config.NormalizePort(*port); p != cfg.WebPort {
		cfg.Sources["WEB_PORT"] = "flag"
		cfg.WebPort = p
	}
	if *ollama != cfg.OllamaAPI {
		cfg.Sources["OLLAMA_API"] = "flag"
		cfg.OllamaAPI = *ollama
	}
	if *model != cfg.ModelName {
		cfg.Sources["MODEL_NAME"] = "flag"
		cfg.ModelName = *model
	}

	srv := server.New(cfg)

	httpServer := &http.Server{
		Addr:              cfg.WebPort,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("🚀 goGioIa listening on http://localhost%s", cfg.WebPort)
		log.Printf("   model = %s", cfg.ModelName)
		log.Printf("   ollama = %s", cfg.OllamaAPI)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
