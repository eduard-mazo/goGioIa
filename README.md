# goGioIa

A self-contained, **offline** desktop-grade chat UI for a local
[Ollama](https://ollama.com) instance. The Go binary embeds the entire compiled
frontend (Vue 3 + TypeScript + Tailwind), so it ships as a **single executable**
with no external assets, CDN calls, or runtime dependencies.

## Features

- **Streaming chat** — tokens are streamed from Ollama to the browser over SSE.
- **RAG assistant (Oracle 23ai)** — upload PDFs to a knowledge base backed by
  Oracle 23ai vector search; questions are answered by Mistral grounded in the
  retrieved context, with cited sources and user feedback. See
  [RAG](#rag--asistente-con-base-de-conocimiento-oracle-23ai) below.
- **Rich Markdown rendering** — headings, tables, lists, blockquotes, and
  syntax-highlighted code blocks with one-click copy.
- **PDF context** — attach a PDF; text is extracted server-side (pure Go) and
  injected into the conversation for the model to reason over.
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
| POST   | `/api/pdf`                | Multipart PDF upload → extracted text (JSON)     |
| GET    | `/api/rag/health`         | Oracle 23ai status + knowledge-base stats        |
| GET    | `/api/rag/documents`      | List ingested documents (status, chunks, pages)  |
| POST   | `/api/rag/documents`      | Upload a PDF → async ingest into the vector store |
| DELETE | `/api/rag/documents/{id}` | Remove a document and its vectors                |
| POST   | `/api/rag/ask`            | RAG answer, streamed as SSE (sources → tokens)   |
| POST   | `/api/rag/feedback`       | Rate an answer (-1/0/1) → `rag_feedback`         |
| GET    | `/api/rag/ops/health`     | Component health (cached, `OPS_HEALTH_TTL`)      |
| GET    | `/api/rag/ops/overview`   | Dashboard summary cards (`?hours=&weak=`)        |
| GET    | `/api/rag/ops/timeseries` | Bucketed series: embeds, tokens, latency, errors |
| GET    | `/api/rag/ops/documents`  | Paginated ingestion table (+`/{id}` detail)      |
| GET    | `/api/rag/ops/queries`    | Paginated query runs (+`/{id}` full trace)       |
| GET    | `/api/rag/ops/tokens`     | Token usage by category, model and document      |
| GET    | `/api/rag/ops/models`     | Ollama tags/ps + per-model metrics from events   |
| GET    | `/api/rag/ops/integrity`  | Read-only corpus consistency checks              |
| GET    | `/api/rag/ops/config`     | Effective config with source, secrets redacted   |
| GET    | `/*`                      | Embedded SPA (with client-side route fallback)   |

## RAG — asistente con base de conocimiento (Oracle 23ai)

El modo **asistente RAG** (icono de base de datos en el compositor) responde
preguntas usando exclusivamente los documentos subidos a la base de
conocimiento, citando archivo y página de cada fuente.

**Ingesta / «entrenamiento»** — desde el botón **Base de conocimiento** de la
cabecera se suben PDFs. Cada subida dispara automáticamente el pipeline:

1. `EXTRACTING` — extracción de texto por página (parser puro Go; los PDF
   escaneados sin capa de texto deben pasar por OCR externo antes de subirse).
   El texto por página se guarda en `document_pages`.
2. `CHUNKED` — troceado (~1800 caracteres con solape de 250, cortando en
   límites de párrafo/frase).
3. `EMBEDDED` — cada chunk se vectoriza con **nomic-embed-text** (Ollama,
   768 dims, prefijo `search_document:`) y se inserta en
   `document_chunks.embedding` (`VECTOR(768, FLOAT32)`).

Los duplicados se detectan por SHA-256 (`uq_documents_hash`); un intento
fallido (`FAILED`) se puede reintentar subiendo el mismo archivo de nuevo.

**Consulta** — `/api/rag/ask` vectoriza la pregunta (`search_query:`),
recupera los `RAG_TOP_K` chunks más afines con
`VECTOR_DISTANCE(embedding, :q, COSINE)`, construye el prompt con la plantilla
activa de `prompt_templates` y genera la respuesta con **Mistral** en
streaming. Cada consulta queda trazada en `rag_queries` +
`rag_retrieved_chunks`, y los pulgares arriba/abajo de la UI alimentan
`rag_feedback` para mejorar el sistema.

El esquema se crea automáticamente en el primer arranque (ver
`deploy/oracle_schema.sql`). El índice vectorial
(`ORGANIZATION INMEMORY NEIGHBOR GRAPH`) requiere `vector_memory_size` en la
instancia; si no está disponible, la búsqueda funciona en modo exacto.

### Operaciones RAG (dashboard)

El botón **Operaciones RAG** de la cabecera (o la ruta `/ops/overview` —
enlaces profundos reales vía el fallback SPA) abre el dashboard de
observabilidad: resumen con salud por componente,
ingesta, tokens y uso, calidad del retrieval, consultas con traza completa,
modelos/Ollama, integridad de datos y configuración efectiva.

- Cada operación del pipeline (llamada de embeddings, retrieval, generación,
  hito de ingesta) queda registrada en la tabla `rag_events`, correlacionada
  por `document_id`/`query_id`. El registro es best-effort: nunca bloquea ni
  tumba el pipeline.
- Los conteos de tokens distinguen su fuente: **exacto** (reportado por
  Ollama: `prompt_eval_count`/`eval_count`) o **estimado** (~4 caracteres por
  token); nunca se combinan sin etiquetar. No se muestran costes: no hay
  configuración de precios.
- La salud del modelo de embeddings se deriva de la actividad reciente y solo
  se sonda (1 token, cacheado `OPS_HEALTH_TTL`) si no la hay; el modelo de
  generación no se sonda para no desalojar el de embeddings de la GPU.
- La página de modelos expone las señales del incidente típico de una GPU
  pequeña: (re)cargas del modelo (`load_duration`), esperas del semáforo de
  concurrencia, resets de conexión, HTTP 400 por tamaño y reintentos.

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
| `OPS_HEALTH_TTL`    | `60`                                | Ops-dashboard health cache (seconds) |
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
