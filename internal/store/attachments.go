package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Attachment es un anexo de conversación persistido (texto ya extraído).
type Attachment struct {
	ID        string
	FileName  string
	CharCount int
	Content   string
	// Chunks > 0 significa que el anexo está vectorizado y se consulta por
	// retrieval en vez de inyectarse completo.
	Chunks int
}

// CreateAttachment guarda el anexo. Si el mismo contenido ya estaba en la
// conversación (uq_conv_attach), devuelve el id existente con existed=true.
func (s *Store) CreateAttachment(ctx context.Context, convID []byte, fileName, hash, mimeType, content string) (id []byte, existed bool, err error) {
	id = newID()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO conversation_attachments
		  (attachment_id, conversation_id, file_name, file_hash, mime_type, char_count, content)
		VALUES (:1, :2, :3, :4, :5, :6, :7)`,
		id, convID, fileName, hash, mimeType, len(content), clob(content))
	if err == nil {
		return id, false, nil
	}
	if !strings.Contains(err.Error(), "ORA-00001") {
		return nil, false, err
	}
	var hexID string
	err = s.db.QueryRowContext(ctx, `
		SELECT RAWTOHEX(attachment_id) FROM conversation_attachments
		WHERE conversation_id = :1 AND file_hash = :2`, convID, hash).Scan(&hexID)
	if err != nil {
		return nil, false, err
	}
	id, err = ParseID(strings.ToLower(hexID))
	return id, true, err
}

// LoadAttachment recupera un anexo con su contenido y nº de chunks vectorizados.
func (s *Store) LoadAttachment(ctx context.Context, id []byte) (*Attachment, error) {
	var a Attachment
	err := s.db.QueryRowContext(ctx, `
		SELECT RAWTOHEX(a.attachment_id), a.file_name, NVL(a.char_count, 0), a.content,
		       (SELECT COUNT(*) FROM attachment_chunks c
		        WHERE c.attachment_id = a.attachment_id AND c.embedding IS NOT NULL)
		FROM conversation_attachments a
		WHERE a.attachment_id = :1`, id).
		Scan(&a.ID, &a.FileName, &a.CharCount, &a.Content, &a.Chunks)
	if err != nil {
		return nil, err
	}
	a.ID = strings.ToLower(a.ID)
	return &a, nil
}

// DeleteAttachment elimina el anexo (y sus chunks por CASCADE), verificando
// que pertenece a la conversación indicada.
func (s *Store) DeleteAttachment(ctx context.Context, convID, attID []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		DELETE FROM conversation_attachments
		WHERE attachment_id = :1 AND conversation_id = :2`, attID, convID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	// Sin el anexo, su vectorización pendiente ya no tiene sentido (y fallaría
	// con ORA-02291 al insertar chunks de un padre inexistente).
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM processing_jobs
		WHERE payload_id = :1 AND job_type = :2 AND status = 'QUEUED'`,
		attID, JobEmbedAttachment); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearAttachmentChunks descarta los chunks de un anexo (revectorización).
func (s *Store) ClearAttachmentChunks(ctx context.Context, attID []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM attachment_chunks WHERE attachment_id = :1`, attID)
	return err
}

// InsertAttachmentChunk guarda un chunk vectorizado de un anexo.
func (s *Store) InsertAttachmentChunk(ctx context.Context, attID []byte, index int, text string, embedding []float32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO attachment_chunks (chunk_id, attachment_id, chunk_index, chunk_text, embedding)
		VALUES (:1, :2, :3, :4, TO_VECTOR(:5))`,
		newID(), attID, index, clob(text), vecLiteral(embedding))
	return err
}

// SearchAttachmentChunks devuelve los topK chunks más afines a la consulta
// dentro de los anexos indicados.
func (s *Store) SearchAttachmentChunks(ctx context.Context, attIDs [][]byte, embedding []float32, topK int) ([]SearchResult, error) {
	if len(attIDs) == 0 {
		return nil, nil
	}
	args := []any{vecLiteral(embedding)}
	for _, id := range attIDs {
		args = append(args, id)
	}
	args = append(args, topK)
	query := fmt.Sprintf(`
		SELECT RAWTOHEX(c.chunk_id), RAWTOHEX(c.attachment_id), a.file_name,
		       c.chunk_index, c.chunk_text,
		       VECTOR_DISTANCE(c.embedding, TO_VECTOR(:1), COSINE) AS dist
		FROM attachment_chunks c
		JOIN conversation_attachments a ON a.attachment_id = c.attachment_id
		WHERE c.embedding IS NOT NULL AND c.attachment_id IN (%s)
		ORDER BY dist
		FETCH FIRST :%d ROWS ONLY`, bindList(2, len(attIDs)), len(attIDs)+2)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		var dist float64
		if err := rows.Scan(&r.ChunkID, &r.DocumentID, &r.FileName,
			&r.ChunkIndex, &r.Text, &dist); err != nil {
			return nil, err
		}
		r.ChunkID = strings.ToLower(r.ChunkID)
		r.DocumentID = strings.ToLower(r.DocumentID)
		r.Similarity = 1 - dist
		// chunkIDRaw queda nil a propósito: estos chunks no van a
		// rag_retrieved_chunks (su FK apunta a document_chunks).
		results = append(results, r)
	}
	return results, rows.Err()
}

// bindList genera «:start, :start+1, …» para armar cláusulas IN dinámicas.
func bindList(start, n int) string {
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprintf(":%d", start+i)
	}
	return strings.Join(parts, ", ")
}
