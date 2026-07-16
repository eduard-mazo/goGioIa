package store

// DDL base del modelo Oracle 23ai para el RAG (migración 1). Se usa la
// «opción simple» del modelo: un único modelo de embeddings (nomic-embed-text,
// 768 dims) con el vector como columna de document_chunks. La evolución
// posterior del esquema vive en migrate.go como migraciones versionadas.
var baseSchemaDDL = []string{
	// ── Ingesta de documentos ────────────────────────────────────────────
	`CREATE TABLE documents (
    document_id     RAW(16)  DEFAULT SYS_GUID() PRIMARY KEY,
    file_name       VARCHAR2(500) NOT NULL,
    file_hash       VARCHAR2(64)  NOT NULL,
    mime_type       VARCHAR2(100) DEFAULT 'application/pdf',
    file_size_bytes NUMBER,
    page_count      NUMBER,
    status          VARCHAR2(20)  DEFAULT 'UPLOADED',
    uploaded_by     VARCHAR2(100),
    uploaded_at     TIMESTAMP DEFAULT SYSTIMESTAMP,
    processed_at    TIMESTAMP,
    error_message   VARCHAR2(4000),
    CONSTRAINT uq_documents_hash UNIQUE (file_hash)
)`,

	`CREATE TABLE document_pages (
    page_id       RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    document_id   RAW(16) NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE,
    page_number   NUMBER NOT NULL,
    raw_text      CLOB,
    CONSTRAINT uq_doc_page UNIQUE (document_id, page_number)
)`,

	// ── Chunks + embeddings (núcleo del vector store) ────────────────────
	`CREATE TABLE document_chunks (
    chunk_id        RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    document_id     RAW(16) NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE,
    chunk_index     NUMBER NOT NULL,
    page_number     NUMBER,
    chunk_text      CLOB NOT NULL,
    token_count     NUMBER,
    embedding       VECTOR(768, FLOAT32),
    embedding_model VARCHAR2(100),
    embedded_at     TIMESTAMP,
    CONSTRAINT uq_doc_chunk UNIQUE (document_id, chunk_index)
)`,

	// ── Módulo mejora RAG ────────────────────────────────────────────────
	`CREATE TABLE rag_queries (
    query_id        RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    session_id      RAW(16),
    user_id         VARCHAR2(100),
    query_text      CLOB NOT NULL,
    query_embedding VECTOR(768, FLOAT32),
    llm_model       VARCHAR2(50),
    prompt_template_id RAW(16),
    response_text   CLOB,
    created_at      TIMESTAMP DEFAULT SYSTIMESTAMP
)`,

	`CREATE TABLE rag_retrieved_chunks (
    query_id           RAW(16) NOT NULL REFERENCES rag_queries(query_id) ON DELETE CASCADE,
    chunk_id           RAW(16) NOT NULL REFERENCES document_chunks(chunk_id),
    rank_position       NUMBER,
    similarity_score    NUMBER,
    was_used_in_prompt  CHAR(1) DEFAULT 'Y',
    PRIMARY KEY (query_id, chunk_id)
)`,

	`CREATE TABLE rag_feedback (
    feedback_id   RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    query_id      RAW(16) NOT NULL REFERENCES rag_queries(query_id) ON DELETE CASCADE,
    rating        NUMBER(1),
    feedback_text VARCHAR2(2000),
    created_by    VARCHAR2(100),
    created_at    TIMESTAMP DEFAULT SYSTIMESTAMP
)`,

	`CREATE TABLE prompt_templates (
    template_id   RAW(16) DEFAULT SYS_GUID() PRIMARY KEY,
    name          VARCHAR2(100) NOT NULL,
    version       NUMBER NOT NULL,
    template_text CLOB NOT NULL,
    is_active     CHAR(1) DEFAULT 'Y',
    created_at    TIMESTAMP DEFAULT SYSTIMESTAMP,
    CONSTRAINT uq_prompt_name_ver UNIQUE (name, version)
)`,
}

// Índice vectorial IVF (NEIGHBOR PARTITIONS): a diferencia del HNSW
// (INMEMORY NEIGHBOR GRAPH) no requiere vector_memory_size en la instancia,
// por eso es el defecto operable. Se crea fuera de las migraciones porque su
// fallo es tolerable: sin índice la búsqueda sigue funcionando (exacta).
const vectorIndexDDL = `CREATE VECTOR INDEX idx_chunks_embedding ON document_chunks(embedding)
  ORGANIZATION NEIGHBOR PARTITIONS
  DISTANCE COSINE
  WITH TARGET ACCURACY 95`

// Índice vectorial de los chunks de anexos (mismo criterio best-effort).
const attachVectorIndexDDL = `CREATE VECTOR INDEX idx_attach_embedding ON attachment_chunks(embedding)
  ORGANIZATION NEIGHBOR PARTITIONS
  DISTANCE COSINE
  WITH TARGET ACCURACY 95`

// defaultTemplateName es la plantilla de prompt sembrada en el primer arranque.
const defaultTemplateName = "rag-default"

const defaultTemplateText = `Eres el asistente virtual de goGioIa. Responde SIEMPRE en español, claro y al grano.
Usa EXCLUSIVAMENTE el contexto proporcionado para responder. Si la respuesta no está en el
contexto, indica que no dispones de esa información en la base de conocimiento; no inventes datos.
Cita el documento y la página cuando sea relevante, por ejemplo: (manual.pdf, pág. 3).
Usa Markdown cuando ayude (listas, tablas y bloques de código con su lenguaje).

### Contexto
{context}

### Pregunta
{question}`
