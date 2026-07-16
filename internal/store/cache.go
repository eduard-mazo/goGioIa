package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── Versión de la base de conocimiento ───────────────────────────────────

// KBVersion devuelve la versión actual de la base de conocimiento.
func (s *Store) KBVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM rag_kb_state WHERE id = 1`).Scan(&v)
	return v, err
}

// BumpKBVersion incrementa la versión: las respuestas cacheadas con la
// versión anterior dejan de ser elegibles (invalidación por clave).
func (s *Store) BumpKBVersion(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rag_kb_state SET version = version + 1 WHERE id = 1`)
	return err
}

// ── Cache de embeddings ──────────────────────────────────────────────────

// CachedEmbedding devuelve el vector cacheado para el hash dado (nil si no
// existe) y actualiza sus contadores de uso.
func (s *Store) CachedEmbedding(ctx context.Context, hash string) ([]float32, error) {
	var lit string
	err := s.db.QueryRowContext(ctx, `
		SELECT FROM_VECTOR(embedding RETURNING CLOB) FROM embedding_cache WHERE text_hash = :1`,
		hash).Scan(&lit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	vec, err := parseVecLiteral(lit)
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE embedding_cache SET use_count = use_count + 1, last_used_at = SYSTIMESTAMP
		WHERE text_hash = :1`, hash)
	return vec, nil
}

// PutCachedEmbedding guarda un embedding (tolera la carrera del duplicado).
func (s *Store) PutCachedEmbedding(ctx context.Context, hash, model string, vec []float32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO embedding_cache (text_hash, model, embedding)
		VALUES (:1, :2, TO_VECTOR(:3))`,
		hash, model, vecLiteral(vec))
	if err != nil && strings.Contains(err.Error(), "ORA-00001") {
		return nil
	}
	return err
}

// ── Cache semántica de respuestas ────────────────────────────────────────

// CachedAnswer es una respuesta reutilizable de la cache semántica.
type CachedAnswer struct {
	ID          []byte
	Response    string
	SourcesJSON string
	Similarity  float64 // 1 para hits exactos
}

func templateHex(templateID []byte) any {
	if len(templateID) == 0 {
		return nil
	}
	return strings.ToUpper(fmt.Sprintf("%x", templateID))
}

// LookupCacheExact busca por hash de la pregunta normalizada (sin embedding).
func (s *Store) LookupCacheExact(ctx context.Context, qhash, model string, templateID []byte, kbVersion int64) (*CachedAnswer, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT cache_id, response_text, NVL(sources_json, '[]')
		FROM rag_semantic_cache
		WHERE question_hash = :1 AND llm_model = :2 AND kb_version = :3
		  AND NVL(RAWTOHEX(template_id), '-') = NVL(:4, '-')
		  AND (expires_at IS NULL OR expires_at > SYSTIMESTAMP)
		FETCH FIRST 1 ROWS ONLY`,
		qhash, model, kbVersion, templateHex(templateID))
	var c CachedAnswer
	if err := row.Scan(&c.ID, &c.Response, &c.SourcesJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c.Similarity = 1
	return &c, nil
}

// LookupCacheSemantic busca la entrada más cercana por coseno; solo la
// devuelve si su distancia no supera maxDist.
func (s *Store) LookupCacheSemantic(ctx context.Context, embedding []float32, model string, templateID []byte, kbVersion int64, maxDist float64) (*CachedAnswer, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT cache_id, response_text, NVL(sources_json, '[]'),
		       VECTOR_DISTANCE(question_embedding, TO_VECTOR(:1), COSINE) AS dist
		FROM rag_semantic_cache
		WHERE llm_model = :2 AND kb_version = :3
		  AND NVL(RAWTOHEX(template_id), '-') = NVL(:4, '-')
		  AND (expires_at IS NULL OR expires_at > SYSTIMESTAMP)
		ORDER BY dist
		FETCH FIRST 1 ROWS ONLY`,
		vecLiteral(embedding), model, kbVersion, templateHex(templateID))
	var c CachedAnswer
	var dist float64
	if err := row.Scan(&c.ID, &c.Response, &c.SourcesJSON, &dist); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if dist > maxDist {
		return nil, nil
	}
	c.Similarity = 1 - dist
	return &c, nil
}

// RecordCacheHit actualiza los contadores de una entrada servida.
func (s *Store) RecordCacheHit(ctx context.Context, cacheID []byte) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE rag_semantic_cache SET hit_count = hit_count + 1, last_hit_at = SYSTIMESTAMP
		WHERE cache_id = :1`, cacheID)
	return err
}

// PutCache guarda una respuesta generada para reutilizarla.
func (s *Store) PutCache(ctx context.Context, qhash, question string, embedding []float32, model string, templateID []byte, kbVersion int64, response, sourcesJSON string, ttl time.Duration) error {
	var expires any
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rag_semantic_cache
		  (cache_id, question_hash, question_text, question_embedding, llm_model,
		   template_id, kb_version, response_text, sources_json, expires_at)
		VALUES (:1, :2, :3, TO_VECTOR(:4), :5, :6, :7, :8, :9, :10)`,
		newID(), qhash, clob(question), vecLiteral(embedding), model,
		templateID, kbVersion, clob(response), clob(sourcesJSON), expires)
	return err
}

// InvalidateCache borra las entradas equivalentes a la consulta valorada
// negativamente: por hash exacto y por cercanía de embedding (la consulta
// pudo ser un hit semántico de una entrada con otro texto).
func (s *Store) InvalidateCache(ctx context.Context, qhash string, queryID []byte, maxDist float64) (int64, error) {
	var total int64
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM rag_semantic_cache WHERE question_hash = :1`, qhash)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	total += n
	res, err = s.db.ExecContext(ctx, `
		DELETE FROM rag_semantic_cache c
		WHERE VECTOR_DISTANCE(c.question_embedding,
		        (SELECT q.query_embedding FROM rag_queries q WHERE q.query_id = :1),
		        COSINE) <= :2`, queryID, maxDist)
	if err != nil {
		return total, err
	}
	n, _ = res.RowsAffected()
	return total + n, nil
}

// PurgeExpiredCache elimina las entradas caducadas.
func (s *Store) PurgeExpiredCache(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM rag_semantic_cache WHERE expires_at < SYSTIMESTAMP`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CacheStats devuelve el tamaño de la cache y la versión de la base.
func (s *Store) CacheStats(ctx context.Context) (entries int, kbVersion int64, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM rag_semantic_cache),
		       (SELECT version FROM rag_kb_state WHERE id = 1)
		FROM dual`).Scan(&entries, &kbVersion)
	return entries, kbVersion, err
}

// QueryText devuelve el texto de una consulta registrada.
func (s *Store) QueryText(ctx context.Context, queryID []byte) (string, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `
		SELECT query_text FROM rag_queries WHERE query_id = :1`, queryID).Scan(&text)
	return text, err
}

// parseVecLiteral deshace el literal «[x,y,…]» de FROM_VECTOR a []float32.
func parseVecLiteral(lit string) ([]float32, error) {
	lit = strings.TrimSpace(lit)
	lit = strings.TrimPrefix(lit, "[")
	lit = strings.TrimSuffix(lit, "]")
	if lit == "" {
		return nil, fmt.Errorf("literal de vector vacío")
	}
	parts := strings.Split(lit, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("literal de vector inválido: %w", err)
		}
		out[i] = float32(f)
	}
	return out, nil
}
