package store

import (
	"context"
	"log"
	"time"
)

// Event es una operación terminada del pipeline RAG, lista para persistirse
// en rag_events. Los campos numéricos a 0 se guardan como NULL cuando no
// aplican (http_status, tokens, etc.) para no contaminar los agregados.
type Event struct {
	Kind         string        // embed | query_embed | retrieval | generation | ingest
	Ref          []byte        // document_id o query_id según kind (opcional)
	Model        string        //
	OK           bool          //
	HTTPStatus   int           //
	ErrorKind    string        // categoría estable (ollama.ClassifyError)
	ErrorDetail  string        //
	Latency      time.Duration //
	QueueWait    time.Duration //
	Attempts     int           //
	BatchSize    int           //
	PayloadBytes int           //
	TokensIn     int           //
	TokensOut    int           //
	TokenSource  string        // "ollama" | "estimated" ("" si no hay tokens)
	LoadDuration time.Duration // tiempo de (re)carga del modelo reportado por Ollama
	Detail       string        // propósito o etapa (ingest|query|warmup, start|done…)
}

// RecordEvent persiste el evento en segundo plano y con la mejor voluntad:
// la observabilidad nunca bloquea ni tumba el pipeline. Si Oracle no está
// listo el evento se descarta (quedan las trazas de log).
func (s *Store) RecordEvent(ev Event) {
	if !s.Ready() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.insertEvent(ctx, ev); err != nil {
			log.Printf("ops: no se pudo registrar el evento %s: %v", ev.Kind, err)
		}
	}()
}

// Ready informa si el esquema ya fue verificado (sin disparar el bootstrap).
func (s *Store) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

func (s *Store) insertEvent(ctx context.Context, ev Event) error {
	status := "OK"
	if !ev.OK {
		status = "ERROR"
	}
	errDetail := ev.ErrorDetail
	if len(errDetail) > 1900 {
		errDetail = errDetail[:1900]
	}
	detail := ev.Detail
	if len(detail) > 900 {
		detail = detail[:900]
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rag_events
		  (event_id, kind, ref_id, model, status, http_status, error_kind, error_detail,
		   latency_ms, queue_ms, attempts, batch_size, payload_bytes,
		   tokens_in, tokens_out, token_source, load_ms, detail)
		VALUES (:1, :2, :3, :4, :5, :6, :7, :8, :9, :10, :11, :12, :13, :14, :15, :16, :17, :18)`,
		newID(), ev.Kind, nullableBytes(ev.Ref), nullable(ev.Model), status,
		nullableInt(ev.HTTPStatus), nullable(ev.ErrorKind), nullable(errDetail),
		ev.Latency.Milliseconds(), nullableInt64(ev.QueueWait.Milliseconds()),
		nullableInt(ev.Attempts), nullableInt(ev.BatchSize), nullableInt(ev.PayloadBytes),
		nullableInt(ev.TokensIn), nullableInt(ev.TokensOut), nullable(ev.TokenSource),
		nullableInt64(ev.LoadDuration.Milliseconds()), nullable(detail))
	return err
}

// nullableBytes convierte un slice vacío en NULL para columnas RAW opcionales.
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// nullableInt convierte 0 en NULL para columnas numéricas opcionales.
func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullableInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
