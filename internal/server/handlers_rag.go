package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"goGioIa/internal/ollama"
	"goGioIa/internal/rag"
	"goGioIa/internal/store"
)

// handleRagHealth reporta el estado del vector store (Oracle 23ai) y del RAG.
func (s *Server) handleRagHealth(w http.ResponseWriter, r *http.Request) {
	status := "online"
	var detail string
	docs, chunks := 0, 0
	if err := s.store.EnsureReady(r.Context()); err != nil {
		status = "offline"
		detail = err.Error()
	} else if d, c, err := s.store.Stats(r.Context()); err == nil {
		docs, chunks = d, c
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"oracle":     status,
		"detail":     detail,
		"documents":  docs,
		"chunks":     chunks,
		"embedModel": s.cfg.EmbedModel,
		"ragModel":   s.cfg.RAGModel,
	})
}

// handleRagUpload recibe un documento (PDF o archivo de texto), lo registra
// en Oracle y lanza el pipeline de entrenamiento (extracción → chunking →
// embeddings) en segundo plano.
func (s *Server) handleRagUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "archivo demasiado grande o formulario inválido"})
		return
	}
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
	if !rag.SupportedFile(header.Filename) {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "tipo de archivo no admitido: " + rag.SupportedTypesMsg})
		return
	}

	uploadedBy := strings.TrimSpace(r.FormValue("uploadedBy"))
	id, err := s.rag.IngestAsync(r.Context(), header.Filename, data, uploadedBy)
	if err != nil {
		if dup, ok := errors.AsType[*rag.ErrDuplicate](err); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":    "este documento ya está en la base de conocimiento",
				"document": dup.Doc,
			})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":       id,
		"fileName": header.Filename,
		"status":   store.StatusUploaded,
	})
}

// handleRagDocuments lista los documentos de la base de conocimiento.
func (s *Server) handleRagDocuments(w http.ResponseWriter, r *http.Request) {
	if err := s.store.EnsureReady(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	docs, err := s.store.ListDocuments(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": docs})
}

// handleRagDeleteDocument elimina un documento y sus chunks del vector store.
func (s *Server) handleRagDeleteDocument(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identificador inválido"})
		return
	}
	if err := s.store.EnsureReady(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err := s.store.DeleteDocument(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "documento no encontrado"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// askRequest es la pregunta que envía el frontend al asistente RAG. Puede
// incluir los documentos adjuntos a la conversación para usarlos como
// contexto adicional junto a la base de conocimiento.
type askRequest struct {
	Question  string            `json:"question"`
	Model     string            `json:"model,omitempty"`
	SessionID string            `json:"sessionId,omitempty"`
	UserID    string            `json:"userId,omitempty"`
	Documents []rag.AttachedDoc `json:"documents,omitempty"`
}

// handleRagAsk responde una pregunta con RAG: embebe la consulta, recupera
// contexto desde Oracle 23ai y genera con el LLM (Mistral por defecto),
// re-emitiendo los tokens como SSE. Eventos: sources → message* → done.
func (s *Server) handleRagAsk(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported by server", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	prep, err := s.rag.PrepareAsk(r.Context(), req.Question, req.Model, req.SessionID, req.UserID, req.Documents)
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}

	// Las fuentes van primero: la UI las muestra mientras el modelo escribe.
	writeSSE(w, "sources", map[string]any{
		"queryId": prep.QueryID,
		"sources": sourcesForUI(prep.Sources),
	})
	flusher.Flush()

	body, err := s.ollama.Stream(r.Context(), prep.Model, prep.Messages, prep.Options)
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	defer body.Close()

	var answer strings.Builder
	finish := func() {
		// La petición pudo abortarse: se persiste con contexto propio.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if answer.Len() > 0 {
			if err := s.rag.FinishAsk(ctx, prep.QueryID, answer.String()); err != nil {
				log.Printf("rag: no se pudo guardar la respuesta: %v", err)
			}
		}
	}
	defer finish()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ollama.ChatChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
		if chunk.Error != "" {
			writeSSE(w, "error", map[string]string{"error": chunk.Error})
			flusher.Flush()
			return
		}
		if chunk.Message.Content != "" {
			answer.WriteString(chunk.Message.Content)
			writeSSE(w, "message", map[string]string{"content": chunk.Message.Content})
			flusher.Flush()
		}
		if chunk.Done {
			writeSSE(w, "done", map[string]any{"done": true, "queryId": prep.QueryID})
			flusher.Flush()
			return
		}
	}
	if err := scanner.Err(); err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
	}
}

// uiSource es la vista compacta de un chunk recuperado que consume la UI.
type uiSource struct {
	ChunkID  string  `json:"chunkId"`
	FileName string  `json:"fileName"`
	Page     int     `json:"page"`
	Score    float64 `json:"score"`
	Snippet  string  `json:"snippet"`
}

const snippetChars = 280

func sourcesForUI(results []store.SearchResult) []uiSource {
	out := make([]uiSource, 0, len(results))
	for _, r := range results {
		snippet := r.Text
		if len(snippet) > snippetChars {
			snippet = snippet[:snippetChars] + "…"
		}
		out = append(out, uiSource{
			ChunkID:  r.ChunkID,
			FileName: r.FileName,
			Page:     r.PageNumber,
			Score:    r.Similarity,
			Snippet:  snippet,
		})
	}
	return out
}

// feedbackRequest es la valoración de una respuesta del asistente.
type feedbackRequest struct {
	QueryID   string `json:"queryId"`
	Rating    int    `json:"rating"` // -1 | 0 | 1
	Comment   string `json:"comment,omitempty"`
	CreatedBy string `json:"createdBy,omitempty"`
}

// handleRagFeedback registra la valoración en rag_feedback.
func (s *Server) handleRagFeedback(w http.ResponseWriter, r *http.Request) {
	var req feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := s.rag.Feedback(r.Context(), req.QueryID, req.Rating, req.Comment, req.CreatedBy); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}
