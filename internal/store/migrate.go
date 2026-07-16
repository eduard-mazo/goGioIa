package store

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// migration agrupa las sentencias de una versión del esquema. Una migración
// aplicada nunca se edita: cualquier cambio posterior es una versión nueva.
type migration struct {
	version     int
	description string
	statements  []string
}

// migrationsTableDDL registra qué versiones ya se aplicaron.
const migrationsTableDDL = `CREATE TABLE rag_schema_migrations (
    version     NUMBER PRIMARY KEY,
    description VARCHAR2(200 CHAR) NOT NULL,
    applied_at  TIMESTAMP DEFAULT SYSTIMESTAMP
)`

// migrations es el historial completo del esquema RAG, en orden.
var migrations = []migration{
	{
		version:     1,
		description: "esquema base RAG (documents, pages, chunks, rag_*, prompt_templates)",
		statements:  baseSchemaDDL,
	},
	{
		version:     2,
		description: "hardening: índices en FKs, CHECKs, semántica CHAR y feedback único",
		statements: []string{
			// Oracle no indexa las FKs automáticamente. Sin estos índices,
			// DeleteDocument hace full scan sobre rag_retrieved_chunks y el
			// DML del padre toma locks de tabla en las hijas.
			`CREATE INDEX idx_retrieved_chunk ON rag_retrieved_chunks(chunk_id)`,
			`CREATE INDEX idx_feedback_query ON rag_feedback(query_id)`,
			`CREATE INDEX idx_queries_template ON rag_queries(prompt_template_id)`,
			// Acceso natural al historial de consultas: por sesión y fecha.
			`CREATE INDEX idx_queries_session ON rag_queries(session_id, created_at)`,

			// Estados y banderas dejan de ser texto libre.
			`ALTER TABLE documents ADD CONSTRAINT ck_documents_status
			   CHECK (status IN ('UPLOADED','EXTRACTING','CHUNKED','EMBEDDED','FAILED'))`,
			`ALTER TABLE rag_feedback ADD CONSTRAINT ck_feedback_rating
			   CHECK (rating BETWEEN -1 AND 1)`,
			`ALTER TABLE prompt_templates ADD CONSTRAINT ck_template_active
			   CHECK (is_active IN ('Y','N'))`,
			`ALTER TABLE rag_retrieved_chunks ADD CONSTRAINT ck_retrieved_used
			   CHECK (was_used_in_prompt IN ('Y','N'))`,

			// Texto humano en caracteres, no bytes (tildes/eñes en AL32UTF8).
			`ALTER TABLE documents MODIFY (file_name VARCHAR2(500 CHAR))`,
			`ALTER TABLE documents MODIFY (error_message VARCHAR2(4000 CHAR))`,
			`ALTER TABLE rag_feedback MODIFY (feedback_text VARCHAR2(2000 CHAR))`,

			// Una valoración por usuario y respuesta. Antes del UNIQUE se
			// depuran los duplicados históricos conservando la más reciente.
			`DELETE FROM rag_feedback a
			 WHERE EXISTS (
			   SELECT 1 FROM rag_feedback b
			   WHERE b.query_id = a.query_id
			     AND NVL(b.created_by, '~') = NVL(a.created_by, '~')
			     AND (b.created_at > a.created_at
			          OR (b.created_at = a.created_at
			              AND RAWTOHEX(b.feedback_id) > RAWTOHEX(a.feedback_id))))`,
			`ALTER TABLE rag_feedback ADD CONSTRAINT uq_feedback_query_user
			   UNIQUE (query_id, created_by)`,
		},
	},
	{
		version:     3,
		description: "cola durable de trabajos (processing_jobs) y archivos en proceso (document_files)",
		statements: []string{
			`CREATE TABLE processing_jobs (
			    job_id      RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
			    job_type    VARCHAR2(30) NOT NULL,
			    payload_id  RAW(16) NOT NULL,
			    status      VARCHAR2(20) DEFAULT 'QUEUED' NOT NULL,
			    attempts    NUMBER DEFAULT 0 NOT NULL,
			    last_error  VARCHAR2(4000 CHAR),
			    created_at  TIMESTAMP DEFAULT SYSTIMESTAMP,
			    started_at  TIMESTAMP,
			    finished_at TIMESTAMP,
			    CONSTRAINT ck_job_type CHECK (job_type IN
			      ('ingest_document','embed_attachment','summarize_conversation','purge_history')),
			    CONSTRAINT ck_job_status CHECK (status IN ('QUEUED','RUNNING','DONE','FAILED'))
			)`,
			`CREATE INDEX idx_jobs_pending ON processing_jobs(status, created_at)`,
			`CREATE INDEX idx_jobs_payload ON processing_jobs(payload_id)`,
			// El archivo original vive aquí mientras se procesa: permite
			// reanudar la ingesta tras un reinicio y reintentar fallos sin
			// volver a subirlo. Se borra al completar la ingesta.
			`CREATE TABLE document_files (
			    document_id RAW(16) PRIMARY KEY
			                REFERENCES documents(document_id) ON DELETE CASCADE,
			    content     BLOB NOT NULL
			)`,
		},
	},
	{
		version:     4,
		description: "conversaciones y mensajes (contexto server-side)",
		statements: []string{
			// conv_mode y no «mode»: MODE es palabra reservada (ORA-03050).
			`CREATE TABLE conversations (
			    conversation_id RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
			    session_id      RAW(16) NOT NULL,
			    user_id         VARCHAR2(100 CHAR),
			    title           VARCHAR2(200 CHAR),
			    conv_mode       VARCHAR2(10) DEFAULT 'chat' NOT NULL,
			    llm_model       VARCHAR2(50),
			    summary         CLOB,
			    summary_upto    NUMBER DEFAULT 0 NOT NULL,
			    created_at      TIMESTAMP DEFAULT SYSTIMESTAMP,
			    updated_at      TIMESTAMP DEFAULT SYSTIMESTAMP,
			    CONSTRAINT ck_conv_mode CHECK (conv_mode IN ('chat','rag'))
			)`,
			`CREATE INDEX idx_conv_session ON conversations(session_id, updated_at)`,
			// query_id enlaza la respuesta con su trazabilidad RAG; SET NULL
			// para que la purga de retención de rag_queries no choque con la FK.
			`CREATE TABLE conversation_messages (
			    message_id      RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
			    conversation_id RAW(16) NOT NULL
			                    REFERENCES conversations(conversation_id) ON DELETE CASCADE,
			    seq             NUMBER NOT NULL,
			    role            VARCHAR2(20) NOT NULL,
			    content         CLOB NOT NULL,
			    query_id        RAW(16) REFERENCES rag_queries(query_id) ON DELETE SET NULL,
			    created_at      TIMESTAMP DEFAULT SYSTIMESTAMP,
			    CONSTRAINT uq_conv_seq UNIQUE (conversation_id, seq),
			    CONSTRAINT ck_msg_role CHECK (role IN ('user','assistant'))
			)`,
			`CREATE INDEX idx_msg_query ON conversation_messages(query_id)`,
		},
	},
	{
		version:     5,
		description: "anexos de conversación por referencia (+ chunks vectorizados)",
		statements: []string{
			// El texto extraído del anexo se guarda una vez; los prompts lo
			// referencian por id en vez de reenviarlo en cada petición.
			`CREATE TABLE conversation_attachments (
			    attachment_id   RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
			    conversation_id RAW(16) NOT NULL
			                    REFERENCES conversations(conversation_id) ON DELETE CASCADE,
			    file_name       VARCHAR2(500 CHAR) NOT NULL,
			    file_hash       VARCHAR2(64) NOT NULL,
			    mime_type       VARCHAR2(100),
			    char_count      NUMBER,
			    content         CLOB NOT NULL,
			    created_at      TIMESTAMP DEFAULT SYSTIMESTAMP,
			    CONSTRAINT uq_conv_attach UNIQUE (conversation_id, file_hash)
			)`,
			// Anexos grandes: troceados y vectorizados para recuperar solo lo
			// relevante en cada pregunta (mismo retrieval que la base de
			// conocimiento, acotado por attachment_id).
			`CREATE TABLE attachment_chunks (
			    chunk_id      RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
			    attachment_id RAW(16) NOT NULL
			                  REFERENCES conversation_attachments(attachment_id) ON DELETE CASCADE,
			    chunk_index   NUMBER NOT NULL,
			    chunk_text    CLOB NOT NULL,
			    embedding     VECTOR(768, FLOAT32),
			    CONSTRAINT uq_attach_chunk UNIQUE (attachment_id, chunk_index)
			)`,
		},
	},
	{
		version:     6,
		description: "cache semántica de respuestas, cache de embeddings y versión de la base",
		statements: []string{
			// sources_json es CLOB con CHECK IS JSON (y no el tipo JSON
			// nativo/OSON) para no depender del soporte del driver go-ora.
			`CREATE TABLE rag_semantic_cache (
			    cache_id           RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
			    question_hash      VARCHAR2(64) NOT NULL,
			    question_text      CLOB NOT NULL,
			    question_embedding VECTOR(768, FLOAT32) NOT NULL,
			    llm_model          VARCHAR2(50) NOT NULL,
			    template_id        RAW(16) REFERENCES prompt_templates(template_id),
			    kb_version         NUMBER NOT NULL,
			    response_text      CLOB NOT NULL,
			    sources_json       CLOB CHECK (sources_json IS JSON),
			    hit_count          NUMBER DEFAULT 0 NOT NULL,
			    created_at         TIMESTAMP DEFAULT SYSTIMESTAMP,
			    last_hit_at        TIMESTAMP,
			    expires_at         TIMESTAMP
			)`,
			`CREATE INDEX idx_cache_lookup ON rag_semantic_cache(question_hash, llm_model, kb_version)`,
			`CREATE INDEX idx_cache_expiry ON rag_semantic_cache(expires_at)`,
			`CREATE INDEX idx_cache_template ON rag_semantic_cache(template_id)`,

			// Versión de la base de conocimiento: cada ingesta/borrado la
			// incrementa e invalida (por clave) las respuestas cacheadas.
			`CREATE TABLE rag_kb_state (
			    id      NUMBER PRIMARY KEY,
			    version NUMBER NOT NULL,
			    CONSTRAINT ck_kb_singleton CHECK (id = 1)
			)`,
			`INSERT INTO rag_kb_state (id, version) VALUES (1, 1)`,

			// Texto idéntico no se re-vectoriza: sha256(modelo‖prefijo‖texto).
			`CREATE TABLE embedding_cache (
			    text_hash    VARCHAR2(64) PRIMARY KEY,
			    model        VARCHAR2(100) NOT NULL,
			    embedding    VECTOR(768, FLOAT32) NOT NULL,
			    use_count    NUMBER DEFAULT 1 NOT NULL,
			    created_at   TIMESTAMP DEFAULT SYSTIMESTAMP,
			    last_used_at TIMESTAMP DEFAULT SYSTIMESTAMP
			)`,
		},
	},
}

