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

-- Requiere vector_memory_size configurado en la instancia. Si falla, la
-- búsqueda vectorial sigue funcionando en modo exacto (sin índice).
CREATE VECTOR INDEX idx_chunks_embedding ON document_chunks(embedding)
  ORGANIZATION INMEMORY NEIGHBOR GRAPH
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
