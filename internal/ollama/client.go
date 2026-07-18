// Package ollama is a minimal streaming client for the Ollama /api/chat endpoint.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Message is a single turn in a chat conversation.
type Message struct {
	Role    string `json:"role"` // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// chatRequest is the payload sent to Ollama's /api/chat endpoint.
type chatRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"` // e.g. num_ctx, temperature
}

// ChatChunk is one NDJSON line emitted by Ollama while streaming. The final
// line (done=true) carries the authoritative token counts and timings that
// feed the operations dashboard; intermediate lines leave them at zero.
type ChatChunk struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`

	Model           string `json:"model,omitempty"`
	DoneReason      string `json:"done_reason,omitempty"`
	TotalDuration   int64  `json:"total_duration,omitempty"` // ns
	LoadDuration    int64  `json:"load_duration,omitempty"`  // ns
	PromptEvalCount int    `json:"prompt_eval_count,omitempty"`
	EvalCount       int    `json:"eval_count,omitempty"`
	EvalDuration    int64  `json:"eval_duration,omitempty"` // ns
}

// Ajustes por defecto del cliente; se cambian con las Option de New.
const (
	defaultEmbedMaxTokens   = 2048 // n_ctx_train de nomic-embed-text
	defaultEmbedConcurrency = 1    // una llamada de embeddings a la vez
	defaultMaxAttempts      = 4    // intentos por llamada de embeddings
)

// Event describe una llamada de embeddings ya terminada, para que el
// observador configurado con WithRecorder la persista (dashboard de
// operaciones). El cliente no conoce Oracle: solo emite el hecho.
type Event struct {
	Op           string        // "embed"
	Purpose      string        // "ingest" | "query" | "warmup" (WithPurpose); "" si no se anotó
	Ref          []byte        // id correlacionado (WithRef), p.ej. document_id
	Model        string
	OK           bool
	HTTPStatus   int           // 0 si el fallo fue de transporte
	ErrorKind    string        // ver ClassifyError
	Error        string        // mensaje saneado (una línea, truncado)
	Latency      time.Duration // total, reintentos incluidos
	QueueWait    time.Duration // espera en el semáforo de concurrencia
	Attempts     int
	BatchSize    int
	PayloadBytes int
	TokensIn     int           // prompt_eval_count de Ollama (autoritativo); 0 si no llegó
	LoadDuration time.Duration // >0 delata una (re)carga del modelo en esta llamada
}

// Recorder recibe los eventos de embeddings. Debe ser rápido y no bloquear.
type Recorder func(Event)

// Client talks to a single Ollama chat endpoint.
type Client struct {
	endpoint       string
	http           *http.Client
	embedSem       chan struct{} // limita las llamadas de embeddings simultáneas
	maxAttempts    int
	embedMaxTokens int
	embedKeepAlive string
	record         Recorder
}

// Option ajusta el comportamiento del cliente al construirlo.
type Option func(*Client)

// WithEmbedConcurrency limita cuántas llamadas de embeddings corren a la vez
// (1 por defecto: adecuado para GPUs con poca VRAM).
func WithEmbedConcurrency(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.embedSem = make(chan struct{}, n)
		}
	}
}

// WithEmbedMaxTokens fija el contexto del modelo de embeddings: las entradas
// que lo superan se rechazan y num_ctx se envía con este valor.
func WithEmbedMaxTokens(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.embedMaxTokens = n
		}
	}
}

// WithEmbedKeepAlive controla cuánto mantiene Ollama cargado el modelo de
// embeddings entre llamadas (formato Ollama, p.ej. "10m").
func WithEmbedKeepAlive(v string) Option {
	return func(c *Client) { c.embedKeepAlive = v }
}

// WithMaxAttempts fija el nº máximo de intentos por llamada de embeddings.
func WithMaxAttempts(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.maxAttempts = n
		}
	}
}

// WithRecorder registra un observador de eventos de embeddings.
func WithRecorder(r Recorder) Option {
	return func(c *Client) { c.record = r }
}

// Claves de contexto para atribuir las llamadas de embeddings en las métricas.
type ctxKey int

const (
	ctxKeyPurpose ctxKey = iota
	ctxKeyRef
)

// WithPurpose etiqueta las llamadas de embeddings hechas con este contexto
// ("ingest", "query", "warmup") para atribuirlas en las métricas.
func WithPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, ctxKeyPurpose, purpose)
}

// WithRef correlaciona las llamadas de embeddings con un identificador de
// negocio (p.ej. el document_id de una ingesta).
func WithRef(ctx context.Context, ref []byte) context.Context {
	return context.WithValue(ctx, ctxKeyRef, ref)
}

func ctxPurpose(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyPurpose).(string); ok {
		return v
	}
	return ""
}

func ctxRef(ctx context.Context) []byte {
	if v, ok := ctx.Value(ctxKeyRef).([]byte); ok {
		return v
	}
	return nil
}

// MaxAttempts expone el tope de intentos por llamada de embeddings (métricas
// de agotamiento de reintentos y página de configuración).
func (c *Client) MaxAttempts() int { return c.maxAttempts }

// New returns a Client for the given fully-qualified chat endpoint URL.
func New(endpoint string, opts ...Option) *Client {
	c := &Client{
		endpoint: endpoint,
		// Un único cliente con pool de conexiones y timeouts explícitos.
		// Sin timeout global: el chat es streaming de larga duración; los
		// embeddings acotan su duración por contexto en cada llamada.
		http: &http.Client{Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// Cargar un modelo en frío puede tardar minutos en GPUs pequeñas;
			// las cabeceras llegan solo cuando el runner está listo.
			ResponseHeaderTimeout: 5 * time.Minute,
		}},
		embedSem:       make(chan struct{}, defaultEmbedConcurrency),
		maxAttempts:    defaultMaxAttempts,
		embedMaxTokens: defaultEmbedMaxTokens,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Stream POSTs a chat request and returns the raw NDJSON response body.
// options (optional) carries model params like num_ctx and temperature. The
// caller is responsible for closing the returned reader.
func (c *Client) Stream(ctx context.Context, model string, msgs []Message, options map[string]any) (io.ReadCloser, error) {
	body, err := json.Marshal(chatRequest{
		Model:    model,
		Messages: msgs,
		Stream:   true,
		Options:  options,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Ollama at %s: %w", c.endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return resp.Body, nil
}

// Ping performs a lightweight reachability check against the Ollama host root.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ModelInfo es un modelo instalado en el host (/api/tags).
type ModelInfo struct {
	Name          string `json:"name"`
	Digest        string `json:"digest"`
	SizeBytes     int64  `json:"sizeBytes"`
	ParameterSize string `json:"parameterSize"`
	Quantization  string `json:"quantization"`
}

// tagsResponse mirrors the fields we need from Ollama's /api/tags response.
type tagsResponse struct {
	Models []struct {
		Name    string `json:"name"`
		Digest  string `json:"digest"`
		Size    int64  `json:"size"`
		Details struct {
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

// Tags returns the models installed on the Ollama host with their digests.
func (c *Client) Tags(ctx context.Context) ([]ModelInfo, error) {
	var tr tagsResponse
	if err := c.getJSON(ctx, "/api/tags", &tr); err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(tr.Models))
	for _, m := range tr.Models {
		if m.Name == "" {
			continue
		}
		models = append(models, ModelInfo{
			Name:          m.Name,
			Digest:        m.Digest,
			SizeBytes:     m.Size,
			ParameterSize: m.Details.ParameterSize,
			Quantization:  m.Details.QuantizationLevel,
		})
	}
	return models, nil
}

// ListModels returns the names of the models installed on the Ollama host.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	models, err := c.Tags(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Name)
	}
	return names, nil
}

// RunningModel es un modelo cargado ahora mismo en el host (/api/ps).
type RunningModel struct {
	Name      string    `json:"name"`
	Digest    string    `json:"digest"`
	SizeVRAM  int64     `json:"sizeVram"`
	SizeBytes int64     `json:"sizeBytes"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// psResponse mirrors the fields we need from Ollama's /api/ps response.
