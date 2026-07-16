package server

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"goGioIa/internal/store"
)

// createConversationRequest da de alta una conversación server-side.
type createConversationRequest struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title,omitempty"`
	Mode      string `json:"mode,omitempty"`  // 'chat' | 'rag'
	Model     string `json:"model,omitempty"` // modelo elegido en la UI
}

// handleConversationCreate registra la conversación y devuelve su id. El
// frontend la crea de forma perezosa con el primer mensaje.
func (s *Server) handleConversationCreate(w http.ResponseWriter, r *http.Request) {
	var req createConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "chat"
	}
	if mode != "chat" && mode != "rag" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode inválido (chat|rag)"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if len(title) > 200 {
		title = title[:200]
	}
	if err := s.store.EnsureReady(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	id, err := s.store.CreateConversation(r.Context(), store.SessionID(req.SessionID), "", title, mode, req.Model)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": hex.EncodeToString(id)})
}

// handleConversationsList lista las conversaciones de una sesión.
func (s *Server) handleConversationsList(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "falta el parámetro sessionId"})
		return
	}
	if err := s.store.EnsureReady(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	convs, err := s.store.ListConversations(r.Context(), store.SessionID(sessionID))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

// handleConversationGet devuelve los mensajes persistidos de una conversación.
func (s *Server) handleConversationGet(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identificador inválido"})
		return
	}
	if err := s.store.EnsureReady(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	msgs, err := s.store.ConversationMessages(r.Context(), id, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// handleAttachmentUpload guarda un anexo (PDF o texto) en la conversación:
// el texto extraído queda en Oracle y los prompts lo referencian por id, sin
// reenviarlo en cada petición. Los anexos grandes se vectorizan en la cola.
func (s *Server) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
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

	id, chars, err := s.rag.IngestAttachment(r.Context(), r.PathValue("id"), header.Filename, data)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "ORA-02291") { // FK: conversación inexistente
			status = http.StatusNotFound
			err = errors.New("conversación no encontrada")
		} else if strings.Contains(err.Error(), "oracle no disponible") {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":       id,
		"fileName": header.Filename,
		"chars":    chars,
	})
}

// handleAttachmentDelete quita un anexo de la conversación.
func (s *Server) handleAttachmentDelete(w http.ResponseWriter, r *http.Request) {
	err := s.rag.DeleteAttachment(r.Context(), r.PathValue("id"), r.PathValue("attId"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "anexo no encontrado"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleConversationDelete elimina una conversación y sus mensajes.
func (s *Server) handleConversationDelete(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identificador inválido"})
		return
	}
	if err := s.store.EnsureReady(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err := s.store.DeleteConversation(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "conversación no encontrada"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
