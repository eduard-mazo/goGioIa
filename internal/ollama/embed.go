package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// embedTimeout limita cada llamada de embeddings, reintentos incluidos.
const embedTimeout = 120 * time.Second

// Backoff exponencial acotado con jitter (vars para acelerar los tests).
var (
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 8 * time.Second
)

// conservativeCharsPerToken es una estimación pesimista (≈3 caracteres por
// token) para no exceder nunca el contexto del modelo aunque su tokenizador
// real (WordPiece en nomic) sea más denso que la media de ≈4 chars/token.
const conservativeCharsPerToken = 3

// MaxInputChars devuelve el máximo de caracteres que puede tener una entrada
// para no superar maxTokens según la estimación conservadora.
func MaxInputChars(maxTokens int) int { return maxTokens * conservativeCharsPerToken }

// HTTPError es una respuesta no-2xx de Ollama con el cuerpo ya saneado
// (una línea, truncado) apto para registrarse en logs.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("ollama devolvió HTTP %d: %s", e.StatusCode, e.Body)
}

// IsInputTooLarge reconoce un 400 causado por el tamaño de la entrada.
func (e *HTTPError) IsInputTooLarge() bool {
	if e.StatusCode != http.StatusBadRequest {
		return false
	}
	b := strings.ToLower(e.Body)
	for _, marker := range []string{"context length", "context size", "input length", "too large", "exceeds", "maximum"} {
		if strings.Contains(b, marker) {
			return true
		}
	}
	return false
}

// ValidationError señala una entrada rechazada antes de llamar a Ollama.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "entrada de embedding inválida: " + e.Reason }

// embedRequest es el payload del endpoint moderno /api/embed (acepta lotes).
type embedRequest struct {
	Model     string         `json:"model"`
	Input     []string       `json:"input"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

// legacyEmbedRequest es el payload del endpoint antiguo /api/embeddings
// (una entrada por llamada), usado como respaldo en hosts Ollama viejos.
type legacyEmbedRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
}

type legacyEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error,omitempty"`
}

// embedOptions fija num_ctx explícitamente: anula cualquier Modelfile o
// variable de entorno del host que pida más contexto del que el modelo
// soporta (p.ej. num_ctx=8192 sobre n_ctx_train=2048).
func (c *Client) embedOptions() map[string]any {
	return map[string]any{"num_ctx": c.embedMaxTokens}
}

