package pgstore

import (
	"context"
	"embed"
	"fmt"
	"sort"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate aplica en orden léxico las migraciones pendientes de migrations/.
// Todo el lote corre en una única transacción (el DDL de PostgreSQL es
// transaccional) serializada con un advisory lock, de modo que varios
// procesos arrancando a la vez no compiten.
func (s *Store) migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Lock exclusivo de migración (clave arbitraria fija de la app).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(729_431_001)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS app;
		CREATE TABLE IF NOT EXISTS app.schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return err
	}

	applied := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT version FROM app.schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("migración %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.schema_migrations (version) VALUES ($1)`, name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
