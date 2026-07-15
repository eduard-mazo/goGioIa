// Package audit escribe eventos sensibles en app.audit_events (append-only).
// La escritura es best-effort respecto al request (no bloquea la respuesta si
// la BD de auditoría falla) pero queda registrada en el log del proceso.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Severidades válidas (CHECK en la tabla).
const (
	SevInfo     = "info"
	SevWarn     = "warn"
	SevCritical = "critical"
)

// Event es un evento de auditoría. Los IDs vacíos se guardan como NULL.
type Event struct {
	Type      string // p. ej. "auth.login.ok"
	Severity  string // info | warn | critical ("" → info)
	ActorID   string // uuid del usuario que actúa
	TargetID  string // uuid del usuario/recurso afectado
	IP        string
	UserAgent string
	RequestID string
	Detail    map[string]any // nunca incluir secretos ni contraseñas
}

// Auditor inserta eventos en PostgreSQL.
type Auditor struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// New construye un Auditor.
func New(pool *pgxpool.Pool, logger *slog.Logger) *Auditor {
	return &Auditor{pool: pool, logger: logger}
}

// Log inserta el evento. Usa un contexto propio (WithoutCancel) para que la
// auditoría no se pierda si el request que la origina se cancela.
func (a *Auditor) Log(ctx context.Context, e Event) {
	if e.Severity == "" {
		e.Severity = SevInfo
	}
	detail := []byte("{}")
	if e.Detail != nil {
		if b, err := json.Marshal(e.Detail); err == nil {
			detail = b
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := a.pool.Exec(ctx, `
		INSERT INTO app.audit_events
		  (event_type, severity, actor_id, target_id, ip, user_agent, request_id, detail)
		VALUES ($1, $2, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid,
		        NULLIF($5,'')::inet, NULLIF($6,''), NULLIF($7,'')::uuid, $8::jsonb)`,
		e.Type, e.Severity, e.ActorID, e.TargetID, e.IP, e.UserAgent, e.RequestID, string(detail))
	if err != nil {
		a.logger.LogAttrs(ctx, slog.LevelError, "audit insert falló",
			slog.String("event_type", e.Type), slog.Any("err", err))
	}
}