// Embed genera un embedding por cada texto de entrada usando el modelo dado
// (p.ej. nomic-embed-text → 768 dimensiones). Valida las entradas contra el
// límite de contexto, serializa las llamadas según la concurrencia
// configurada y reintenta solo fallos transitorios. Intenta el endpoint por
// lotes /api/embed y, si el host no lo soporta, recurre a /api/embeddings.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	maxChars := MaxInputChars(c.embedMaxTokens)
	for i, in := range inputs {
		if strings.TrimSpace(in) == "" {
			return nil, &ValidationError{Reason: fmt.Sprintf("la entrada %d está vacía", i)}
		}
		if len(in) > maxChars {
			return nil, &ValidationError{Reason: fmt.Sprintf(
				"la entrada %d (%d caracteres, ~%d tokens) supera el límite de %d tokens del modelo",
				i, len(in), len(in)/conservativeCharsPerToken, c.embedMaxTokens)}
		}
	}

	// Semáforo de embeddings: en GPUs pequeñas una llamada a la vez.
	select {
	case c.embedSem <- struct{}{}:
		defer func() { <-c.embedSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	payload, err := json.Marshal(embedRequest{
		Model:     model,
		Input:     inputs,
		Options:   c.embedOptions(),
		KeepAlive: c.embedKeepAlive,
	})
	if err != nil {
		return nil, err
	}

	respBody, err := c.postWithRetry(ctx, c.baseURL()+"/api/embed", payload, len(inputs))
	if err != nil {
		if httpErr, ok := errors.AsType[*HTTPError](err); ok && httpErr.StatusCode == http.StatusNotFound {
			return c.embedLegacy(ctx, model, inputs)
		}
		return nil, err
	}

	var er embedResponse
	if err := json.Unmarshal(respBody, &er); err != nil {
		return nil, err
	}
	if er.Error != "" {
		return nil, fmt.Errorf("ollama embed: %s", er.Error)
	}
	if len(er.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama embed devolvió %d vectores para %d entradas", len(er.Embeddings), len(inputs))
	}
	return er.Embeddings, nil
}

// EmbedOne es un atajo para una única entrada (p.ej. la pregunta del usuario).
func (c *Client) EmbedOne(ctx context.Context, model, input string) ([]float32, error) {
	vecs, err := c.Embed(ctx, model, []string{input})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// WarmEmbed precarga el modelo de embeddings y comprueba que responde de
// verdad: un GET / al host no demuestra que el runner del modelo esté listo.
func (c *Client) WarmEmbed(ctx context.Context, model string) error {
	vecs, err := c.Embed(ctx, model, []string{"warmup"})
	if err != nil {
		return fmt.Errorf("precalentar modelo de embeddings %q: %w", model, err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return fmt.Errorf("precalentar modelo de embeddings %q: respuesta sin vector", model)
	}
	return nil
}

// postWithRetry ejecuta el POST recreando el cuerpo en cada intento.
// Reintenta solo fallos transitorios (reset/EOF/timeout de red y HTTP
// 429/502/503/504) con backoff exponencial acotado y jitter. Un HTTP 400
// nunca se reintenta: se devuelve tipado con su cuerpo saneado.
func (c *Client) postWithRetry(ctx context.Context, url string, payload []byte, batchSize int) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		start := time.Now()
		respBody, err := c.postOnce(ctx, url, payload)
		latency := time.Since(start).Round(time.Millisecond)
		if err == nil {
			if attempt > 1 {
				log.Printf("ollama embed: recuperado en el intento %d (lote=%d bytes=%d latencia=%s)",
					attempt, batchSize, len(payload), latency)
			}
			return respBody, nil
		}
		lastErr = err

		if httpErr, ok := errors.AsType[*HTTPError](err); ok {
			if !retryableStatus(httpErr.StatusCode) {
				// 404 = negociación del endpoint legacy; no es un fallo.
				if httpErr.StatusCode != http.StatusNotFound {
					log.Printf("ollama embed: HTTP %d sin reintento (lote=%d bytes=%d latencia=%s intento=%d cuerpo=%q)",
						httpErr.StatusCode, batchSize, len(payload), latency, attempt, httpErr.Body)
				}
				return nil, err
			}
		} else if !isTransient(err) {
			return nil, err
		}
		if attempt == c.maxAttempts {
			break
		}
		delay := backoffDelay(attempt)
		log.Printf("ollama embed: intento %d/%d falló (%s); reintento en %s (lote=%d bytes=%d latencia=%s)",
			attempt, c.maxAttempts, sanitizeText(lastErr.Error()), delay, batchSize, len(payload), latency)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("ollama embed: agotados %d intentos: %w", c.maxAttempts, lastErr)
}

// postOnce realiza un único intento y garantiza el cierre del cuerpo.
func (c *Client) postOnce(ctx context.Context, url string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: sanitizeText(string(body))}
	}
	return body, nil
}

// retryableStatus: códigos HTTP que merecen reintento.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// isTransient reconoce errores de transporte recuperables. Cancelación o
// deadline del contexto no lo son: el presupuesto de la llamada se agotó.
func isTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") || strings.Contains(s, "broken pipe")
}

// backoffDelay: exponencial con techo y jitter ±50 % (evita sincronizar reintentos).
func backoffDelay(attempt int) time.Duration {
	d := min(retryBaseDelay<<(attempt-1), retryMaxDelay)
	return d/2 + time.Duration(rand.Int64N(int64(d)))
}

// sanitizeText deja un texto apto para logs: una sola línea y truncado.
func sanitizeText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 512 {
		s = s[:512] + "…"
	}
	return s
}

// embedLegacy llama /api/embeddings entrada por entrada (hosts antiguos).
func (c *Client) embedLegacy(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	out := make([][]float32, 0, len(inputs))
	for _, in := range inputs {
		payload, err := json.Marshal(legacyEmbedRequest{
			Model:     model,
			Prompt:    in,
			Options:   c.embedOptions(),
			KeepAlive: c.embedKeepAlive,
		})
		if err != nil {
			return nil, err
		}
		respBody, err := c.postWithRetry(ctx, c.baseURL()+"/api/embeddings", payload, 1)
		if err != nil {
			return nil, err
		}
		var lr legacyEmbedResponse
		if err := json.Unmarshal(respBody, &lr); err != nil {
			return nil, err
		}
		if lr.Error != "" {
			return nil, fmt.Errorf("ollama embeddings: %s", lr.Error)
		}
		out = append(out, lr.Embedding)
	}
	return out, nil
}
