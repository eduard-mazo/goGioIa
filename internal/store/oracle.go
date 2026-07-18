// Package store implementa el vector store del RAG sobre Oracle 23ai usando
// el driver puro Go go-ora (sin CGO, coherente con el binario estático).
// Los vectores se escriben/consultan vía TO_VECTOR/VECTOR_DISTANCE, de modo
// que no se depende del soporte nativo de VECTOR del driver.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"goGioIa/internal/config"
)

// Store encapsula la conexión a Oracle y las operaciones del RAG.
type Store struct {
	db *sql.DB

	mu    sync.Mutex
	ready bool
}

// Open crea el pool de conexiones (perezoso: no marca hasta el primer uso).
func Open(cfg config.Config) (*Store, error) {
	url := go_ora.BuildUrl(cfg.OracleHost, cfg.OraclePort, "", cfg.OracleUser, cfg.OraclePassword, map[string]string{
		"SID": cfg.OracleSID,
		// Trae los LOB en la misma respuesta: permite escanear CLOB a string.
		"lob fetch": "pre",
	})
	db, err := sql.Open("oracle", url)
	if err != nil {
		return nil, fmt.Errorf("abrir conexión Oracle: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	return &Store{db: db}, nil
}

// Close libera el pool.
func (s *Store) Close() error { return s.db.Close() }

// Ping comprueba la conectividad con la base de datos.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
}

// EnsureReady garantiza (una sola vez) que el esquema existe. Es tolerante a
// arrancar con Oracle caído: cada petición reintenta hasta lograrlo.
func (s *Store) EnsureReady(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return nil
	}
	if err := s.Ping(ctx); err != nil {
		return fmt.Errorf("oracle no disponible: %w", err)
	}
	for _, ddl := range schemaDDL {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			if isAlreadyExists(err) {
				continue
			}
			return fmt.Errorf("bootstrap del esquema: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, vectorIndexDDL); err != nil && !isAlreadyExists(err) {
		// Sin índice la búsqueda vectorial sigue funcionando (exacta).
		log.Printf("aviso: no se pudo crear el índice vectorial (¿vector_memory_size?): %v", err)
	}
	if err := s.seedTemplate(ctx); err != nil {
		return fmt.Errorf("sembrar plantilla de prompt: %w", err)
	}
	s.ready = true
	log.Printf("oracle: esquema RAG verificado")
	return nil
}

// isAlreadyExists reconoce «ORA-00955: name is already used by an existing object».
func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ORA-00955")
}

func (s *Store) seedTemplate(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM prompt_templates WHERE name = :1`, defaultTemplateName).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO prompt_templates (template_id, name, version, template_text, is_active)
		 VALUES (:1, :2, 1, :3, 'Y')`,
		newID(), defaultTemplateName, clob(defaultTemplateText))
	return err
}

// ── Identificadores RAW(16) ──────────────────────────────────────────────

// newID genera 16 bytes aleatorios para columnas RAW(16).
func newID() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

// ParseID convierte el id hexadecimal expuesto por la API a RAW(16).
func ParseID(hexID string) ([]byte, error) {
	b, err := hex.DecodeString(strings.ToLower(hexID))
	if err != nil || len(b) != 16 {
		return nil, fmt.Errorf("identificador inválido")
	}
	return b, nil
}

// SessionID deriva un RAW(16) estable a partir del id de sesión del cliente.
func SessionID(clientID string) []byte {
	if clientID == "" {
		return newID()
	}
	sum := sha256.Sum256([]byte(clientID))
	return sum[:16]
}

// derivedID genera un RAW(16) determinista para páginas y chunks: reintentos
// e ingestas reanudadas apuntan siempre a la misma fila (sin duplicados).
func derivedID(kind string, docID []byte, n int) []byte {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write(docID)
	fmt.Fprintf(h, ":%d", n)
	return h.Sum(nil)[:16]
}

// clob envuelve un string como CLOB de entrada. go-ora v2.9 exige Valid=true:
// sin él bindea NULL aunque String tenga contenido (ORA-01400 en NOT NULL).
func clob(s string) go_ora.Clob {
	return go_ora.Clob{String: s, Valid: true}
}

