package pgstore

import (
	"context"
	"fmt"
	"time"
)

// Tablas particionadas por RANGE (created_at) con particiones mensuales.
var partitionedTables = []string{"ai_requests", "token_usage", "audit_events"}

// ensurePartitions crea (si faltan) las particiones del mes corriente y del
// siguiente para todas las tablas particionadas. Idempotente.
func (s *Store) ensurePartitions(ctx context.Context) error {
	now := time.Now().UTC()
	months := []time.Time{
		time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0),
	}
	for _, table := range partitionedTables {
		for _, from := range months {
			to := from.AddDate(0, 1, 0)
			// Identificadores y fechas generados internamente (no hay input
			// de usuario), por eso es seguro construir el DDL con Sprintf.
			ddl := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS app.%s_%s PARTITION OF app.%s
				   FOR VALUES FROM ('%s') TO ('%s')`,
				table, from.Format("2006_01"), table,
				from.Format("2006-01-02"), to.Format("2006-01-02"))
			if _, err := s.pool.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("partición %s_%s: %w", table, from.Format("2006_01"), err)
			}
		}
	}
	return nil
}
