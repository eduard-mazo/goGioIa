package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// embedTimeout limita cada llamada de embeddings (los lotes pueden tardar).
const embedTimeout = 120 * time.Second

// embedRequest es el payload del endpoint moderno /api/embed (acepta lotes).
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
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
// /api/embed y, si el host no lo soporta, recurre a /api/embeddings.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	body, err := json.Marshal(embedRequest{Model: model, Input: inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no se pudo contactar Ollama (embeddings): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return c.embedLegacy(ctx, model, inputs)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama embed devolvió %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
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
