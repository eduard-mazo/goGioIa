package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
)

// Tipos de trabajo de processing_jobs.
const (
	JobIngestDocument        = "ingest_document"
	JobEmbedAttachment       = "embed_attachment"
	JobSummarizeConversation = "summarize_conversation"
)

// ErrNoSourceFile señala que el archivo original ya no está en document_files
// (se purga al completar la ingesta), así que no se puede reintentar.
var ErrNoSourceFile = errors.New("el archivo original ya no está disponible; vuelve a subirlo")

// Job es un trabajo reclamado de la cola.
type Job struct {
	ID        []byte
	Type      string
	PayloadID []byte
	Attempts  int
}

// EnqueueJob inserta un trabajo QUEUED en la cola.
func (s *Store) EnqueueJob(ctx context.Context, jobType string, payloadID []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO processing_jobs (job_id, job_type, payload_id)
		VALUES (:1, :2, :3)`,
		newID(), jobType, payloadID)
	return err
}

// EnqueueJobOnce encola el trabajo solo si no hay ya uno pendiente o en
// ejecución del mismo tipo para el mismo payload.
func (s *Store) EnqueueJobOnce(ctx context.Context, jobType string, payloadID []byte) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var pending int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM processing_jobs
		WHERE payload_id = :1 AND job_type = :2 AND status IN ('QUEUED','RUNNING')`,
		payloadID, jobType).Scan(&pending); err != nil {
		return false, err
	}
	if pending > 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO processing_jobs (job_id, job_type, payload_id)
		VALUES (:1, :2, :3)`,
		newID(), jobType, payloadID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ClaimJob toma el trabajo QUEUED más antiguo de los tipos dados y lo marca
// RUNNING. Devuelve nil si la cola está vacía. FOR UPDATE SKIP LOCKED evita
// que dos workers reclamen el mismo trabajo.
func (s *Store) ClaimJob(ctx context.Context, jobTypes ...string) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	args := make([]any, len(jobTypes))
	for i, t := range jobTypes {
		args[i] = t
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT job_id, job_type, payload_id, attempts
		FROM processing_jobs
		WHERE status = 'QUEUED' AND job_type IN (%s)
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED`, bindList(1, len(jobTypes))), args...)
	if err != nil {
		return nil, err
	}
	var job *Job
	if rows.Next() {
		job = &Job{}
		if err := rows.Scan(&job.ID, &job.Type, &job.PayloadID, &job.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if job == nil {
		return nil, nil
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE processing_jobs
		SET status = 'RUNNING', attempts = attempts + 1,
		    started_at = SYSTIMESTAMP, last_error = NULL
		WHERE job_id = :1`, job.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job.Attempts++
	return job, nil
}

// FinishJob cierra el trabajo: DONE si errMsg está vacío, FAILED con el motivo
// si no.
func (s *Store) FinishJob(ctx context.Context, jobID []byte, errMsg string) error {
	status := "DONE"
	if errMsg != "" {
		status = "FAILED"
		if len(errMsg) > 3900 {
			errMsg = errMsg[:3900]
		}
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE processing_jobs
		SET status = :1, last_error = :2, finished_at = SYSTIMESTAMP
		WHERE job_id = :3`,
		status, nullable(errMsg), jobID)
	return err
}

// RequeueOrphanJobs devuelve a QUEUED los trabajos que quedaron RUNNING tras
// un reinicio (el archivo sigue en document_files, así que se reanudan solos).
// Asume una única instancia del servidor, como todo el despliegue.
func (s *Store) RequeueOrphanJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE processing_jobs SET status = 'QUEUED', started_at = NULL
		WHERE status = 'RUNNING'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PendingJobs cuenta los trabajos en cola o en ejecución (para el health).
func (s *Store) PendingJobs(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM processing_jobs
		WHERE status IN ('QUEUED','RUNNING')`).Scan(&n)
	return n, err
}

// LoadDocumentFile recupera el nombre y el contenido original de un documento
// pendiente de procesar.
func (s *Store) LoadDocumentFile(ctx context.Context, docID []byte) (fileName string, data []byte, err error) {
	var blob go_ora.Blob
	err = s.db.QueryRowContext(ctx, `
		SELECT d.file_name, f.content
		FROM documents d JOIN document_files f ON f.document_id = d.document_id
		WHERE d.document_id = :1`, docID).Scan(&fileName, &blob)
	if err != nil {
		return "", nil, err
	}
	return fileName, blob.Data, nil
}

// DeleteDocumentFile libera el BLOB original una vez ingerido el documento.
func (s *Store) DeleteDocumentFile(ctx context.Context, docID []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM document_files WHERE document_id = :1`, docID)
	return err
}

// RequeueDocument reencola la ingesta de un documento FAILED: restaura su
// estado y crea un trabajo nuevo. Falla con sql.ErrNoRows si el documento no
// existe y con ErrNoSourceFile si su archivo original ya se purgó.
func (s *Store) RequeueDocument(ctx context.Context, docID []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var hasFile int
	err = tx.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM document_files f WHERE f.document_id = d.document_id)
		FROM documents d WHERE d.document_id = :1`, docID).Scan(&hasFile)
	if err != nil {
		return err // incluye sql.ErrNoRows: documento inexistente
	}
	if hasFile == 0 {
		return ErrNoSourceFile
	}
	// Evitar trabajos duplicados si ya hay uno pendiente para este documento.
	var pending int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM processing_jobs
		WHERE payload_id = :1 AND status IN ('QUEUED','RUNNING')`, docID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE documents SET status = :1, error_message = NULL WHERE document_id = :2`,
		StatusUploaded, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO processing_jobs (job_id, job_type, payload_id)
		VALUES (:1, :2, :3)`,
		newID(), JobIngestDocument, docID); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetDocumentContent elimina páginas y chunks de un documento (y las
// referencias de trazabilidad a esos chunks) para poder reingerirlo limpio.
func (s *Store) ResetDocumentContent(ctx context.Context, docID []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM rag_retrieved_chunks
		WHERE chunk_id IN (SELECT chunk_id FROM document_chunks WHERE document_id = :1)`, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = :1`, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_pages WHERE document_id = :1`, docID); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeOldQueries elimina la trazabilidad más antigua que la retención dada
// (rag_retrieved_chunks y rag_feedback caen por ON DELETE CASCADE).
func (s *Store) PurgeOldQueries(ctx context.Context, retention time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM rag_queries WHERE created_at < SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'SECOND')`,
		int64(retention.Seconds()))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
