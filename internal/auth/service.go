// Servicio de autenticación: login con lockout progresivo, cambio de
// contraseña y gestión administrativa de usuarios. Todas las rutas de fallo
// de login devuelven ErrInvalidCredentials para no filtrar si la cuenta
// existe; el detalle real queda en la auditoría.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goGioIa/internal/audit"
)

// IDs de rol (semilla 0002_seed.sql).
const (
	RoleIDAdmin = 1
	RoleIDUser  = 2
)

// Errores de negocio que los handlers traducen a códigos HTTP.
var (
	ErrInvalidCredentials = errors.New("auth: credenciales inválidas")
	ErrLocked             = errors.New("auth: cuenta bloqueada temporalmente")
	ErrWeakPassword       = errors.New("auth: la contraseña debe tener entre 12 y 128 caracteres")
	ErrEmailTaken         = errors.New("auth: el email ya está registrado")
	ErrNotFound           = errors.New("auth: usuario no existe")
	ErrLastAdmin          = errors.New("auth: no se puede dejar el sistema sin administradores activos")
)

// Máximo bloqueo por fuerza bruta (el backoff exponencial se capa aquí).
const maxLockoutSeconds = 900

// User es la identidad autenticada que consumen los handlers.
type User struct {
	ID                 string    `json:"id"`
	Email              string    `json:"email"`
	DisplayName        string    `json:"display_name"`
	Role               string    `json:"role"`
	Status             string    `json:"status"`
	MustChangePassword bool      `json:"must_change_password"`
	LockedUntil        time.Time `json:"-"`
	CreatedAt          time.Time `json:"created_at,omitempty"`
}

// Meta acompaña cada operación con el contexto del request para auditoría.
type Meta struct {
	IP        string
	UserAgent string
	RequestID string
}

// Settings expone la lectura de parámetros dinámicos (app.app_settings).
// La implementa *pgstore.Store; se usa una interfaz para evitar el ciclo de
// imports pgstore → auth (bootstrap) → pgstore.
type Settings interface {
	SettingInt(ctx context.Context, key string, fallback int) int
}

// Service implementa los casos de uso de autenticación sobre PostgreSQL.
type Service struct {
	pool     *pgxpool.Pool
	settings Settings
	aud      *audit.Auditor
}

// NewService construye el servicio.
func NewService(pool *pgxpool.Pool, settings Settings, aud *audit.Auditor) *Service {
	return &Service{pool: pool, settings: settings, aud: aud}
}

// ValidatePassword aplica la política mínima de contraseñas.
func ValidatePassword(pw string) error {
	if len(pw) < 12 || len(pw) > 128 {
		return ErrWeakPassword
	}
	return nil
}

// Login verifica email+contraseña. En fallo repite el costo de un hash real
// (DummyVerify) y aplica lockout progresivo por cuenta.
func (s *Service) Login(ctx context.Context, email, password string, m Meta) (*User, error) {
	var (
		u         User
		hash      string
		fails     int
		lockedRaw *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.display_name, u.password_hash, u.status,
		       u.failed_login_count, u.locked_until, u.must_change_password, r.name
		FROM app.users u JOIN app.roles r ON r.id = u.role_id
		WHERE lower(u.email) = lower($1)`,
		email).Scan(&u.ID, &u.Email, &u.DisplayName, &hash, &u.Status,
		&fails, &lockedRaw, &u.MustChangePassword, &u.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			DummyVerify() // tiempo uniforme frente a cuentas inexistentes
			s.aud.Log(ctx, audit.Event{Type: "auth.login.fail", Severity: audit.SevWarn,
				IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID,
				Detail: map[string]any{"reason": "unknown_user", "email": email}})
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("buscar usuario: %w", err)
	}

	if lockedRaw != nil && time.Now().Before(*lockedRaw) {
		u.LockedUntil = *lockedRaw
		s.aud.Log(ctx, audit.Event{Type: "auth.login.fail", Severity: audit.SevWarn,
			ActorID: u.ID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID,
			Detail: map[string]any{"reason": "locked"}})
		return &u, ErrLocked
	}

	if err := VerifyPassword(password, hash); err != nil {
		threshold := s.settings.SettingInt(ctx, "security.lockout_threshold", 5)
		var until *time.Time
		s.pool.QueryRow(ctx, `
			UPDATE app.users
			SET failed_login_count = failed_login_count + 1,
			    locked_until = CASE
			      WHEN failed_login_count + 1 >= $2
			      THEN now() + make_interval(secs =>
			             LEAST(30 * power(2, failed_login_count + 1 - $2), $3))
			      ELSE locked_until END,
			    updated_at = now()
			WHERE id = $1
			RETURNING locked_until`, u.ID, threshold, maxLockoutSeconds).Scan(&until)
		detail := map[string]any{"reason": "bad_password"}
		if until != nil && time.Now().Before(*until) {
			detail["locked_until"] = until.Format(time.RFC3339)
			s.aud.Log(ctx, audit.Event{Type: "auth.lockout", Severity: audit.SevWarn,
				ActorID: u.ID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID, Detail: detail})
		}
		s.aud.Log(ctx, audit.Event{Type: "auth.login.fail", Severity: audit.SevWarn,
			ActorID: u.ID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID, Detail: detail})
		return nil, ErrInvalidCredentials
	}

	if u.Status != "active" {
		s.aud.Log(ctx, audit.Event{Type: "auth.login.fail", Severity: audit.SevWarn,
			ActorID: u.ID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID,
			Detail: map[string]any{"reason": "status_" + u.Status}})
		return nil, ErrInvalidCredentials
	}

	if fails > 0 || lockedRaw != nil {
		_, _ = s.pool.Exec(ctx, `
			UPDATE app.users SET failed_login_count = 0, locked_until = NULL, updated_at = now()
			WHERE id = $1`, u.ID)
	}
	s.aud.Log(ctx, audit.Event{Type: "auth.login.ok",
		ActorID: u.ID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID})
	return &u, nil
}

// ChangePassword valida la contraseña actual y fija la nueva. El handler es
// responsable de revocar las demás sesiones del usuario.
func (s *Service) ChangePassword(ctx context.Context, userID, current, newPw string, m Meta) error {
	if err := ValidatePassword(newPw); err != nil {
		return err
	}
	var hash string
	err := s.pool.QueryRow(ctx,
		`SELECT password_hash FROM app.users WHERE id = $1`, userID).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("buscar usuario: %w", err)
	}
	if err := VerifyPassword(current, hash); err != nil {
		s.aud.Log(ctx, audit.Event{Type: "auth.password.change_fail", Severity: audit.SevWarn,
			ActorID: userID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID})
		return ErrInvalidCredentials
	}
	newHash, err := HashPassword(newPw)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE app.users
		SET password_hash = $2, password_changed_at = now(),
		    must_change_password = false, updated_at = now()
		WHERE id = $1`, userID, newHash)
	if err != nil {
		return fmt.Errorf("actualizar contraseña: %w", err)
	}
	s.aud.Log(ctx, audit.Event{Type: "auth.password.changed",
		ActorID: userID, IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID})
	return nil
}

