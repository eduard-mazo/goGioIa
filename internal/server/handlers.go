package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"goGioIa/internal/ollama"
	"goGioIa/internal/pdf"
)

// maxUploadBytes caps the size of an uploaded PDF (32 MiB).
const maxUploadBytes = 32 << 20

// handleConfig returns non-secret runtime settings the frontend needs on boot.
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"model":  s.cfg.ModelName,
		"name":   "goGioIa",
		"ollama": s.cfg.OllamaAPI,
	})
}

// handleModels returns the models installed on the Ollama host plus the
// configured default. On failure it still responds 200 with an empty list so
// the UI degrades gracefully (as handleHealth does for reachability).
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.ollama.ListModels(r.Context())
	if err != nil {
		models = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  models,
		"default": s.cfg.ModelName,
	})
}

// handleHealth reports whether the configured Ollama endpoint is reachable.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "online"
	var detail string
	if err := s.ollama.Ping(r.Context()); err != nil {
		status = "offline"
		detail = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ollama": status,
		"detail": detail,
		"model":  s.cfg.ModelName,
	})
}

// chatRequest is the payload accepted from the frontend.
type chatRequest struct {
	Model    string           `json:"model"`
	Messages []ollama.Message `json:"messages"`
	Options  map[string]any   `json:"options,omitempty"`
}

// handleChat proxies a chat conversation to Ollama and re-emits the streamed
// tokens to the browser as Server-Sent Events.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages must not be empty", http.StatusBadRequest)
		return
	}

	model := req.Model
	if model == "" {
		model = s.cfg.ModelName
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported by server", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)

	body, err := s.ollama.Stream(r.Context(), model, req.Messages, req.Options)
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ollama.ChatChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue // skip malformed lines
		}
		if chunk.Error != "" {
			writeSSE(w, "error", map[string]string{"error": chunk.Error})
			flusher.Flush()
			return
		}
		if chunk.Message.Content != "" {
			writeSSE(w, "message", map[string]string{"content": chunk.Message.Content})
			flusher.Flush()
		}
		if chunk.Done {
			writeSSE(w, "done", map[string]bool{"done": true})
			flusher.Flush()
			return
		}
	}
	if err := scanner.Err(); err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
	}
}

// handlePDF accepts a multipart PDF upload and returns its extracted text.
//
// The upload is handled entirely in memory: the request body is capped by
// MaxBytesReader and ParseMultipartForm is given the same value as maxMemory,
// so the file part never spills to a temp file on disk. This keeps the
// container filesystem read-only and stateless (see deploy/Dockerfile.ppc64le).
func (s *Server) handlePDF(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "archivo demasiado grande o formulario inválido"})
		return
	}
	// Belt-and-suspenders: drop any temp files should a future change ever
	// raise the cap above the in-memory limit.
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "falta el campo 'file'"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no se pudo leer el archivo"})
		return
	}

	text, err := pdf.ExtractText(data)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filename": header.Filename,
		"chars":    len(text),
		"text":     text,
	})
}

// writeJSON serialises v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeSSE writes a single named Server-Sent Event with a JSON data payload.
func writeSSE(w io.Writer, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}