// tolerableORA son los errores «ya existe / ya aplicado». El DDL de Oracle
// hace auto-commit sentencia a sentencia, así que una migración interrumpida
// solo puede repararse reintentándola completa: cada sentencia debe tolerar
// haberse aplicado ya. También cubre dos instancias migrando a la vez.
var tolerableORA = []string{
	"ORA-00955", // el nombre ya lo usa otro objeto (tablas, índices)
	"ORA-00001", // unique violado (fila de versión ya registrada)
	"ORA-01430", // la columna a añadir ya existe
	"ORA-02260", // la tabla ya tiene clave primaria
	"ORA-02261", // la clave única ya existe en la tabla
	"ORA-02264", // el nombre de constraint ya está en uso
	"ORA-02275", // la referential constraint ya existe
}

// isTolerable indica si el error significa «esto ya estaba aplicado».
func isTolerable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range tolerableORA {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// migrate lleva el esquema a la última versión. Las instalaciones anteriores
// al versionado (tablas creadas por el bootstrap original) quedan absorbidas
// por la migración 1 gracias a la tolerancia de «ya existe».
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, migrationsTableDDL); err != nil && !isTolerable(err) {
		return fmt.Errorf("crear rag_schema_migrations: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx,
		`SELECT NVL(MAX(version), 0) FROM rag_schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("leer versión del esquema: %w", err)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		for _, stmt := range m.statements {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil && !isTolerable(err) {
				return fmt.Errorf("migración %d (%s): %w", m.version, m.description, err)
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO rag_schema_migrations (version, description) VALUES (:1, :2)`,
			m.version, m.description); err != nil && !isTolerable(err) {
			return fmt.Errorf("registrar migración %d: %w", m.version, err)
		}
		log.Printf("oracle: migración %d aplicada — %s", m.version, m.description)
	}
	return nil
}