// vecLiteral serializa un embedding al literal que acepta TO_VECTOR: [x,y,...].
// Se envía como CLOB porque 768 floats superan los 4000 bytes de VARCHAR2.
func vecLiteral(v []float32) go_ora.Clob {
	var b strings.Builder
	b.Grow(len(v) * 12)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return clob(b.String())
}

// ── Documentos ───────────────────────────────────────────────────────────

// Estados del ciclo de vida de un documento.
const (
	StatusUploaded   = "UPLOADED"
	StatusExtracting = "EXTRACTING"
	StatusChunked    = "CHUNKED"
	StatusEmbedded   = "EMBEDDED"
	StatusFailed     = "FAILED"
)

// Document es la vista de la tabla documents que consume la API.
type Document struct {
	ID          string     `json:"id"`
	FileName    string     `json:"fileName"`
	SizeBytes   int64      `json:"sizeBytes"`
	PageCount   int        `json:"pageCount"`
	ChunkCount  int        `json:"chunkCount"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	UploadedBy  string     `json:"uploadedBy,omitempty"`
	UploadedAt  time.Time  `json:"uploadedAt"`
	ProcessedAt *time.Time `json:"processedAt,omitempty"`
}

// FindDocumentByHash devuelve el documento con ese SHA-256, o nil si no existe.
func (s *Store) FindDocumentByHash(ctx context.Context, hash string) (*Document, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT RAWTOHEX(document_id), file_name, status, NVL(error_message, ' ')
		FROM documents WHERE file_hash = :1`, hash)
	var d Document
	var errMsg string
	if err := row.Scan(&d.ID, &d.FileName, &d.Status, &errMsg); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	d.ID = strings.ToLower(d.ID)
	d.Error = strings.TrimSpace(errMsg)
	return &d, nil
}

// CreateDocument inserta la fila inicial (status UPLOADED) y devuelve su id.
func (s *Store) CreateDocument(ctx context.Context, fileName, hash string, size int64, uploadedBy string) ([]byte, error) {
	id := newID()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO documents (document_id, file_name, file_hash, mime_type, file_size_bytes, status, uploaded_by)
		VALUES (:1, :2, :3, 'application/pdf', :4, :5, :6)`,
		id, fileName, hash, size, StatusUploaded, uploadedBy)
	if err != nil {
		return nil, err
	}
	return id, nil
}

// SetDocumentStatus actualiza el estado (y opcionalmente el error) de un documento.
func (s *Store) SetDocumentStatus(ctx context.Context, id []byte, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents SET status = :1, error_message = :2 WHERE document_id = :3`,
		status, nullable(errMsg), id)
	return err
}

