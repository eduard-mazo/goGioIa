# goGioIa

A self-contained, **offline** desktop-grade chat UI for a local
[Ollama](https://ollama.com) instance. The Go binary embeds the entire compiled
frontend (Vue 3 + TypeScript + Tailwind), so it ships as a **single executable**
with no external assets, CDN calls, or runtime dependencies.

## Features

- **Streaming chat** — tokens are streamed from Ollama to the browser over SSE.
- **RAG assistant (Oracle 23ai)** — upload PDFs or plain-text files (.txt,
  .log, .md, .csv, .json, .yaml, source code…) to a knowledge base backed by
  Oracle 23ai vector search; questions are answered by Mistral grounded in the
  retrieved context, with cited sources and user feedback. See
  [RAG](#rag--asistente-con-base-de-conocimiento-oracle-23ai) below.
- **Rich Markdown rendering** — headings, tables, lists, blockquotes, and
  syntax-highlighted code blocks with one-click copy.
- **Document context** — attach a PDF or text file; text is extracted
  server-side (pure Go) and injected into the conversation for the model to
  reason over.
- **Gateway-console theme** — professional dark/light palette via CSS variables.
- **Health indicator** — live status of the configured Ollama endpoint.

## Architecture

```
goGioIa/
├── cmd/server/          # main entrypoint
├── internal/
│   ├── config/          # env-driven configuration
│   ├── ollama/          # streaming Ollama /api/chat client + /api/embed
│   ├── pdf/             # pure-Go PDF text extraction (whole doc + per page)
│   ├── rag/             # RAG pipeline: chunking, ingestion, retrieval, prompt
│   ├── store/           # Oracle 23ai vector store (go-ora, no CGO)
│   └── server/          # HTTP routing, SSE, static SPA serving
├── deploy/
│   └── oracle_schema.sql# reference DDL (auto-created on first boot)
├── web/
│   ├── embed.go         # //go:embed of the built frontend
│   └── dist/            # Vite build output (embedded)
└── frontend/            # Vue 3 + TS + Vite + Tailwind v4 source
    └── src/
        ├── components/  # UI + chat components (incl. KnowledgeBase)
        ├── composables/ # useChat (state + streaming + RAG mode)
        └── lib/         # api client, markdown, utils
```

### API

| Method | Path                      | Purpose                                          |
| ------ | ------------------------- | ------------------------------------------------ |
| GET    | `/api/config`             | Model name and app metadata                      |
| GET    | `/api/health`             | Ollama reachability                              |
| POST   | `/api/chat`               | Chat completion, streamed back as SSE            |
| POST   | `/api/conversations`      | Create a server-side conversation                |
| GET    | `/api/conversations`      | List conversations for a `sessionId`             |
| GET    | `/api/conversations/{id}` | Stored messages of a conversation                |
| DELETE | `/api/conversations/{id}` | Delete a conversation and its messages           |
| POST   | `/api/conversations/{id}/attachments` | Upload an attachment (stored server-side) |
| DELETE | `/api/conversations/{id}/attachments/{attId}` | Remove a stored attachment       |
| POST   | `/api/pdf`                | Multipart PDF/text upload → extracted text (JSON) |
| GET    | `/api/rag/health`         | Oracle 23ai status + knowledge-base stats        |
| GET    | `/api/rag/documents`      | List ingested documents (status, chunks, pages)  |
| POST   | `/api/rag/documents`      | Upload a document → async ingest into the vector store |
| DELETE | `/api/rag/documents/{id}` | Remove a document and its vectors                |
| POST   | `/api/rag/documents/{id}/retry` | Re-queue ingestion of a FAILED document    |
| POST   | `/api/rag/ask`            | RAG answer, streamed as SSE (sources → tokens)   |
| POST   | `/api/rag/feedback`       | Rate an answer (-1/0/1) → `rag_feedback`         |
| GET    | `/*`                      | Embedded SPA (with client-side route fallback)   |

## RAG — asistente con base de conocimiento (Oracle 23ai)

El modo **asistente RAG** (icono de base de datos en el compositor) responde
preguntas usando exclusivamente los documentos subidos a la base de
conocimiento, citando archivo y página de cada fuente.

**Ingesta / «entrenamiento»** — desde el botón **Base de conocimiento** de la
cabecera se suben PDF o archivos de texto (.txt, .log, .md, .csv, .json,
.yaml, .sql, código fuente…; lista completa en `internal/rag/files.go`).
Cada subida dispara automáticamente el pipeline:

1. `EXTRACTING` — los PDF se extraen por página con parser puro Go (los
   escaneados sin capa de texto deben pasar por OCR externo antes de subirse);
   los archivos de texto se decodifican como UTF-8 (o Latin-1 como respaldo) y
   se ingieren sin paginación, por lo que sus citas no llevan «pág.». El texto
   se guarda en `document_pages`.
2. `CHUNKED` — troceado (~1800 caracteres con solape de 250, cortando en
   límites de párrafo/frase).
3. `EMBEDDED` — cada chunk se vectoriza con **nomic-embed-text** (Ollama,
   768 dims, prefijo `search_document:`) y se inserta en
   `document_chunks.embedding` (`VECTOR(768, FLOAT32)`).

La ingesta corre en una **cola durable**: cada subida guarda el archivo en
`document_files` y encola un trabajo en `processing_jobs`, que consumen
`RAG_WORKERS` workers (así se acota la carga de embeddings sobre Ollama y un
reinicio del servidor reanuda lo pendiente automáticamente). Los duplicados
se detectan por SHA-256 (`uq_documents_hash`); un intento fallido (`FAILED`)
se reintenta con el botón de la UI (o `POST /api/rag/documents/{id}/retry`)
o subiendo el archivo de nuevo. Las generaciones de chat simultáneas hacia
Ollama se limitan con `OLLAMA_MAX_CONCURRENT`.

**Consulta** — `/api/rag/ask` vectoriza la pregunta (`search_query:`),
recupera los `RAG_TOP_K` chunks más afines con
`VECTOR_DISTANCE(embedding, :q, COSINE)`, construye el prompt con la plantilla
activa de `prompt_templates` y genera la respuesta con **Mistral** en
streaming. Los adjuntos del clip se suben **una sola vez** a la conversación
(`conversation_attachments`) y las peticiones los referencian por id
(`attachments`), sin reenviar su texto: los pequeños (≤ 8k caracteres) se
inyectan completos como `[Adjunto N]` y los grandes se trocean y vectorizan
en la cola (`attachment_chunks`), recuperando en cada pregunta solo los
fragmentos afines. Si Oracle no está disponible, el clip degrada al envío
inline de siempre (`documents`).

**Cache semántica** — las preguntas sin contexto adicional (sin adjuntos ni
historial de seguimiento) pasan por `rag_semantic_cache`: primero un atajo por
hash exacto de la pregunta normalizada (sin tocar Ollama) y luego una búsqueda
por coseno sobre el embedding (`RAG_CACHE_THRESHOLD`, similitud mínima 0.97);
un hit se emite al instante con sus fuentes originales y un badge «caché» en
la UI. La cache se invalida por versión de la base (`rag_kb_state` se
incrementa en cada ingesta/borrado), por TTL (`RAG_CACHE_TTL_HOURS`) y por
**feedback negativo** (un 👎 borra las entradas equivalentes). Los embeddings
de consultas también se cachean (`embedding_cache`): el mismo texto no se
vectoriza dos veces.

**Contexto de conversación** — el primer mensaje crea (de forma perezosa) una
conversación server-side (`conversations` / `conversation_messages`); a partir
de ahí el frontend solo envía el mensaje nuevo y el backend reconstruye la
ventana de historial (`HISTORY_WINDOW`) desde Oracle — el modo RAG gana
memoria de seguimiento («¿y en qué página está eso?»). En conversaciones
largas, un trabajo en segundo plano (`summarize_conversation`) condensa lo
que queda fuera de la ventana en un **resumen rodante**
(`conversations.summary`), y el contexto pasa a ser resumen + últimos N
mensajes: conversaciones arbitrariamente largas con contexto acotado y sin
latencia añadida. Si Oracle no está disponible, el chat degrada al historial
local del navegador sin perder funcionalidad. Cada consulta queda trazada en `rag_queries` +
`rag_retrieved_chunks`, y los pulgares arriba/abajo de la UI alimentan
`rag_feedback` para mejorar el sistema.

El esquema se crea y evoluciona automáticamente en el arranque mediante
migraciones versionadas (`rag_schema_migrations`, ver
`internal/store/migrate.go`; referencia en `deploy/oracle_schema.sql`). El
índice vectorial es IVF (`ORGANIZATION NEIGHBOR PARTITIONS`), que no requiere
`vector_memory_size`; si aun así no puede crearse, la búsqueda funciona en
modo exacto.

## Configuration

Defaults live in `internal/config/config.go` and can be overridden by env vars:

| Variable            | Default                             | Description                          |
| ------------------- | ----------------------------------- | ------------------------------------ |
| `OLLAMA_API`        | `http://10.14.16.193:9091/api/chat` | Ollama chat endpoint URL             |
| `WEB_PORT`          | `:8080`                             | HTTP listen address                  |
| `MODEL_NAME`        | `llama3.1:latest`                   | Default chat model (see below)       |
| `EMBED_MODEL`       | `nomic-embed-text`                  | Embeddings model (768 dims)          |
| `RAG_MODEL`         | `mistral:latest`                    | LLM that answers RAG questions       |
| `RAG_TOP_K`         | `5`                                 | Chunks retrieved per question        |
| `RAG_CHUNK_SIZE`    | `1800`                              | Chunk size (characters)              |
| `RAG_CHUNK_OVERLAP` | `250`                               | Chunk overlap (characters)           |
| `RAG_EMBED_BATCH`   | `8`                                 | Chunks per `/api/embed` call         |
| `RAG_WORKERS`       | `2`                                 | Ingest queue workers                 |
| `OLLAMA_MAX_CONCURRENT` | `2`                             | Max simultaneous chat generations    |
| `RAG_HISTORY_RETENTION_DAYS` | `180`                      | Purge `rag_queries` older than this (0 = keep) |
| `HISTORY_WINDOW`    | `12`                                | Stored messages fed back into the prompt |
| `RAG_CACHE`         | `1`                                 | Semantic answer cache (0 disables)   |
| `RAG_CACHE_THRESHOLD` | `0.97`                            | Min. cosine similarity to reuse an answer |
| `RAG_CACHE_TTL_HOURS` | `168`                             | Cached answers expire after this     |
| `ORACLE_USER`       | `useria`                            | Oracle 23ai user                     |
| `ORACLE_PASSWORD`   | *(built-in)*                        | Oracle password                      |
| `ORACLE_HOST`       | `10.14.16.193`                      | Oracle host                          |
| `ORACLE_PORT`       | `1521`                              | Oracle listener port                 |
| `ORACLE_SID`        | `orcl`                              | Oracle SID                           |

> Set `MODEL_NAME` to a model you actually have pulled in Ollama, e.g.
> `mistral`, `llama3`, `qwen2.5`.

## Build & run (production)

Requires Go 1.26+ and Node 18+.

```bash
make build      # builds the frontend + embeds it + compiles the binary
make run        # or: ./bin/gogioia
```

Then open <http://localhost:8080>.

The resulting `bin/gogioia` is fully self-contained — copy it anywhere and run
it; all assets are baked in.

## Develop

Run the backend and the Vite dev server (with hot-reload) side by side:

```bash
make server     # terminal 1 — Go API on :8080
make dev        # terminal 2 — Vite on :5173, proxies /api to :8080
```

Open <http://localhost:5173> for live-reloading development.

## Deploy to ppc64le (offline / air-gapped)

goGioIa is a pure-Go, CGO-free binary with the frontend embedded, so it
cross-compiles to a fully static ppc64le ELF and ships as a `FROM scratch`
container — no base OS, no shell, no package manager.

```bash
make build-ppc64le     # static ppc64le binary  -> bin/gogioia-linux-ppc64le
make image-ppc64le     # FROM-scratch OCI image  (needs docker + binfmt/qemu)
make export-ppc64le    # -> gogioia-ppc64le.tar  (transfer this to the target)
```

On the air-gapped ppc64le host (rootless — runs as your user, no `sudo`):

```bash
podman load -i gogioia-ppc64le.tar
mkdir -p ~/.config/containers/systemd
cp deploy/gogioia.container ~/.config/containers/systemd/
systemctl --user daemon-reload
systemctl --user enable --now gogioia
loginctl enable-linger "$USER"   # keep it running after logout
```

Verify with `systemctl --user status gogioia` and
`journalctl --user -u gogioia -f`.

The Podman **quadlet** unit (`deploy/gogioia.container`) runs the image
**rootless** with `Pull=never`, `ReadOnly=true`, `NoNewPrivileges=true`,
`DropCapability=ALL`, as UID/GID `65534` inside the user namespace, publishing
only `:8080`. Configure it via the `WEB_PORT`, `OLLAMA_API`, and `MODEL_NAME`
environment variables.

### Stateless — including PDFs

There is **no writable volume**. The app keeps no server-side state:

- The chat is stateless — the browser sends the full message history each turn.
- **PDF uploads are processed entirely in memory.** The request body is capped
  (32 MiB) and `multipart` parsing uses the same value as its in-memory limit,
  so a file part never spills to a temp file on disk. The extracted text is
  returned to the browser and held only in the frontend; nothing touches the
  container filesystem. That is why the root filesystem can be mounted
  read-only and no `/data` mount (or `tmpfs`) is required.
