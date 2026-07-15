// Command server boots the goGioIa web application: an offline, self-contained
// chat UI (embedded assets) backed by a local Ollama instance.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"goGioIa/internal/auth"
	"goGioIa/internal/config"
	"goGioIa/internal/server"
)

func main() {
	// Subcomandos utilitarios (mismo binario que el servidor):
	//   echo -n 'MiClave' | gogioia hash-password   → hash argon2id (PHC)
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		hashPasswordCmd()
		return
	}

	// Flags default to the env/compile-time config, so precedence is
	// flag > environment variable > default.
	cfg := config.Load()
	port := flag.String("port", cfg.WebPort, "HTTP listen address (e.g. :8080 or 8080)")
	ollama := flag.String("ollama", cfg.OllamaAPI, "Ollama chat endpoint URL")
	model := flag.String("model", cfg.ModelName, "default Ollama model name")
	flag.Parse()

	cfg.WebPort = config.NormalizePort(*port)
	cfg.OllamaAPI = *ollama
	cfg.ModelName = *model

	if cfg.PGPassword == "" {
		log.Printf("aviso: POSTGRES_PASSWORD no definida — PostgreSQL fallará al autenticar " +
			"(en desarrollo: set -a; . deploy/dev.local.env; set +a)")
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

// hashPasswordCmd lee una contraseña por stdin (evita dejarla en el historial
// del shell) e imprime su hash argon2id, útil para seeds manuales por SQL.
func hashPasswordCmd() {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		log.Fatalf("leer contraseña de stdin: %v", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if len(password) < 12 {
		log.Fatal("la contraseña debe tener al menos 12 caracteres")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("hashear: %v", err)
	}
	fmt.Println(hash)
}