// FinishDocument marca el documento como EMBEDDED con su nº de páginas.
func (s *Store) FinishDocument(ctx context.Context, id []byte, pageCount int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents
		SET status = :1, page_count = :2, processed_at = SYSTIMESTAMP, error_message = NULL
		WHERE document_id = :3`,
		StatusEmbedded, pageCount, id)
	return err
}

// ListDocuments devuelve todos los documentos, más recientes primero.
func (s *Store) ListDocuments(ctx context.Context) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT RAWTOHEX(d.document_id), d.file_name, NVL(d.file_size_bytes, 0),
		       NVL(d.page_count, 0), d.status, NVL(d.error_message, ' '),
		       NVL(d.uploaded_by, ' '), d.uploaded_at, d.processed_at,
		       (SELECT COUNT(*) FROM document_chunks c WHERE c.document_id = d.document_id)
		FROM documents d
		ORDER BY d.uploaded_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	docs := []Document{}
	for rows.Next() {
		var d Document
		var errMsg, by string
		var processed sql.NullTime
		if err := rows.Scan(&d.ID, &d.FileName, &d.SizeBytes, &d.PageCount, &d.Status,
			&errMsg, &by, &d.UploadedAt, &processed, &d.ChunkCount); err != nil {
			return nil, err
		}
		d.ID = strings.ToLower(d.ID)
		d.Error = strings.TrimSpace(errMsg)
		d.UploadedBy = strings.TrimSpace(by)
		if processed.Valid {
			t := processed.Time
			d.ProcessedAt = &t
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// DeleteDocument elimina el documento y sus dependencias. Las páginas y chunks
// caen por ON DELETE CASCADE, pero rag_retrieved_chunks referencia los chunks
// sin CASCADE, así que se limpia primero para no violar la FK.
func (s *Store) DeleteDocument(ctx context.Context, id []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM rag_retrieved_chunks
		WHERE chunk_id IN (SELECT chunk_id FROM document_chunks WHERE document_id = :1)`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE document_id = :1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// InsertPage guarda el texto extraído de una página. Upsert por
// (document_id, page_number) con id determinista: re-extraer un documento al
// reanudar una ingesta fallida no viola uq_doc_page ni duplica filas.
func (s *Store) InsertPage(ctx context.Context, docID []byte, pageNumber int, text string) error {
	_, err := s.db.ExecContext(ctx, `
		MERGE INTO document_pages p
		USING (SELECT :1 AS document_id, :2 AS page_number FROM dual) src
		ON (p.document_id = src.document_id AND p.page_number = src.page_number)
		WHEN MATCHED THEN UPDATE SET p.raw_text = :3
		WHEN NOT MATCHED THEN INSERT (page_id, document_id, page_number, raw_text)
		VALUES (:4, :5, :6, :7)`,
		docID, pageNumber, clob(text),
		derivedID("page", docID, pageNumber), docID, pageNumber, clob(text))
	return err
}

// InsertChunk guarda un chunk con su embedding (literal → TO_VECTOR). Upsert
// por (document_id, chunk_index) con id determinista: reintentar un lote tras
// un fallo transitorio nunca duplica vectores.
func (s *Store) InsertChunk(ctx context.Context, docID []byte, index, page int, text string, tokenCount int, embedding []float32, model string) error {
	vec := vecLiteral(embedding)
	_, err := s.db.ExecContext(ctx, `
		MERGE INTO document_chunks c
		USING (SELECT :1 AS document_id, :2 AS chunk_index FROM dual) src
		ON (c.document_id = src.document_id AND c.chunk_index = src.chunk_index)
		WHEN MATCHED THEN UPDATE SET
		  c.page_number = :3, c.chunk_text = :4, c.token_count = :5,
		  c.embedding = TO_VECTOR(:6), c.embedding_model = :7, c.embedded_at = SYSTIMESTAMP
		WHEN NOT MATCHED THEN INSERT
		  (chunk_id, document_id, chunk_index, page_number, chunk_text, token_count,
		   embedding, embedding_model, embedded_at)
		VALUES (:8, :9, :10, :11, :12, :13, TO_VECTOR(:14), :15, SYSTIMESTAMP)`,
		docID, index, page, clob(text), tokenCount, vec, model,
		derivedID("chunk", docID, index), docID, index, page, clob(text), tokenCount, vec, model)
	return err
}

// EmbeddedChunkIndexes devuelve los índices de chunk del documento que ya
// tienen embedding persistido, para reanudar una ingesta sin repetir trabajo.
func (s *Store) EmbeddedChunkIndexes(ctx context.Context, docID []byte) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT chunk_index FROM document_chunks
		WHERE document_id = :1 AND embedding IS NOT NULL`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	done := map[int]bool{}
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			return nil, err
		}
		done[idx] = true
	}
	return done, rows.Err()
}

// DeleteChunksFrom elimina los chunks con índice >= fromIndex: restos de un
// intento anterior troceado con otra configuración. Limpia primero
// rag_retrieved_chunks porque su FK a document_chunks no tiene CASCADE.
func (s *Store) DeleteChunksFrom(ctx context.Context, docID []byte, fromIndex int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM rag_retrieved_chunks WHERE chunk_id IN (
			SELECT chunk_id FROM document_chunks
			WHERE document_id = :1 AND chunk_index >= :2)`,
		docID, fromIndex); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM document_chunks WHERE document_id = :1 AND chunk_index >= :2`,
		docID, fromIndex); err != nil {
		return err
	}
	return tx.Commit()
}

// ── Búsqueda vectorial ───────────────────────────────────────────────────

// SearchResult es un chunk recuperado por similitud coseno.
type SearchResult struct {
	ChunkID    string  `json:"chunkId"`
	DocumentID string  `json:"documentId"`
	FileName   string  `json:"fileName"`
	ChunkIndex int     `json:"chunkIndex"`
	PageNumber int     `json:"page"`
	Text       string  `json:"text"`
	Similarity float64 `json:"score"`

	chunkIDRaw []byte
}

// SearchChunks devuelve los topK chunks más cercanos al embedding de la consulta.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int) ([]SearchResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT RAWTOHEX(c.chunk_id), RAWTOHEX(c.document_id), d.file_name,
		       c.chunk_index, NVL(c.page_number, 0), c.chunk_text,
		       VECTOR_DISTANCE(c.embedding, TO_VECTOR(:1), COSINE) AS dist
		FROM document_chunks c
		JOIN documents d ON d.document_id = c.document_id
		WHERE c.embedding IS NOT NULL
		ORDER BY dist
		FETCH FIRST :2 ROWS ONLY`,
		vecLiteral(embedding), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		var dist float64
		if err := rows.Scan(&r.ChunkID, &r.DocumentID, &r.FileName,
			&r.ChunkIndex, &r.PageNumber, &r.Text, &dist); err != nil {
			return nil, err
		}
		r.ChunkID = strings.ToLower(r.ChunkID)
		r.DocumentID = strings.ToLower(r.DocumentID)
		r.Similarity = 1 - dist
		r.chunkIDRaw, _ = ParseID(r.ChunkID)
		results = append(results, r)
	}
	return results, rows.Err()
}

