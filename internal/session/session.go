// Package session implementa sesiones opacas server-side sobre PostgreSQL.
// El token que viaja en la cookie son 32 bytes aleatorios; en BD solo se
// guarda su SHA-256, de modo que un volcado de la tabla no permite suplantar
// sesiones. La revocación es inmediata (estado en BD, no JWT).
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"goGioIa/internal/pgstore"
)

// Tipos de sesión: completa o pendiente de segundo factor (fase MFA).
const (
	KindFull       = "full"
	KindMFAPending = "mfa_pending"
)

// Defaults si la configuración en BD no está disponible.
const (
	defaultAbsoluteTTLHours = 12
	defaultIdleTTLMinutes   = 30
	mfaPendingTTL           = 5 * time.Minute
	touchInterval           = time.Minute
)

// ErrInvalid cubre token desconocido, sesión expirada/revocada o usuario
// inactivo. Se colapsan en un solo error para no filtrar el motivo.
var ErrInvalid = errors.New("session: inválida o expirada")

// Session es el resultado de validar un token.
type Session struct {
	ID                 string
	UserID             string
	Kind               string
	Email              string
	DisplayName        string
	Role               string // 'admin' | 'user'
	MustChangePassword bool
	ExpiresAt          time.Time // expiración efectiva: min(absoluta, inactividad)
}

// Info es la vista administrativa de una sesión activa.
type Info struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Email      string    `json:"email"`
	Kind       string    `json:"kind"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

// Manager crea, valida y revoca sesiones.
type Manager struct {
	st *pgstore.Store
}

// NewManager construye un Manager sobre el store de aplicación.
func NewManager(st *pgstore.Store) *Manager { return &Manager{st: st} }

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Create emite una sesión nueva y devuelve el token opaco (solo existe en la
// respuesta; en BD queda su hash) y la expiración absoluta.
func (m *Manager) Create(ctx context.Context, userID, kind, ip, userAgent string) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generar token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	ttl := time.Duration(m.st.SettingInt(ctx, "session.absolute_ttl_h", defaultAbsoluteTTLHours)) * time.Hour
	if kind == KindMFAPending {
		ttl = mfaPendingTTL
	}
	expires := time.Now().Add(ttl)

	_, err := m.st.Pool().Exec(ctx, `
		INSERT INTO app.sessions (user_id, token_hash, kind, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, NULLIF($4,'')::inet, NULLIF($5,''), $6)`,
		userID, hashToken(token), kind, ip, userAgent, expires)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("crear sesión: %w", err)
	}
	return token, expires, nil
}

// Validate resuelve un token a su sesión, aplicando expiración absoluta, por
// inactividad y estado del usuario. Actualiza last_seen_at como máximo una
// vez por minuto para no castigar la BD en cada request.
func (m *Manager) Validate(ctx context.Context, token string) (*Session, error) {
	var (
		s        Session
		expires  time.Time
		lastSeen time.Time
		status   string
	)
	err := m.st.Pool().QueryRow(ctx, `
		SELECT s.id, s.user_id, s.kind, s.expires_at, s.last_seen_at,
		       u.email, u.display_name, r.name, u.must_change_password, u.status
		FROM app.sessions s
		JOIN app.users u ON u.id = s.user_id
		JOIN app.roles r ON r.id = u.role_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL`,
		hashToken(token)).Scan(
		&s.ID, &s.UserID, &s.Kind, &expires, &lastSeen,
		&s.Email, &s.DisplayName, &s.Role, &s.MustChangePassword, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalid
		}
		return nil, fmt.Errorf("validar sesión: %w", err)
	}

	now := time.Now()
	if now.After(expires) || status != "active" {
		return nil, ErrInvalid
	}
	idle := time.Duration(m.st.SettingInt(ctx, "session.idle_ttl_min", defaultIdleTTLMinutes)) * time.Minute
	idleDeadline := lastSeen.Add(idle)
	if s.Kind == KindFull && now.After(idleDeadline) {
		return nil, ErrInvalid
	}

	if now.Sub(lastSeen) > touchInterval {
		if _, err := m.st.Pool().Exec(ctx,
			`UPDATE app.sessions SET last_seen_at = now() WHERE id = $1`, s.ID); err == nil {
			idleDeadline = now.Add(idle)
		}
	}

	s.ExpiresAt = expires
	if s.Kind == KindFull && idleDeadline.Before(expires) {
		s.ExpiresAt = idleDeadline
	}
	return &s, nil
}

// Revoke revoca una sesión concreta. Devuelve false si no existía o ya
// estaba revocada.
func (m *Manager) Revoke(ctx context.Context, sessionID, byUserID, reason string) (bool, error) {
	tag, err := m.st.Pool().Exec(ctx, `
		UPDATE app.sessions
		SET revoked_at = now(), revoked_by = NULLIF($2,'')::uuid, revoke_reason = $3
		WHERE id = $1 AND revoked_at IS NULL`,
		sessionID, byUserID, reason)
	if err != nil {
		return false, fmt.Errorf("revocar sesión: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeAllForUser revoca todas las sesiones vivas de un usuario, opcionalmente
// exceptuando una (p. ej. la sesión actual tras un cambio de contraseña).
func (m *Manager) RevokeAllForUser(ctx context.Context, userID, exceptSessionID, byUserID, reason string) (int, error) {
	tag, err := m.st.Pool().Exec(ctx, `
		UPDATE app.sessions
		SET revoked_at = now(), revoked_by = NULLIF($3,'')::uuid, revoke_reason = $4
		WHERE user_id = $1 AND revoked_at IS NULL AND id <> COALESCE(NULLIF($2,'')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`,
		userID, exceptSessionID, byUserID, reason)
	if err != nil {
		return 0, fmt.Errorf("revocar sesiones de usuario: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListActive devuelve las sesiones vivas (no revocadas, no vencidas por
// tiempo absoluto ni inactividad), opcionalmente filtradas por usuario.
func (m *Manager) ListActive(ctx context.Context, userID string) ([]Info, error) {
	idleMin := m.st.SettingInt(ctx, "session.idle_ttl_min", defaultIdleTTLMinutes)
	rows, err := m.st.Pool().Query(ctx, `
		SELECT s.id, s.user_id, u.email, s.kind,
		       COALESCE(host(s.ip), ''), COALESCE(s.user_agent, ''),
		       s.created_at, s.last_seen_at, s.expires_at
		FROM app.sessions s
		JOIN app.users u ON u.id = s.user_id
		WHERE s.revoked_at IS NULL
		  AND s.expires_at > now()
		  AND s.last_seen_at > now() - make_interval(mins => $1)
		  AND ($2 = '' OR s.user_id = $2::uuid)
		ORDER BY s.last_seen_at DESC`,
		idleMin, userID)
	if err != nil {
		return nil, fmt.Errorf("listar sesiones: %w", err)
	}
	defer rows.Close()

	var out []Info
	for rows.Next() {
		var i Info
		if err := rows.Scan(&i.ID, &i.UserID, &i.Email, &i.Kind, &i.IP, &i.UserAgent,
			&i.CreatedAt, &i.LastSeenAt, &i.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
