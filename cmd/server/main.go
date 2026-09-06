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
	// Los flags se dejan vacíos para distinguir "sin indicar" de "igual al
	// default": la precedencia es flag > variable de entorno > archivo de
	// configuración > default.
	confPath := flag.String("config", "", "ruta del archivo de configuración (por defecto: gogioia.env o .env junto al ejecutable)")
	port := flag.String("port", "", "HTTP listen address (e.g. :8080 or 8080)")
	ollama := flag.String("ollama", "", "Ollama chat endpoint URL")
	model := flag.String("model", "", "default Ollama model name")
	flag.Parse()

	cfg := config.LoadFrom(*confPath)
	if cfg.ConfigFileError != "" {
		// Una ruta pedida a mano que no se puede leer es un error de
		// despliegue; el descubrimiento automático solo avisa.
		if *confPath != "" || os.Getenv(config.EnvFileVar) != "" {
			log.Fatalf("no se pudo leer el archivo de configuración: %s", cfg.ConfigFileError)
		}
		log.Printf("aviso: %s", cfg.ConfigFileError)
	}

	// Al sobrescribir por flag, se refleja la procedencia (página de
	// configuración del dashboard de operaciones).
	if *port != "" {
		cfg.Sources["WEB_PORT"] = "flag"
		cfg.WebPort = config.NormalizePort(*port)
	}
	if *ollama != "" {
		cfg.Sources["OLLAMA_API"] = "flag"
		cfg.OllamaAPI = *ollama
	}
	if *model != "" {
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
		if cfg.ConfigFile != "" {
			log.Printf("   config = %s", cfg.ConfigFile)
		} else {
			log.Printf("   config = (sin archivo; entorno y defaults)")
		}
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