// ── Trazabilidad del RAG (rag_queries / rag_retrieved_chunks / rag_feedback) ──

// CreateQuery registra la pregunta (con su embedding) y devuelve el query_id.
func (s *Store) CreateQuery(ctx context.Context, sessionID []byte, userID, question string, embedding []float32, llmModel string, templateID []byte) ([]byte, error) {
	id := newID()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rag_queries
		  (query_id, session_id, user_id, query_text, query_embedding, llm_model, prompt_template_id)
		VALUES (:1, :2, :3, :4, TO_VECTOR(:5), :6, :7)`,
		id, sessionID, nullable(userID), clob(question),
		vecLiteral(embedding), llmModel, templateID)
	if err != nil {
		return nil, err
	}
	return id, nil
}

// LogRetrievedChunks registra qué chunks alimentaron el prompt y con qué score.
func (s *Store) LogRetrievedChunks(ctx context.Context, queryID []byte, results []SearchResult) error {
	for i, r := range results {
		if r.chunkIDRaw == nil {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO rag_retrieved_chunks (query_id, chunk_id, rank_position, similarity_score, was_used_in_prompt)
			VALUES (:1, :2, :3, :4, 'Y')`,
			queryID, r.chunkIDRaw, i+1, r.Similarity); err != nil {
			return err
		}
	}
	return nil
}

// SetQueryResponse completa la fila de rag_queries con la respuesta generada.
func (s *Store) SetQueryResponse(ctx context.Context, queryID []byte, response string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE rag_queries SET response_text = :1 WHERE query_id = :2`,
		clob(response), queryID)
	return err
}

// InsertFeedback guarda la valoración del usuario sobre una respuesta.
func (s *Store) InsertFeedback(ctx context.Context, queryID []byte, rating int, text, createdBy string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rag_feedback (feedback_id, query_id, rating, feedback_text, created_by)
		VALUES (:1, :2, :3, :4, :5)`,
		newID(), queryID, rating, nullable(text), nullable(createdBy))
	return err
}

// ActiveTemplate devuelve la plantilla activa más reciente con ese nombre
// (vacío → la plantilla por defecto sembrada en el bootstrap).
func (s *Store) ActiveTemplate(ctx context.Context, name string) (id []byte, text string, err error) {
	if name == "" {
		name = defaultTemplateName
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT template_id, template_text FROM prompt_templates
		WHERE name = :1 AND is_active = 'Y'
		ORDER BY version DESC
		FETCH FIRST 1 ROWS ONLY`, name)
	if err = row.Scan(&id, &text); err != nil {
		return nil, "", err
	}
	return id, text, nil
}

// Stats devuelve contadores globales para el health del RAG.
func (s *Store) Stats(ctx context.Context) (docs, chunks int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM documents),
		       (SELECT COUNT(*) FROM document_chunks WHERE embedding IS NOT NULL)
		FROM dual`).Scan(&docs, &chunks)
	return docs, chunks, err
}

// nullable convierte "" en NULL para columnas opcionales.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
