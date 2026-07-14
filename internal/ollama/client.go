// Package ollama is a minimal streaming client for the Ollama /api/chat endpoint.
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

// Message is a single turn in a chat conversation.
type Message struct {
	Role    string `json:"role"` // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// chatRequest is the payload sent to Ollama's /api/chat endpoint.
type chatRequest struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   json.RawMessage `json:"format,omitempty"`  // "json" or a JSON schema
	Options  map[string]any  `json:"options,omitempty"` // e.g. num_ctx, temperature
}

// ChatChunk is one NDJSON line emitted by Ollama while streaming.
type ChatChunk struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

// Client talks to a single Ollama chat endpoint.
type Client struct {
	endpoint string
	http     *http.Client
}

// New returns a Client for the given fully-qualified chat endpoint URL.
func New(endpoint string) *Client {
	return &Client{
		endpoint: endpoint,
		// No overall timeout: chat responses stream and may run for a while.
		http: &http.Client{},
	}
}

// Stream POSTs a chat request and returns the raw NDJSON response body.
// format (optional) constrains the output ("json" or a JSON schema); options
// (optional) carries model params like num_ctx and temperature. The caller is
// responsible for closing the returned reader.
func (c *Client) Stream(ctx context.Context, model string, msgs []Message, format json.RawMessage, options map[string]any) (io.ReadCloser, error) {
	body, err := json.Marshal(chatRequest{
		Model:    model,
		Messages: msgs,
		Stream:   true,
		Format:   format,
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

// tagsResponse mirrors the fields we need from Ollama's /api/tags response.
type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// ListModels returns the names of the models installed on the Ollama host.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned %s", resp.Status)
	}

	var tr tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tr.Models))
	for _, m := range tr.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return names, nil
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
