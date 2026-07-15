package pgstore

import (
	"context"
	"fmt"
	"log"

	"goGioIa/internal/auth"
)

// bootstrapAdmin crea el usuario administrador inicial si no existe ningún
// admin. La contraseña sale de GOGIOIA_ADMIN_PASSWORD; si no está definida se
// genera una aleatoria y se imprime UNA sola vez en el log (patrón de
// contraseña inicial estilo Grafana/Jenkins) marcada para cambio obligatorio.
func (s *Store) bootstrapAdmin(ctx context.Context) error {
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM app.users WHERE role_id = 1`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	password := s.cfg.AdminPassword
	generated := false
	if password == "" {
		var err error
		password, err = auth.RandomPassword(20)
		if err != nil {
			return fmt.Errorf("generar contraseña inicial: %w", err)
		}
		generated = true
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hashear contraseña inicial: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO app.users (email, display_name, password_hash, role_id, status, must_change_password)
		VALUES ($1, 'Administrador', $2, 1, 'active', true)`,
		s.cfg.AdminEmail, hash)
	if err != nil {
		return err
	}

	if generated {
		log.Printf("═══════════════════════════════════════════════════════════")
		log.Printf("  Usuario admin inicial creado: %s", s.cfg.AdminEmail)
		log.Printf("  Contraseña temporal (cámbiala en el primer login):")
		log.Printf("      %s", password)
		log.Printf("  No volverá a mostrarse.")
		log.Printf("═══════════════════════════════════════════════════════════")
	} else {
		log.Printf("usuario admin inicial creado: %s (contraseña de GOGIOIA_ADMIN_PASSWORD)", s.cfg.AdminEmail)
	}
	return nil
}
