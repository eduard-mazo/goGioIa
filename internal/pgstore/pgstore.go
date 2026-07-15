// Package pgstore gestiona la base de datos de aplicación en PostgreSQL 16:
// usuarios, sesiones, MFA, configuración centralizada, trazabilidad de
// requests a Ollama, uso de tokens y auditoría. Driver puro Go (pgx), sin CGO,
// coherente con el binario estático.
//
// Sigue el mismo patrón perezoso que internal/store (Oracle): Open no exige
// conectividad; EnsureReady aplica migraciones/particiones/bootstrap la
// primera vez que la base está disponible y es seguro reintentarla.
package pgstore

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"goGioIa/internal/config"
)

// Store encapsula el pool de PostgreSQL y el estado de preparación.
type Store struct {
	pool *pgxpool.Pool
	cfg  config.Config

	mu        sync.Mutex
	ready     bool
	maintOnce sync.Once

	settingsMu    sync.Mutex
	settingsCache map[string]cachedSetting
}

// Open crea el pool de conexiones (perezoso: no conecta hasta el primer uso).
func Open(cfg config.Config) (*Store, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.PGConnString())
	if err != nil {
		return nil, fmt.Errorf("configurar pool PostgreSQL: %w", err)
	}
	pcfg.MaxConns = 8
	pcfg.MinConns = 0
	pcfg.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		return nil, fmt.Errorf("crear pool PostgreSQL: %w", err)
	}
	return &Store{pool: pool, cfg: cfg, settingsCache: map[string]cachedSetting{}}, nil
}

// Close libera el pool.
func (s *Store) Close() { s.pool.Close() }

// Pool expone el pool para los repositorios de módulos superiores
// (auth, session, settings, usage...).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping comprueba la conectividad con la base de datos.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.pool.Ping(ctx)
}

// EnsureReady garantiza (una sola vez) que el esquema, las particiones y el
// usuario admin inicial existen. Tolerante a base caída: devuelve error y la
// siguiente llamada reintenta.
func (s *Store) EnsureReady(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return nil
	}
	if err := s.Ping(ctx); err != nil {
		return fmt.Errorf("postgres no disponible: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		return fmt.Errorf("aplicar migraciones: %w", err)
	}
	if err := s.ensurePartitions(ctx); err != nil {
		return fmt.Errorf("asegurar particiones: %w", err)
	}
	if err := s.bootstrapAdmin(ctx); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	s.ready = true
	// Mantenimiento periódico: particiones del mes corriente y siguiente
	// (para procesos que corren meses sin reinicio) y purga de sesiones
	// vencidas/revocadas antiguas.
	s.maintOnce.Do(func() { go s.maintain() })
	return nil
}

func (s *Store) maintain() {
	t := time.NewTicker(12 * time.Hour)
	defer t.Stop()
	for range t.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := s.ensurePartitions(ctx); err != nil {
			log.Printf("aviso: mantenimiento de particiones: %v", err)
		}
		// Las sesiones muertas se conservan 7 días para inspección; el rastro
		// permanente vive en app.audit_events.
		if _, err := s.pool.Exec(ctx, `
			DELETE FROM app.sessions
			WHERE expires_at < now() - interval '7 days'
			   OR (revoked_at IS NOT NULL AND revoked_at < now() - interval '7 days')`); err != nil {
			log.Printf("aviso: purga de sesiones: %v", err)
		}
		cancel()
	}
}