// CreateUser da de alta un usuario (solo admin). La cuenta nace con
// must_change_password para que la contraseña provisional no sobreviva.
func (s *Service) CreateUser(ctx context.Context, email, displayName, password, role string, actorID string, m Meta) (*User, error) {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > 254 || !strings.Contains(email, "@") {
		return nil, errors.New("auth: email inválido")
	}
	if strings.TrimSpace(displayName) == "" {
		return nil, errors.New("auth: display_name requerido")
	}
	roleID, ok := map[string]int{"admin": RoleIDAdmin, "user": RoleIDUser}[role]
	if !ok {
		return nil, errors.New("auth: rol inválido (admin|user)")
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := User{Email: email, DisplayName: displayName, Role: role, Status: "active", MustChangePassword: true}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO app.users (email, display_name, password_hash, role_id, status, must_change_password)
		VALUES ($1, $2, $3, $4, 'active', true)
		RETURNING id, created_at`,
		email, displayName, hash, roleID).Scan(&u.ID, &u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("crear usuario: %w", err)
	}
	s.aud.Log(ctx, audit.Event{Type: "user.created", ActorID: actorID, TargetID: u.ID,
		IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID,
		Detail: map[string]any{"email": email, "role": role}})
	return &u, nil
}

// ListUsers devuelve todos los usuarios (vista administrativa).
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email, u.display_name, r.name, u.status, u.must_change_password, u.created_at
		FROM app.users u JOIN app.roles r ON r.id = u.role_id
		ORDER BY u.created_at`)
	if err != nil {
		return nil, fmt.Errorf("listar usuarios: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Status,
			&u.MustChangePassword, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser cambia estado y/o rol. Rechaza dejar el sistema sin ningún
// administrador activo.
func (s *Service) UpdateUser(ctx context.Context, id string, status, role *string, actorID string, m Meta) error {
	if status == nil && role == nil {
		return nil
	}
	if status != nil && *status != "active" && *status != "disabled" {
		return errors.New("auth: status inválido (active|disabled)")
	}
	var newRoleID *int
	if role != nil {
		rid, ok := map[string]int{"admin": RoleIDAdmin, "user": RoleIDUser}[*role]
		if !ok {
			return errors.New("auth: rol inválido (admin|user)")
		}
		newRoleID = &rid
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var curRoleID int
	var curStatus string
	err = tx.QueryRow(ctx,
		`SELECT role_id, status FROM app.users WHERE id = $1 FOR UPDATE`, id).
		Scan(&curRoleID, &curStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	// ¿La operación quita de servicio a un admin? Verificar que quede otro.
	losesAdmin := curRoleID == RoleIDAdmin &&
		((newRoleID != nil && *newRoleID != RoleIDAdmin) || (status != nil && *status == "disabled"))
	if losesAdmin {
		var others int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM app.users
			WHERE role_id = $1 AND status = 'active' AND id <> $2`,
			RoleIDAdmin, id).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return ErrLastAdmin
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE app.users
		SET status  = COALESCE($2, status),
		    role_id = COALESCE($3, role_id),
		    updated_at = now()
		WHERE id = $1`, id, status, newRoleID)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	detail := map[string]any{}
	if status != nil {
		detail["status"] = *status
	}
	if role != nil {
		detail["role"] = *role
	}
	s.aud.Log(ctx, audit.Event{Type: "user.updated", Severity: audit.SevWarn,
		ActorID: actorID, TargetID: id,
		IP: m.IP, UserAgent: m.UserAgent, RequestID: m.RequestID, Detail: detail})
	return nil
}
