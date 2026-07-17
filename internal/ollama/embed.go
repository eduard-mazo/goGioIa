package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// embedTimeout limita cada intento de embeddings (los lotes pueden tardar).
const embedTimeout = 120 * time.Second

// embedAttempts reintenta fallos transitorios (connection reset, 5xx…): bajo
// carga sostenida el runner de Ollama puede reciclarse y cortar la conexión.
const embedAttempts = 3

// embedKeepAlive mantiene el modelo de embeddings cargado en el host: es
// pequeño (~centenares de MB) y recargarlo tras cada generación del LLM
// añadía segundos al primer embed (visto en producción: 6s vs 400ms).
const embedKeepAlive = "30m"

// embedRequest es el payload del endpoint moderno /api/embed (acepta lotes).
type embedRequest struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	KeepAlive string   `json:"keep_alive,omitempty"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

// legacyEmbedRequest es el payload del endpoint antiguo /api/embeddings
// (una entrada por llamada), usado como respaldo en hosts Ollama viejos.
type legacyEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type legacyEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error,omitempty"`
}

// Embed genera un embedding por cada texto de entrada usando el modelo dado
// (p.ej. nomic-embed-text → 768 dimensiones). Intenta el endpoint por lotes
// /api/embed y, si el host no lo soporta, recurre a /api/embeddings. Los
// fallos transitorios se reintentan con pausa creciente antes de rendirse.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: model, Input: inputs, KeepAlive: embedKeepAlive})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= embedAttempts; attempt++ {
		if attempt > 1 {
			// Pausa creciente (3s, 6s): da tiempo a que el runner se recupere.
			wait := time.Duration(attempt-1) * 3 * time.Second
			log.Printf("ollama: embed falló (%v); reintento %d/%d en %s", lastErr, attempt, embedAttempts, wait)
			select {
			case <-ctx.Done():
				return nil, lastErr
			case <-time.After(wait):
			}
		}
		vecs, retryable, err := c.embedOnce(ctx, model, inputs, body)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w (tras %d intentos)", lastErr, embedAttempts)
}

// embedOnce ejecuta un intento contra /api/embed. retryable indica si el
// fallo es plausiblemente transitorio (error de transporte o 5xx) y merece
// otro intento; los errores del API (payload inválido, modelo inexistente…)
// son definitivos.
func (c *Client) embedOnce(ctx context.Context, model string, inputs []string, body []byte) (vecs [][]float32, retryable bool, err error) {
	// Mismo semáforo que la generación: los embeddings de los trabajos de
	// fondo (ingesta, anexos) no deben saturar el host Ollama mientras un
	// usuario espera una respuesta. Se toma por intento: durante la pausa de
	// un reintento el hueco queda libre para el tráfico interactivo.
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("no se pudo contactar Ollama (embeddings): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		vecs, err := c.embedLegacy(ctx, model, inputs)
		return vecs, false, err
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode >= 500,
			fmt.Errorf("ollama embed devolvió %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		// La conexión también puede cortarse a mitad de la respuesta.
		return nil, true, fmt.Errorf("leer respuesta de embeddings: %w", err)
	}
	if er.Error != "" {
		return nil, false, fmt.Errorf("ollama embed: %s", er.Error)
	}
	if len(er.Embeddings) != len(inputs) {
		return nil, false, fmt.Errorf("ollama embed devolvió %d vectores para %d entradas", len(er.Embeddings), len(inputs))
	}
	return er.Embeddings, false, nil
}

// EmbedOne es un atajo para una única entrada (p.ej. la pregunta del usuario).
func (c *Client) EmbedOne(ctx context.Context, model, input string) ([]float32, error) {
	vecs, err := c.Embed(ctx, model, []string{input})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// embedLegacy llama /api/embeddings entrada por entrada.
func (c *Client) embedLegacy(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	out := make([][]float32, 0, len(inputs))
	for _, in := range inputs {
		body, err := json.Marshal(legacyEmbedRequest{Model: model, Prompt: in})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("no se pudo contactar Ollama (embeddings): %w", err)
		}
		var lr legacyEmbedResponse
		decErr := json.NewDecoder(resp.Body).Decode(&lr)
		resp.Body.Close()
		if decErr != nil {
			return nil, decErr
		}
		if lr.Error != "" {
			return nil, fmt.Errorf("ollama embeddings: %s", lr.Error)
		}
		out = append(out, lr.Embedding)
	}
	return out, nil
}
