package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"goGioIa/internal/audit"
	"goGioIa/internal/auth"
)

// handleAdminListUsers lista todos los usuarios.
//
//	GET /api/admin/users
func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo listar usuarios")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// handleAdminCreateUser da de alta un usuario con contraseña provisional.
//
//	POST /api/admin/users  {"email","display_name","password","role"}
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "bad_request", "cuerpo JSON inválido")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}

	sess := sessionFrom(r)
	u, err := s.auth.CreateUser(r.Context(), req.Email, req.DisplayName, req.Password, req.Role, sess.UserID, reqMeta(r))
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		errJSON(w, http.StatusConflict, "email_taken", "el email ya está registrado")
		return
	case errors.Is(err, auth.ErrWeakPassword):
		errJSON(w, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	case err != nil:
		errJSON(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": u})
}

// handleAdminUpdateUser cambia estado y/o rol de un usuario.
//
//	PATCH /api/admin/users/{id}  {"status"?: "active|disabled", "role"?: "admin|user"}
func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := sessionFrom(r)
	if id == sess.UserID {
		errJSON(w, http.StatusBadRequest, "bad_request", "no puedes modificar tu propia cuenta desde aquí")
		return
	}

	var req struct {
		Status *string `json:"status"`
		Role   *string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "bad_request", "cuerpo JSON inválido")
		return
	}

	err := s.auth.UpdateUser(r.Context(), id, req.Status, req.Role, sess.UserID, reqMeta(r))
	switch {
	case errors.Is(err, auth.ErrNotFound):
		errJSON(w, http.StatusNotFound, "not_found", "usuario no existe")
		return
	case errors.Is(err, auth.ErrLastAdmin):
		errJSON(w, http.StatusConflict, "last_admin", "no se puede dejar el sistema sin administradores activos")
		return
	case err != nil:
		errJSON(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// Deshabilitar a un usuario corta sus sesiones vivas de inmediato.
	if req.Status != nil && *req.Status == "disabled" {
		_, _ = s.sessions.RevokeAllForUser(r.Context(), id, "", sess.UserID, "user_disabled")
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminListSessions lista las sesiones activas, opcionalmente de un
// usuario concreto, marcando cuál es la del propio admin.
//
//	GET /api/admin/sessions?user_id=<uuid>
func (s *Server) handleAdminListSessions(w http.ResponseWriter, r *http.Request) {
	infos, err := s.sessions.ListActive(r.Context(), r.URL.Query().Get("user_id"))
	if err != nil {
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo listar sesiones")
		return
	}
	sess := sessionFrom(r)
	for i := range infos {
		infos[i].Current = infos[i].ID == sess.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

// handleAdminRevokeSession revoca una sesión concreta.
//
//	DELETE /api/admin/sessions/{id}
func (s *Server) handleAdminRevokeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := sessionFrom(r)
	ok, err := s.sessions.Revoke(r.Context(), id, sess.UserID, "admin_revoked")
	if err != nil {
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo revocar la sesión")
		return
	}
	if !ok {
		errJSON(w, http.StatusNotFound, "not_found", "sesión no existe o ya estaba revocada")
		return
	}
	m := reqMeta(r)
	s.auditor.Log(r.Context(), audit.Event{Type: "session.revoked", Severity: audit.SevWarn,
		ActorID: sess.UserID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID,
		Detail: map[string]any{"session_id": id}})
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminRevokeUserSessions revoca todas las sesiones vivas de un usuario.
//
//	DELETE /api/admin/users/{id}/sessions
func (s *Server) handleAdminRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := sessionFrom(r)
	n, err := s.sessions.RevokeAllForUser(r.Context(), id, "", sess.UserID, "admin_revoked")
	if err != nil {
		errJSON(w, http.StatusServiceUnavailable, "upstream_unavailable", "no se pudo revocar las sesiones")
		return
	}
	m := reqMeta(r)
	s.auditor.Log(r.Context(), audit.Event{Type: "session.revoked_all", Severity: audit.SevWarn,
		ActorID: sess.UserID, TargetID: id, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID,
		Detail: map[string]any{"count": n}})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}
