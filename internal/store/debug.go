package store

import (
	"context"
	"strings"
	"time"
)

// Snapshot es la radiografía de la base para depurar el RAG sin acceso a
// Oracle: qué documentos hay y cómo quedaron troceados, qué trabajos hay en
// cola o fallidos, qué recuperaron las últimas consultas y cómo está la cache.
// La expone GET /api/rag/debug.
type Snapshot struct {
	KBVersion         int64           `json:"kbVersion"`
	Documents         []DocSnapshot   `json:"documents"`
	Jobs              []JobSnapshot   `json:"jobs"`
	Queries           []QuerySnapshot `json:"lastQueries"`
	Cache             []CacheSnapshot `json:"cacheByKbVersion"`
	EmbedCacheEntries int             `json:"embedCacheEntries"`
	Attachments       int             `json:"attachments"`
	AttachmentChunks  int             `json:"attachmentChunks"`
}

// DocSnapshot resume un documento y la forma de sus chunks (en caracteres).
type DocSnapshot struct {
	FileName  string `json:"fileName"`
	Status    string `json:"status"`
	Pages     int    `json:"pages"`
	Chunks    int    `json:"chunks"`
	MinChunk  int    `json:"minChunkChars"`
	AvgChunk  int    `json:"avgChunkChars"`
	MaxChunk  int    `json:"maxChunkChars"`
	TotalText int    `json:"totalChars"`
	Error     string `json:"error,omitempty"`
}

// JobSnapshot es un trabajo pendiente o fallido de processing_jobs.
type JobSnapshot struct {
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"lastError,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// QuerySnapshot resume una consulta reciente: cuántas fuentes recuperó y con
// qué afinidad máxima (0 fuentes = la respuesta se generó sin contexto).
type QuerySnapshot struct {
	Question    string    `json:"question"`
	Model       string    `json:"model"`
	CreatedAt   time.Time `json:"createdAt"`
	Sources     int       `json:"sources"`
	TopSim      float64   `json:"topSimilarity"`
	HasResponse bool      `json:"hasResponse"`
}

// CacheSnapshot agrupa la cache semántica por generación de la base: las
// entradas con kbVersion menor a la actual ya no son elegibles.
type CacheSnapshot struct {
	KBVersion int64 `json:"kbVersion"`
	Entries   int   `json:"entries"`
	Hits      int   `json:"hits"`
}

// DebugSnapshot recolecta el estado; cada bloque es best-effort pero un fallo
// de SQL se devuelve para no depurar con datos a medias.
func (s *Store) DebugSnapshot(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{
		Documents: []DocSnapshot{},
		Jobs:      []JobSnapshot{},
		Queries:   []QuerySnapshot{},
		Cache:     []CacheSnapshot{},
	}
	if v, err := s.KBVersion(ctx); err == nil {
		snap.KBVersion = v
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT d.file_name, d.status, NVL(d.page_count, 0), NVL(d.error_message, ' '),
		       NVL(c.cnt, 0), NVL(c.minc, 0), NVL(c.avgc, 0), NVL(c.maxc, 0), NVL(c.total, 0)
		FROM documents d
		LEFT JOIN (
			SELECT document_id, COUNT(*) cnt,
			       MIN(LENGTH(chunk_text)) minc,
			       ROUND(AVG(LENGTH(chunk_text))) avgc,
			       MAX(LENGTH(chunk_text)) maxc,
			       SUM(LENGTH(chunk_text)) total
			FROM document_chunks GROUP BY document_id
		) c ON c.document_id = d.document_id
		ORDER BY d.uploaded_at DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d DocSnapshot
		if err := rows.Scan(&d.FileName, &d.Status, &d.Pages, &d.Error,
			&d.Chunks, &d.MinChunk, &d.AvgChunk, &d.MaxChunk, &d.TotalText); err != nil {
			rows.Close()
			return nil, err
		}
		d.Error = strings.TrimSpace(d.Error)
		snap.Documents = append(snap.Documents, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT job_type, status, attempts, NVL(last_error, ' '), created_at
		FROM processing_jobs
		WHERE status IN ('QUEUED', 'RUNNING', 'FAILED')
		ORDER BY created_at DESC
		FETCH FIRST 20 ROWS ONLY`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var j JobSnapshot
		if err := rows.Scan(&j.Type, &j.Status, &j.Attempts, &j.LastError, &j.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		j.LastError = strings.TrimSpace(j.LastError)
		snap.Jobs = append(snap.Jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT q.query_text, NVL(q.llm_model, ' '), q.created_at,
		       CASE WHEN q.response_text IS NULL THEN 0 ELSE 1 END,
		       (SELECT COUNT(*) FROM rag_retrieved_chunks r WHERE r.query_id = q.query_id),
		       (SELECT NVL(MAX(r.similarity_score), 0) FROM rag_retrieved_chunks r WHERE r.query_id = q.query_id)
		FROM rag_queries q
		ORDER BY q.created_at DESC
		FETCH FIRST 10 ROWS ONLY`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q QuerySnapshot
		var hasResp int
		if err := rows.Scan(&q.Question, &q.Model, &q.CreatedAt, &hasResp, &q.Sources, &q.TopSim); err != nil {
			rows.Close()
			return nil, err
		}
		q.HasResponse = hasResp == 1
		q.Model = strings.TrimSpace(q.Model)
		if r := []rune(q.Question); len(r) > 200 {
			q.Question = string(r[:200]) + "…"
		}
		snap.Queries = append(snap.Queries, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT kb_version, COUNT(*), NVL(SUM(hit_count), 0)
		FROM rag_semantic_cache GROUP BY kb_version ORDER BY kb_version`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c CacheSnapshot
		if err := rows.Scan(&c.KBVersion, &c.Entries, &c.Hits); err != nil {
			rows.Close()
			return nil, err
		}
		snap.Cache = append(snap.Cache, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM embedding_cache),
		       (SELECT COUNT(*) FROM conversation_attachments),
		       (SELECT COUNT(*) FROM attachment_chunks)
		FROM dual`).Scan(&snap.EmbedCacheEntries, &snap.Attachments, &snap.AttachmentChunks); err != nil {
		return nil, err
	}
	return snap, nil
}