type psResponse struct {
	Models []struct {
		Name      string    `json:"name"`
		Digest    string    `json:"digest"`
		Size      int64     `json:"size"`
		SizeVRAM  int64     `json:"size_vram"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"models"`
}

// ListRunning returns the models currently loaded on the Ollama host. On a
// 2 GiB GPU this is the direct signal for model-eviction diagnosis: the
// embeddings model disappearing between calls means it is being evicted.
func (c *Client) ListRunning(ctx context.Context) ([]RunningModel, error) {
	var pr psResponse
	if err := c.getJSON(ctx, "/api/ps", &pr); err != nil {
		return nil, err
	}
	models := make([]RunningModel, 0, len(pr.Models))
	for _, m := range pr.Models {
		models = append(models, RunningModel{
			Name:      m.Name,
			Digest:    m.Digest,
			SizeVRAM:  m.SizeVRAM,
			SizeBytes: m.Size,
			ExpiresAt: m.ExpiresAt,
		})
	}
	return models, nil
}

// getJSON GETs a host-root endpoint and decodes the JSON response.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// baseURL strips the /api/... path from the configured endpoint, leaving the
// Ollama host root (scheme + host[:port]).
func (c *Client) baseURL() string {
	base := c.endpoint
	if i := strings.Index(base, "/api/"); i >= 0 {
		base = base[:i]
	}
	return base
}
