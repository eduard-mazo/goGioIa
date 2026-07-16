-- Esquema del RAG en Oracle 23ai (usuario: useria).
-- El backend crea estos objetos automáticamente en el primer arranque
-- (internal/store/schema.go); este archivo es la referencia manual.
--
-- Nota de diseño: se usa la «opción simple» del modelo — un único modelo de
-- embeddings (nomic-embed-text, 768 dims) con el vector como columna de
-- document_chunks. La opción normalizada (tabla chunk_embeddings) queda
-- documentada al final por si en el futuro conviven varios modelos.

-- ── Ingesta de documentos ──────────────────────────────────────────────────

CREATE TABLE documents (
    document_id     RAW(16)  DEFAULT SYS_GUID() PRIMARY KEY,
    file_name       VARCHAR2(500) NOT NULL,
    file_hash       VARCHAR2(64)  NOT NULL,     -- SHA-256, evita reprocesar el mismo PDF
    mime_type       VARCHAR2(100) DEFAULT 'application/pdf',
    file_size_bytes NUMBER,
    page_count      NUMBER,
    status          VARCHAR2(20)  DEFAULT 'UPLOADED', -- UPLOADED, EXTRACTING, CHUNKED, EMBEDDED, FAILED
    uploaded_by     VARCHAR2(100),
    uploaded_at     TIMESTAMP DEFAULT SYSTIMESTAMP,
    processed_at    TIMESTAMP,
    error_message   VARCHAR2(4000),
    CONSTRAINT uq_documents_hash UNIQUE (file_hash)
);

CREATE TABLE document_pages (
    page_id       RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    document_id   RAW(16) NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE,
    page_number   NUMBER NOT NULL,
    raw_text      CLOB,
    CONSTRAINT uq_doc_page UNIQUE (document_id, page_number)
);

-- ── Chunks + embeddings (núcleo del vector store) ──────────────────────────

CREATE TABLE document_chunks (
    chunk_id        RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    document_id     RAW(16) NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE,
    chunk_index     NUMBER NOT NULL,
    page_number     NUMBER,
    chunk_text      CLOB NOT NULL,
    token_count     NUMBER,
    embedding       VECTOR(768, FLOAT32),   -- dimensión = la del modelo de embeddings
    embedding_model VARCHAR2(100),
    embedded_at     TIMESTAMP,
    CONSTRAINT uq_doc_chunk UNIQUE (document_id, chunk_index)
);

-- Índice IVF: no requiere vector_memory_size (a diferencia del HNSW
-- INMEMORY NEIGHBOR GRAPH). Si falla, la búsqueda vectorial sigue
-- funcionando en modo exacto (sin índice).
CREATE VECTOR INDEX idx_chunks_embedding ON document_chunks(embedding)
  ORGANIZATION NEIGHBOR PARTITIONS
  DISTANCE COSINE
  WITH TARGET ACCURACY 95;

-- ── Módulo mejora RAG ──────────────────────────────────────────────────────

CREATE TABLE rag_queries (
    query_id        RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    session_id      RAW(16),
    user_id         VARCHAR2(100),
    query_text      CLOB NOT NULL,
    query_embedding VECTOR(768, FLOAT32),
    llm_model       VARCHAR2(50),         -- 'llama3.1' | 'mistral'
    prompt_template_id RAW(16),
    response_text   CLOB,
    created_at      TIMESTAMP DEFAULT SYSTIMESTAMP
);

CREATE TABLE rag_retrieved_chunks (
    query_id           RAW(16) NOT NULL REFERENCES rag_queries(query_id) ON DELETE CASCADE,
    chunk_id           RAW(16) NOT NULL REFERENCES document_chunks(chunk_id),
    rank_position       NUMBER,
    similarity_score    NUMBER,
    was_used_in_prompt  CHAR(1) DEFAULT 'Y',
    PRIMARY KEY (query_id, chunk_id)
);

CREATE TABLE rag_feedback (
    feedback_id   RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    query_id      RAW(16) NOT NULL REFERENCES rag_queries(query_id) ON DELETE CASCADE,
    rating        NUMBER(1),        -- -1/0/1
    feedback_text VARCHAR2(2000),
    created_by    VARCHAR2(100),
    created_at    TIMESTAMP DEFAULT SYSTIMESTAMP
);

CREATE TABLE prompt_templates (
    template_id   RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    name          VARCHAR2(100) NOT NULL,
    version       NUMBER NOT NULL,
    template_text CLOB NOT NULL,
    is_active     CHAR(1) DEFAULT 'Y',
    created_at    TIMESTAMP DEFAULT SYSTIMESTAMP,
    CONSTRAINT uq_prompt_name_ver UNIQUE (name, version)
);

