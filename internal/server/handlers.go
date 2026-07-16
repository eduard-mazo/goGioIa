package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"goGioIa/internal/ollama"
	"goGioIa/internal/rag"
	"goGioIa/internal/store"
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

// chatSystemPrompt fija el comportamiento del chat general (antes vivía en el
// frontend; server-side aplica igual para cualquier cliente del API).
const chatSystemPrompt = "Eres el asistente local de goGioIa. Responde en español, claro y al grano. " +
	"Usa Markdown cuando ayude (listas, tablas y bloques de código con su lenguaje)."

const (
	// maxDocContextChars limita el texto de adjuntos inyectado por petición.
	maxDocContextChars = 24000
	// maxChatHistoryMsgChars recorta cada mensaje del historial en el prompt.
	maxChatHistoryMsgChars = 6000
)

// chatRequest is the payload accepted from the frontend. Con conversationId
// el historial sale de Oracle y la conversación se persiste; history es el
// respaldo cuando la conversación no pudo crearse (Oracle caído).
type chatRequest struct {
	ConversationID string            `json:"conversationId,omitempty"`
	Message        string            `json:"message"`
	History        []ollama.Message  `json:"history,omitempty"`
	Documents      []rag.AttachedDoc `json:"documents,omitempty"`
	// Attachments son ids de anexos ya subidos a la conversación: el
	// contenido se resuelve server-side (completo o por retrieval).
	Attachments []string       `json:"attachments,omitempty"`
	Model       string         `json:"model,omitempty"`
	Options     map[string]any `json:"options,omitempty"`
}

// docContext construye el mensaje de sistema con los adjuntos del chat.
func docContext(docs []rag.AttachedDoc) string {
	var b strings.Builder
	b.WriteString("El usuario adjuntó los siguientes documentos. Úsalos como contexto al responder y cita el nombre del documento cuando sea relevante:\n")
	budget := maxDocContextChars
	for _, d := range docs {
		if budget <= 0 {
			break
		}
		text := d.Text
		if len(text) > budget {
			text = text[:budget] + "… (recortado)"
		}
		budget -= len(text)
		fmt.Fprintf(&b, "\n# Documento: %s\n\n%s\n", d.Name, text)
	}
	return b.String()
}

// trimHistory filtra el historial a turnos user/assistant no vacíos, se queda
// con los últimos `window` y recorta cada contenido a maxChars.
func trimHistory(history []ollama.Message, window, maxChars int) []ollama.Message {
	kept := make([]ollama.Message, 0, len(history))
	for _, m := range history {
		if (m.Role != "user" && m.Role != "assistant") || strings.TrimSpace(m.Content) == "" {
			continue
		}
		if len(m.Content) > maxChars {
			m.Content = m.Content[:maxChars] + "…"
		}
		kept = append(kept, m)
	}
	if window > 0 && len(kept) > window {
		kept = kept[len(kept)-window:]
	}
	return kept
}

// handleChat proxies a chat conversation to Ollama and re-emits the streamed
// tokens to the browser as Server-Sent Events. Si llega conversationId (y
// Oracle está disponible), el contexto se construye server-side y el turno
// queda persistido; si no, se degrada al historial enviado por el cliente.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	question := strings.TrimSpace(req.Message)
	if question == "" {
		http.Error(w, "message must not be empty", http.StatusBadRequest)
		return
	}

	model := req.Model
	if model == "" {
		model = s.cfg.ModelName
	}

	// Historial: de Oracle si hay conversación y el store está listo (Ready
	// no bloquea: con Oracle caído el chat sigue con el respaldo del cliente).
	history := req.History
	var convID []byte
	if req.ConversationID != "" && s.store.Ready() {
		if id, err := store.ParseID(req.ConversationID); err == nil {
			convID = id
			if stored, err := s.store.ConversationMessages(r.Context(), id, s.cfg.HistoryWindow); err == nil {
				history = history[:0]
				for _, m := range stored {
					history = append(history, ollama.Message{Role: m.Role, Content: m.Content})
				}
			} else {
				log.Printf("chat: no se pudo leer el historial: %v", err)
			}
		}
	}

	// Anexos referenciados por id: el contenido vive en Oracle (los grandes
	// se resuelven por retrieval con la pregunta).
	docs := append(s.rag.AttachmentContext(r.Context(), req.Attachments, question), req.Documents...)

	msgs := []ollama.Message{{Role: "system", Content: chatSystemPrompt}}
	if len(docs) > 0 {
		msgs = append(msgs, ollama.Message{Role: "system", Content: docContext(docs)})
	}
	msgs = append(msgs, trimHistory(history, s.cfg.HistoryWindow, maxChatHistoryMsgChars)...)
	msgs = append(msgs, ollama.Message{Role: "user", Content: question})

	options := req.Options
	if options == nil && len(docs) > 0 {
		// Con adjuntos hace falta más ventana o el modelo los descarta.
		options = map[string]any{"num_ctx": 8192, "temperature": 0.3}
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

	body, err := s.ollama.Stream(r.Context(), model, msgs, options)
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	defer body.Close()

	// Persistencia best-effort: la conversación no debe romper el chat.
	if convID != nil {
		if err := s.store.AppendMessage(r.Context(), convID, "user", question, nil); err != nil {
			log.Printf("chat: no se pudo persistir la pregunta: %v", err)
			convID = nil // sin la pregunta guardada, no guardar la respuesta suelta
		}
	}
	var answer strings.Builder
	defer func() {
		if convID == nil || answer.Len() == 0 {
			return
		}
		// La petición pudo abortarse: se persiste con contexto propio.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.store.AppendMessage(ctx, convID, "assistant", answer.String(), nil); err != nil {
			log.Printf("chat: no se pudo persistir la respuesta: %v", err)
		}
	}()

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
			answer.WriteString(chunk.Message.Content)
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

// handlePDF accepts a multipart upload (PDF or plain-text file) and returns
// its extracted text so the chat can use it as context.
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
	if !rag.SupportedFile(header.Filename) {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "tipo de archivo no admitido: " + rag.SupportedTypesMsg})
		return
	}

	text, err := rag.ExtractText(header.Filename, data)
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