-- ── Opción normalizada (no usada; para varios modelos de embeddings) ───────
--
-- CREATE TABLE chunk_embeddings (
--     chunk_id        RAW(16) NOT NULL REFERENCES document_chunks(chunk_id) ON DELETE CASCADE,
--     embedding_model VARCHAR2(100) NOT NULL,   -- p.ej. 'nomic-embed-text'
--     embedding       VECTOR(768, FLOAT32) NOT NULL,
--     embedded_at     TIMESTAMP DEFAULT SYSTIMESTAMP,
--     PRIMARY KEY (chunk_id, embedding_model)
-- );
--
-- CREATE VECTOR INDEX idx_chunk_emb ON chunk_embeddings(embedding)
--   ORGANIZATION INMEMORY NEIGHBOR GRAPH
--   DISTANCE COSINE
--   WITH TARGET ACCURACY 95;

-- ── Migración 2: hardening (aplicada automáticamente por el servidor) ──────
-- El esquema se versiona en rag_schema_migrations; estas sentencias son la
-- referencia de lo que aplica internal/store/migrate.go.

-- CREATE TABLE rag_schema_migrations (
--     version     NUMBER PRIMARY KEY,
--     description VARCHAR2(200 CHAR) NOT NULL,
--     applied_at  TIMESTAMP DEFAULT SYSTIMESTAMP
-- );

-- Oracle no indexa las FKs automáticamente:
CREATE INDEX idx_retrieved_chunk ON rag_retrieved_chunks(chunk_id);
CREATE INDEX idx_feedback_query ON rag_feedback(query_id);
CREATE INDEX idx_queries_template ON rag_queries(prompt_template_id);
CREATE INDEX idx_queries_session ON rag_queries(session_id, created_at);

-- Estados y banderas validados por la base:
ALTER TABLE documents ADD CONSTRAINT ck_documents_status
  CHECK (status IN ('UPLOADED','EXTRACTING','CHUNKED','EMBEDDED','FAILED'));
ALTER TABLE rag_feedback ADD CONSTRAINT ck_feedback_rating
  CHECK (rating BETWEEN -1 AND 1);
ALTER TABLE prompt_templates ADD CONSTRAINT ck_template_active
  CHECK (is_active IN ('Y','N'));
ALTER TABLE rag_retrieved_chunks ADD CONSTRAINT ck_retrieved_used
  CHECK (was_used_in_prompt IN ('Y','N'));

-- Texto humano en caracteres, no bytes (AL32UTF8):
ALTER TABLE documents MODIFY (file_name VARCHAR2(500 CHAR));
ALTER TABLE documents MODIFY (error_message VARCHAR2(4000 CHAR));
ALTER TABLE rag_feedback MODIFY (feedback_text VARCHAR2(2000 CHAR));

-- Una valoración por usuario y respuesta (el código usa MERGE):
ALTER TABLE rag_feedback ADD CONSTRAINT uq_feedback_query_user
  UNIQUE (query_id, created_by);

-- ── Migración 3: cola durable de ingesta ───────────────────────────────────

CREATE TABLE processing_jobs (
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
);
CREATE INDEX idx_jobs_pending ON processing_jobs(status, created_at);
CREATE INDEX idx_jobs_payload ON processing_jobs(payload_id);

-- Archivo original mientras se procesa (se borra al completar la ingesta):
CREATE TABLE document_files (
    document_id RAW(16) PRIMARY KEY REFERENCES documents(document_id) ON DELETE CASCADE,
    content     BLOB NOT NULL
);

-- ── Migración 4: conversaciones y mensajes (contexto server-side) ──────────

CREATE TABLE conversations (
    conversation_id RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    session_id      RAW(16) NOT NULL,
    user_id         VARCHAR2(100 CHAR),
    title           VARCHAR2(200 CHAR),
    mode            VARCHAR2(10) DEFAULT 'chat' NOT NULL,
    llm_model       VARCHAR2(50),
    summary         CLOB,            -- resumen rodante (fase futura)
    summary_upto    NUMBER DEFAULT 0 NOT NULL,
    created_at      TIMESTAMP DEFAULT SYSTIMESTAMP,
    updated_at      TIMESTAMP DEFAULT SYSTIMESTAMP,
    CONSTRAINT ck_conv_mode CHECK (mode IN ('chat','rag'))
);
CREATE INDEX idx_conv_session ON conversations(session_id, updated_at);

CREATE TABLE conversation_messages (
    message_id      RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    conversation_id RAW(16) NOT NULL
                    REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    seq             NUMBER NOT NULL,
    role            VARCHAR2(20) NOT NULL,
    content         CLOB NOT NULL,
    -- SET NULL: la purga de retención de rag_queries no debe chocar con la FK
    query_id        RAW(16) REFERENCES rag_queries(query_id) ON DELETE SET NULL,
    created_at      TIMESTAMP DEFAULT SYSTIMESTAMP,
    CONSTRAINT uq_conv_seq UNIQUE (conversation_id, seq),
    CONSTRAINT ck_msg_role CHECK (role IN ('user','assistant'))
);
CREATE INDEX idx_msg_query ON conversation_messages(query_id);
